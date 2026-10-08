package filters

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

func makeA5Filters(t *testing.T) (*Plugin, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatal(err)
	}
	p := New(NewSQLiteRepository(db), nil)
	return p, db
}
func putA5Filter(t *testing.T, p *Plugin, chat int64, keyword, reply string) {
	t.Helper()
	if err := p.db.SaveFilter(context.Background(), chat, keyword, savedresponse.NewText(reply)); err != nil {
		t.Fatal(err)
	}
}

type a5FiltersTaskClient struct{}

func (*a5FiltersTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
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
func (*a5FiltersTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*a5FiltersTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*a5FiltersTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type a5FiltersTelegram struct {
	core.MockTelegramServicer
	markup tg.ReplyMarkupClass
}

func (m *a5FiltersTelegram) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, _ string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.markup = markup
	return &tg.Message{ID: 100}, nil
}
func (m *a5FiltersTelegram) EditMessageMarkup(_ context.Context, _ tg.InputPeerClass, _ int, _ string, markup tg.ReplyMarkupClass) error {
	m.markup = markup
	return nil
}

type a5FiltersRoles struct {
	role     core.GroupActorRole
	requests []core.GroupRoleRequest
	err      error
}

func (r *a5FiltersRoles) ResolveGroupRole(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.ResolveGroupRoleFresh(context.Background(), req)
}
func (r *a5FiltersRoles) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return core.GroupRoleSnapshot{}, r.err
	}
	return core.GroupRoleSnapshot{Principal: core.GroupActorPrincipal{UserID: req.UserID, Verified: true, Role: r.role}}, nil
}

func a5FiltersCallback(t *testing.T, markup tg.ReplyMarkupClass, label string) []byte {
	t.Helper()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("missing a2 markup: %T", markup)
	}
	for _, row := range inline.Rows {
		for _, button := range row.Buttons {
			cb, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if !rootinteraction.OwnsCallbackData(cb.Data) {
				t.Fatalf("unexpected callback protocol %q", cb.Data)
			}
			if strings.Contains(cb.Text, label) {
				return append([]byte(nil), cb.Data...)
			}
		}
	}
	t.Fatalf("missing callback %q", label)
	return nil
}

func TestA5C2FiltersPageAndBoundedState(t *testing.T) {
	p, _ := makeA5Filters(t)
	for i := 0; i < 13; i++ {
		putA5Filter(t, p, 500, string(rune('a'+i)), "example")
	}
	state := nativeFiltersState{Scope: nativeinteraction.GroupActionScope{ChatID: 500, Kind: core.ChatKindSupergroup, TopicID: 77}}
	raw, view, err := p.nativeFiltersView(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 4096 || len(view.Rows) > 8 {
		t.Fatalf("unbounded view state=%d rows=%d", len(raw), len(view.Rows))
	}
	if !strings.Contains(view.Text, "Halaman 1/3") {
		t.Fatal(view.Text)
	}
	var next nativeFiltersState
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	if next.Scope.ChatID != 500 || next.Scope.TopicID != 77 || next.Digest == "" || len(next.Choices) > nativeFiltersSlots {
		t.Fatalf("unexpected session scope: %+v", next)
	}
	seen := false
	for _, c := range next.Choices {
		if c.Kind == "page" && c.Page == 1 {
			seen = true
		}
	}
	if !seen {
		t.Fatal("next page button missing")
	}
	next.Page = 1
	_, view, err = p.nativeFiltersView(context.Background(), next)
	if err != nil || !strings.Contains(view.Text, "Halaman 2/3") {
		t.Fatalf("page two view %q error %v", view.Text, err)
	}
}

func TestA5C2FiltersPreviewDetectsContentReplacement(t *testing.T) {
	p, _ := makeA5Filters(t)
	putA5Filter(t, p, 500, "hello", "first response")
	raw, _, err := p.nativeFiltersView(context.Background(), nativeFiltersState{Scope: nativeinteraction.GroupActionScope{ChatID: 500, Kind: core.ChatKindGroup}})
	if err != nil {
		t.Fatal(err)
	}
	var state nativeFiltersState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	putA5Filter(t, p, 500, "hello", "updated response")
	if _, _, err := p.nativeFiltersView(context.Background(), state); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("replaced response accepted: %v", err)
	}
}

