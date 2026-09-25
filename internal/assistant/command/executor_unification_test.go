package command_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

type denyCanonicalLimiter struct{ calls atomic.Int32 }

func (l *denyCanonicalLimiter) Allow(string) bool {
	l.calls.Add(1)
	return false
}

func TestAssistantCanonicalCommandUsesSharedExecutor(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	client := &inlineTaskClient{}
	r.SetTasks(client)

	limiter := &denyCanonicalLimiter{}
	executor := core.NewCommandExecutor(zap.NewNop(), core.NewCooldownTracker(), 30*time.Second)
	executor.SetRateLimiter(limiter)
	r.SetCommandExecutor(executor)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "sharedexec",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := dispatchTest(r, context.Background(), 42, &tg.InputPeerUser{UserID: 42}, "/sharedexec", &fakeInteraction{})
	if err == nil {
		t.Fatal("expected shared executor rate-limit rejection")
	}
	if got := limiter.calls.Load(); got != 1 {
		t.Fatalf("shared executor limiter calls=%d, want 1", got)
	}
	if called {
		t.Fatal("handler executed after shared executor rate-limit rejection")
	}
}

func TestAssistantCanonicalExecutorHonorsAssistantPermissionOverride(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	client := &inlineTaskClient{}
	r.SetTasks(client)
	r.SetOwner(100, nil)
	r.SetCommandExecutor(core.NewCommandExecutor(zap.NewNop(), core.NewCooldownTracker(), 30*time.Second))

	everyone := core.PermissionEveryone
	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:                "assistantoverride",
		Permission:          core.PermissionOwner,
		AssistantPermission: &everyone,
		Invocation:          core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:            execution.SurfaceAssistant,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	if err := dispatchTest(r, context.Background(), 200, &tg.InputPeerUser{UserID: 200}, "/assistantoverride", &fakeInteraction{}); err != nil {
		t.Fatalf("assistant override dispatch: %v", err)
	}
	if !called {
		t.Fatal("AssistantPermission override did not reach handler through shared executor")
	}
}
