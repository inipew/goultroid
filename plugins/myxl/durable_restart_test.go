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
func d4MyXLCallbackData(t *testing.T, svc *mockTgService, label string) []byte {
	t.Helper()
	inline, ok := svc.lastMarkup.(*tg.ReplyInlineMarkup)
	if !ok || inline == nil {
		t.Fatalf("MyXL markup = %T, want *tg.ReplyInlineMarkup", svc.lastMarkup)
	}
	for _, row := range inline.Rows {
		for _, button := range row.Buttons {
			callbackButton, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if strings.Contains(callbackButton.Text, label) {
				return append([]byte(nil), callbackButton.Data...)
			}
		}
	}
	t.Fatalf("MyXL callback button containing %q not found", label)
	return nil
}

func openD4MyXLAssistant(t *testing.T, ctx context.Context, p *Plugin, svc *mockTgService, ownerID int64) presentationtelegram.MessageTarget {
	t.Helper()
	peer := &tg.InputPeerUser{UserID: ownerID}
	cmd := &core.Context{
		Ctx:     ctx,
		Source:  core.ExecutionAssistant,
		Message: &core.Message{ID: 1, SenderID: ownerID},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		Svc:     svc,
		PeerID:  peer,
	}
	if err := p.openAssistant(cmd); err != nil {
		t.Fatalf("open MyXL Assistant: %v", err)
	}
	return presentationtelegram.MessageTarget{Peer: peer, ChatID: ownerID, MessageID: 10}
}

func dispatchD4MyXLButton(
	t *testing.T,
	ctx context.Context,
	generation *durableMyXLGeneration,
	svc *mockTgService,
	actorID, queryID int64,
	target presentationtelegram.MessageTarget,
	label string,
) []byte {
	t.Helper()
	data := d4MyXLCallbackData(t, svc, label)
	if err := generation.engine.Dispatch(ctx, orchestration.CallbackRequest{
		Data:    data,
		ActorID: actorID,
		QueryID: queryID,
		Target:  target,
	}); err != nil {
		t.Fatalf("dispatch MyXL button %q: %v", label, err)
	}
	return data
}

func TestD4MyXLSummaryNavigationSurvivesDurableRestart(t *testing.T) {
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

	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	tg1 := &mockTgService{}
	first := newDurableMyXLGeneration(t, p1, tg1, store, scope1, ownerID, false)
	target := openD4MyXLAssistant(t, ctx, p1, tg1, ownerID)
	dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7430, target, "Rincian Kuota")
	oldData := d4MyXLCallbackData(t, tg1, "Kembali ke Ringkasan")
	token, err := rootinteraction.ParseCallbackToken(oldData)
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

	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}
	restored, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatalf("resolve restored MyXL summary navigation: %v", err)
	}
	state := decodeAssistantState(restored.Session.State)
	if restored.Session.Scope != scope2 || !containsMyXLIntent(state.Slots, "myxl:home") {
		t.Fatalf("restored MyXL summary navigation state = %+v scope=%+v", state, restored.Session.Scope)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7431, Target: target}); err != nil {
		t.Fatalf("dispatch restored MyXL home callback: %v", err)
	}
	if !strings.Contains(tg2.sent, "Ringkasan MyXL") || !strings.Contains(tg2.sent, msisdn) {
		t.Fatalf("restored MyXL home view = %q", tg2.sent)
	}
	after, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	afterState := decodeAssistantState(after.Session.State)
	if after.Session.Revision <= restored.Session.Revision || !containsMyXLIntent(afterState.Slots, "myxl:refresh") {
		t.Fatalf("restored MyXL home continuation = %+v revision=%d", afterState, after.Session.Revision)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7432, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed MyXL home callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
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

	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	tg1 := &mockTgService{}
	first := newDurableMyXLGeneration(t, p1, tg1, store, scope1, ownerID, false)
	target := openD4MyXLAssistant(t, ctx, p1, tg1, ownerID)
	dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7440, target, "Paket Favorit")
	oldData := d4MyXLCallbackData(t, tg1, "Store")
	token, err := rootinteraction.ParseCallbackToken(oldData)
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

	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}
	restored, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeAssistantState(restored.Session.State)
	if restored.Session.Scope != scope2 || !state.Sustain || !containsMyXLIntent(state.Slots, "myxl:store") || !containsMyXLIntent(state.Slots, "myxl:home") {
		t.Fatalf("restored MyXL saved-package state = %+v scope=%+v", state, restored.Session.Scope)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7441, Target: target}); err != nil {
		t.Fatalf("dispatch saved-package callback after restart: %v", err)
	}
	if !strings.Contains(tg2.sent, "Pilih Paket MyXL") {
		t.Fatalf("saved-package continuation view = %q", tg2.sent)
	}
	after, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	afterState := decodeAssistantState(after.Session.State)
	if !containsMyXLIntent(afterState.Slots, "myxl:saved") || !containsMyXLIntent(afterState.Slots, "myxl:home") {
		t.Fatalf("saved-package continuation slots = %v", afterState.Slots)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: oldData, ActorID: ownerID, QueryID: 7442, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed MyXL saved-package callback error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}

