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
}
