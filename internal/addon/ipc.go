package addon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/platform/process"
	"go.uber.org/zap"
)

const AddonProtocolVersion = 1

type IPCRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type IPCResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type HelloParams struct {
	Protocol int          `json:"protocol"`
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Caps     []Capability `json:"capabilities"`
}

type HelloResult struct {
	Protocol int `json:"protocol"`
}

// CapabilityBroker is the only privileged boundary an addon runtime should
// use before performing a host-side operation.
type CapabilityBroker struct {
	gate *CapabilityGate
}

func NewCapabilityBroker(gate *CapabilityGate) *CapabilityBroker {
	if gate == nil {
		gate = NewCapabilityGate()
	}
	return &CapabilityBroker{gate: gate}
}

func (b *CapabilityBroker) Authorize(addon string, capability Capability) error {
	if b == nil || b.gate == nil {
		return ErrUnauthorizedCapability
	}
	return b.gate.Assert(addon, capability)
}

// ExternalRuntime manages one isolated addon process and a JSON-lines IPC
// channel. The runtime intentionally does not use Go's plugin package.
type ExternalRuntime struct {
	manifest    Manifest
	broker      *CapabilityBroker
	executable  string
	procMgr     *process.Manager
	procID      string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	logger      *zap.Logger
	mu          sync.Mutex
	callMu      sync.Mutex
	running     bool
	stopping    bool
	seq         uint64
	exitDone    chan struct{}
	exitErr     error
	lifetimeEnd context.CancelFunc
}

func NewExternalRuntime(manifest Manifest, executable string, broker *CapabilityBroker) *ExternalRuntime {
	return &ExternalRuntime{manifest: manifest, executable: executable, broker: broker}
}

func (r *ExternalRuntime) SetLogger(logger *zap.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = logger
}

func (r *ExternalRuntime) SetProcessManager(pm *process.Manager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procMgr = pm
}

func (r *ExternalRuntime) Manifest() Manifest { return r.manifest }

func (r *ExternalRuntime) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Do not hold r.mu while performing the handshake. handshake -> callLocked
	// needs the same mutex to access the IPC streams; holding it here deadlocks
	// every otherwise-valid addon during startup.
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("addon runtime already running")
	}
	path, err := validateExecutable(r.executable)
	if err != nil {
		r.mu.Unlock()
		return err
	}

	lifetimeCtx, lifetimeEnd := context.WithCancel(context.Background())
	cmd := exec.CommandContext(lifetimeCtx, path)
	runtimeDir := filepath.Join("data", "addons", "runtime", r.manifest.Name)
	if err := os.MkdirAll(runtimeDir, 0700); err == nil {
		cmd.Dir = runtimeDir
	} else {
		cmd.Dir = filepath.Dir(path)
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOUTROID_ADDON_NAME=" + r.manifest.Name, "GOUTROID_ADDON_VERSION=" + r.manifest.Version}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		lifetimeEnd()
		r.mu.Unlock()
		return fmt.Errorf("addon stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		lifetimeEnd()
		_ = stdin.Close()
		r.mu.Unlock()
		return fmt.Errorf("addon stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		lifetimeEnd()
		_ = stdin.Close()
		_ = stdout.Close()
		r.mu.Unlock()
		return fmt.Errorf("addon stderr: %w", err)
	}

	if r.procMgr != nil {
		procID, err := r.procMgr.StartCmd(ctx, "addon:"+r.manifest.Name, cmd)
		if err != nil {
			lifetimeEnd()
			_ = stdin.Close()
			_ = stdout.Close()
			_ = stderrPipe.Close()
			r.mu.Unlock()
			return fmt.Errorf("start addon via process manager: %w", err)
		}
		r.procID = procID
	} else {
		if err := cmd.Start(); err != nil {
			lifetimeEnd()
			_ = stdin.Close()
			_ = stdout.Close()
			_ = stderrPipe.Close()
			r.mu.Unlock()
			return fmt.Errorf("start addon: %w", err)
		}
	}

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			r.mu.Lock()
			l := r.logger
			r.mu.Unlock()
			if l != nil {
				l.Warn("addon stderr", zap.String("addon", r.manifest.Name), zap.String("line", line))
			}
		}
	}()

	r.cmd = cmd
	r.stdin = stdin
	r.stdout = bufio.NewReader(io.LimitReader(stdout, 8<<20))
	r.running = true
	r.stopping = false
	r.exitDone = make(chan struct{})
	r.exitErr = nil
	r.lifetimeEnd = lifetimeEnd
	r.mu.Unlock()
	go r.watchProcess(cmd)

	if err := r.handshake(ctx); err != nil {
		_ = r.Stop()
		return err
	}
	return nil
}

