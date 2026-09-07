package interaction_test

import (
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

func TestTarget_MessageTarget(t *testing.T) {
	peer := &tg.InputPeerUser{UserID: 12345}
	mt := interaction.NewMessageTarget(peer, 42, 100, 200)

	if mt.Kind() != interaction.TargetKindMessage {
		t.Fatalf("expected TargetKindMessage, got %v", mt.Kind())
	}
	if !mt.IsValid() {
		t.Fatalf("expected MessageTarget to be valid")
	}
	if mt.Peer != peer || mt.MessageID != 42 || mt.ChatID != 100 || mt.ChatInstance != 200 {
		t.Fatalf("fields did not match constructor arguments")
	}

	// Invalid cases
	invalidPeer := interaction.NewMessageTarget(nil, 42, 100, 200)
	if invalidPeer.IsValid() {
		t.Fatalf("expected nil peer to be invalid")
	}

	invalidMsgID := interaction.NewMessageTarget(peer, 0, 100, 200)
	if invalidMsgID.IsValid() {
		t.Fatalf("expected 0 msgID to be invalid")
	}
}

func TestTarget_InlineTarget(t *testing.T) {
	inlineMsgID := &tg.InputBotInlineMessageID{DCID: 1, ID: 999, AccessHash: 888}
	it := interaction.NewInlineTarget(777, inlineMsgID, 555)

	if it.Kind() != interaction.TargetKindInline {
		t.Fatalf("expected TargetKindInline, got %v", it.Kind())
	}
	if !it.IsValid() {
		t.Fatalf("expected InlineTarget to be valid")
	}
	if it.QueryID != 777 || it.MessageID != inlineMsgID || it.ChatInstance != 555 {
		t.Fatalf("fields did not match constructor arguments")
	}

	invalidInline := interaction.NewInlineTarget(777, nil, 555)
	if invalidInline.IsValid() {
		t.Fatalf("expected nil inline message ID to be invalid")
	}
}

func TestClassifyRPCError(t *testing.T) {
	if err := interaction.ClassifyRPCError(nil); err != nil {
		t.Fatalf("expected nil for nil error, got %v", err)
	}

	msgIDErr := errors.New("rpc error code 400: MESSAGE_ID_INVALID")
	if !errors.Is(interaction.ClassifyRPCError(msgIDErr), interaction.ErrMessageAlreadyDeleted) {
		t.Fatalf("expected ErrMessageAlreadyDeleted for MESSAGE_ID_INVALID")
	}

	accessHashErr := errors.New("rpc error code 400: ACCESS_HASH_INVALID")
	if !errors.Is(interaction.ClassifyRPCError(accessHashErr), interaction.ErrAccessHashStale) {
		t.Fatalf("expected ErrAccessHashStale for ACCESS_HASH_INVALID")
	}

	queryErr := errors.New("rpc error code 400: QUERY_ID_INVALID")
	if !errors.Is(interaction.ClassifyRPCError(queryErr), interaction.ErrCallbackExpired) {
		t.Fatalf("expected ErrCallbackExpired for QUERY_ID_INVALID")
	}

	genericErr := errors.New("some unexpected database error")
	if interaction.ClassifyRPCError(genericErr) != genericErr {
		t.Fatalf("expected generic error to pass through unmodified")
	}
}
