package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestMessageHooksSameNumericIDAcrossPeerTypes(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), nil, nil, zap.NewNop())
	configureDispatcherTasks(t, d)

	decisions := make(chan core.PeerKind, 3)
	eventsStarted := make(chan core.PeerKind, 3)
	eventsDone := make(chan core.PeerKind, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:identity-decision", Generation: 1},
		Priority: PrioritySecurity,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: func(_ context.Context, message *core.MessageEnvelope) error {
			decisions <- message.Peer.Kind
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:identity-event", Generation: 1},
		Priority: PriorityFeature,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookEvent},
		Handler: func(ctx context.Context, message *core.MessageEnvelope) error {
			eventsStarted <- message.Peer.Kind
			if message.Peer.Kind == core.PeerKindUser {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			eventsDone <- message.Peer.Kind
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{
		Users:    map[int64]*tg.User{42: {ID: 42}, 99: {ID: 99}},
		Chats:    map[int64]*tg.Chat{42: {ID: 42}},
		Channels: map[int64]*tg.Channel{42: {ID: 42, Megagroup: true}},
	}
	dispatch := func(peer tg.PeerClass) {
		t.Helper()
		fromID := int64(99)
		if _, ok := peer.(*tg.PeerUser); ok {
			fromID = 42
		}
		msg := &tg.Message{ID: 101, PeerID: peer, FromID: &tg.PeerUser{UserID: fromID}, Message: "hello"}
		var err error
		if _, ok := peer.(*tg.PeerChannel); ok {
			err = d.OnNewChannelMessage(context.Background(), entities, &tg.UpdateNewChannelMessage{Message: msg})
		} else {
			err = d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{Message: msg})
		}
		if err != nil {
			t.Fatalf("dispatch %T: %v", peer, err)
		}
	}

	// Keep the user event running while other peer namespaces submit tasks
	// with the exact same numeric chat and message IDs.
	dispatch(&tg.PeerUser{UserID: 42})
	select {
	case kind := <-eventsStarted:
		if kind != core.PeerKindUser {
			t.Fatalf("first hook kind=%v, want user", kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("user event did not start")
	}
	dispatch(&tg.PeerChat{ChatID: 42})
	dispatch(&tg.PeerChannel{ChannelID: 42})
	unblock()

	decisionCounts := make(map[core.PeerKind]int)
	eventCounts := make(map[core.PeerKind]int)
	for i := 0; i < 3; i++ {
		select {
		case kind := <-decisions:
			decisionCounts[kind]++
		case <-time.After(5 * time.Second):
			t.Fatal("decision hook missing after cross-peer admission")
		}
		select {
		case kind := <-eventsDone:
			eventCounts[kind]++
		case <-time.After(5 * time.Second):
			t.Fatal("event hook missing after cross-peer admission")
		}
	}
	for _, kind := range []core.PeerKind{core.PeerKindUser, core.PeerKindChat, core.PeerKindChannel} {
		if decisionCounts[kind] != 1 || eventCounts[kind] != 1 {
			t.Fatalf("kind=%v decision=%d event=%d; want 1 each", kind, decisionCounts[kind], eventCounts[kind])
		}
	}
}
