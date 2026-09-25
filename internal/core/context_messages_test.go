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
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
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
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	if err := ctx.Messages().ReplyAndDelete("purged 4 messages"); err != nil {
		t.Fatalf("ReplyAndDelete() error = %v, want nil after successful reply", err)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID = %d, want 42", ctx.LastResponseID)
	}
}

func TestMessagesFacadeEditOrReplyIncomingDeletesTrigger(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104, IsOutgoing: false}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	if err := ctx.Messages().EditOrReply("purged 4 messages"); err != nil {
		t.Fatalf("EditOrReply() error = %v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 104 {
		t.Fatalf("deleted IDs = %v, want [104]", mock.deletedIDs)
	}
}

func TestMessagesFacadeAssistantReplyPreservesTriggerAndReusesAnchor(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Source:  ExecutionAssistant,
		Message: &Message{ID: 104, IsOutgoing: false},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}
	if err := ctx.Messages().EditOrReply("working"); err != nil {
		t.Fatalf("EditOrReply() error = %v", err)
	}
	if len(mock.deletedIDs) != 0 {
		t.Fatalf("assistant trigger deleted: %v", mock.deletedIDs)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID=%d, want 42", ctx.LastResponseID)
	}
	if err := ctx.Messages().EditOrReply("done"); err != nil {
		t.Fatalf("second EditOrReply() error = %v", err)
	}
	if mock.editedText != "done" {
		t.Fatalf("edited text=%q, want done", mock.editedText)
	}
}

func TestMessagesFacadeAssistantDelayedReplyPreservesTrigger(t *testing.T) {
	mock := &delayedDeleteMock{mockTelegramServicer: &mockTelegramServicer{}, deleted: make(chan int, 1)}
	scheduler := &immediateDelayedActions{}
	ctx := &Context{
		Ctx:            context.Background(),
		Source:         ExecutionAssistant,
		Message:        &Message{ID: 104, IsOutgoing: false},
		Svc:            mock,
		PeerID:         &tg.InputPeerSelf{},
		DelayedActions: scheduler,
	}
	if err := ctx.Messages().EditOrReplyWithDelay("done", time.Second); err != nil {
		t.Fatalf("EditOrReplyWithDelay() error = %v", err)
	}
	if len(mock.mockTelegramServicer.deletedIDs) != 1 || mock.mockTelegramServicer.deletedIDs[0] != 42 {
		t.Fatalf("deleted IDs=%v, want only response [42]", mock.mockTelegramServicer.deletedIDs)
	}
}

type immediateDelayedActions struct {
	delays        []time.Duration
	retainedBytes []int64
}

func (s *immediateDelayedActions) Schedule(ctx context.Context, delay time.Duration, retainedBytes int64, action func(context.Context) error) error {
	s.delays = append(s.delays, delay)
	s.retainedBytes = append(s.retainedBytes, retainedBytes)
	return action(ctx)
}

type delayedDeleteMock struct {
	*mockTelegramServicer
	deleted chan int
}

func (m *delayedDeleteMock) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	err := m.mockTelegramServicer.DeleteMessage(ctx, peer, msgIDs)
	if err == nil && len(msgIDs) > 0 {
		m.deleted <- msgIDs[0]
	}
	return err
}

func TestMessagesFacadeReplyAndDeleteWithDelay(t *testing.T) {
	mock := &delayedDeleteMock{mockTelegramServicer: &mockTelegramServicer{}, deleted: make(chan int, 2)}
	scheduler := &immediateDelayedActions{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 104}, Svc: mock, PeerID: &tg.InputPeerSelf{}, DelayedActions: scheduler}
	delay := 30 * time.Millisecond
	if err := ctx.Messages().ReplyAndDeleteWithDelay("purged 5 messages", delay); err != nil {
		t.Fatalf("ReplyAndDeleteWithDelay() error = %v", err)
	}
	if mock.sentText != "purged 5 messages" {
		t.Fatalf("sent text = %q, want purge result", mock.sentText)
	}
	if len(scheduler.retainedBytes) != 1 || scheduler.retainedBytes[0] != delayedDeleteRetainedBytes {
		t.Fatalf("delayed delete retained weight = %v, want [%d]", scheduler.retainedBytes, delayedDeleteRetainedBytes)
	}
	select {
	case id := <-mock.deleted:
		if id != 104 {
			t.Fatalf("first deleted ID = %d, want 104", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for trigger deletion")
	}
	select {
	case id := <-mock.deleted:
		if id != 42 {
			t.Fatalf("delayed deleted ID = %d, want 42", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delayed response deletion")
	}
}

func TestMessagesFacadeEditOrReplyWithDelay(t *testing.T) {
	mock := &delayedDeleteMock{mockTelegramServicer: &mockTelegramServicer{}, deleted: make(chan int, 2)}
	// Outgoing command message
	scheduler := &immediateDelayedActions{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 205, IsOutgoing: true}, Svc: mock, PeerID: &tg.InputPeerSelf{}, DelayedActions: scheduler}
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

func TestMessagesFacadeDelayedDeleteRequiresOwnedScheduler(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{Ctx: context.Background(), Message: &Message{ID: 205, IsOutgoing: true}, Svc: mock, PeerID: &tg.InputPeerSelf{}}
	err := ctx.Messages().EditOrReplyWithDelay("approved user", time.Second)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable without delayed action owner, got %v", err)
	}
}

func TestP5UserbotSemanticResponseReusesOneAnchor(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 205, IsOutgoing: true},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := ctx.Status("starting"); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if ctx.LastResponseID != 205 {
		t.Fatalf("status anchor = %d, want outgoing command 205", ctx.LastResponseID)
	}
	if err := ctx.Progress("halfway"); err != nil {
		t.Fatalf("Progress() error = %v", err)
	}
	if err := ctx.Result("done"); err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if ctx.LastResponseID != 205 {
		t.Fatalf("final anchor = %d, want 205", ctx.LastResponseID)
	}
	if mock.sentText != "" {
		t.Fatalf("semantic response sent a new message %q instead of reusing the outgoing command", mock.sentText)
	}
	if len(mock.deletedIDs) != 0 {
		t.Fatalf("semantic response deleted messages: %v", mock.deletedIDs)
	}
}
