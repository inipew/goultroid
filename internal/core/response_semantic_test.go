package core

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestSemanticResponseIncomingReusesReplyForFinalResult(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 104, IsOutgoing: false},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := ctx.Progress("Working"); err != nil {
		t.Fatalf("Progress() error = %v", err)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID=%d, want 42", ctx.LastResponseID)
	}
	if !strings.Contains(mock.sentText, "Processing:") {
		t.Fatalf("progress sent text=%q", mock.sentText)
	}

	if err := ctx.Success("Done"); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	if !strings.Contains(mock.editedText, "Success:") || !strings.Contains(mock.editedText, "Done") {
		t.Fatalf("final edit text=%q", mock.editedText)
	}
}

func TestSemanticResponseOutgoingEditsTriggerInPlaceAndTracksAnchor(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 205, IsOutgoing: true},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := ctx.Status("Ready"); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(mock.editedText, "Status:") {
		t.Fatalf("edited text=%q", mock.editedText)
	}
	if mock.sentText != "" {
		t.Fatalf("outgoing semantic response unexpectedly sent new message %q", mock.sentText)
	}
	if ctx.LastResponseID != 205 {
		t.Fatalf("LastResponseID=%d, want outgoing anchor 205", ctx.LastResponseID)
	}
	if err := ctx.Messages().DeleteResponse(); err != nil {
		t.Fatalf("DeleteResponse() error=%v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 205 {
		t.Fatalf("deleted IDs=%v, want [205]", mock.deletedIDs)
	}
}

func TestSemanticResponseRejectsEmptyBody(t *testing.T) {
	ctx := &Context{}
	if err := ctx.Result("   "); err != ErrInvalidArgs {
		t.Fatalf("Result(empty) error=%v, want ErrInvalidArgs", err)
	}
}

func TestSemanticResponsePreservesTopicCoordinates(t *testing.T) {
	svc := &p7jContextualSendServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100},
	}
	if err := ctx.Progress("Working"); err != nil {
		t.Fatalf("Progress() error = %v", err)
	}
	if svc.lastSend.ReplyToID != 900 || svc.lastSend.TopicID != 100 {
		t.Fatalf("semantic send context=%+v want reply=900 topic=100", svc.lastSend)
	}
}
