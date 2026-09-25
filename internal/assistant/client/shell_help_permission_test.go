package client

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

func TestAssistantDirectHelpPreservesPublicCommandPermission(t *testing.T) {
	manager, client, port, _ := newShellEngine(t)
	defer manager.Shutdown()

	legacyCalled := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:        "help",
		Description: "Show help",
		Category:    "Utility",
		Permission:  core.PermissionEveryone,
		Surfaces:    execution.SurfaceAssistant,
		Handler: func(*core.Context) error {
			legacyCalled = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	client.SetCoreRouter(coreRouter)

	const visitorID int64 = 99
	peer := &tg.InputPeerUser{UserID: visitorID}
	err := client.cmdRouter.DispatchMessageContext(
		context.Background(),
		visitorID,
		peer,
		"/help",
		command.MessageContext{Chat: core.Chat{ID: visitorID, Type: "private"}},
		nil,
	)
	if err != nil {
		t.Fatalf("visitor /help error = %v", err)
	}
	if legacyCalled {
		t.Fatal("visitor /help fell back to legacy handler")
	}
	if !strings.Contains(port.sent.Text, "Help") || len(port.sent.Rows) == 0 {
		t.Fatalf("visitor /help did not render canonical a2 Help: %q", port.sent.Text)
	}
}
