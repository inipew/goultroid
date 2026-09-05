package core

import (
	"context"
	"errors"
	"testing"
	"time"

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
