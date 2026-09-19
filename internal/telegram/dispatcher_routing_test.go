package telegram

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestDispatcher_IndexedMessageRoutingSkipsIrrelevantHandlers(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())

	var privateCalls atomic.Int32
	var groupCalls atomic.Int32
	d.AddPrioritizedMessageHandlerWithRouting(PrioritySecurity, core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerPrivate,
		}},
	}, func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		privateCalls.Add(1)
		return nil
	})
	d.AddPrioritizedMessageHandlerWithRouting(PriorityModeration, core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerGroup | core.MessagePeerChannel,
			Commands:   core.MessagePlain,
			RequireText: true,
		}},
	}, func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		groupCalls.Add(1)
		return nil
	})

	msg := &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 7}, Message: "hello"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := privateCalls.Load(); got != 0 {
		t.Fatalf("private-only hook calls=%d, want 0", got)
	}
	if got := groupCalls.Load(); got != 1 {
		t.Fatalf("group hook calls=%d, want 1", got)
	}
}

func TestDispatcher_MessageRoutingSeparatesDecisionAndEventLanes(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	interest := []core.MessageHookInterest{{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup}}
	d.AddPrioritizedMessageHandlerWithRouting(PrioritySecurity, core.MessageHookRouting{
		Lane: core.MessageHookDecision, Interests: interest,
	}, func(context.Context, tg.Entities, *tg.Message, bool, string) error { return nil })
	d.AddScopedMessageHandlerWithRouting(PriorityFeature, tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}, core.MessageHookRouting{
		Lane: core.MessageHookEvent, Interests: interest,
	}, func(context.Context, tg.Entities, *tg.Message, bool, string) error { return nil })

	decision, event := d.messageHandlersFor(&tg.Message{PeerID: &tg.PeerChat{ChatID: 7}, Message: "hello"}, false)
	if len(decision) != 1 || len(event) != 1 {
		t.Fatalf("lane sizes decision=%d event=%d, want 1/1", len(decision), len(event))
	}
	if decision[0].routing.Lane != core.MessageHookDecision {
		t.Fatal("decision handler routed to wrong lane")
	}
	if event[0].routing.Lane != core.MessageHookEvent {
		t.Fatal("event handler routed to wrong lane")
	}
}

func TestDispatcher_MessageRoutingMentionAndReplyInterests(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.AddPrioritizedMessageHandlerWithRouting(PriorityFeature, core.MessageHookRouting{
		Lane: core.MessageHookEvent,
		Interests: []core.MessageHookInterest{
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireMention: true},
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireReply: true},
		},
	}, func(context.Context, tg.Entities, *tg.Message, bool, string) error { return nil })

	plain := &tg.Message{PeerID: &tg.PeerChat{ChatID: 7}, Message: "hello"}
	_, event := d.messageHandlersFor(plain, false)
	if len(event) != 0 {
		t.Fatalf("plain group message routed to mention/reply hook: %d handlers", len(event))
	}

	mentioned := &tg.Message{PeerID: &tg.PeerChat{ChatID: 7}, Message: "@someone", Mentioned: true}
	_, event = d.messageHandlersFor(mentioned, false)
	if len(event) != 1 {
		t.Fatalf("mention candidate event handlers=%d, want 1", len(event))
	}

	reply := &tg.Message{PeerID: &tg.PeerChat{ChatID: 7}, Message: "reply", ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 1}}
	_, event = d.messageHandlersFor(reply, false)
	if len(event) != 1 {
		t.Fatalf("reply candidate event handlers=%d, want 1", len(event))
	}
}

func TestDispatcher_LegacyHookRoutingCompatibility(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.AddMessageHandler(func(context.Context, tg.Entities, *tg.Message, bool, string) error { return nil })

	decision, event := d.messageHandlersFor(&tg.Message{PeerID: &tg.PeerUser{UserID: 1}}, false)
	if len(decision) != 1 || len(event) != 0 {
		t.Fatalf("legacy unscoped hook routed decision=%d event=%d, want 1/0", len(decision), len(event))
	}
}

func BenchmarkDispatcherMessageRouteLookup(b *testing.B) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	noop := func(context.Context, tg.Entities, *tg.Message, bool, string) error { return nil }
	for i := 0; i < 5; i++ {
		d.AddPrioritizedMessageHandlerWithRouting(PriorityFeature, core.MessageHookRouting{
			Lane: core.MessageHookEvent,
			Interests: []core.MessageHookInterest{{
				Directions: core.MessageDirectionIncoming,
				Peers:      core.MessagePeerGroup | core.MessagePeerChannel,
				Commands:   core.MessagePlain,
				RequireText: true,
			}},
		}, noop)
	}
	msg := &tg.Message{PeerID: &tg.PeerChat{ChatID: 7}, Message: "ordinary group traffic"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decision, event := d.messageHandlersFor(msg, false)
		if len(decision) != 0 || len(event) != 5 {
			b.Fatal("unexpected route bucket")
		}
	}
}

func TestDispatcher_CanonicalHandlerReceivesNormalizedEnvelope(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(42, nil), nil, zap.NewNop())
	d.SetSelfID(42)

	var got *core.MessageEnvelope
	d.AddPrioritizedCanonicalMessageHandlerWithRouting(PrioritySecurity, core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerGroup,
			RequireText: true,
		}},
	}, func(_ context.Context, message *core.MessageEnvelope) error {
		got = message
		return nil
	})

	msg := &tg.Message{
		ID:      9,
		PeerID:  &tg.PeerChat{ChatID: 77},
		FromID:  &tg.PeerUser{UserID: 7},
		Message: "hello @owner",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMentionName{Offset: 6, Length: 6, UserID: 42},
		},
	}
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			7:  {ID: 7, FirstName: "Alice"},
			42: {ID: 42, Username: "owner", Self: true},
		},
		Chats: map[int64]*tg.Chat{77: {ID: 77, Title: "Room"}},
	}
	if err := d.dispatch(context.Background(), entities, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got == nil {
		t.Fatal("canonical hook was not invoked")
	}
	if got.ID != 9 || got.ChatID != 77 || got.Chat.Title != "Room" {
		t.Fatalf("unexpected message/chat: %+v", got)
	}
	if got.Sender.ID != 7 || got.Sender.FirstName != "Alice" {
		t.Fatalf("unexpected sender: %+v", got.Sender)
	}
	if !got.MentionsUser(42) {
		t.Fatalf("owner mention missing: %+v", got.Mentions)
	}
}

func TestCanonicalAndRawRouteClassificationParity(t *testing.T) {
	msg := &tg.Message{
		Out:       true,
		PeerID:    &tg.PeerChannel{ChannelID: 5},
		Message:   "@x",
		Mentioned: true,
		ReplyTo:   &tg.MessageReplyHeader{ReplyToMsgID: 1},
	}
	rawClass := classifyMessageRoute(msg, true)
	envelope := NormalizeMessageEnvelope(tg.Entities{
		Channels: map[int64]*tg.Channel{5: {ID: 5, Megagroup: true}},
	}, msg, true, "cmd", 1)
	canonicalClass := classifyCanonicalMessageRoute(envelope)
	if rawClass != canonicalClass {
		t.Fatalf("route class mismatch raw=%07b canonical=%07b", rawClass, canonicalClass)
	}
}
