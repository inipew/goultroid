package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/resource"
)

func TestProcessManager_AllowlistAndExecution(t *testing.T) {
	rm := resource.NewManager()
	mgr := NewManager([]string{"echo"}, 1024, rm)

	// Unapproved binary must be rejected
	_, _, err := mgr.Execute(context.Background(), "plugin:test", "sh", "-c", "ls")
	if !errors.Is(err, ErrBinaryNotAllowed) {
		t.Fatalf("expected ErrBinaryNotAllowed, got: %v", err)
	}

	// Approved binary must execute
	stdout, stderr, err := mgr.Execute(context.Background(), "plugin:test", "echo", "hello", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(stdout)) != "hello world" {
		t.Errorf("unexpected stdout: %s", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("unexpected stderr: %s", string(stderr))
	}

	// After completion, process must be released from ResourceManager
	active := rm.ByOwner("plugin:test")
	if len(active) != 0 {
		t.Errorf("expected process to be released from manager, got: %+v", active)
	}
}

func TestProcessManager_Cancellation(t *testing.T) {
	rm := resource.NewManager()
	mgr := NewManager([]string{"sleep"}, 1024, rm)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := mgr.Execute(ctx, "plugin:test", "sleep", "5")
	if err == nil {
		t.Fatalf("expected error on cancelled process")
	}
}

func TestProcessManager_Auditor(t *testing.T) {
	auditor := audit.NewService(nil, 10)
	mgr := NewManager([]string{"echo"}, 1024, nil)
	mgr.SetAuditor(auditor)

	// Denied execution is audited
	_, _, _ = mgr.Execute(context.Background(), "test-owner", "forbidden-bin")
	recent := auditor.Recent(5)
	if len(recent) != 1 || recent[0].Action != "process.execute.denied" {
		t.Fatalf("expected 1 process.execute.denied audit event, got %+v", recent)
	}

	// Allowed execution is audited
	_, _, _ = mgr.Execute(context.Background(), "test-owner", "echo", "hi")
	recent = auditor.Recent(5)
	if len(recent) != 2 || recent[0].Action != "process.execute" {
		t.Fatalf("expected process.execute audit event, got %+v", recent)
	}
}
