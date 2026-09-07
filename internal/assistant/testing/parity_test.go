package testing

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

func TestParity_SingleCommandMultipleSurfaces(t *testing.T) {
	ctx := context.Background()

	coreRouter := core.NewRouter(".")

	var executedSource core.ExecutionSource
	var replySent string

	err := coreRouter.Register(core.Command{
		Name:        "uniping",
		Description: "Unified ping command",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		Permission:  core.PermissionEveryone,
		Handler: func(c *core.Context) error {
			executedSource = c.Source
			replySent = "🏓 unified pong"
			return c.Reply(replySent)
		},
	})
	if err != nil {
		t.Fatalf("failed to register uniping: %v", err)
	}

	// 1. Execute on Assistant Surface
	asstRouter := command.NewRouter(zap.NewNop())
	asstRouter.SetCoreRouter(coreRouter)

	fakeAsst := NewFakeInteraction()
	peer := &tg.InputPeerUser{UserID: 12345}

	err = asstRouter.Dispatch(ctx, 12345, peer, "/uniping", fakeAsst)
	if err != nil {
		t.Fatalf("assistant dispatch failed: %v", err)
	}
	if executedSource != core.ExecutionAssistant {
		t.Fatalf("expected ExecutionAssistant, got %v", executedSource)
	}
	if len(fakeAsst.SentMessages) == 0 || fakeAsst.SentMessages[len(fakeAsst.SentMessages)-1] != "🏓 unified pong" {
		t.Fatalf("expected '🏓 unified pong', got %v", fakeAsst.SentMessages)
	}

	// 2. Execute on Userbot Surface via core.CommandExecutor
	executor := core.NewCommandExecutor(zap.NewNop(), core.NewCooldownTracker(), 0)
	userbotCmd, exists := coreRouter.Find("uniping")
	if !exists {
		t.Fatalf("uniping not found in core router")
	}

	mockServicer := &core.MockTelegramServicer{}
	userbotExec := core.CommandExecution{
		Ctx:     ctx,
		Source:  core.ExecutionInteractive,
		Command: "uniping",
		PeerID:  &tg.InputPeerSelf{},
	}

	err = executor.ExecuteExecution(userbotExec, userbotCmd, mockServicer)
	if err != nil {
		t.Fatalf("userbot execution failed: %v", err)
	}
	if executedSource != core.ExecutionInteractive {
		t.Fatalf("expected ExecutionInteractive, got %v", executedSource)
	}
}

func TestParity_SurfaceFiltering(t *testing.T) {
	ctx := context.Background()
	coreRouter := core.NewRouter(".")

	// Userbot-only command
	_ = coreRouter.Register(core.Command{
		Name:     "useronly",
		Surfaces: execution.SurfaceUserbot,
		Handler: func(c *core.Context) error {
			return c.Reply("user only")
		},
	})

	asstRouter := command.NewRouter(zap.NewNop())
	asstRouter.SetCoreRouter(coreRouter)

	fakeAsst := NewFakeInteraction()
	peer := &tg.InputPeerUser{UserID: 12345}

	err := asstRouter.Dispatch(ctx, 12345, peer, "/useronly", fakeAsst)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("expected ErrUnknownCommand for userbot-only command on assistant, got %v", err)
	}
}

func TestParity_PermissionFailClosed_OwnerZero(t *testing.T) {
	ctx := context.Background()
	coreRouter := core.NewRouter(".")

	adminRan := false
	_ = coreRouter.Register(core.Command{
		Name:       "owneronly",
		Permission: core.PermissionOwner,
		Surfaces:   execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			adminRan = true
			return nil
		},
	})

	asstRouter := command.NewRouter(zap.NewNop())
	asstRouter.SetCoreRouter(coreRouter)
	// ownerID is 0 (unconfigured)
	asstRouter.SetOwner(0, nil)

	fakeAsst := NewFakeInteraction()
	peer := &tg.InputPeerUser{UserID: 12345}

	_ = asstRouter.Dispatch(ctx, 12345, peer, "/owneronly", fakeAsst)
	if adminRan {
		t.Fatalf("owner-only command must fail-closed when ownerID is unconfigured (0)")
	}
}
