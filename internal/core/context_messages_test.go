package core

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func TestMessagesFacadeReplyAndDelete(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID: 104,
		},
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
	}

	if err := ctx.Messages().ReplyAndDelete("purged 4 messages"); err != nil {
		t.Fatalf("ReplyAndDelete() error = %v", err)
	}
	if mock.sentText != "purged 4 messages" {
		t.Fatalf("sent text = %q, want purge result", mock.sentText)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID = %d, want 42", ctx.LastResponseID)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 104 {
		t.Fatalf("deleted IDs = %v, want [104]", mock.deletedIDs)
	}
}

func TestMessagesFacadeReplyAndDeleteKeepsTriggerWhenReplyFails(t *testing.T) {
	replyErr := errors.New("send failed")
	mock := &mockTelegramServicer{errToSend: replyErr}
	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID: 104,
		},
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
	}

	err := ctx.Messages().ReplyAndDelete("purge failed")
	if err == nil || !errors.Is(err, replyErr) {
		t.Fatalf("ReplyAndDelete() error = %v, want wrapped send error", err)
	}
	if len(mock.deletedIDs) != 0 {
		t.Fatalf("deleted IDs = %v, want no deletion when reply fails", mock.deletedIDs)
	}
}

func TestMessagesFacadeReplyAndDeleteIgnoresTriggerDeleteFailure(t *testing.T) {
	mock := &mockTelegramServicer{errToDelete: errors.New("delete forbidden")}
	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID: 104,
		},
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
	}

	if err := ctx.Messages().ReplyAndDelete("purged 4 messages"); err != nil {
		t.Fatalf("ReplyAndDelete() error = %v, want nil after successful reply", err)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID = %d, want 42", ctx.LastResponseID)
	}
}

func TestMessagesFacadeEditOrReplyIncomingDeletesTrigger(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID:         104,
			IsOutgoing: false,
		},
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
	}

	if err := ctx.Messages().EditOrReply("purged 4 messages"); err != nil {
		t.Fatalf("EditOrReply() error = %v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 104 {
		t.Fatalf("deleted IDs = %v, want [104]", mock.deletedIDs)
	}
}
