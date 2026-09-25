package core

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
)

func TestPermissionMiddlewareForSourceHonorsAssistantOverride(t *testing.T) {
	everyone := PermissionEveryone
	cmd := Command{
		Name:                "assistant-public",
		Permission:          PermissionOwner,
		AssistantPermission: &everyone,
		Surfaces:            execution.SurfaceAssistant,
	}
	ctx := &Context{
		Ctx:    context.Background(),
		Source: ExecutionAssistant,
		Sender: &User{ID: 200},
		Perms:  NewPermissions(100, nil),
	}

	if err := PermissionMiddlewareForSource(cmd, ExecutionAssistant).Then(nil)(ctx); err != nil {
		t.Fatalf("assistant override rejected: %v", err)
	}
	if err := PermissionMiddleware(cmd).Then(nil)(ctx); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("interactive permission wrapper error=%v, want ErrPermissionDenied", err)
	}
}

func TestPreflightCommandRejectsBeforeExecutionWithoutStatefulAccounting(t *testing.T) {
	cmd := Command{
		Name:       "owner-only",
		Permission: PermissionOwner,
		Invocation: InvocationPolicy{Assistant: InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
	}
	ctx := &Context{
		Ctx:     context.Background(),
		Source:  ExecutionAssistant,
		Sender:  &User{ID: 200},
		Perms:   NewPermissions(100, nil),
		Chat:    &Chat{ID: 200, Type: "private"},
		Message: &Message{ID: 1, SenderID: 200},
	}

	if err := PreflightCommand(ctx, cmd, ExecutionAssistant); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("PreflightCommand error=%v, want ErrPermissionDenied", err)
	}
}
