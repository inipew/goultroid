package client

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestP7JAssistantMessageContextCarriesMediaMentionAndLinkedReply(t *testing.T) {
	message := &tg.Message{
		ID:        701,
		PeerID:    &tg.PeerChannel{ChannelID: 77},
		FromID:    &tg.PeerUser{UserID: 42},
		Message:   "/probe @helper",
		GroupedID: 55,
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 7, Length: 7},
		},
		Media: &tg.MessageMediaWebPage{Webpage: &tg.WebPage{URL: "https://example.com"}},
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:    true,
			ReplyToMsgID:  123,
			ReplyToTopID:  100,
			ReplyToPeerID: &tg.PeerChannel{ChannelID: 88},
		},
	}
	entities := tg.Entities{Channels: map[int64]*tg.Channel{
		77: {ID: 77, AccessHash: 700, Megagroup: true, Title: "Manager Group"},
		88: {ID: 88, AccessHash: 800, Megagroup: true, Title: "Linked Group"},
	}}

	got := assistantCommandMessageContext(message, entities, 999, "helper")
	if got.Chat.ID != 77 || got.Chat.Type != string(core.ChatKindSupergroup) {
		t.Fatalf("chat context=%+v", got.Chat)
	}
	if got.ReplyToMessageID != 123 || got.TopicID != 100 || got.ReplyIsTopicRoot {
		t.Fatalf("reply/topic context=%+v", got)
	}
	if got.ReplyPeer.Kind != core.PeerKindChannel || got.ReplyPeer.ID != 88 || got.ReplyPeer.AccessHash != 800 {
		t.Fatalf("linked reply peer=%+v", got.ReplyPeer)
	}
	if got.Media == nil || got.Media.Type != "webpage" || got.Media.WebURL != "https://example.com" {
		t.Fatalf("media context=%+v", got.Media)
	}
	if got.GroupedID != 55 || len(got.Entities) != 1 {
		t.Fatalf("grouped/entities=%d/%d", got.GroupedID, len(got.Entities))
	}
	if !got.MentionedSelf || len(got.Mentions) != 1 || got.Mentions[0].Username != "helper" {
		t.Fatalf("mention context self=%v mentions=%+v", got.MentionedSelf, got.Mentions)
	}
	if got.Self.ID != 999 || got.Self.Username != "helper" || !got.Self.IsBot {
		t.Fatalf("self context=%+v", got.Self)
	}
}
