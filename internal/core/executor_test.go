package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

func TestCommandExecutor_Success(t *testing.T) {
	logger := zap.NewNop()
	cooldown := NewCooldownTracker()
	exec := NewCommandExecutor(logger, cooldown, 5*time.Second)

	executed := false
	cmd := Command{
		Name:       "test",
		Permission: PermissionEveryone,
		Handler: func(ctx *Context) error {
			executed = true
			return nil
		},
	}

	ctx := &Context{
		Ctx:     context.Background(),
		Command: "test",
	}

	err := exec.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !executed {
		t.Fatal("expected command handler to be executed")
	}
}

func TestCommandExecutor_PermissionDenied(t *testing.T) {
	logger := zap.NewNop()
	cooldown := NewCooldownTracker()
	exec := NewCommandExecutor(logger, cooldown, 5*time.Second)

	perms := NewPermissions(1001, nil)
	cmd := Command{
		Name:       "owner_cmd",
		Permission: PermissionOwner,
		Handler: func(ctx *Context) error {
			return nil
		},
	}

	ctx := &Context{
		Ctx:     context.Background(),
		Command: "owner_cmd",
		Sender:  &User{ID: 2002}, // not owner
		Perms:   perms,
	}

	err := exec.Execute(ctx, cmd)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied, got: %v", err)
	}
}

func TestCommandExecutor_PanicRecovery(t *testing.T) {
	logger := zap.NewNop()
	cooldown := NewCooldownTracker()
	exec := NewCommandExecutor(logger, cooldown, 5*time.Second)

	cmd := Command{
		Name:       "panic_cmd",
		Permission: PermissionEveryone,
		Handler: func(ctx *Context) error {
			panic("something went wrong")
		},
	}

	ctx := &Context{
		Ctx:     context.Background(),
		Command: "panic_cmd",
	}

	err := exec.Execute(ctx, cmd)
	if err == nil || !errors.Is(err, ErrInternal) {
		t.Fatalf("expected ErrInternal from panic recovery, got: %v", err)
	}
}

func TestCommandExecutor_WithMetrics(t *testing.T) {
	logger := zap.NewNop()
	exec := NewCommandExecutor(logger, nil, 5*time.Second)
	metrics := NewDefaultMetricsTracker()
	exec.SetMetrics(metrics)

	cmd := Command{
		Name:       "metric_test",
		Permission: PermissionEveryone,
		Handler: func(ctx *Context) error {
			time.Sleep(2 * time.Millisecond)
			return nil
		},
	}

	ctx := &Context{
		Ctx:     context.Background(),
		Command: "metric_test",
	}

	if err := exec.Execute(ctx, cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := metrics.Snapshot()
	if snap.TotalCommands != 1 {
		t.Errorf("expected 1 command recorded, got %d", snap.TotalCommands)
	}
	if st, ok := snap.Commands["metric_test"]; !ok || st.TotalCalls != 1 {
		t.Errorf("expected metric_test stats recorded, got %+v", st)
	}
}

func TestCommandExecutor_SurfaceEnforcement(t *testing.T) {
	logger := zap.NewNop()
	exec := NewCommandExecutor(logger, nil, 5*time.Second)

	// Command enabled ONLY for Assistant surface
	cmd := Command{
		Name:       "assistant_only",
		Permission: PermissionEveryone,
		Surfaces:   execution.SurfaceAssistant,
		Handler: func(ctx *Context) error {
			return nil
		},
	}

	// 1. Invoked on Userbot (ExecutionInteractive) -> should be denied
	ctxUserbot := &Context{
		Ctx:     context.Background(),
		Source:  ExecutionInteractive,
		Command: "assistant_only",
	}
	err := exec.Execute(ctxUserbot, cmd)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied when running assistant-only command on userbot, got: %v", err)
	}

	// 2. Invoked on Assistant (ExecutionAssistant) -> should succeed
	ctxAssistant := &Context{
		Ctx:     context.Background(),
		Source:  ExecutionAssistant,
		Command: "assistant_only",
	}
	err = exec.Execute(ctxAssistant, cmd)
	if err != nil {
		t.Fatalf("expected success when running assistant-only command on assistant, got: %v", err)
	}
}