func TestD4MyXLStillValidConfirmationSurvivesDurableRestart(t *testing.T) {
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

	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	tg1 := &mockTgService{}
	first := newDurableMyXLGeneration(t, p1, tg1, store, scope1, ownerID, false)
	target := openD4MyXLAssistant(t, ctx, p1, tg1, ownerID)
	dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7450, target, "Paket Favorit")
	dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7451, target, "Combo 10GB")
	dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7452, target, "Pulsa")
	confirmData := d4MyXLCallbackData(t, tg1, "Konfirmasi Pembayaran")
	token, err := rootinteraction.ParseCallbackToken(confirmData)
	if err != nil {
		t.Fatal(err)
	}
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 10}
	before, err := first.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := decodeAssistantState(before.Session.State)
	if beforeState.Draft == nil || beforeState.Draft.QuotedPrice != 25000 || !containsMyXLIntent(beforeState.Slots, "myxl:checkout") {
		t.Fatalf("pre-restart MyXL confirmation state = %+v", beforeState)
	}
	assertNoMyXLPurchaseReservations(t, repo)
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

	restored, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatalf("resolve restored MyXL confirmation: %v", err)
	}
	restoredState := decodeAssistantState(restored.Session.State)
	if restored.Session.Scope != scope2 || restoredState.Draft == nil || restoredState.Draft.QuotedPrice != 25000 {
		t.Fatalf("restored MyXL confirmation state = %+v scope=%+v", restoredState, restored.Session.Scope)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: confirmData, ActorID: ownerID, QueryID: 7453, Target: target}); err != nil {
		t.Fatalf("dispatch restored MyXL confirmation: %v", err)
	}
	assertMyXLPurchaseReservations(t, repo, 1, "SUCCESS")
	if !strings.Contains(tg2.sent, "Pembelian Berhasil") {
		t.Fatalf("restored MyXL purchase result = %q", tg2.sent)
	}
	after, err := second.runtime.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatalf("resolve MyXL purchase result: %v", err)
	}
	afterState := decodeAssistantState(after.Session.State)
	if afterState.Draft != nil || after.Session.Revision <= restored.Session.Revision {
		t.Fatalf("restored MyXL purchase result state = %+v revision=%d", afterState, after.Session.Revision)
	}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: confirmData, ActorID: ownerID, QueryID: 7454, Target: target}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed MyXL confirmation error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
	assertMyXLPurchaseReservations(t, repo, 1, "SUCCESS")
}

func TestD4MyXLQuoteDriftRejectsRestoredConfirmation(t *testing.T) {
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

	target := presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: ownerID}, ChatID: ownerID, MessageID: 10}
	if err := second.engine.Dispatch(ctx, orchestration.CallbackRequest{Data: confirmData, ActorID: ownerID, QueryID: 7460, Target: target}); err != nil {
		t.Fatalf("dispatch restored quote-drift confirmation: %v", err)
	}
	assertNoMyXLPurchaseReservations(t, repo)
	if !strings.Contains(tg2.sent, "Harga paket berubah") {
		t.Fatalf("restored confirmation did not revalidate quote: %q", tg2.sent)
	}
	if _, err := second.runtime.Resolve(ctx, confirmation.Session.ID, binding); !errors.Is(err, rootinteraction.ErrNotFound) {
		t.Fatalf("quote-drift confirmation session error = %v, want %v", err, rootinteraction.ErrNotFound)
	}
}

