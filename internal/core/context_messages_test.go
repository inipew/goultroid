package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestMessagesFacadeReplyAndDelete(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	if err := ctx.Messages().ReplyAndDelete("purged 4 messages"); err != nil {
		t.Fatalf("ReplyAndDelete() error = %v", err)
	}
	if mock.sentText != "purged 4 messages" { t.Fatalf("sent text = %q, want purge result", mock.sentText) }
	if ctx.LastResponseID != 42 { t.Fatalf("LastResponseID = %d, want 42", ctx.LastResponseID) }
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 104 { t.Fatalf("deleted IDs = %v, want [104]", mock.deletedIDs) }
}

func TestMessagesFacadeReplyAndDeleteKeepsTriggerWhenReplyFails(t *testing.T) {
	replyErr := errors.New("send failed")
	mock := &mockTelegramServicer{errToSend: replyErr}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	err := ctx.Messages().ReplyAndDelete("purge failed")
	if err == nil || !errors.Is(err, replyErr) { t.Fatalf("ReplyAndDelete() error = %v, want wrapped send error", err) }
	if len(mock.deletedIDs) != 0 { t.Fatalf("deleted IDs = %v, want no deletion when reply fails", mock.deletedIDs) }
}

func TestMessagesFacadeReplyAndDeleteIgnoresTriggerDeleteFailure(t *testing.T) {
	mock := &mockTelegramServicer{errToDelete: errors.New("delete forbidden")}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	if err := ctx.Messages().ReplyAndDelete("purged 4 messages"); err != nil { t.Fatalf("ReplyAndDelete() error = %v, want nil after successful reply", err) }
	if ctx.LastResponseID != 42 { t.Fatalf("LastResponseID = %d, want 42", ctx.LastResponseID) }
}

func TestMessagesFacadeEditOrReplyIncomingDeletesTrigger(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104, IsOutgoing: false}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	if err := ctx.Messages().EditOrReply("purged 4 messages"); err != nil { t.Fatalf("EditOrReply() error = %v", err) }
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 104 { t.Fatalf("deleted IDs = %v, want [104]", mock.deletedIDs) }
}

type delayedDeleteMock struct {
	*mockTelegramServicer
	deleted chan int
}

func (m *delayedDeleteMock) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	err := m.mockTelegramServicer.DeleteMessage(ctx, peer, msgIDs)
	if err == nil && len(msgIDs) > 0 { m.deleted <- msgIDs[0] }
	return err
}

func TestMessagesFacadeReplyAndDeleteWithDelay(t *testing.T) {
	mock := &delayedDeleteMock{mockTelegramServicer: &mockTelegramServicer{}, deleted: make(chan int, 2)}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	delay := 30 * time.Millisecond
	if err := ctx.Messages().ReplyAndDeleteWithDelay("purged 5 messages", delay); err != nil { t.Fatalf("ReplyAndDeleteWithDelay() error = %v", err) }
	if mock.sentText != "purged 5 messages" { t.Fatalf("sent text = %q, want purge result", mock.sentText) }
	select {
	case id := <-mock.deleted:
		if id != 104 { t.Fatalf("first deleted ID = %d, want 104", id) }
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for trigger deletion")
	}
	select {
	case id := <-mock.deleted:
		if id != 42 { t.Fatalf("delayed deleted ID = %d, want 42", id) }
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delayed response deletion")
	}
}

func TestMessagesFacadeEditOrReplyWithDelay(t *testing.T) {
	mock := &delayedDeleteMock{mockTelegramServicer: &mockTelegramServicer{}, deleted: make(chan int, 2)}
	// Outgoing command message
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 205, IsOutgoing: true}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	delay := 30 * time.Millisecond
	if err := ctx.Messages().EditOrReplyWithDelay("approved user", delay); err != nil {
		t.Fatalf("EditOrReplyWithDelay() error = %v", err)
	}
	if mock.editedText != "approved user" {
		t.Fatalf("edited text = %q, want 'approved user'", mock.editedText)
	}
	select {
	case id := <-mock.deleted:
		if id != 205 {
			t.Fatalf("delayed deleted ID = %d, want 205", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delayed edit message deletion")
	}
}

