package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestP1F2AssistantSettingsUsesA2AndRejectsStaleReplay(t *testing.T) {
	const ownerID int64 = 12345
	p, svc, tgSvc := setupTestPlugin(t)

	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:settings", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	engine, err := orchestration.New(
		sessions,
		rootinteraction.NewDispatcher(sessions),
		presentationtelegram.NewBridge(tgSvc),
	)
	if err != nil {
		t.Fatal(err)
	}

	admit := func(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error {
		if featureID != p.Name() || actorID != ownerID {
			return core.ErrPermissionDenied
		}
		decl, ok := registry.FindInteraction(featureID, kind, interactionID)
		if !ok || !decl.Surfaces.Supports(execution.SourceAssistant) {
			return core.ErrPermissionDenied
		}
		return nil
	}
	cleanup, err := p.BindAssistant(assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: registry,
		Service: tgSvc,
		Admit:   admit,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}
	cmd := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionAssistant,
		Message: &core.Message{ID: 1, SenderID: ownerID},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		Svc:     tgSvc,
		PeerID:  peer,
		Args:    []string{"security"},
	}
	if err := p.handleSettingsCommand(cmd); err != nil {
		t.Fatalf("open Assistant settings: %v", err)
	}

	markup := snapshotNativeMarkup(t, tgSvc)
	data := findNativeCallbackData(t, markup, "PM Guard Protection")
	if !rootinteraction.OwnsCallbackData(data) {
		t.Fatalf("Assistant Settings emitted non-a2 callback %q", data)
	}
	if _, err := rootinteraction.ParseCallbackToken(data); err != nil {
		t.Fatalf("Assistant Settings emitted invalid a2 token: %v", err)
	}
	if got := sessions.CancelScope(tasks.ScopeIdentity{Owner: "unused", Generation: 99}); got != 0 {
		t.Fatalf("unexpected unrelated scope cancellation count %d", got)
	}

	target := presentationtelegram.MessageTarget{
		Peer:      peer,
		ChatID:    ownerID,
		MessageID: 100,
	}
	prepared, err := engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data:    data,
		ActorID: ownerID,
		QueryID: 7001,
		Target:  target,
	})
	if err != nil {
		t.Fatalf("prepare Assistant settings callback: %v", err)
	}
	if err := prepared.Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch Assistant settings callback: %v", err)
	}
	enabled, err := svc.ResolveBool(context.Background(), ownerID, 0, "pmpermit", "enabled")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("Assistant a2 toggle did not mutate pmpermit:enabled")
	}

	_, err = engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data:    data,
		ActorID: ownerID,
		QueryID: 7002,
		Target:  target,
	})
	if !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale Assistant callback error=%v, want ErrStaleToken", err)
	}

	fresh := snapshotNativeMarkup(t, tgSvc)
	freshData := findNativeCallbackData(t, fresh, "PM Guard Protection")
	_, err = engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data:    freshData,
		ActorID: ownerID + 1,
		QueryID: 7003,
		Target:  target,
	})
	if err == nil {
		t.Fatal("wrong actor unexpectedly prepared Assistant settings action")
	}
}

func TestP1F2SettingsFeatureDeclaresBothA2Surfaces(t *testing.T) {
	p, _, _ := setupTestPlugin(t)
	spec := p.FeatureSpec()
	var (
		nativeScreen    bool
		assistantScreen bool
		nativeAction    bool
		assistantAction bool
	)
	for _, interaction := range spec.Interactions {
		switch interaction.ID {
		case nativeSettingsScreenDashboard:
			nativeScreen = interaction.Surfaces.Supports(execution.SourceUserbot)
		case assistantSettingsScreenDashboard:
			assistantScreen = interaction.Surfaces.Supports(execution.SourceAssistant)
		case nativeSettingsSlotID(0):
			nativeAction = interaction.Surfaces.Supports(execution.SourceUserbot)
		case assistantSettingsSlotID(0):
			assistantAction = interaction.Surfaces.Supports(execution.SourceAssistant)
		}
	}
	if !nativeScreen || !assistantScreen || !nativeAction || !assistantAction {
		t.Fatalf("incomplete Settings a2 surfaces: native screen=%v action=%v assistant screen=%v action=%v",
			nativeScreen, nativeAction, assistantScreen, assistantAction)
	}
}

func TestP1F2AssistantViewDoesNotLeakInternalIntent(t *testing.T) {
	p, _, _ := setupTestPlugin(t)
	raw, view, err := p.assistantSettingsView(context.Background(), MenuState{
		Scope:    "global",
		Category: "security",
		Page:     1,
		OwnerID:  12345,
		ChatID:   12345,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || len(view.Rows) == 0 {
		t.Fatal("Assistant Settings view missing state or actions")
	}
	for _, row := range view.Rows {
		for _, button := range row {
			if !strings.HasPrefix(button.ActionID, "assistant_slot_") {
				t.Fatalf("Assistant Settings action ID=%q, want assistant slot", button.ActionID)
			}
		}
	}
	if strings.Contains(view.Text, "v1:") || strings.Contains(view.Text, "settings:") {
		t.Fatalf("Assistant Settings presentation leaked internal intent: %q", view.Text)
	}
}
