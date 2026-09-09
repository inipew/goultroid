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
	Command string
	Args    []string
	// Shell is intentionally unsupported for untrusted/user-derived input.
	// Keep it only as an explicit compatibility guard; callers must use argv.
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

// DiagnosticsSnapshot describes process runner pressure without exposing the
// semaphore or mutable runner internals.
type DiagnosticsSnapshot struct {
	Capacity int
	Active   int
}

// Runner defines the contract for executing system processes with resource bounds.
type Runner interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// OSRunner implements Runner with process isolation and concurrent process throttling.
type OSRunner struct {
	sem            chan struct{}
	defaultTimeout time.Duration
	maxOutputBytes int64
	mu             sync.Mutex
}

// Diagnostics returns current process concurrency usage.
func (r *OSRunner) Diagnostics() DiagnosticsSnapshot {
	if r == nil {
		return DiagnosticsSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return DiagnosticsSnapshot{Capacity: cap(r.sem), Active: len(r.sem)}
}

var _ Runner = (*OSRunner)(nil)

func NewOSRunner(maxConcurrent int, defaultTimeout time.Duration, maxOutputBytes int64) *OSRunner {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	if defaultTimeout <= 0 {
		defaultTimeout = 60 * time.Second
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = 2 * 1024 * 1024
	}
	return &OSRunner{
		sem:            make(chan struct{}, maxConcurrent),
		defaultTimeout: defaultTimeout,
		maxOutputBytes: maxOutputBytes,
	}
}

type limitedBuffer struct {
	buf       *bytes.Buffer
	remain    int64
	truncated bool
	mu        sync.Mutex
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{buf: new(bytes.Buffer), remain: limit}
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

// Run executes a binary directly with argv semantics. Shell execution is
// deliberately rejected so user-derived arguments can never become shell code.
func (r *OSRunner) Run(ctx context.Context, req Request) (*Result, error) {
	if req.Shell {
		return nil, fmt.Errorf("%w: shell execution is disabled; use Command plus Args", core.ErrInvalidArgs)
	}

	cmdName := strings.TrimSpace(req.Command)
	cmdArgs := req.Args
	if cmdName == "" && len(cmdArgs) > 0 {
		cmdName = cmdArgs[0]
		cmdArgs = cmdArgs[1:]
	}
	if cmdName == "" {
		return nil, fmt.Errorf("%w: process command cannot be empty", core.ErrInvalidArgs)
	}

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

	cmd := exec.CommandContext(execCtx, cmdName, cmdArgs...)
	// Do not pass session tokens, API credentials, passwords, or other known
	// secrets to child processes. Unlike SanitizeEnv (which is for logging),
	// this helper removes sensitive variables instead of replacing values with
	// the literal string "[REDACTED]".
	cmd.Env = childEnv(req.Env)

	if req.WorkingDir != "" {
		if _, err := os.Stat(req.WorkingDir); err != nil {
			return nil, fmt.Errorf("%w: invalid working directory: %v", core.ErrInvalidArgs, err)
		}
		cmd.Dir = req.WorkingDir
	}

	// Put the command in its own process group so cancellation also kills
	// descendants instead of leaving orphaned ffmpeg/python/etc. processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
		Stdout: stdout, Stderr: stderr, Combined: combined,
		ExitCode: exitCode, Duration: elapsed, Truncated: truncated,
	}
	if execCtx.Err() != nil {
		return res, fmt.Errorf("%w: process timed out after %v: %v", core.ErrTimeout, timeout, execCtx.Err())
	}
	return res, runErr
}

func isSensitiveEnvKey(key string) bool {
	sensitiveKeywords := []string{
		"SESSION", "TOKEN", "API_HASH", "API_ID", "SECRET", "PASSWORD",
		"PASS", "KEY", "CRED", "AUTH", "DATABASE", "PRIVATE",
	}
	keyUpper := strings.ToUpper(key)
	for _, kw := range sensitiveKeywords {
		if strings.Contains(keyUpper, kw) {
			return true
		}
	}
	return false
}

// childEnv builds an environment suitable for a child process. Sensitive
// variables are omitted entirely; ordinary variables (including PATH) remain
// available so direct argv commands can resolve normally.
func childEnv(customEnv []string) []string {
	base := customEnv
	if len(base) == 0 {
		base = os.Environ()
	}
	filtered := make([]string, 0, len(base))
	for _, env := range base {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || isSensitiveEnvKey(parts[0]) {
			continue
		}
		filtered = append(filtered, env)
	}
	return filtered
}

// SanitizeEnv removes or redacts known secret keys from an environment slice.
// If customEnv is empty, os.Environ() is used as the base. This helper is for
// safe logging/display, not for constructing the child process environment.
func SanitizeEnv(customEnv []string) []string {
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
		if isSensitiveEnvKey(parts[0]) {
			sanitized = append(sanitized, parts[0]+"=[REDACTED]")
		} else {
			sanitized = append(sanitized, env)
		}
	}
	return sanitized
}
