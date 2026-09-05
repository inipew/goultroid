package telegram

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
)

// Ensure Service implements core.TelegramServicer.
var _ core.TelegramServicer = (*Service)(nil)

func TestExtractMessageFromUpdates(t *testing.T) {
	// 1. tg.Updates with UpdateNewMessage
	msg := &tg.Message{ID: 101, Message: "hello"}
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: msg},
		},
	}
	extracted := extractMessageFromUpdates(upd)
	if extracted == nil || extracted.ID != 101 {
		t.Errorf("expected extracted message ID 101, got %v", extracted)
	}

	// 2. tg.Updates with UpdateNewChannelMessage
	chMsg := &tg.Message{ID: 202, Message: "channel msg"}
	updCh := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: chMsg},
		},
	}
	extractedCh := extractMessageFromUpdates(updCh)
	if extractedCh == nil || extractedCh.ID != 202 {
		t.Errorf("expected extracted channel message ID 202, got %v", extractedCh)
	}

	// 3. tg.UpdateShortSentMessage
	shortSent := &tg.UpdateShortSentMessage{ID: 303, Date: 12345}
	extractedShort := extractMessageFromUpdates(shortSent)
	if extractedShort == nil || extractedShort.ID != 303 {
		t.Errorf("expected extracted short sent message ID 303, got %v", extractedShort)
	}

	// 4. tg.UpdateShortMessage
	shortMsg := &tg.UpdateShortMessage{ID: 404, Message: "short", Date: 67890}
	extractedShortMsg := extractMessageFromUpdates(shortMsg)
	if extractedShortMsg == nil || extractedShortMsg.ID != 404 || extractedShortMsg.Message != "short" {
		t.Errorf("expected extracted short message ID 404, got %v", extractedShortMsg)
	}

	// 5. Unknown update class
	if extractMessageFromUpdates(&tg.UpdatesTooLong{}) != nil {
		t.Errorf("expected nil for non-message update")
	}
}

func TestDeleteMessage_EmptyIDs(t *testing.T) {
	s := &Service{}
	err := s.DeleteMessage(context.Background(), &tg.InputPeerSelf{}, nil)
	if err != nil {
		t.Errorf("expected no error when deleting empty message IDs: %v", err)
	}
}
