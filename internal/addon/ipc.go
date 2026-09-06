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
	manifest   Manifest
	broker     *CapabilityBroker
	executable string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	mu         sync.Mutex
	callMu     sync.Mutex
	running    bool
	seq        uint64
}

func NewExternalRuntime(manifest Manifest, executable string, broker *CapabilityBroker) *ExternalRuntime {
	return &ExternalRuntime{manifest: manifest, executable: executable, broker: broker}
}

func (r *ExternalRuntime) Manifest() Manifest { return r.manifest }

func (r *ExternalRuntime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return errors.New("addon runtime already running")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := validateExecutable(r.executable)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOUTROID_ADDON_NAME=" + r.manifest.Name, "GOUTROID_ADDON_VERSION=" + r.manifest.Version}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("addon stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("addon stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start addon: %w", err)
	}

	r.cmd = cmd
	r.stdin = stdin
	r.stdout = bufio.NewReader(io.LimitReader(stdout, 8<<20))
	r.running = true

	if err := r.handshake(ctx); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		r.running = false
		r.cmd, r.stdin, r.stdout = nil, nil, nil
		return err
	}
	return nil
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
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write addon IPC request: %w", err)
	}

	resultCh := make(chan struct {
		response IPCResponse
		err      error
	}, 1)
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		line, err := r.stdout.ReadBytes('\n')
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
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	cmd := r.cmd
	stdin := r.stdin
	r.running = false
	r.cmd, r.stdin, r.stdout = nil, nil, nil
	r.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return cmd.Wait()
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
