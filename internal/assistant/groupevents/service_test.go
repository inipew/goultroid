package groupevents

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/groupstate"
)

type adminRoleResolver struct{}

func (*adminRoleResolver) ResolveGroupRole(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return core.GroupRoleSnapshot{Principal: core.GroupActorPrincipal{
		UserID: req.UserID, Role: core.GroupActorRoleAdministrator, Verified: true,
	}}, nil
}

func (*adminRoleResolver) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return core.GroupRoleSnapshot{Principal: core.GroupActorPrincipal{
		UserID: req.UserID, Role: core.GroupActorRoleAdministrator, Verified: true,
	}}, nil
}

type recordingTransport struct {
	mu       sync.Mutex
	messages []string
	sent     chan string
}

func (r *recordingTransport) SendMessage(
	_ context.Context,
	_ tg.InputPeerClass,
	text string,
	_ tg.ReplyMarkupClass,
) (*tg.Message, error) {
	r.mu.Lock()
	r.messages = append(r.messages, text)
	messageID := len(r.messages)
	r.mu.Unlock()
	select {
	case r.sent <- text:
	default:
	}
	return &tg.Message{ID: messageID, Message: text}, nil
}

func newGroupEventTestRuntime(t *testing.T) (*groupstate.SQLiteStore, *core.EventBus) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, groupstate.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	store := groupstate.NewSQLiteStore(db)
	if store == nil {
		t.Fatal("group state store is nil")
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return store, bus
}

func groupEventAdminContext(store core.GroupStateStore, chatID, actorID int64) *core.Context {
	ctx := &core.Context{
		Ctx:        context.Background(),
		Source:     core.ExecutionAssistant,
		Chat:       &core.Chat{ID: chatID, Type: "supergroup"},
		PeerID:     &tg.InputPeerChannel{ChannelID: chatID, AccessHash: chatID + 100},
		Sender:     &core.User{ID: actorID},
		GroupRoles: &adminRoleResolver{},
	}
	core.AttachGroupStateStore(ctx, store)
	return ctx
}

