package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestNormalizer_MessageEvents(t *testing.T) {
	n := NewNormalizer()
	ctx := context.Background()

	// 1. UpdateNewMessage
	msg := &tg.Message{
		ID:      100,
		Message: "Hello",
		PeerID:  &tg.PeerUser{UserID: 200},
	}
	evt, err := n.Normalize(ctx, tg.Entities{}, &tg.UpdateNewMessage{Message: msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	createdEvt, ok := evt.(*core.MessageCreatedEvent)
	if !ok || createdEvt.Message.ID != 100 || createdEvt.Message.Text != "Hello" {
		t.Fatalf("unexpected normalized MessageCreatedEvent: %+v", evt)
	}
	if createdEvt.Meta().ID != "msg:200:100" {
		t.Fatalf("unexpected message event id %q", createdEvt.Meta().ID)
	}

	channelMsg := &tg.Message{ID: 101, Message: "Channel", PeerID: &tg.PeerChannel{ChannelID: 900}}
	evt, err = n.Normalize(ctx, tg.Entities{}, &tg.UpdateNewChannelMessage{Message: channelMsg})
	if err != nil {
		t.Fatalf("normalize channel message: %v", err)
	}
	channelEvt, ok := evt.(*core.MessageCreatedEvent)
	if !ok {
		t.Fatalf("unexpected channel event type: %T", evt)
	}
	if channelEvt.Meta().ID != "msg:900:101" {
		t.Fatalf("channel message must use canonical message id, got %q", channelEvt.Meta().ID)
	}

	// 2. UpdateEditMessage
	editMsg := &tg.Message{
		ID:      100,
		Message: "Hello Edited",
		PeerID:  &tg.PeerUser{UserID: 200},
	}
	evt, err = n.Normalize(ctx, tg.Entities{}, &tg.UpdateEditMessage{Message: editMsg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	editedEvt, ok := evt.(*core.MessageEditedEvent)
	if !ok || editedEvt.MsgID != 100 || editedEvt.Text != "Hello Edited" {
		t.Fatalf("unexpected normalized MessageEditedEvent: %+v", evt)
	}

	// 3. UpdateDeleteMessages
	evt, err = n.Normalize(ctx, tg.Entities{}, &tg.UpdateDeleteMessages{Messages: []int{100, 101}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	delEvt, ok := evt.(*core.MessagesDeletedEvent)
	if !ok || len(delEvt.MsgIDs) != 2 || delEvt.MsgIDs[0] != 100 {
		t.Fatalf("unexpected normalized MessagesDeletedEvent: %+v", evt)
	}
}

func TestNormalizer_CallbackEvents(t *testing.T) {
	n := NewNormalizer()
	ctx := context.Background()

	// 1. BotCallbackQuery
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			500: {ID: 500, AccessHash: 9999},
		},
	}
	update := &tg.UpdateBotCallbackQuery{
		QueryID:      12345,
		UserID:       500,
		Peer:         &tg.PeerUser{UserID: 500},
		MsgID:        42,
		ChatInstance: 777,
		Data:         []byte("action:test"),
	}

	evt, err := n.Normalize(ctx, entities, update)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cbEvt, ok := evt.(*core.CallbackQueryEvent)
	if !ok || cbEvt.QueryID != 12345 || cbEvt.Origin != core.CallbackOriginMessage {
		t.Fatalf("unexpected CallbackQueryEvent: %+v", evt)
	}
	if cbEvt.Meta().ID != "cb:12345" || cbEvt.ChatID != 500 || cbEvt.MsgID != 42 {
		t.Fatalf("callback compatibility metadata drifted: meta=%q chat=%d msg=%d", cbEvt.Meta().ID, cbEvt.ChatID, cbEvt.MsgID)
	}
	if cbEvt.Target.Peer == nil || cbEvt.Target.MessageID != 42 {
		t.Fatalf("callback target not populated canonically: %+v", cbEvt.Target)
	}

	// 2. InlineBotCallbackQuery
	inlineID := &tg.InputBotInlineMessageID64{DCID: 1, ID: 888, AccessHash: 555}
	inlineUpdate := &tg.UpdateInlineBotCallbackQuery{
		QueryID:      67890,
		UserID:       500,
		MsgID:        inlineID,
		ChatInstance: 777,
		Data:         []byte("inline:test"),
	}
	evt, err = n.Normalize(ctx, entities, inlineUpdate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inlineCbEvt, ok := evt.(*core.CallbackQueryEvent)
	if !ok || inlineCbEvt.QueryID != 67890 || inlineCbEvt.Origin != core.CallbackOriginInline {
		t.Fatalf("unexpected inline CallbackQueryEvent: %+v", evt)
	}
	if inlineCbEvt.Meta().ID != "inline_cb:67890" || inlineCbEvt.Target.InlineID == nil {
		t.Fatalf("inline callback metadata/target drifted: meta=%q target=%+v", inlineCbEvt.Meta().ID, inlineCbEvt.Target)
	}
}

func TestCanonicalCallbackEventMatchesNormalizer(t *testing.T) {
	n := NewNormalizer()
	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			55: {ID: 55, AccessHash: 1234},
		},
	}
	update := &tg.UpdateBotCallbackQuery{
		QueryID:      77,
		UserID:       88,
		Peer:         &tg.PeerChannel{ChannelID: 55},
		MsgID:        99,
		ChatInstance: 111,
		Data:         []byte("v1:test:act:-"),
	}
	normalized, err := n.Normalize(context.Background(), entities, update)
	if err != nil {
		t.Fatalf("normalize callback: %v", err)
	}
	got := normalized.(*core.CallbackQueryEvent)
	direct := canonicalCallbackQueryEvent(update, &tg.InputPeerChannel{ChannelID: 55, AccessHash: 1234}, got.At)

	if got.Meta().ID != direct.Meta().ID ||
		got.ChatID != direct.ChatID ||
		got.MsgID != direct.MsgID ||
		got.QueryID != direct.QueryID ||
		got.UserID != direct.UserID ||
		got.Origin != direct.Origin ||
		got.Target.MessageID != direct.Target.MessageID {
		t.Fatalf("normalizer and canonical callback helper diverged: normalized=%+v direct=%+v", got, direct)
	}
}
