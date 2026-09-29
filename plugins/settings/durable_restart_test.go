package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type durableSettingsGeneration struct {
	registry     *feature.Registry
	registration *feature.Registration
	runtime      *rootinteraction.Runtime
	engine       *orchestration.Engine
	cleanup      func()
}

func newDurableSettingsGeneration(
	t *testing.T,
	p *Plugin,
	tgSvc *mockTelegramService,
	store rootinteraction.DurableStore,
	scope tasks.ScopeIdentity,
	ownerID int64,
	restore bool,
) *durableSettingsGeneration {
	t.Helper()
	registry := feature.NewRegistry()
	registration, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		registration.Close()
		t.Fatal(err)
	}
	if err := runtime.SetDurableStore(store); err != nil {
		registration.Close()
		_ = runtime.Close()
		t.Fatal(err)
	}
	if restore {
		if err := runtime.RestoreDurable(context.Background()); err != nil {
			registration.Close()
			_ = runtime.Close()
			t.Fatal(err)
		}
	}
	dispatcher := rootinteraction.NewDispatcher(runtime)
	engine, err := orchestration.New(runtime, dispatcher, presentationtelegram.NewBridge(tgSvc))
	if err != nil {
		registration.Close()
		_ = runtime.Close()
		t.Fatal(err)
	}
	admit := func(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, _ presentation.Target) error {
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
		registration.Close()
		_ = runtime.Close()
		t.Fatal(err)
	}
	return &durableSettingsGeneration{registry: registry, registration: registration, runtime: runtime, engine: engine, cleanup: cleanup}
}

func TestD4SettingsAssistantMutationSurvivesDurableRestart(t *testing.T) {
	const ownerID int64 = 12345
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, interactionsqlite.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	store := interactionsqlite.NewStore(db.DB)

	p1, svc, tg1 := setupTestPlugin(t)
	scope1 := tasks.ScopeIdentity{Owner: "plugin:settings", Generation: 1}
	first := newDurableSettingsGeneration(t, p1, tg1, store, scope1, ownerID, false)

	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}
	cmd := &core.Context{
		Ctx:     ctx,
		Source:  core.ExecutionAssistant,
		Message: &core.Message{ID: 1, SenderID: ownerID},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		Svc:     tg1,
		PeerID:  peer,
		Args:    []string{"security"},
	}
	if err := p1.handleSettingsCommand(cmd); err != nil {
		t.Fatalf("open settings before restart: %v", err)
	}
	oldData := findNativeCallbackData(t, snapshotNativeMarkup(t, tg1), "PM Guard Protection")
	token, err := rootinteraction.ParseCallbackToken(oldData)
	if err != nil {
		t.Fatal(err)
	}

	first.cleanup()
	first.runtime.PreserveDurableOnShutdown()
	_ = first.runtime.Close()
	first.registration.Close()

	p2 := New(svc)
	tg2 := &mockTelegramService{}
	scope2 := tasks.ScopeIdentity{Owner: "plugin:settings", Generation: 2}
	second := newDurableSettingsGeneration(t, p2, tg2, store, scope2, ownerID, true)
	defer second.cleanup()
	defer second.registration.Close()
	defer second.runtime.Close()

	restored, err := second.runtime.Resolve(ctx, token.SessionID, rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 100})
	if err != nil {
		t.Fatalf("resolve restored Settings session: %v", err)
	}
	if restored.Session.Scope != scope2 {
		t.Fatalf("restored Settings scope = %+v, want %+v", restored.Session.Scope, scope2)
	}
	if _, err := decodeNativeSettingsState(restored.Session.State); err != nil {
		t.Fatalf("decode restored Settings state: %v", err)
	}

	target := presentationtelegram.MessageTarget{Peer: peer, ChatID: ownerID, MessageID: 100}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7201, Target: target}); err != nil {
		t.Fatalf("dispatch pre-restart Settings callback: %v", err)
	}
	enabled, err := svc.ResolveBool(ctx, ownerID, 0, "pmpermit", "enabled")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("restored Settings callback did not mutate pmpermit:enabled")
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7202, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed Settings callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}
