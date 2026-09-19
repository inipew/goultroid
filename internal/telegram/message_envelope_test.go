package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestNormalizeMessageEnvelope(t *testing.T) {
	msg := &tg.Message{
		ID:      10,
		PeerID:  &tg.PeerChannel{ChannelID: 99},
		FromID:  &tg.PeerUser{UserID: 7},
		Message: "hi 😀 @Owner",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 6, Length: 6},
			&tg.MessageEntityMentionName{Offset: 0, Length: 2, UserID: 42},
		},
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 3, ReplyToTopID: 2, ForumTopic: true},
	}
	e := tg.Entities{
		Users: map[int64]*tg.User{
			7:  {ID: 7, FirstName: "Sender", Bot: true, Verified: true},
			42: {ID: 42, Username: "owner", Self: true},
		},
		Channels: map[int64]*tg.Channel{
			99: {ID: 99, Title: "Group", AccessHash: 123, Megagroup: true},
		},
	}
	got := NormalizeMessageEnvelope(e, msg, false, "", 42)
	if got == nil {
		t.Fatal("nil envelope")
	}
	if got.ChatID != 99 || got.Chat.Type != "supergroup" || got.Peer.AccessHash != 123 {
		t.Fatalf("unexpected chat/peer: %+v %+v", got.Chat, got.Peer)
	}
	if got.Sender.ID != 7 || !got.Sender.IsBot || !got.SenderVerified {
		t.Fatalf("unexpected sender: %+v verified=%v", got.Sender, got.SenderVerified)
	}
	if got.Self.Username != "owner" || got.Self.ID != 42 {
		t.Fatalf("unexpected self: %+v", got.Self)
	}
	if got.Mentioned {
		t.Fatal("mention entities must not manufacture Telegram's direct Mentioned flag")
	}
	if !got.MentionsUsername("Owner") || !got.MentionsUser(42) {
		t.Fatalf("mentions not normalized: %+v", got.Mentions)
	}
	if got.ReplyToID != 3 || got.TopicID != 2 || got.ReplyIsTopicRoot {
		t.Fatalf("unexpected reply metadata: %+v", got)
	}
}

func TestTelegramUTF16SliceRejectsSurrogateSplit(t *testing.T) {
	if _, ok := telegramUTF16Slice("😀x", 1, 1); ok {
		t.Fatal("expected surrogate split to be rejected")
	}
	if got, ok := telegramUTF16Slice("😀x", 0, 2); !ok || got != "😀" {
		t.Fatalf("slice=(%q,%v), want emoji,true", got, ok)
	}
}

func TestNormalizeMessageEnvelopeAnonymousChannelSender(t *testing.T) {
	msg := &tg.Message{
		ID:        11,
		PeerID:    &tg.PeerChannel{ChannelID: 500},
		FromID:    &tg.PeerChannel{ChannelID: 777},
		Message:   "anonymous mention",
		Mentioned: true,
	}
	e := tg.Entities{
		Channels: map[int64]*tg.Channel{
			500: {ID: 500, AccessHash: 333, Megagroup: true},
			777: {ID: 777, AccessHash: 444},
		},
	}
	got := NormalizeMessageEnvelope(e, msg, false, "", 1001)
	if got == nil {
		t.Fatal("nil envelope")
	}
	if got.Sender.ID != 0 {
		t.Fatalf("anonymous channel sender unexpectedly became user: %+v", got.Sender)
	}
	if got.SenderPeer.Kind != core.PeerKindChannel || got.SenderPeer.ID != 777 || got.SenderPeer.AccessHash != 444 {
		t.Fatalf("anonymous sender peer=%+v, want channel 777/444", got.SenderPeer)
	}
}

func TestNormalizeMessageEnvelopePreservesDirectMentionFlag(t *testing.T) {
	msg := &tg.Message{
		ID:        12,
		PeerID:    &tg.PeerChat{ChatID: 5},
		Message:   "direct mention candidate",
		Mentioned: true,
	}
	got := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 1)
	if got == nil || !got.Mentioned {
		t.Fatalf("direct Mentioned flag was not preserved: %+v", got)
	}
}
