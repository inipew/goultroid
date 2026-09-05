package telegram

import (
	"context"
	"os"
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

func TestCheckRestartState_FileHandling(t *testing.T) {
	// 1. Missing file -> no-op, no panic
	checkRestartState(context.Background(), nil, nil)

	// 2. Self peer state
	_ = os.MkdirAll("data", 0755)
	defer os.RemoveAll("data")

	selfJSON := []byte(`{"peer_type":"self","chat_id":0,"msg_id":99,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", selfJSON, 0644)

	// Calls checkRestartState with nil sender; svc.EditMessage will fail safely and remove file
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected data/restart.json to be removed by checkRestartState")
	}

	// 3. User peer with access hash
	userJSON := []byte(`{"peer_type":"user","chat_id":12345,"access_hash":67890,"msg_id":101,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", userJSON, 0644)
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected data/restart.json to be removed")
	}

	// 4. Legacy format fallback
	legacyJSON := []byte(`{"chat_id":777,"is_channel":true,"access_hash":888,"msg_id":102,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", legacyJSON, 0644)
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected legacy data/restart.json to be removed")
	}
}
