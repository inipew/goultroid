package myxl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

type durableMyXLGeneration struct {
	registry     *feature.Registry
	registration *feature.Registration
	runtime      *rootinteraction.Runtime
	engine       *orchestration.Engine
	cleanup      func()
}

func newDurableMyXLGeneration(
	t *testing.T,
	p *Plugin,
	tgSvc *mockTgService,
	store rootinteraction.DurableStore,
	scope tasks.ScopeIdentity,
	ownerID int64,
	restore bool,
) *durableMyXLGeneration {
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
	return &durableMyXLGeneration{registry: registry, registration: registration, runtime: runtime, engine: engine, cleanup: cleanup}
}

func saveD4MyXLAccount(t *testing.T, repo *SQLiteRepository, msisdn string) {
	t.Helper()
	now := time.Now()
	if err := repo.Save(context.Background(), &Account{
		MSISDN:         msisdn,
		IsActive:       true,
		AccessToken:    "access-token",
		IDToken:        "id-token",
		RefreshToken:   "refresh-token",
		TokenExpiresAt: now.Add(time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatal(err)
	}
}

func containsMyXLIntent(slots []string, want string) bool {
	for _, slot := range slots {
		if slot == want {
			return true
		}
	}
	return false
}

func TestD4MyXLSavedPackageNavigationSurvivesDurableRestart(t *testing.T) {
	const ownerID int64 = 1001
	const msisdn = "6281987654321"
	ctx := context.Background()
	p1, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	defer repo.db.Close()
	if err := database.RunFeatureMigrations(ctx, repo.db, interactionsqlite.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	saveD4MyXLAccount(t, repo, msisdn)
	if err := repo.SavePackage(ctx, &SavedPackage{MSISDN: msisdn, OptionCode: "OPT-10GB", Name: "Combo 10GB", Price: 25000}); err != nil {
		t.Fatal(err)
	}
	store := interactionsqlite.NewStore(repo.db.DB)
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}

	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	first := newDurableMyXLGeneration(t, p1, &mockTgService{}, store, scope1, ownerID, false)
	raw, err := encodeAssistantState(assistantState{Slots: []string{"myxl:saved"}, Sustain: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.runtime.Create(ctx, rootinteraction.CreateRequest{FeatureID: p1.Name(), Binding: binding, State: raw, TTL: assistantTTL})
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := first.runtime.CallbackData(ctx, created.Session.ID, assistantSlotID(0))
	if err != nil {
		t.Fatal(err)
	}
	first.cleanup()
	first.runtime.PreserveDurableOnShutdown()
	_ = first.runtime.Close()
	first.registration.Close()

	p2 := New(repo, p1.client)
	tg2 := &mockTgService{}
	scope2 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 2}
	second := newDurableMyXLGeneration(t, p2, tg2, store, scope2, ownerID, true)
	defer second.cleanup()
	defer second.registration.Close()
	defer second.runtime.Close()

	restored, err := second.runtime.Resolve(ctx, created.Session.ID, binding)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeAssistantState(restored.Session.State)
	if restored.Session.Scope != scope2 || !state.Sustain || !containsMyXLIntent(state.Slots, "myxl:saved") {
		t.Fatalf("restored MyXL navigation state = %+v scope=%+v", state, restored.Session.Scope)
	}
	target := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: ownerID}, ChatID: ownerID, MessageID: 10}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7401, Target: target}); err != nil {
		t.Fatalf("dispatch saved-package callback after restart: %v", err)
	}
	if !strings.Contains(tg2.sent, "Paket Tersimpan") {
		t.Fatalf("saved-package navigation view = %q", tg2.sent)
	}
	after, err := second.runtime.Resolve(ctx, created.Session.ID, binding)
	if err != nil {
		t.Fatal(err)
	}
	afterState := decodeAssistantState(after.Session.State)
	if !containsMyXLIntent(afterState.Slots, "myxl:store") || !containsMyXLIntent(afterState.Slots, "myxl:home") {
		t.Fatalf("saved-package continuation slots = %v", afterState.Slots)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7402, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed MyXL navigation callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}

