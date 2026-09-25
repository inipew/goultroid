package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestContextRespondUsesOwnedDelayedScheduler(t *testing.T) {
	mock := &mockTelegramServicer{}
	scheduler := &immediateDelayedActions{}
	ctx := &Context{
		Ctx:            context.Background(),
		Message:        &Message{ID: 104},
		Svc:            mock,
		PeerID:         &tg.InputPeerSelf{},
		DelayedActions: scheduler,
	}

	if err := ctx.Respond("done", ResponseOptions{AutoDeleteDelay: time.Minute}); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	if len(scheduler.delays) != 1 || scheduler.delays[0] != time.Minute {
		t.Fatalf("scheduled delays = %v, want [1m]", scheduler.delays)
	}
	if len(scheduler.retainedBytes) != 1 || scheduler.retainedBytes[0] != delayedDeleteRetainedBytes {
		t.Fatalf("retained weights = %v, want [%d]", scheduler.retainedBytes, delayedDeleteRetainedBytes)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 42 {
		t.Fatalf("deleted IDs = %v, want delayed response [42]", mock.deletedIDs)
	}
}

func TestContextRespondAutoDeleteRequiresOwnedScheduler(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 104},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}

	err := ctx.Respond("done", ResponseOptions{AutoDeleteDelay: time.Minute})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Respond() error = %v, want ErrUnavailable", err)
	}
	if ctx.LastResponseID != 42 {
		t.Fatalf("LastResponseID = %d, want sent response 42", ctx.LastResponseID)
	}
}