func TestA5C2FiltersScopedDeleteDemotionAndDBFailure(t *testing.T) {
	p, _ := makeA5Filters(t)
	putA5Filter(t, p, 500, "shared", "hello")
	putA5Filter(t, p, 600, "shared", "different group")
	filters, err := p.db.ListFilters(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := filtersSnapshot(filters)
	if err != nil {
		t.Fatal(err)
	}
	scope := nativeinteraction.GroupActionScope{ChatID: 500, Kind: core.ChatKindSupergroup, TopicID: 55}
	session := rootinteraction.Session{Binding: rootinteraction.Binding{ActorID: 1001, ChatID: 500, MessageID: 100}}
	target := presentationtelegram.MessageTarget{Peer: &tg.InputPeerChannel{ChannelID: 500, AccessHash: 777}, ChatID: 500, MessageID: 100}
	roles := &a5FiltersRoles{role: core.GroupActorRoleMember}
	err = p.removeNativeFilterIfCurrent(context.Background(), session, target, scope, "shared", digest, roles)
	if !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("demoted admin removed rule: %v", err)
	}
	roles.role = core.GroupActorRoleAdministrator
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.removeNativeFilterIfCurrent(canceled, session, target, scope, "shared", digest, roles); err == nil {
		t.Fatal("canceled delete succeeded")
	}
	if err := p.removeNativeFilterIfCurrent(context.Background(), session, target, scope, "shared", digest, roles); err != nil {
		t.Fatal(err)
	}
	groupA, err := p.db.ListFilters(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := p.db.ListFilters(context.Background(), 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(groupA) != 0 || len(groupB) != 1 || groupB[0].Keyword != "shared" {
		t.Fatalf("cross-group corruption: A=%v B=%v", groupA, groupB)
	}
}

func TestA5C2FiltersInvalidScopeAndNoRuntimeFallback(t *testing.T) {
	p, _ := makeA5Filters(t)
	for _, kind := range []core.ChatKind{core.ChatKindPrivate, core.ChatKindChannel, core.ChatKindUnknown} {
		_, _, err := p.nativeFiltersView(context.Background(), nativeFiltersState{Scope: nativeinteraction.GroupActionScope{ChatID: 500, Kind: kind}})
		if !errors.Is(err, core.ErrGroupOnly) {
			t.Fatalf("invalid chat kind %q allowed: %v", kind, err)
		}
	}
	if opened, err := p.openNativeFilters(&core.Context{}); opened || err != nil {
		t.Fatalf("native free fallback: opened=%v err=%v", opened, err)
	}
}

func TestA5C2FiltersA2DemotionIsolationAndStale(t *testing.T) {
	p, _ := makeA5Filters(t)
	putA5Filter(t, p, 500, "scam", "remove me")
	putA5Filter(t, p, 600, "scam", "keep me")
	reg := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:filters", Generation: 1}
	registration, err := reg.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	sessions, err := rootinteraction.NewRuntime(reg, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	svc := &a5FiltersTelegram{}
	adapter, err := nativeinteraction.New(reg, sessions, rootinteraction.NewDispatcher(sessions), &a5FiltersTaskClient{}, func() presentationtelegram.BridgeService { return svc }, core.NewPermissions(1001, nil))
	if err != nil {
		t.Fatal(err)
	}
	roles := &a5FiltersRoles{role: core.GroupActorRoleAdministrator}
	adapter.SetGroupRoleProvider(func() core.GroupRoleResolver { return roles })
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter, Catalog: reg, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	peer := &tg.InputPeerChannel{ChannelID: 500, AccessHash: 777}
	cmd := &core.Context{Ctx: context.Background(), Source: core.ExecutionInteractive, Sender: &core.User{ID: 1001}, Message: &core.Message{ID: 40, SenderID: 1001, IsOutgoing: true, TopicID: 55}, Chat: &core.Chat{ID: 500, Type: "supergroup"}, PeerID: peer}
	if opened, err := p.openNativeFilters(cmd); !opened || err != nil {
		t.Fatalf("open: %v %v", opened, err)
	}
	selected := a5FiltersCallback(t, svc.markup, "scam")
	event := &core.CallbackQueryEvent{QueryID: 501, UserID: 1001, ChatID: 500, MsgID: 100, Data: selected, Origin: core.CallbackOriginMessage, Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100}}
	if handled, err := adapter.HandleCallback(context.Background(), event); !handled || err != nil {
		t.Fatalf("select: %v %v", handled, err)
	}
	confirm := a5FiltersCallback(t, svc.markup, "Confirm")
	roles.role = core.GroupActorRoleMember
	demoted := *event
	demoted.QueryID++
	demoted.Data = confirm
	if handled, err := adapter.HandleCallback(context.Background(), &demoted); !handled || err != nil {
		t.Fatalf("demotion: %v %v", handled, err)
	}
	current, err := p.db.ListFilters(context.Background(), 500)
	if err != nil || len(current) != 1 {
		t.Fatalf("demotion allowed deletion: %v %v", current, err)
	}
	spoof := demoted
	spoof.QueryID++
	spoof.ChatID = 600
	spoof.Target.Peer = &tg.InputPeerChannel{ChannelID: 600, AccessHash: 777}
	_, _ = adapter.HandleCallback(context.Background(), &spoof)
	roles.role = core.GroupActorRoleAdministrator
	valid := demoted
	valid.QueryID = 504
	if handled, err := adapter.HandleCallback(context.Background(), &valid); !handled || err != nil {
		t.Fatalf("confirmed: %v %v", handled, err)
	}
	current, err = p.db.ListFilters(context.Background(), 500)
	if err != nil || len(current) != 0 {
		t.Fatalf("authorized deletion failed: %v %v", current, err)
	}
	other, err := p.db.ListFilters(context.Background(), 600)
	if err != nil || len(other) != 1 {
		t.Fatalf("cross-group deletion: %v %v", other, err)
	}
	again := demoted
	again.QueryID = 505
	if handled, err := adapter.HandleCallback(context.Background(), &again); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("replay: %v %v", handled, err)
	}
	if got := sessions.CancelScope(scope); got != 1 {
		t.Fatalf("remaining sessions: %d", got)
	}
}
