package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

func TestCommandRouterPresentationOverrideShadowsCanonicalEntry(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	legacyCalled := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:     "help",
		Aliases:  []string{"h"},
		Surfaces: execution.SurfaceAssistant,
		Handler: func(*core.Context) error {
			legacyCalled = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	overrideCalled := false
	r.RegisterPresentationOverride("/help", func(c *command.Context) error {
		overrideCalled = true
		_, err := c.Reply("a2-help", nil)
		return err
	})

	fake := &fakeInteraction{}
	if err := dispatchTest(
		r,
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/help",
		fake,
	); err != nil {
		t.Fatal(err)
	}
	if !overrideCalled {
		t.Fatal("presentation override was not called")
	}
	if legacyCalled {
		t.Fatal("canonical handler ran despite explicit presentation override")
	}
	if fake.lastSentText != "a2-help" {
		t.Fatalf("response = %q, want a2-help", fake.lastSentText)
	}
}

func TestCommandRouterPresentationOverrideRequiresCanonicalCommand(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	r.RegisterPresentationOverride("/missing", func(*command.Context) error {
		t.Fatal("orphaned presentation override should not run")
		return nil
	})

	err := dispatchTest(
		r,
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/missing",
		&fakeInteraction{},
	)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("Dispatch(missing) error = %v, want %v", err, command.ErrUnknownCommand)
	}
}
