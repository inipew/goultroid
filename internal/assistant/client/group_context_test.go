package client

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestAssistantCommandMessageContextClassifiesSupergroupAndTopic(t *testing.T) {
	msg := &tg.Message{
		ID:     10,
		PeerID: &tg.PeerChannel{ChannelID: 99},
		ReplyTo: &tg.MessageReplyHeader{
			ReplyToMsgID: 3,
			ReplyToTopID: 7,
			ForumTopic:   true,
		},
	}
	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			99: {ID: 99, Title: "Group", AccessHash: 123, Megagroup: true},
		},
	}

	got := assistantCommandMessageContext(msg, entities)
	if got.Chat.ID != 99 || got.Chat.Type != string(core.ChatKindSupergroup) || got.Chat.AccessHash != 123 {
		t.Fatalf("unexpected chat context: %+v", got.Chat)
	}
	if got.MessageID != 10 || got.ReplyToMessageID != 3 || got.TopicID != 7 {
		t.Fatalf("unexpected message context: %+v", got)
	}
}

func TestAssistantCommandMessageContextFailsClosedWithoutChannelMetadata(t *testing.T) {
	msg := &tg.Message{ID: 11, PeerID: &tg.PeerChannel{ChannelID: 88}}
	got := assistantCommandMessageContext(msg, tg.Entities{})
	if got.Chat.Type != string(core.ChatKindChannel) {
		t.Fatalf("ambiguous channel peer must remain channel, got %+v", got.Chat)
	}
}

func TestAssistantCommandMessageContextClassifiesBasicGroup(t *testing.T) {
	msg := &tg.Message{ID: 12, PeerID: &tg.PeerChat{ChatID: 55}}
	entities := tg.Entities{
		Chats: map[int64]*tg.Chat{
			55: {ID: 55, Title: "Basic Group"},
		},
	}
	got := assistantCommandMessageContext(msg, entities)
	if got.Chat.ID != 55 || got.Chat.Type != string(core.ChatKindGroup) || got.Chat.Title != "Basic Group" {
		t.Fatalf("unexpected basic group context: %+v", got.Chat)
	}
}
