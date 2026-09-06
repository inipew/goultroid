package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// Request defines the input parameters for executing a system process.
type Request struct {
	Command    string
	Args       []string
	Shell      bool
	Timeout    time.Duration
	MaxOutput  int64
	WorkingDir string
	Env        []string
}

// Result contains the outcome and outputs of an executed process.
type Result struct {
	Stdout    string
	Stderr    string
	Combined  string
	ExitCode  int
	Duration  time.Duration
	Truncated bool
}

// Runner defines the contract for executing system processes with resource bounds.
type Runner interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// OSRunner implements Runner with process isolation, environment sanitization,
// memory limits, and concurrent process throttling.
type OSRunner struct {
	sem            chan struct{}
	defaultTimeout time.Duration
	maxOutputBytes int64
	mu             sync.Mutex
}

// Ensure OSRunner implements Runner.
var _ Runner = (*OSRunner)(nil)

// NewOSRunner creates a new production-grade process runner.
func NewOSRunner(maxConcurrent int, defaultTimeout time.Duration, maxOutputBytes int64) *OSRunner {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	if defaultTimeout <= 0 {
		defaultTimeout = 60 * time.Second
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = 2 * 1024 * 1024 // 2 MB default cap
	}

	return &OSRunner{
		sem:            make(chan struct{}, maxConcurrent),
		defaultTimeout: defaultTimeout,
		maxOutputBytes: maxOutputBytes,
	}
}

// limitedBuffer captures output up to a maximum limit and flags truncation.
type limitedBuffer struct {
	buf       *bytes.Buffer
	remain    int64
	truncated bool
	mu        sync.Mutex
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{
		buf:    new(bytes.Buffer),
		remain: limit,
	}
}

func (lb *limitedBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	origLen := len(p)
	if lb.remain <= 0 {
		lb.truncated = true
		return origLen, nil
	}
	if int64(len(p)) > lb.remain {
		lb.truncated = true
		p = p[:lb.remain]
	}
	_, err = lb.buf.Write(p)
	lb.remain -= int64(len(p))
	return origLen, err
}

func (lb *limitedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

func (lb *limitedBuffer) IsTruncated() bool {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.truncated
}

// Run executes the requested command in an isolated child process.
func (r *OSRunner) Run(ctx context.Context, req Request) (*Result, error) {
	if req.Command == "" && len(req.Args) == 0 {
		return nil, fmt.Errorf("%w: process command cannot be empty", core.ErrInvalidArgs)
	}

	// Throttle concurrent process executions
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", core.ErrTimeout, ctx.Err())
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if req.Shell {
		shell := "bash"
		if _, err := exec.LookPath("bash"); err != nil {
			shell = "sh"
		}
		var fullCmd string
		if req.Command != "" {
			fullCmd = req.Command
			if len(req.Args) > 0 {
				fullCmd += " " + strings.Join(req.Args, " ")
			}
		} else {
			fullCmd = strings.Join(req.Args, " ")
		}
		cmd = exec.CommandContext(execCtx, shell, "-c", fullCmd)
	} else {
		cmdName := req.Command
		cmdArgs := req.Args
		if cmdName == "" && len(req.Args) > 0 {
			cmdName = req.Args[0]
			cmdArgs = req.Args[1:]
		}
		cmd = exec.CommandContext(execCtx, cmdName, cmdArgs...)
	}

	// Environment sanitization
	cmd.Env = SanitizeEnv(req.Env)

	// Working directory
	if req.WorkingDir != "" {
		if _, err := os.Stat(req.WorkingDir); err != nil {
			return nil, fmt.Errorf("%w: invalid working directory: %v", core.ErrInvalidArgs, err)
		}
		cmd.Dir = req.WorkingDir
	}

	// Process group isolation: kill all child processes on cancellation
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	maxOutput := req.MaxOutput
	if maxOutput <= 0 {
		maxOutput = r.maxOutputBytes
	}

	outBuf := newLimitedBuffer(maxOutput)
	errBuf := newLimitedBuffer(maxOutput)
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	stdout := outBuf.String()
	stderr := errBuf.String()
	truncated := outBuf.IsTruncated() || errBuf.IsTruncated()

	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n" + stderr
		} else {
			combined = stderr
		}
	}

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	res := &Result{
		Stdout:    stdout,
		Stderr:    stderr,
		Combined:  combined,
		ExitCode:  exitCode,
		Duration:  elapsed,
		Truncated: truncated,
	}

	if execCtx.Err() != nil {
		return res, fmt.Errorf("%w: process timed out after %v: %v", core.ErrTimeout, timeout, execCtx.Err())
	}

	return res, runErr
}

// SanitizeEnv removes or redacts known secret keys from an environment slice.
// If customEnv is empty, os.Environ() is used as the base.
func SanitizeEnv(customEnv []string) []string {
	sensitiveKeywords := []string{
		"SESSION", "TOKEN", "API_HASH", "API_ID", "SECRET", "PASSWORD",
		"PASS", "KEY", "CRED", "AUTH", "DATABASE", "PRIVATE",
	}

	base := customEnv
	if len(base) == 0 {
		base = os.Environ()
	}

	var sanitized []string
	for _, env := range base {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		keyUpper := strings.ToUpper(parts[0])
		isSensitive := false
		for _, kw := range sensitiveKeywords {
			if strings.Contains(keyUpper, kw) {
				isSensitive = true
				break
			}
		}
		if isSensitive {
			sanitized = append(sanitized, parts[0]+"=[REDACTED]")
		} else {
			sanitized = append(sanitized, env)
		}
	}
	return sanitized
}
