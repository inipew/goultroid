package client

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/settings"
)

func TestAssistantDirectHelpAndSettingsUseA2Shell(t *testing.T) {
	manager, client, port, _ := newShellEngine(t)
	defer manager.Shutdown()

	legacyHelpCalled := false
	legacySettingsCalled := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.RegisterBatch([]core.Command{
		{
			Name:        "help",
			Aliases:     []string{"h", "commands"},
			Description: "Show help",
			Category:    "Utility",
			Surfaces:    execution.SurfaceAssistant,
			Handler: func(*core.Context) error {
				legacyHelpCalled = true
				return nil
			},
		},
		{
			Name:        "ping",
			Description: "Ping command",
			Category:    "Utility",
			Surfaces:    execution.SurfaceAssistant,
			Handler:     func(*core.Context) error { return nil },
		},
		{
			Name:        "settings",
			Description: "Open settings",
			Category:    "Settings",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceAssistant,
			Handler: func(*core.Context) error {
				legacySettingsCalled = true
				return nil
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	client.SetCoreRouter(coreRouter)

	registry := settings.NewRegistry()
	if err := registry.Register(settings.SettingDefinition{
		Namespace:    "ui",
		Key:          "inline_buttons",
		Type:         settings.TypeBool,
		DefaultValue: "true",
		Category:     settings.CategoryGeneral,
		Title:        "Inline buttons",
	}); err != nil {
		t.Fatal(err)
	}
	client.SetSettingsService(settings.NewService(newShellSettingsRepo(), registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	messageContext := command.MessageContext{
		Chat: core.Chat{ID: 7, Type: "private"},
	}
	if err := client.cmdRouter.DispatchMessageContext(
		context.Background(),
		7,
		peer,
		"/help ping",
		messageContext,
		nil,
	); err != nil {
		t.Fatalf("DispatchMessageContext(/help ping) error = %v", err)
	}
	if legacyHelpCalled {
		t.Fatal("direct /help executed legacy plugin handler")
	}
	if !strings.Contains(port.sent.Text, "/ping") || len(port.sent.Rows) == 0 {
		t.Fatalf("direct /help did not render a2 command detail: %q", port.sent.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("sessions after /help = %d, want 1", got)
	}

	if err := client.cmdRouter.DispatchMessageContext(
		context.Background(),
		7,
		peer,
		"/settings general",
		messageContext,
		nil,
	); err != nil {
		t.Fatalf("DispatchMessageContext(/settings general) error = %v", err)
	}
	if legacySettingsCalled {
		t.Fatal("direct /settings executed legacy plugin handler")
	}
	if !strings.Contains(port.sent.Text, "Settings") || len(port.sent.Rows) == 0 {
		t.Fatalf("direct /settings did not render a2 settings view: %q", port.sent.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 2 {
		t.Fatalf("sessions after /settings = %d, want 2", got)
	}
}
