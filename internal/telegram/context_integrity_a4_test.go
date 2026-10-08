package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestA4ChannelClassificationRequiresMegagroupEvidence(t *testing.T) {
	const chatID int64 = 991
	cases := []struct {
		name     string
		entities tg.Entities
		wantType string
	}{
		{name: "missing metadata", wantType: "channel"},
		{name: "broadcast", entities: tg.Entities{Channels: map[int64]*tg.Channel{chatID: {ID: chatID, Megagroup: false}}}, wantType: "channel"},
		{name: "megagroup", entities: tg.Entities{Channels: map[int64]*tg.Channel{chatID: {ID: chatID, Megagroup: true}}}, wantType: "supergroup"},
	}
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 71, PeerID: &tg.PeerChannel{ChannelID: chatID}}
			envelope := NormalizeMessageEnvelope(tt.entities, msg, false, "", 1)
			chat := d.resolveDispatchChat(tt.entities, msg)
			if envelope == nil || envelope.Chat.Type != tt.wantType || chat.Type != tt.wantType {
				t.Fatalf("envelope=%+v dispatch=%+v want type %s", envelope, chat, tt.wantType)
			}
			if got, want := classifyCanonicalMessageRoute(envelope), classifyMessageRoute(msg, false); got != want {
				t.Fatalf("route class mismatch: canonical=%08b raw=%08b", got, want)
			}
		})
	}
}

func TestA4PrivateSenderRejectsMismatchedOrAnonymousFromID(t *testing.T) {
	cases := []struct {
		name       string
		from       tg.PeerClass
		want       bool
		wantSender int64
	}{
		{name: "omitted private sender", want: true, wantSender: 2002},
		{name: "matching private sender", from: &tg.PeerUser{UserID: 2002}, want: true, wantSender: 2002},
		{name: "mismatched private sender", from: &tg.PeerUser{UserID: 9000}, wantSender: 9000},
		{name: "explicit anonymous channel sender", from: &tg.PeerChannel{ChannelID: 42}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 72, PeerID: &tg.PeerUser{UserID: 2002}, FromID: tt.from, Message: ".owner"}
			if got := privateMessageSenderConsistent(msg); got != tt.want {
				t.Fatalf("private sender consistent=%v want %v", got, tt.want)
			}
			envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, true, "owner", 1)
			if envelope.Sender.ID != tt.wantSender {
				t.Fatalf("sender ID %d should be %d", envelope.Sender.ID, tt.wantSender)
			}
			if !tt.want && invocationSenderID(msg, 1) == 2002 {
				t.Fatal("explicit invalid FromID fell back to private dialog owner")
			}
		})
	}
}

func TestA4DispatcherDoesNotInvokeMismatchedPrivateSenderHooks(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	hits := 0
	d.AddPrioritizedCanonicalMessageHandlerWithRouting(PrioritySecurity,
		core.MessageHookRouting{Lane: core.MessageHookDecision, Interests: []core.MessageHookInterest{
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate},
		}}, func(context.Context, *core.MessageEnvelope) error {
			hits++
			return nil
		})
	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002}}}
	if err := d.dispatch(context.Background(), entities, &tg.Message{
		ID: 73, PeerID: &tg.PeerUser{UserID: 2002}, FromID: &tg.PeerUser{UserID: 1}, Message: "spoof",
	}); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("invalid sender reached security decision hook %d times", hits)
	}
	if err := d.dispatch(context.Background(), entities, &tg.Message{
		ID: 74, PeerID: &tg.PeerUser{UserID: 2002}, Message: "legitimate DM",
	}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("legitimate omitted FromID did not reach security hook: %d", hits)
	}
}

func TestA4NormalizeCrossChatAndForumReplyContext(t *testing.T) {
	const chatID int64 = 500
	tests := []struct {
		name       string
		reply      *tg.MessageReplyHeader
		wantTopic  int
		wantRoot   bool
		wantTarget core.PeerRef
	}{
		{name: "topic root", reply: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 11}, wantTopic: 11, wantRoot: true},
		{name: "reply inside forum topic", reply: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 33, ReplyToTopID: 11}, wantTopic: 11},
		{name: "cross channel reply", reply: &tg.MessageReplyHeader{ReplyToMsgID: 92, ReplyToPeerID: &tg.PeerChannel{ChannelID: 999}}, wantTarget: core.PeerRef{Kind: core.PeerKindChannel, ID: 999}},
		{name: "ordinary same chat reply", reply: &tg.MessageReplyHeader{ReplyToMsgID: 23}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 75, PeerID: &tg.PeerChannel{ChannelID: chatID}, ReplyTo: tt.reply}
			envelope := NormalizeMessageEnvelope(tg.Entities{Channels: map[int64]*tg.Channel{chatID: {ID: chatID, Megagroup: true}}}, msg, false, "", 1)
			if envelope.TopicID != tt.wantTopic || envelope.ReplyIsTopicRoot != tt.wantRoot || envelope.ReplyPeer != tt.wantTarget {
				t.Fatalf("unexpected reply context: %+v", envelope)
			}
			if envelope.ReplyToID != tt.reply.ReplyToMsgID {
				t.Fatalf("reply ID was lost: %+v", envelope)
			}
		})
	}
}