func TestD4MyXLConfirmationAndProcessingDoNotReplayOnRestore(t *testing.T) {
	const ownerID int64 = 1001
	const msisdn = "6281987654321"
	ctx := context.Background()
	p1, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	defer repo.db.Close()
	if err := database.RunFeatureMigrations(ctx, repo.db, interactionsqlite.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	saveD4MyXLAccount(t, repo, msisdn)
	store := interactionsqlite.NewStore(repo.db.DB)
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}
	intent := purchaseIntentState{MSISDN: msisdn, OptionCode: "OPT-10GB", Method: "balance", QuotedPrice: 24000}

	registry1 := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
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
	confirmationRaw, err := encodeAssistantState(assistantState{
		Slots:   []string{"myxl:checkout", "myxl:cancel_draft"},
		Draft:   &intent,
		Sustain: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := runtime1.Create(ctx, rootinteraction.CreateRequest{FeatureID: p1.Name(), Binding: binding, State: confirmationRaw, TTL: assistantConfirmationTTL})
	if err != nil {
		t.Fatal(err)
	}
	confirmData, err := runtime1.CallbackData(ctx, confirmation.Session.ID, assistantSlotID(0))
	if err != nil {
		t.Fatal(err)
	}
	processingRaw, err := encodeAssistantState(assistantState{Draft: &intent, Sustain: false})
	if err != nil {
		t.Fatal(err)
	}
	processing, err := runtime1.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: p1.Name(),
		Binding:   rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 11},
		State:     processingRaw,
		TTL:       purchaseProcessingTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	processingData, err := runtime1.CallbackData(ctx, processing.Session.ID, assistantSlotID(0))
	if err != nil {
		t.Fatal(err)
	}
	assertNoMyXLPurchaseReservations(t, repo)
	runtime1.PreserveDurableOnShutdown()
	_ = runtime1.Close()
	registration1.Close()

	p2 := New(repo, p1.client)
	tg2 := &mockTgService{}
	scope2 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 2}
	second := newDurableMyXLGeneration(t, p2, tg2, store, scope2, ownerID, true)
	defer second.cleanup()
	defer second.registration.Close()
	defer second.runtime.Close()

	assertNoMyXLPurchaseReservations(t, repo)
	if stats := second.runtime.Stats(); stats.Sessions != 2 {
		t.Fatalf("restored MyXL confirmation/processing sessions = %+v", stats)
	}
	processingTarget := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: ownerID}, ChatID: ownerID, MessageID: 11}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: processingData, ActorID: ownerID, QueryID: 7410, Target: processingTarget}); err != nil {
		t.Fatalf("dispatch restored processing callback: %v", err)
	}
	assertNoMyXLPurchaseReservations(t, repo)

	confirmTarget := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: ownerID}, ChatID: ownerID, MessageID: 10}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: confirmData, ActorID: ownerID, QueryID: 7411, Target: confirmTarget}); err != nil {
		t.Fatalf("dispatch restored confirmation callback: %v", err)
	}
	assertNoMyXLPurchaseReservations(t, repo)
	if !strings.Contains(tg2.sent, "Harga paket berubah") {
		t.Fatalf("restored confirmation did not revalidate quote: %q", tg2.sent)
	}
	if _, err := second.runtime.Resolve(ctx, confirmation.Session.ID, binding); !errors.Is(err, rootinteraction.ErrNotFound) {
		t.Fatalf("quote-drift confirmation session error = %v, want %v", err, rootinteraction.ErrNotFound)
	}
}

func assertNoMyXLPurchaseReservations(t *testing.T, repo *SQLiteRepository) {
	t.Helper()
	var count int
	if err := repo.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM myxl_purchase_requests").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("purchase reservations = %d, want 0", count)
	}
}

func TestD4MyXLPendingInputRestoreHonorsDeadlineAndBinding(t *testing.T) {
	for _, tc := range []struct {
		name          string
		expireBefore  bool
		wantLiveInput bool
	}{
		{name: "live", wantLiveInput: true},
		{name: "expired", expireBefore: true, wantLiveInput: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const ownerID int64 = 1001
			ctx := context.Background()
			p1, server, repo := setupTestMyXLEnv(t)
			defer server.Close()
			defer repo.db.Close()
			if err := database.RunFeatureMigrations(ctx, repo.db, interactionsqlite.MigrationProvider{}); err != nil {
				t.Fatal(err)
			}
			store := interactionsqlite.NewStore(repo.db.DB)
			scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
			first := newDurableMyXLGeneration(t, p1, &mockTgService{}, store, scope1, ownerID, false)

			raw, err := encodeAssistantState(assistantState{Slots: []string{"myxl:alias_req:6281987654321"}, Sustain: true})
			if err != nil {
				t.Fatal(err)
			}
			binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}
			created, err := first.runtime.Create(ctx, rootinteraction.CreateRequest{FeatureID: p1.Name(), Binding: binding, State: raw, TTL: assistantTTL})
			if err != nil {
				t.Fatal(err)
			}
			data, err := first.runtime.CallbackData(ctx, created.Session.ID, assistantSlotID(0))
			if err != nil {
				t.Fatal(err)
			}
			target := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: ownerID}, ChatID: ownerID, MessageID: 10}
			if err := first.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: data, ActorID: ownerID, QueryID: 7420, Target: target}); err != nil {
				t.Fatalf("arm MyXL input: %v", err)
			}
			if stats := first.runtime.Stats(); stats.Inputs != 1 {
				t.Fatalf("armed MyXL input stats = %+v", stats)
			}
			first.cleanup()
			first.runtime.PreserveDurableOnShutdown()
			_ = first.runtime.Close()
			first.registration.Close()

			if tc.expireBefore {
				if _, err := repo.db.ExecContext(ctx, "UPDATE interaction_sessions SET input_expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute).UnixNano(), created.Session.ID); err != nil {
					t.Fatal(err)
				}
			}

			p2 := New(repo, p1.client)
			scope2 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 2}
			second := newDurableMyXLGeneration(t, p2, &mockTgService{}, store, scope2, ownerID, true)
			defer second.cleanup()
			defer second.registration.Close()
			defer second.runtime.Close()

			stats := second.runtime.Stats()
			if got := stats.Inputs == 1; got != tc.wantLiveInput {
				t.Fatalf("restored input live=%v stats=%+v want=%v", got, stats, tc.wantLiveInput)
			}
			if _, handled, err := second.engine.TakeInput(ctx, ownerID+1, ownerID); err != nil || handled {
				t.Fatalf("wrong actor input handled=%v err=%v", handled, err)
			}
			if _, handled, err := second.engine.TakeInput(ctx, ownerID, ownerID+1); err != nil || handled {
				t.Fatalf("wrong chat input handled=%v err=%v", handled, err)
			}
			inputCtx, handled, err := second.engine.TakeInput(ctx, ownerID, ownerID)
			if err != nil {
				t.Fatal(err)
			}
			if handled != tc.wantLiveInput {
				t.Fatalf("matching input handled=%v, want %v", handled, tc.wantLiveInput)
			}
			if handled {
				state := decodeAssistantState(inputCtx.State())
				if state.Wizard != "alias" || state.MSISDN != "6281987654321" {
					t.Fatalf("restored MyXL input state = %+v", state)
				}
			}
		})
	}
}
