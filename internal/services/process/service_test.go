package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestOSRunner_RunEcho(t *testing.T) {
	runner := NewOSRunner(2, 5*time.Second, 1024)
	res, err := runner.Run(context.Background(), Request{Command: "echo", Args: []string{"hello", "world"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello world" {
		t.Errorf("expected 'hello world', got %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
}

func TestOSRunner_RejectsShell(t *testing.T) {
	runner := NewOSRunner(2, 5*time.Second, 1024)
	_, err := runner.Run(context.Background(), Request{Command: "echo unsafe", Shell: true})
	if err == nil || !strings.Contains(err.Error(), "shell execution is disabled") {
		t.Fatalf("expected shell execution to be rejected, got %v", err)
	}
}

func TestOSRunner_ArgvIsNotShellExpanded(t *testing.T) {
	runner := NewOSRunner(2, 5*time.Second, 1024)
	res, err := runner.Run(context.Background(), Request{
		Command: "printf",
		Args:    []string{"%s", "$(echo injected)"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "$(echo injected)" {
		t.Fatalf("argument was unexpectedly interpreted by a shell: %q", res.Stdout)
	}
}

func TestOSRunner_OutputTruncation(t *testing.T) {
	runner := NewOSRunner(2, 5*time.Second, 20)
	res, err := runner.Run(context.Background(), Request{
		Command:   "printf",
		Args:      []string{"%s", "this is a very long string that should exceed the limit"},
		MaxOutput: 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Truncated {
		t.Errorf("expected output to be marked as truncated")
	}
	if len(res.Stdout) > 20 {
		t.Errorf("expected stdout length <= 20, got %d", len(res.Stdout))
	}
}

func TestOSRunner_Timeout(t *testing.T) {
	runner := NewOSRunner(2, 100*time.Millisecond, 1024)
	t0 := time.Now()
	res, err := runner.Run(context.Background(), Request{
		Command: "sleep",
		Args:    []string{"2"},
		Timeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, core.ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("process did not terminate quickly on timeout: took %v", elapsed)
	}
	_ = res
}

func TestSanitizeEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"BOT_TOKEN=123456:ABC-DEF",
		"API_HASH=abcdef123456",
		"SESSION_STRING=1B2C3D",
		"NORMAL_USER=john",
		"DATABASE_URL=postgres://user:pass@localhost/db",
	}
	sanitized := SanitizeEnv(env)
	for _, e := range sanitized {
		if strings.HasPrefix(e, "BOT_TOKEN=") && !strings.Contains(e, "[REDACTED]") {
			t.Errorf("BOT_TOKEN was not redacted: %s", e)
		}
		if strings.HasPrefix(e, "API_HASH=") && !strings.Contains(e, "[REDACTED]") {
			t.Errorf("API_HASH was not redacted: %s", e)
		}
		if strings.HasPrefix(e, "SESSION_STRING=") && !strings.Contains(e, "[REDACTED]") {
			t.Errorf("SESSION_STRING was not redacted: %s", e)
		}
		if strings.HasPrefix(e, "DATABASE_URL=") && !strings.Contains(e, "[REDACTED]") {
			t.Errorf("DATABASE_URL was not redacted: %s", e)
		}
		if strings.HasPrefix(e, "NORMAL_USER=") && strings.Contains(e, "[REDACTED]") {
			t.Errorf("NORMAL_USER should not be redacted: %s", e)
		}
	}
}
