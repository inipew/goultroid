package filters

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
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestD4FiltersConfirmedRemovalSurvivesDurableRestart(t *testing.T) {
	const actorID, chatID int64 = 1001, 500
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, Module, interactionsqlite.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	store := interactionsqlite.NewStore(db.DB)
	first := New(NewSQLiteRepository(db), nil)
	if err := first.db.SaveFilter(ctx, chatID, "scam", savedresponse.NewText("remove me")); err != nil {
		t.Fatal(err)
	}
	roles := &a5FiltersRoles{role: core.GroupActorRoleAdministrator}
	peer := &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 777}
	binding := rootinteraction.Binding{ActorID: actorID, ChatID: chatID, MessageID: 100}

	reg1 := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:filters", Generation: 1}
	registered1, err := reg1.Register(feature.Owner{ID: first.Name(), Scope: scope1}, first.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	runtime1, err := rootinteraction.NewRuntime(reg1, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime1.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	tg1 := &a5FiltersTelegram{}
	adapter1, err := nativeinteraction.New(reg1, runtime1, rootinteraction.NewDispatcher(runtime1), &a5FiltersTaskClient{}, func() presentationtelegram.BridgeService { return tg1 }, core.NewPermissions(actorID, nil))
	if err != nil {
		t.Fatal(err)
	}
	adapter1.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup1, err := first.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter1, Catalog: reg1, Scope: scope1})
	if err != nil {
		t.Fatal(err)
	}
	cmd := &core.Context{Ctx: ctx, Source: core.ExecutionInteractive, Sender: &core.User{ID: actorID}, Message: &core.Message{ID: 40, SenderID: actorID, IsOutgoing: true, TopicID: 55}, Chat: &core.Chat{ID: chatID, Type: "supergroup"}, PeerID: peer}
	if opened, err := first.openNativeFilters(cmd); !opened || err != nil {
		t.Fatalf("open filters: %v %v", opened, err)
	}
	selectData := a5FiltersCallback(t, tg1.markup, "scam")
	selectEvent := &core.CallbackQueryEvent{QueryID: 901, UserID: actorID, ChatID: chatID, MsgID: 100, Data: selectData, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter1.HandleCallback(ctx, selectEvent); !handled || err != nil {
		t.Fatalf("select filter: %v %v", handled, err)
	}
	confirmData := a5FiltersCallback(t, tg1.markup, "Confirm")
	token, err := rootinteraction.ParseCallbackToken(confirmData)
	if err != nil {
		t.Fatal(err)
	}
	cleanup1()
	runtime1.PreserveDurableOnShutdown()
	if err := runtime1.Close(); err != nil {
		t.Fatal(err)
	}
	registered1.Close()

	second := New(NewSQLiteRepository(db), nil)
	reg2 := feature.NewRegistry()
	scope2 := tasks.ScopeIdentity{Owner: "plugin:filters", Generation: 2}
	registered2, err := reg2.Register(feature.Owner{ID: second.Name(), Scope: scope2}, second.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registered2.Close()
	runtime2, err := rootinteraction.NewRuntime(reg2, rootinteraction.Config{})
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
	var state nativeFiltersState
	if err := json.Unmarshal(restored.Session.State, &state); err != nil {
		t.Fatal(err)
	}
	if restored.Session.Scope != scope2 || state.Scope.ChatID != chatID || state.Scope.TopicID != 55 || state.Selected != "scam" || state.Digest == "" {
		t.Fatalf("bad restored state: scope=%+v state=%+v", restored.Session.Scope, state)
	}
	tg2 := &a5FiltersTelegram{}
	adapter2, err := nativeinteraction.New(reg2, runtime2, rootinteraction.NewDispatcher(runtime2), &a5FiltersTaskClient{}, func() presentationtelegram.BridgeService { return tg2 }, core.NewPermissions(actorID, nil))
	if err != nil {
		t.Fatal(err)
	}
	adapter2.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup2, err := second.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter2, Catalog: reg2, Scope: scope2})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	event := &core.CallbackQueryEvent{QueryID: 902, UserID: actorID, ChatID: chatID, MsgID: 100, Data: confirmData, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter2.HandleCallback(ctx, event); !handled || err != nil {
		t.Fatalf("restored confirmed delete: %v %v", handled, err)
	}
	left, err := second.db.ListFilters(ctx, chatID)
	if err != nil || len(left) != 0 {
		t.Fatalf("restored filter delete did not persist: %v %v", left, err)
	}
	replay := *event
	replay.QueryID++
	if handled, err := adapter2.HandleCallback(ctx, &replay); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replayed old confirmation: %v %v", handled, err)
	}
}
