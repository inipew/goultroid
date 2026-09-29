package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type durableShellGeneration struct {
	registry     *feature.Registry
	registration *feature.Registration
	runtime      *rootinteraction.Runtime
	client       *AssistantClient
	port         *shellTestPort
	engine       *orchestration.Engine
}

func newDurableShellGeneration(
	t *testing.T,
	store rootinteraction.DurableStore,
	scope tasks.ScopeIdentity,
	restore bool,
) *durableShellGeneration {
	t.Helper()

	registry := feature.NewRegistry()
	registration, err := registry.Register(feature.Owner{ID: assistantshell.FeatureID, Scope: scope}, assistantshell.NewFeature().FeatureSpec())
	if err != nil {
		t.Fatalf("register shell feature: %v", err)
	}
	runtime, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		registration.Close()
		t.Fatalf("new interaction runtime: %v", err)
	}
	if err := runtime.SetDurableStore(store); err != nil {
		registration.Close()
		_ = runtime.Close()
		t.Fatalf("set durable store: %v", err)
	}
	if restore {
		if err := runtime.RestoreDurable(context.Background()); err != nil {
			registration.Close()
			_ = runtime.Close()
			t.Fatalf("restore durable shell sessions: %v", err)
		}
	}
	port := &shellTestPort{}
	dispatcher := rootinteraction.NewDispatcher(runtime)
	engine, err := orchestration.New(runtime, dispatcher, port)
	if err != nil {
		registration.Close()
		_ = runtime.Close()
		t.Fatalf("new shell orchestration engine: %v", err)
	}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	client.SetInteractionFoundation(registry, runtime, dispatcher)
	client.interactionIngress = &interactionIngress{engine: engine, ack: newInteractionPresentationServicer(nil)}
	if err := client.syncShellActions(engine, registry); err != nil {
		registration.Close()
		_ = runtime.Close()
		t.Fatalf("sync shell actions: %v", err)
	}
	return &durableShellGeneration{
		registry:     registry,
		registration: registration,
		runtime:      runtime,
		client:       client,
		port:         port,
		engine:       engine,
	}
}

func TestD4AssistantShellHelpSurvivesDurableRestart(t *testing.T) {
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

	scope1 := tasks.ScopeIdentity{Owner: "plugin:" + assistantshell.FeatureID, Generation: 1}
	first := newDurableShellGeneration(t, store, scope1, false)

	router := core.NewRouter(".")
	if err := router.RegisterBatch([]core.Command{
		{Name: "alpha", Category: "General", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "beta", Category: "Utility", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
	}); err != nil {
		t.Fatal(err)
	}
	first.client.SetCoreRouter(router)

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, first.engine, first.port, peer)
	if err := dispatchShell(t, first.engine, callbackForAction(t, first.port.sent, assistantshell.ActionHelp), 9101, peer); err != nil {
		t.Fatalf("open help before restart: %v", err)
	}
	oldModule := callbackForAction(t, first.port.edited, assistantshell.HelpModuleSlotActionIDs()[0])
	token, err := rootinteraction.ParseCallbackToken(oldModule)
	if err != nil {
		t.Fatal(err)
	}
	before, err := first.runtime.Resolve(ctx, token.SessionID, rootinteraction.Binding{ActorID: 7, ChatID: 7, MessageID: 77})
	if err != nil {
		t.Fatal(err)
	}
	if got := assistantshell.DecodeState(before.Session.State).Screen; got != assistantshell.ScreenHelp {
		t.Fatalf("pre-restart shell screen = %v, want help", got)
	}

	first.client.clearShellActions()
	first.runtime.PreserveDurableOnShutdown()
	_ = first.runtime.Close()
	first.registration.Close()

	scope2 := tasks.ScopeIdentity{Owner: "plugin:" + assistantshell.FeatureID, Generation: 2}
	second := newDurableShellGeneration(t, store, scope2, true)
	defer second.client.clearShellActions()
	defer second.registration.Close()
	defer second.runtime.Close()
	second.client.SetCoreRouter(router)

	restored, err := second.runtime.Resolve(ctx, token.SessionID, rootinteraction.Binding{ActorID: 7, ChatID: 7, MessageID: 77})
	if err != nil {
		t.Fatalf("resolve restored shell session: %v", err)
	}
	if restored.Session.Scope != scope2 {
		t.Fatalf("restored shell scope = %+v, want %+v", restored.Session.Scope, scope2)
	}
	if got := assistantshell.DecodeState(restored.Session.State).Screen; got != assistantshell.ScreenHelp {
		t.Fatalf("restored shell screen = %v, want help", got)
	}

	if err := dispatchShell(t, second.engine, oldModule, 9102, peer); err != nil {
		t.Fatalf("dispatch pre-restart help callback after restore: %v", err)
	}
	if !strings.Contains(second.port.edited.Text, "GoUltroid Help") {
		t.Fatalf("restored help callback did not render help view: %q", second.port.edited.Text)
	}
	if err := dispatchShell(t, second.engine, oldModule, 9103, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed pre-restart callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}
