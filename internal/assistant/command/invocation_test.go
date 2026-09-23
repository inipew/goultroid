package command_test

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

func TestCommandRouter_InvocationPolicyIsSeparateFromPermission(t *testing.T) {
	const ownerID int64 = 100
	const regularID int64 = 300

	r := command.NewRouter(zap.NewNop())
	r.SetOwner(ownerID, nil)

	var privateEveryoneRan bool
	var ownerAnyoneRan bool
	coreRouter := core.NewRouter(".")
	if err := coreRouter.RegisterBatch([]core.Command{
		{
			Name:       "privateeveryone",
			Permission: core.PermissionEveryone,
			Invocation: core.InvocationPolicy{Assistant: core.InvocationSelfOnly},
			Surfaces:   execution.SurfaceAssistant,
			Handler: func(*core.Context) error {
				privateEveryoneRan = true
				return nil
			},
		},
		{
			Name:       "owneranyone",
			Permission: core.PermissionOwner,
			Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
			Surfaces:   execution.SurfaceAssistant,
			Handler: func(*core.Context) error {
				ownerAnyoneRan = true
				return nil
			},
		},
	}); err != nil {
		t.Fatalf("register commands: %v", err)
	}
	r.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	peer := &tg.InputPeerUser{UserID: regularID}
	if err := dispatchTest(r, context.Background(), regularID, peer, "/privateeveryone", fake); err != nil {
		t.Fatalf("dispatch privateeveryone: %v", err)
	}
	if privateEveryoneRan {
		t.Fatal("PermissionEveryone bypassed SelfOnly invocation")
	}
	if fake.lastSentText == "" {
		t.Fatal("expected invocation rejection response")
	}

	fake.lastSentText = ""
	if err := dispatchTest(r, context.Background(), regularID, peer, "/owneranyone", fake); err != nil {
		t.Fatalf("dispatch owneranyone: %v", err)
	}
	if ownerAnyoneRan {
		t.Fatal("InvocationAnyone bypassed PermissionOwner")
	}
	if fake.lastSentText == "" {
		t.Fatal("expected permission rejection response")
	}

	ownerPeer := &tg.InputPeerUser{UserID: ownerID}
	if err := dispatchTest(r, context.Background(), ownerID, ownerPeer, "/privateeveryone", fake); err != nil {
		t.Fatalf("owner dispatch: %v", err)
	}
	if !privateEveryoneRan {
		t.Fatal("owner could not invoke SelfOnly assistant command")
	}
}