func TestP7HSubscriptionTracksEnabledChatsAndKeepsDisabledRevision(t *testing.T) {
	store, bus := newGroupEventTestRuntime(t)
	service := New(store, bus)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	if got := bus.SubscriptionCount("assistant:groupevents"); got != 0 {
		t.Fatalf("initial subscriptions=%d, want 0", got)
	}
	if service.Interested(77, core.GroupServiceMemberJoined) {
		t.Fatal("inactive chat unexpectedly interested")
	}

	ctx := groupEventAdminContext(store, 77, 9)
	enabled, err := service.Configure(
		ctx,
		core.GroupServiceMemberJoined,
		true,
		"Hello {user} in {chat}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Revision != 1 || !enabled.Config.Enabled {
		t.Fatalf("enabled state=%+v", enabled)
	}
	if !service.Interested(77, core.GroupServiceMemberJoined) {
		t.Fatal("enabled welcome did not become interested")
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 1 {
		t.Fatalf("subscriptions after enable=%d, want 1", got)
	}

	disabled, err := service.Configure(ctx, core.GroupServiceMemberJoined, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Revision != 2 || disabled.Config.Enabled {
		t.Fatalf("disabled state=%+v", disabled)
	}
	if service.Interested(77, core.GroupServiceMemberJoined) {
		t.Fatal("disabled welcome remained interested")
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 0 {
		t.Fatalf("subscriptions after final disable=%d, want 0", got)
	}

	status, err := service.State(77, core.GroupServiceMemberJoined)
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 2 || status.Config.Template != "Hello {user} in {chat}" {
		t.Fatalf("disabled durable state lost from snapshot: %+v", status)
	}
}

func TestP7HActiveEventSendsSingleBoundedWelcome(t *testing.T) {
	store, bus := newGroupEventTestRuntime(t)
	service := New(store, bus)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx := groupEventAdminContext(store, 88, 9)
	if _, err := service.Configure(
		ctx,
		core.GroupServiceMemberJoined,
		true,
		"Welcome {user} to <b>{chat}</b> ({count})",
	); err != nil {
		t.Fatal(err)
	}

	transport := &recordingTransport{sent: make(chan string, 1)}
	service.SetTransport(transport)
	users := make([]core.GroupServiceUser, 0, 12)
	for i := int64(1); i <= 12; i++ {
		users = append(users, core.GroupServiceUser{ID: 100 + i, FirstName: "User"})
	}
	service.Publish(&core.GroupServiceEvent{
		At:        time.Now(),
		Kind:      core.GroupServiceMemberJoined,
		ChatID:    88,
		ChatTitle: "A&B",
		Peer:      &tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
		MessageID: 50,
		Users:     users,
	})

	select {
	case text := <-transport.sent:
		if !strings.Contains(text, "A&amp;B") ||
			!strings.Contains(text, "(12)") ||
			!strings.Contains(text, "and 4 more") {
			t.Fatalf("rendered welcome=%q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for welcome delivery")
	}

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.messages) != 1 {
		t.Fatalf("one service update produced %d messages, want 1", len(transport.messages))
	}
}

func TestP7HRestartPreloadRestoresInterestAndSubscription(t *testing.T) {
	store, bus := newGroupEventTestRuntime(t)
	first := New(store, bus)
	if err := first.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := groupEventAdminContext(store, 99, 7)
	state, err := first.Configure(
		ctx,
		core.GroupServiceMemberLeft,
		true,
		"Bye {user}",
	)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 0 {
		t.Fatalf("subscription remained after first service close: %d", got)
	}

	restarted := New(store, bus)
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	if !restarted.Interested(99, core.GroupServiceMemberLeft) {
		t.Fatal("restart preload did not restore goodbye interest")
	}
	if restarted.Interested(99, core.GroupServiceMemberJoined) {
		t.Fatal("restart preload enabled unrelated welcome interest")
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 1 {
		t.Fatalf("restart subscriptions=%d, want 1", got)
	}
	loaded, err := restarted.State(99, core.GroupServiceMemberLeft)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != state.Revision || loaded.Config.Template != "Bye {user}" {
		t.Fatalf("restart state=%+v, want revision/template from %+v", loaded, state)
	}
}

func TestP7HTwoFeaturesShareOneSubscriptionUntilLastDisable(t *testing.T) {
	store, bus := newGroupEventTestRuntime(t)
	service := New(store, bus)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx := groupEventAdminContext(store, 123, 7)

	if _, err := service.Configure(ctx, core.GroupServiceMemberJoined, true, "Welcome {user}"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Configure(ctx, core.GroupServiceMemberLeft, true, "Bye {user}"); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 1 {
		t.Fatalf("two enabled features created %d subscriptions, want one shared subscription", got)
	}

	if _, err := service.Configure(ctx, core.GroupServiceMemberJoined, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 1 {
		t.Fatalf("disabling welcome removed shared subscription while goodbye active: %d", got)
	}
	if service.Interested(123, core.GroupServiceMemberJoined) ||
		!service.Interested(123, core.GroupServiceMemberLeft) {
		t.Fatalf("interest mismatch welcome=%v goodbye=%v",
			service.Interested(123, core.GroupServiceMemberJoined),
			service.Interested(123, core.GroupServiceMemberLeft))
	}

	if _, err := service.Configure(ctx, core.GroupServiceMemberLeft, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 0 {
		t.Fatalf("last disable left %d subscriptions, want 0", got)
	}
}

func TestP7HOversizedTemplateFailsBeforePersistedInterest(t *testing.T) {
	store, bus := newGroupEventTestRuntime(t)
	service := New(store, bus)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx := groupEventAdminContext(store, 321, 7)

	_, err := service.Configure(
		ctx,
		core.GroupServiceMemberJoined,
		true,
		strings.Repeat("x", MaxTemplateBytes+1),
	)
	if !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("oversized template error=%v, want ErrInvalidTemplate", err)
	}
	if service.Interested(321, core.GroupServiceMemberJoined) {
		t.Fatal("oversized template activated chat interest")
	}
	if got := bus.SubscriptionCount("assistant:groupevents"); got != 0 {
		t.Fatalf("oversized template created %d subscriptions", got)
	}
	if _, err := ctx.GetGroupState(Namespace, WelcomeKey); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("oversized template persisted state: %v", err)
	}
}
