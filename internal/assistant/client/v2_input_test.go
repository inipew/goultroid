package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/settings"
)

func registerStringSetting(t *testing.T, registry *settings.Registry, namespace, key, defaultValue string, sensitive bool) (*settings.SettingDefinition, uint64) {
	t.Helper()
	if err := registry.Register(settings.SettingDefinition{
		Namespace: namespace, Key: key, Type: settings.TypeString, DefaultValue: defaultValue,
		Title: "Text Value", Description: "Free-form string setting.", Category: settings.CategoryGeneral,
		Sensitive: sensitive, UI: settings.UIHint{Widget: settings.WidgetText},
	}); err != nil {
		t.Fatalf("Register(%s:%s) error = %v", namespace, key, err)
	}
	def, version, ok := registry.GetVersioned(namespace, key)
	if !ok || def == nil || version == 0 {
		t.Fatalf("versioned definition %s:%s missing", namespace, key)
	}
	return def, version
}

func beginBoundStringInput(t *testing.T, engine *orchestration.Engine, port *shellTestPort, peer tg.InputPeerClass, def *settings.SettingDefinition, version uint64, current, source string, explicit bool) []byte {
	t.Helper()
	state := assistantshell.OpenCategoryState(assistantshell.InitialState(), 1)
	state = assistantshell.OpenSettingState(state, 1)
	state = assistantshell.BindSettingState(state, def.Namespace, def.Key, version)
	_, err := engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: assistantshell.FeatureID,
		ActorID:   7,
		State:     state,
		Target:    presentationtelegram.MessageTarget{Peer: peer, ChatID: 7},
		View: assistantshell.SettingDetailView(assistantshell.SettingDetailModel{
			Definition:   *def,
			Current:      current,
			Source:       source,
			ExplicitUser: explicit,
		}),
	})
	if err != nil {
		t.Fatalf("Begin(bound string) error = %v", err)
	}
	return callbackForAction(t, port.sent, assistantshell.ActionSettingInput)
}

func armBoundStringInput(t *testing.T, client *AssistantClient, engine *orchestration.Engine, port *shellTestPort, peer tg.InputPeerClass, def *settings.SettingDefinition, version uint64, current, source string, explicit bool, queryID int64) *interactionIngress {
	t.Helper()
	button := beginBoundStringInput(t, engine, port, peer, def, version, current, source, explicit)
	if err := dispatchShell(t, engine, button, queryID, peer); err != nil {
		t.Fatalf("Dispatch(setting input) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Send the new value") {
		t.Fatalf("input prompt not rendered: %q", port.edited.Text)
	}
	return &interactionIngress{engine: engine, input: client.handleInteractionTextInput}
}

func TestAssistantShellStringInputEndToEnd(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := newShellSettingsRepo()
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 600)
	if stats := manager.InteractionRuntime().Stats(); stats.Inputs != 1 || stats.Sessions != 1 {
		t.Fatalf("armed stats = %+v", stats)
	}

	if handled, err := ingress.tryText(context.Background(), "/start", 7, 7, peer); err != nil || handled {
		t.Fatalf("slash command handled=%v err=%v", handled, err)
	}
	if manager.InteractionRuntime().Stats().Inputs != 1 {
		t.Fatal("slash command consumed pending input")
	}

	handled, err := ingress.tryText(context.Background(), "!", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("tryText() handled=%v err=%v", handled, err)
	}
	item, err := repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, "core", "prefix")
	if err != nil || item == nil || item.Value != "!" {
		t.Fatalf("persisted setting = %+v err=%v", item, err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Inputs != 0 || stats.Sessions != 1 {
		t.Fatalf("completed stats = %+v", stats)
	}
	if !strings.Contains(port.edited.Text, "<code>!</code>") || !strings.Contains(port.edited.Text, "User override saved.") {
		t.Fatalf("completed detail not rendered: %q", port.edited.Text)
	}
}

func TestAssistantShellInvalidStringInputRearmsSameSession(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := newShellSettingsRepo()
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 610)
	handled, err := ingress.tryText(context.Background(), "   ", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("invalid tryText() handled=%v err=%v", handled, err)
	}
	if manager.InteractionRuntime().Stats().Inputs != 1 {
		t.Fatal("invalid input was not re-armed")
	}
	if !strings.Contains(port.edited.Text, "Value cannot be empty.") {
		t.Fatalf("invalid input recovery view = %q", port.edited.Text)
	}

	handled, err = ingress.tryText(context.Background(), "ok", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("retry tryText() handled=%v err=%v", handled, err)
	}
	item, _ := repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, "core", "prefix")
	if item == nil || item.Value != "ok" {
		t.Fatalf("retry persisted item = %+v", item)
	}
}

func TestAssistantShellStringInputCancelButtonClearsClaim(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := &mutationTrackingRepo{shellSettingsRepo: newShellSettingsRepo()}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 615)
	cancel := callbackForAction(t, port.edited, assistantshell.ActionSettingInputCancel)
	if err := dispatchShell(t, engine, cancel, 616, peer); err != nil {
		t.Fatalf("Dispatch(cancel input) error = %v", err)
	}
	if repo.setCalls != 0 || manager.InteractionRuntime().Stats().Inputs != 0 {
		t.Fatalf("cancel button wrote or retained claim: setCalls=%d stats=%+v", repo.setCalls, manager.InteractionRuntime().Stats())
	}
	if !strings.Contains(port.edited.Text, "Input cancelled.") {
		t.Fatalf("cancel button detail not rendered: %q", port.edited.Text)
	}
}