func TestD4MyXLProcessingRestoreDoesNotReplayPurchase(t *testing.T) {
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
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 11}
	intent := purchaseIntentState{MSISDN: msisdn, OptionCode: "OPT-10GB", Method: "balance", QuotedPrice: 25000}

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
	processingRaw, err := encodeAssistantState(assistantState{Draft: &intent, Sustain: false})
	if err != nil {
		t.Fatal(err)
	}
	processing, err := runtime1.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: p1.Name(),
		Binding:   binding,
		State:     processingRaw,
		TTL:       purchaseProcessingTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoMyXLPurchaseReservations(t, repo)
	runtime1.PreserveDurableOnShutdown()
	_ = runtime1.Close()
	registration1.Close()

	p2 := New(repo, p1.client)
	scope2 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 2}
	second := newDurableMyXLGeneration(t, p2, &mockTgService{}, store, scope2, ownerID, true)
	defer second.cleanup()
	defer second.registration.Close()
	defer second.runtime.Close()

	assertNoMyXLPurchaseReservations(t, repo)
	if stats := second.runtime.Stats(); stats.Sessions != 1 || stats.Inputs != 0 {
		t.Fatalf("restored MyXL processing stats = %+v", stats)
	}
	restored, err := second.runtime.Resolve(ctx, processing.Session.ID, binding)
	if err != nil {
		t.Fatalf("resolve restored MyXL processing state: %v", err)
	}
	state := decodeAssistantState(restored.Session.State)
	if restored.Session.Scope != scope2 || len(state.Slots) != 0 || state.Wizard != "" || state.Sustain || state.Draft == nil || state.Draft.QuotedPrice != 25000 {
		t.Fatalf("restored MyXL processing state = %+v scope=%+v", state, restored.Session.Scope)
	}
	assertNoMyXLPurchaseReservations(t, repo)
}

func assertMyXLPurchaseReservations(t *testing.T, repo *SQLiteRepository, wantCount int, wantStatus string) {
	t.Helper()
	var count int
	var status string
	if err := repo.db.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*), COALESCE(MAX(status), '') FROM myxl_purchase_requests",
	).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != wantCount || status != wantStatus {
		t.Fatalf("purchase reservations = %d status=%q, want %d status=%q", count, status, wantCount, wantStatus)
	}
}

func assertNoMyXLPurchaseReservations(t *testing.T, repo *SQLiteRepository) {
	t.Helper()
	assertMyXLPurchaseReservations(t, repo, 0, "")
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
			scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
			tg1 := &mockTgService{}
			first := newDurableMyXLGeneration(t, p1, tg1, store, scope1, ownerID, false)
			target := openD4MyXLAssistant(t, ctx, p1, tg1, ownerID)
			dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7470, target, "Kelola Akun")
			dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7471, target, "Ubah Alias")
			aliasData := dispatchD4MyXLButton(t, ctx, first, tg1, ownerID, 7472, target, msisdn)
			aliasToken, err := rootinteraction.ParseCallbackToken(aliasData)
			if err != nil {
				t.Fatal(err)
			}
			if stats := first.runtime.Stats(); stats.Inputs != 1 {
				t.Fatalf("armed MyXL input stats = %+v", stats)
			}
			first.cleanup()
			first.runtime.PreserveDurableOnShutdown()
			_ = first.runtime.Close()
			first.registration.Close()

			if tc.expireBefore {
				if _, err := repo.db.ExecContext(ctx, "UPDATE interaction_sessions SET input_expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute).UnixNano(), aliasToken.SessionID); err != nil {
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
				if state.Wizard != "alias" || state.MSISDN != msisdn {
					t.Fatalf("restored MyXL input state = %+v", state)
				}
			}
		})
	}
}
