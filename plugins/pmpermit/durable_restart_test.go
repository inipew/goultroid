package pmpermit

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	pmpermitsvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestD4PMPermitOwnerToggleSurvivesDurableRestart(t *testing.T) {
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
	svc := pmpermitsvc.NewService(NewSQLiteRepository(db), nil, ownerID, core.NewPermissions(ownerID, nil), zap.NewNop())
	wasEnabled := svc.IsEnabled()
	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: ownerID, MessageID: 100}

	first := New(svc)
	catalog1 := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:pmpermit", Generation: 1}
	registration1, err := catalog1.Register(feature.Owner{ID: first.Name(), Scope: scope1}, first.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	runtime1, err := rootinteraction.NewRuntime(catalog1, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime1.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	telegram1 := &a5NativeTelegram{}
	adapter1, err := nativeinteraction.New(catalog1, runtime1, rootinteraction.NewDispatcher(runtime1), &a5NativeTaskClient{}, func() presentationtelegram.BridgeService { return telegram1 }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	cleanup1, err := first.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter1, Catalog: catalog1, Scope: scope1})
	if err != nil {
		t.Fatal(err)
	}
	cmd := &core.Context{Ctx: ctx, Source: core.ExecutionInteractive, Sender: &core.User{ID: ownerID}, Chat: &core.Chat{ID: ownerID, Type: "private"}, PeerID: peer, Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true}}
	if opened, err := first.openNativePMPermit(cmd); !opened || err != nil {
		t.Fatalf("open PMPermit before restart: opened=%v err=%v", opened, err)
	}
	markup, ok := telegram1.markup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("unexpected PMPermit markup: %T", telegram1.markup)
	}
	oldData := append([]byte(nil), markup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback).Data...)
	token, err := rootinteraction.ParseCallbackToken(oldData)
	if err != nil {
		t.Fatal(err)
	}
	cleanup1()
	runtime1.PreserveDurableOnShutdown()
	if err := runtime1.Close(); err != nil {
		t.Fatal(err)
	}
	registration1.Close()

	second := New(svc)
	catalog2 := feature.NewRegistry()
	scope2 := tasks.ScopeIdentity{Owner: "plugin:pmpermit", Generation: 2}
	registration2, err := catalog2.Register(feature.Owner{ID: second.Name(), Scope: scope2}, second.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration2.Close()
	runtime2, err := rootinteraction.NewRuntime(catalog2, rootinteraction.Config{})
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
	restored, err := runtime2.Resolve(ctx, token.SessionID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.Scope != scope2 || len(restored.Session.State) != 1 || restored.Session.State[0] != 1 {
		t.Fatalf("invalid PMPermit restored state: %+v", restored.Session)
	}
	telegram2 := &a5NativeTelegram{}
	adapter2, err := nativeinteraction.New(catalog2, runtime2, rootinteraction.NewDispatcher(runtime2), &a5NativeTaskClient{}, func() presentationtelegram.BridgeService { return telegram2 }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	cleanup2, err := second.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter2, Catalog: catalog2, Scope: scope2})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	event := &core.CallbackQueryEvent{QueryID: 8803, UserID: ownerID, ChatID: ownerID, MsgID: 100, Data: oldData, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter2.HandleCallback(ctx, event); !handled || err != nil {
		t.Fatalf("restored PMPermit callback: handled=%v err=%v", handled, err)
	}
	if svc.IsEnabled() == wasEnabled {
		t.Fatal("restored PMPermit action did not toggle service")
	}
	stale := *event
	stale.QueryID++
	if handled, err := adapter2.HandleCallback(ctx, &stale); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed PMPermit callback: handled=%v err=%v", handled, err)
	}
	if svc.IsEnabled() == wasEnabled {
		t.Fatal("stale PMPermit callback reverted service")
	}
}