func TestAssistantShellStringInputCancelConsumesClaimWithoutWrite(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := &mutationTrackingRepo{shellSettingsRepo: newShellSettingsRepo()}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 620)
	handled, err := ingress.tryText(context.Background(), "/cancel", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("cancel handled=%v err=%v", handled, err)
	}
	if repo.setCalls != 0 || manager.InteractionRuntime().Stats().Inputs != 0 {
		t.Fatalf("cancel wrote or retained claim: setCalls=%d stats=%+v", repo.setCalls, manager.InteractionRuntime().Stats())
	}
	if !strings.Contains(port.edited.Text, "Input cancelled.") {
		t.Fatalf("cancel detail not rendered: %q", port.edited.Text)
	}
}

func TestAssistantShellStringInputPersistenceFailureRearms(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := &mutationTrackingRepo{
		shellSettingsRepo: newShellSettingsRepo(),
		setErr:            errors.New("storage unavailable"),
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 630)
	handled, err := ingress.tryText(context.Background(), "x", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("persist failure handled=%v err=%v", handled, err)
	}
	if repo.setCalls != 1 || manager.InteractionRuntime().Stats().Inputs != 1 {
		t.Fatalf("persistence recovery setCalls=%d stats=%+v", repo.setCalls, manager.InteractionRuntime().Stats())
	}
	if !strings.Contains(port.edited.Text, "Save failed. Send the value again to retry.") {
		t.Fatalf("persistence recovery view = %q", port.edited.Text)
	}

	repo.setErr = nil
	handled, err = ingress.tryText(context.Background(), "x", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("recovered input handled=%v err=%v", handled, err)
	}
}

func TestAssistantShellStringInputSchemaChangeFailsClosed(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	repo := &mutationTrackingRepo{shellSettingsRepo: newShellSettingsRepo()}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 640)
	if err := registry.SetDefault("core", "prefix", "/"); err != nil {
		t.Fatalf("SetDefault() error = %v", err)
	}
	handled, err := ingress.tryText(context.Background(), "!", 7, 7, peer)
	if !handled {
		t.Fatal("stale schema input was not claimed")
	}
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Stage != assistantshell.MutationStageBinding {
		t.Fatalf("schema change error = %+v raw=%v", mutationErr, err)
	}
	if repo.setCalls != 0 {
		t.Fatalf("stale schema reached persistence: %d", repo.setCalls)
	}
	if got := interactionTextInputErrorMessage(err); !strings.Contains(got, "changed while input was pending") {
		t.Fatalf("schema feedback = %q", got)
	}
}

func TestAssistantShellStringInputRenderFailureAfterCommitDoesNotRearm(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "security", "token", "default-secret", true)
	repo := &mutationTrackingRepo{shellSettingsRepo: newShellSettingsRepo()}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, "default-secret", "Default", false, 650)
	port.editErr = errors.New("telegram edit failed")
	handled, err := ingress.tryText(context.Background(), "runtime-secret", 7, 7, peer)
	if !handled {
		t.Fatal("input was not claimed")
	}
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Stage != assistantshell.MutationStageRender || !mutationErr.Committed {
		t.Fatalf("render failure = %+v raw=%v", mutationErr, err)
	}
	if mutationErr.Result.Previous != "••••" || mutationErr.Result.Persisted != "••••" || mutationErr.Result.Effective != "••••" {
		t.Fatalf("sensitive mutation result leaked: %+v", mutationErr.Result)
	}
	if manager.InteractionRuntime().Stats().Inputs != 0 {
		t.Fatal("committed render failure re-armed input")
	}
	item, _ := repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, "security", "token")
	if item == nil || item.Value != "runtime-secret" {
		t.Fatalf("committed sensitive value = %+v", item)
	}
	if got := interactionTextInputErrorMessage(err); !strings.Contains(got, "was saved") {
		t.Fatalf("render-failure feedback = %q", got)
	}
}

func TestAssistantShellStringInputGenerationCleanupDropsClaim(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerStringSetting(t, registry, "core", "prefix", ".", false)
	client.SetSettingsService(settings.NewService(newShellSettingsRepo(), registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	ingress := armBoundStringInput(t, client, engine, port, peer, def, version, ".", "Default", false, 660)
	if manager.InteractionRuntime().Stats().Inputs != 1 {
		t.Fatal("input claim not armed")
	}
	if err := manager.Disable(context.Background(), assistantshell.FeatureID); err != nil {
		t.Fatalf("Disable(shell) error = %v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Inputs != 0 || stats.Sessions != 0 {
		t.Fatalf("generation cleanup stats = %+v", stats)
	}
	if handled, err := ingress.tryText(context.Background(), "ignored", 7, 7, peer); err != nil || handled {
		t.Fatalf("stale ingress handled=%v err=%v", handled, err)
	}
}

func TestV2TextInputExpiredFeedback(t *testing.T) {
	if got := interactionTextInputErrorMessage(rootinteraction.ErrInputExpired); !strings.Contains(got, "expired") {
		t.Fatalf("expired feedback = %q", got)
	}
}
