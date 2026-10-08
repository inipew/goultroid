package blacklist

import (
	"context"
	"encoding/json"
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
	"github.com/inipew/goultroid/internal/tasks"
)

func TestD4BlacklistConfirmedRemovalSurvivesDurableRestart(t *testing.T) {
	const ownerID, chatID int64 = 1001, 500
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
	peer := &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 777}
	binding := rootinteraction.Binding{ActorID: ownerID, ChatID: chatID, MessageID: 100}
	first := NewWithMessageDeleter(NewSQLiteRepository(db), nil)
	if err := first.addBlacklistRule(ctx, chatID, "scam"); err != nil {
		t.Fatal(err)
	}
	catalog1 := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:blacklist", Generation: 1}
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
	telegram1 := &a5BlacklistTelegram{}
	adapter1, err := nativeinteraction.New(catalog1, runtime1, rootinteraction.NewDispatcher(runtime1), &a5BlacklistTaskClient{}, func() presentationtelegram.BridgeService { return telegram1 }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	roles := &a5BlacklistRoles{role: core.GroupActorRoleAdministrator}
	adapter1.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup1, err := first.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter1, Catalog: catalog1, Scope: scope1})
	if err != nil {
		t.Fatal(err)
	}
	cmd := &core.Context{Ctx: ctx, Source: core.ExecutionInteractive, Sender: &core.User{ID: ownerID}, Chat: &core.Chat{ID: chatID, Type: "supergroup"}, PeerID: peer, Message: &core.Message{ID: 40, SenderID: ownerID, IsOutgoing: true}}
	if opened, err := first.openNativeBlacklist(cmd); !opened || err != nil {
		t.Fatalf("open blacklist: opened=%v err=%v", opened, err)
	}
	selectData := a5BlacklistCallback(t, telegram1.markup, "scam")
	selectEvent := &core.CallbackQueryEvent{QueryID: 8805, UserID: ownerID, ChatID: chatID, MsgID: 100, Data: selectData, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter1.HandleCallback(ctx, selectEvent); !handled || err != nil {
		t.Fatalf("select blacklist rule: handled=%v err=%v", handled, err)
	}
	confirmData := a5BlacklistCallback(t, telegram1.markup, "Confirm")
	token, err := rootinteraction.ParseCallbackToken(confirmData)
	if err != nil {
		t.Fatal(err)
	}
	cleanup1()
	runtime1.PreserveDurableOnShutdown()
	if err := runtime1.Close(); err != nil {
		t.Fatal(err)
	}
	registration1.Close()

	second := NewWithMessageDeleter(NewSQLiteRepository(db), nil)
	catalog2 := feature.NewRegistry()
	scope2 := tasks.ScopeIdentity{Owner: "plugin:blacklist", Generation: 2}
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
	var state nativeBlacklistState
	if err := json.Unmarshal(restored.Session.State, &state); err != nil {
		t.Fatal(err)
	}
	if restored.Session.Scope != scope2 || state.Scope.ChatID != chatID || state.Selected != "scam" || state.Digest == "" {
		t.Fatalf("invalid restored blacklist session: scope=%+v state=%+v", restored.Session.Scope, state)
	}
	telegram2 := &a5BlacklistTelegram{}
	adapter2, err := nativeinteraction.New(catalog2, runtime2, rootinteraction.NewDispatcher(runtime2), &a5BlacklistTaskClient{}, func() presentationtelegram.BridgeService { return telegram2 }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	adapter2.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup2, err := second.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter2, Catalog: catalog2, Scope: scope2})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	event := &core.CallbackQueryEvent{QueryID: 8806, UserID: ownerID, ChatID: chatID, MsgID: 100, Data: confirmData, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter2.HandleCallback(ctx, event); !handled || err != nil {
		t.Fatalf("restored blacklist confirm: handled=%v err=%v", handled, err)
	}
	if words, err := second.db.ListBlacklists(ctx, chatID); err != nil || len(words) != 0 {
		t.Fatalf("restored blacklist confirm failed: %v (%v)", words, err)
	}
	stale := *event
	stale.QueryID++
	if handled, err := adapter2.HandleCallback(ctx, &stale); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed blacklist callback: handled=%v err=%v", handled, err)
	}
}
