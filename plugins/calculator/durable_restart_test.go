package calculator

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type calculatorRestartPort struct {
	edited   presentation.CompiledView
	answered presentation.Answer
}

func (p *calculatorRestartPort) Send(_ context.Context, target presentation.Target, _ presentation.CompiledView) (presentation.Target, error) {
	return target, nil
}

func (p *calculatorRestartPort) Edit(_ context.Context, _ presentation.Target, view presentation.CompiledView) error {
	p.edited = view
	return nil
}

func (p *calculatorRestartPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.answered = answer
	return nil
}

func TestD4CalculatorCallbackStateSurvivesDurableRestart(t *testing.T) {
	const actorID int64 = 7
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
	binding := rootinteraction.Binding{ActorID: actorID, ChatID: actorID, MessageID: 77}

	p1 := New()
	registry1 := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:calculator", Generation: 1}
	registration1, err := registry1.Register(feature.Owner{ID: p1.Name(), Scope: scope1}, p1.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	runtime1, err := rootinteraction.NewRuntime(registry1, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime1.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	created, err := runtime1.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: p1.Name(),
		Binding:   binding,
		State:     []byte("12+"),
		TTL:       interactionTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := runtime1.CallbackData(ctx, created.Session.ID, "key_3")
	if err != nil {
		t.Fatal(err)
	}
	runtime1.PreserveDurableOnShutdown()
	_ = runtime1.Close()
	registration1.Close()

	p2 := New()
	registry2 := feature.NewRegistry()
	scope2 := tasks.ScopeIdentity{Owner: "plugin:calculator", Generation: 2}
	registration2, err := registry2.Register(feature.Owner{ID: p2.Name(), Scope: scope2}, p2.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration2.Close()
	runtime2, err := rootinteraction.NewRuntime(registry2, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime2.Close()
	if err := runtime2.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	if err := runtime2.RestoreDurable(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := runtime2.Resolve(ctx, created.Session.ID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.Scope != scope2 || string(restored.Session.State) != "12+" {
		t.Fatalf("restored calculator session = %+v", restored.Session)
	}

	port := &calculatorRestartPort{}
	dispatcher := rootinteraction.NewDispatcher(runtime2)
	engine, err := orchestration.New(runtime2, dispatcher, port)
	if err != nil {
		t.Fatal(err)
	}
	admit := func(featureID string, kind feature.InteractionKind, interactionID string, userID int64, _ presentation.Target) error {
		if featureID != p2.Name() || userID != actorID {
			return core.ErrPermissionDenied
		}
		if _, ok := registry2.FindInteraction(featureID, kind, interactionID); !ok {
			return core.ErrPermissionDenied
		}
		return nil
	}
	cleanup, err := p2.BindAssistant(assistantinteraction.DriverRuntime{Engine: engine, Catalog: registry2, Admit: admit})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	target := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: actorID}, ChatID: actorID, MessageID: 77}
	if err := engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: actorID, QueryID: 7301, Target: target}); err != nil {
		t.Fatalf("dispatch pre-restart calculator callback: %v", err)
	}
	after, err := runtime2.Resolve(ctx, created.Session.ID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Session.State) != "12+3" || after.Session.Revision != 2 {
		t.Fatalf("calculator state after restored callback = %q rev=%d", after.Session.State, after.Session.Revision)
	}
	if port.edited.Text == "" {
		t.Fatal("restored calculator callback did not render next view")
	}
	if err := engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: actorID, QueryID: 7302, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed calculator callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}
