package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/settings"
)

func directAssistantMessageContext() command.MessageContext {
	return command.MessageContext{Chat: core.Chat{ID: 7, Type: "private"}}
}

func TestAssistantDirectHelpNavigationStaysOnA2(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	legacyHelpCalled := false
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
	}); err != nil {
		t.Fatal(err)
	}
	client.SetCoreRouter(coreRouter)

	peer := &tg.InputPeerUser{UserID: 7}
	if err := client.cmdRouter.DispatchMessageContext(
		context.Background(),
		7,
		peer,
		"/help",
		directAssistantMessageContext(),
		nil,
	); err != nil {
		t.Fatalf("DispatchMessageContext(/help) error = %v", err)
	}
	if legacyHelpCalled {
		t.Fatal("direct /help executed legacy plugin handler")
	}
	if !strings.Contains(port.sent.Text, "Help") {
		t.Fatalf("direct /help root text = %q", port.sent.Text)
	}

	module := callbackForAction(t, port.sent, assistantshell.HelpModuleSlotActionIDs()[0])
	if err := dispatchShell(t, engine, module, 710, peer); err != nil {
		t.Fatalf("Dispatch(help module) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Utility") {
		t.Fatalf("help module view = %q", port.edited.Text)
	}

	commandDetail := callbackForAction(t, port.edited, assistantshell.HelpCommandSlotActionIDs()[0])
	if err := dispatchShell(t, engine, commandDetail, 711, peer); err != nil {
		t.Fatalf("Dispatch(help command) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Command") {
		t.Fatalf("help command detail = %q", port.edited.Text)
	}

	back := callbackForAction(t, port.edited, assistantshell.ActionHelpBack)
	if err := dispatchShell(t, engine, back, 712, peer); err != nil {
		t.Fatalf("Dispatch(help back) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Utility") {
		t.Fatalf("help back did not restore module view: %q", port.edited.Text)
	}
}

func TestAssistantDirectSettingsMutationIsRevisionFenced(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	legacySettingsCalled := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:        "settings",
		Description: "Open settings",
		Category:    "Settings",
		Permission:  core.PermissionOwner,
		Surfaces:    execution.SurfaceAssistant,
		Handler: func(*core.Context) error {
			legacySettingsCalled = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	client.SetCoreRouter(coreRouter)

	registry := settings.NewRegistry()
	definition := settings.SettingDefinition{
		Namespace:    "core",
		Key:          "prefix",
		Type:         settings.TypeString,
		DefaultValue: ".",
		Title:        "Prefix",
		Category:     settings.CategoryGeneral,
	}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	repo := newShellSettingsRepo()
	if err := repo.SetSetting(context.Background(), &settings.SettingItem{
		ScopeType: string(settings.ScopeUser),
		ScopeID:   7,
		Namespace: "core",
		Key:       "prefix",
		ValueType: string(settings.TypeString),
		Value:     "!",
	}); err != nil {
		t.Fatal(err)
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	openDirect := func() {
		t.Helper()
		if err := client.cmdRouter.DispatchMessageContext(
			context.Background(),
			7,
			peer,
			"/settings general",
			directAssistantMessageContext(),
			nil,
		); err != nil {
			t.Fatalf("DispatchMessageContext(/settings general) error = %v", err)
		}
		if legacySettingsCalled {
			t.Fatal("direct /settings executed legacy plugin handler")
		}
	}
	openResetConfirmation := func(queryID int64) []byte {
		t.Helper()
		setting := callbackForAction(t, port.sent, assistantshell.SettingSlotActionIDs()[0])
		if err := dispatchShell(t, engine, setting, queryID, peer); err != nil {
			t.Fatalf("Dispatch(setting slot) error = %v", err)
		}
		reset := callbackForAction(t, port.edited, assistantshell.ActionSettingReset)
		if err := dispatchShell(t, engine, reset, queryID+1, peer); err != nil {
			t.Fatalf("Dispatch(reset opener) error = %v", err)
		}
		return callbackForAction(t, port.edited, assistantshell.ActionSettingResetConfirm)
	}

	openDirect()
	staleConfirm := openResetConfirmation(720)
	definition.Description = "Schema revision changed while confirmation was open."
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := dispatchShell(t, engine, staleConfirm, 722, peer); !errors.Is(err, ErrShellSettingBindingStale) {
		t.Fatalf("stale direct /settings confirm error = %v, want %v", err, ErrShellSettingBindingStale)
	}
	value, err := repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, "core", "prefix")
	if err != nil || value == nil || value.Value != "!" {
		t.Fatalf("stale confirmation mutated setting: value=%+v err=%v", value, err)
	}

	openDirect()
	freshConfirm := openResetConfirmation(723)
	if err := dispatchShell(t, engine, freshConfirm, 725, peer); err != nil {
		t.Fatalf("fresh direct /settings reset error = %v", err)
	}
	value, err = repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, "core", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("fresh revision-fenced reset retained override: %+v", value)
	}
}
