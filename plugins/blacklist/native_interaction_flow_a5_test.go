package blacklist

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type a5BlacklistTaskClient struct{}

func (*a5BlacklistTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	err := spec.Handler(ctx)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err != nil {
		result.Outcome = tasks.OutcomeFailed
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	return nil, nil
}
func (*a5BlacklistTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*a5BlacklistTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*a5BlacklistTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type a5BlacklistTelegram struct {
	core.MockTelegramServicer
	markup tg.ReplyMarkupClass
}

func (m *a5BlacklistTelegram) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, _ string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.markup = markup
	return &tg.Message{ID: 100}, nil
}
func (m *a5BlacklistTelegram) EditMessageMarkup(_ context.Context, _ tg.InputPeerClass, _ int, _ string, markup tg.ReplyMarkupClass) error {
	m.markup = markup
	return nil
}

type a5BlacklistRoles struct {
	role     core.GroupActorRole
	requests []core.GroupRoleRequest
}

func (r *a5BlacklistRoles) ResolveGroupRole(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.ResolveGroupRoleFresh(context.Background(), req)
}
func (r *a5BlacklistRoles) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.requests = append(r.requests, req)
	return core.GroupRoleSnapshot{Principal: core.GroupActorPrincipal{UserID: req.UserID, Verified: true, Role: r.role, Rights: core.GroupAdminRights{DeleteMessages: true}}}, nil
}

func a5BlacklistCallback(t *testing.T, markup tg.ReplyMarkupClass, label string) []byte {
	t.Helper()
	rows, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("expected inline a2 markup, got %T", markup)
	}
	for _, row := range rows.Rows {
		for _, button := range row.Buttons {
			cb, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if !rootinteraction.OwnsCallbackData(cb.Data) {
				t.Fatalf("non-a2 callback: %q", cb.Data)
			}
			if strings.Contains(cb.Text, label) {
				return append([]byte(nil), cb.Data...)
			}
		}
	}
	t.Fatalf("callback %q missing", label)
	return nil
}

func TestA5C2BlacklistA2DemotionAndChatBinding(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	if err := p.addBlacklistRule(ctx, 500, "scam"); err != nil {
		t.Fatal(err)
	}
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:blacklist", Generation: 1}
	reg, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	transport := &a5BlacklistTelegram{}
	adapter, err := nativeinteraction.New(registry, sessions, rootinteraction.NewDispatcher(sessions),
		&a5BlacklistTaskClient{}, func() presentationtelegram.BridgeService { return transport }, core.NewPermissions(1001, nil))
	if err != nil {
		t.Fatal(err)
	}
	roles := &a5BlacklistRoles{role: core.GroupActorRoleAdministrator}
	adapter.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter, Catalog: registry, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := &core.Context{Ctx: ctx, Source: core.ExecutionInteractive, Sender: &core.User{ID: 1001},
		Message: &core.Message{ID: 40, SenderID: 1001, IsOutgoing: true},
		Chat:    &core.Chat{ID: 500, Type: "supergroup"}, PeerID: &tg.InputPeerChannel{ChannelID: 500, AccessHash: 777}}
	opened, err := p.openNativeBlacklist(cmd)
	if !opened || err != nil {
		t.Fatalf("open native manager: opened=%v err=%v", opened, err)
	}
	selected := a5BlacklistCallback(t, transport.markup, "scam")
	event := &core.CallbackQueryEvent{QueryID: 900, UserID: 1001, ChatID: 500, MsgID: 100, Data: selected, Origin: core.CallbackOriginMessage,
		Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: &tg.InputPeerChannel{ChannelID: 500, AccessHash: 777}, MessageID: 100}}
	handled, err := adapter.HandleCallback(ctx, event)
	if !handled || err != nil {
		t.Fatalf("select: handled=%v err=%v", handled, err)
	}
	confirm := a5BlacklistCallback(t, transport.markup, "Confirm")
	if len(roles.requests) < 2 {
		t.Fatal("fresh group role not queried for both opening and selection")
	}
	roles.role = core.GroupActorRoleMember
	demoted := *event
	demoted.QueryID++
	demoted.Data = confirm
	handled, err = adapter.HandleCallback(ctx, &demoted)
	if !handled || err != nil {
		t.Fatalf("demoted confirmation was not handled: handled=%v err=%v", handled, err)
	}
	if rules, err := p.db.ListBlacklists(ctx, 500); err != nil || len(rules) != 1 {
		t.Fatalf("demoted user removed a rule: %v (%v)", rules, err)
	}
	// Even a still-valid token cannot cross to another chat.
	forged := demoted
	forged.QueryID++
	forged.ChatID = 600
	forged.Target.Peer = &tg.InputPeerChannel{ChannelID: 600, AccessHash: 777}
	_, _ = adapter.HandleCallback(ctx, &forged)
	if rules, err := p.db.ListBlacklists(ctx, 500); err != nil || len(rules) != 1 {
		t.Fatalf("cross chat callback removed a rule: %v (%v)", rules, err)
	}
	roles.role = core.GroupActorRoleAdministrator
	restored := demoted
	restored.QueryID = 903
	handled, err = adapter.HandleCallback(ctx, &restored)
	if !handled || err != nil {
		t.Fatalf("restored admin confirmation: handled=%v err=%v", handled, err)
	}
	if rules, err := p.db.ListBlacklists(ctx, 500); err != nil || len(rules) != 0 {
		t.Fatalf("authorized remove did not persist: %v (%v)", rules, err)
	}
	if count := sessions.CancelScope(scope); count != 1 {
		t.Fatalf("generation session count=%d", count)
	}
}