func (r *ExternalRuntime) watchProcess(cmd *exec.Cmd) {
	err := cmd.Wait()

	r.mu.Lock()
	if r.cmd != cmd {
		r.mu.Unlock()
		return
	}
	stdin := r.stdin
	done := r.exitDone
	procMgr := r.procMgr
	procID := r.procID
	logger := r.logger
	unexpected := !r.stopping
	r.running = false
	r.cmd, r.stdin, r.stdout = nil, nil, nil
	r.procID = ""
	r.lifetimeEnd = nil
	r.exitErr = err
	r.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if procMgr != nil && procID != "" {
		procMgr.ReleaseCmd(procID)
	}
	if done != nil {
		close(done)
	}
	if unexpected && logger != nil {
		logger.Warn("addon process exited", zap.String("addon", r.manifest.Name), zap.Error(err))
	}
}

func (r *ExternalRuntime) handshake(ctx context.Context) error {
	params, _ := json.Marshal(HelloParams{Protocol: AddonProtocolVersion, Name: r.manifest.Name, Version: r.manifest.Version, Caps: r.manifest.Capabilities})
	resp, err := r.callLocked(ctx, "hello", params)
	if err != nil {
		return err
	}
	var result HelloResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("invalid addon hello response: %w", err)
	}
	if result.Protocol != AddonProtocolVersion {
		return fmt.Errorf("addon protocol mismatch: got %d want %d", result.Protocol, AddonProtocolVersion)
	}
	return nil
}

func (r *ExternalRuntime) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method == "" {
		return nil, errors.New("addon IPC method cannot be empty")
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal addon params: %w", err)
	}
	r.callMu.Lock()
	defer r.callMu.Unlock()
	return r.callLocked(ctx, method, data)
}

func (r *ExternalRuntime) callLocked(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if !r.running || r.stdin == nil || r.stdout == nil {
		r.mu.Unlock()
		return nil, ErrAddonDisabled
	}
	r.seq++
	id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), r.seq)
	request, _ := json.Marshal(IPCRequest{ID: id, Method: method, Params: params})
	_, err := r.stdin.Write(append(request, '\n'))
	stdout := r.stdout
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write addon IPC request: %w", err)
	}

	resultCh := make(chan struct {
		response IPCResponse
		err      error
	}, 1)
	go func() {
		line, err := stdout.ReadBytes('\n')
		if err != nil {
			resultCh <- struct {
				response IPCResponse
				err      error
			}{err: err}
			return
		}
		var response IPCResponse
		if err := json.Unmarshal(line, &response); err != nil {
			resultCh <- struct {
				response IPCResponse
				err      error
			}{err: fmt.Errorf("decode addon IPC response: %w", err)}
			return
		}
		if response.ID != id {
			resultCh <- struct {
				response IPCResponse
				err      error
			}{err: fmt.Errorf("addon IPC response ID mismatch: got %q want %q", response.ID, id)}
			return
		}
		resultCh <- struct {
			response IPCResponse
			err      error
		}{response: response}
	}()

	select {
	case <-ctx.Done():
		// Subprocess timed out or canceled. Terminate process to unblock reader goroutine
		// and avoid stream desynchronization.
		_ = r.Stop()
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		if !result.response.OK {
			return nil, errors.New(result.response.Error)
		}
		return result.response.Result, nil
	}
}

func (r *ExternalRuntime) Stop() error {
	r.mu.Lock()
	if r.exitDone == nil {
		r.mu.Unlock()
		return nil
	}
	cmd := r.cmd
	stdin := r.stdin
	done := r.exitDone
	lifetimeEnd := r.lifetimeEnd
	r.running = false
	r.stopping = true
	r.mu.Unlock()
	if lifetimeEnd != nil {
		lifetimeEnd()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	var killErr error
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			killErr = err
		}
	}
	if done != nil {
		<-done
	}
	return killErr
}

func (r *ExternalRuntime) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func validateExecutable(path string) (string, error) {
	if path == "" {
		return "", errors.New("addon executable is required")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", errors.New("addon executable must use an absolute path")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("stat addon executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("addon executable is not a regular file")
	}
	if info.Mode().Perm()&0111 == 0 {
		return "", errors.New("addon executable is not executable")
	}
	return clean, nil
}

// VerifySHA256 verifies an executable artifact before it is started.
func VerifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("addon checksum mismatch: got %s want %s", got, expected)
	}
	return nil
}
