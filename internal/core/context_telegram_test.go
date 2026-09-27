package core

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestContextUsesCapabilityContainerWithoutAggregateService(t *testing.T) {
	t.Parallel()

	ctx := &Context{
		Ctx:      context.Background(),
		Telegram: TelegramCapabilities{Messages: &messageCapabilityFake{}},
		PeerID:   &tg.InputPeerSelf{},
	}
	if ctx.Svc != nil {
		t.Fatal("compatibility aggregate unexpectedly configured")
	}
	if err := ctx.Reply("hello"); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
}

func TestTelegramCapabilitiesFromDiscoversContextualExtensionsIndependently(t *testing.T) {
	t.Parallel()

	svc := &MockTelegramServicer{}
	caps := TelegramCapabilitiesFrom(svc)
	if caps.Messages == nil || caps.Admin == nil || caps.Media == nil || caps.Peers == nil || caps.Profile == nil {
		t.Fatal("aggregate service did not populate command capabilities")
	}
	if caps.ContextualMessages != nil || caps.ContextualMedia != nil {
		t.Fatal("plain command service unexpectedly gained contextual extensions")
	}
}

type recordingMessageCapability struct {
	messageCapabilityFake
	lastEditID   int
	lastDeleteID int
}

func (r *recordingMessageCapability) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return &tg.Message{ID: 42}, nil
}

func (r *recordingMessageCapability) EditMessage(_ context.Context, _ tg.InputPeerClass, msgID int, _ string) error {
	r.lastEditID = msgID
	return nil
}

func (r *recordingMessageCapability) DeleteMessage(_ context.Context, _ tg.InputPeerClass, msgIDs []int) error {
	if len(msgIDs) > 0 {
		r.lastDeleteID = msgIDs[0]
	}
	return nil
}

type immediateDelayedActionScheduler struct {
	scheduled bool
}

func (s *immediateDelayedActionScheduler) Schedule(ctx context.Context, _ time.Duration, _ int64, action func(context.Context) error) error {
	s.scheduled = true
	return action(ctx)
}

func TestM2EditUsesMessageCapabilityWithoutSvc(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageCapability{}
	ctx := &Context{
		Ctx:            context.Background(),
		Telegram:       TelegramCapabilities{Messages: messages},
		PeerID:         &tg.InputPeerSelf{},
		LastResponseID: 17,
	}
	if err := ctx.Edit("updated"); err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if messages.lastEditID != 17 {
		t.Fatalf("Edit() message id = %d, want 17", messages.lastEditID)
	}
}

func TestM2RespondAutoDeleteUsesMessageCapabilityWithoutSvc(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageCapability{}
	delayed := &immediateDelayedActionScheduler{}
	ctx := &Context{
		Ctx:            context.Background(),
		Telegram:       TelegramCapabilities{Messages: messages},
		PeerID:         &tg.InputPeerSelf{},
		DelayedActions: delayed,
	}
	if err := ctx.Respond("hello", ResponseOptions{AutoDeleteDelay: time.Second}); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	if !delayed.scheduled {
		t.Fatal("auto-delete was not scheduled")
	}
	if messages.lastDeleteID != 42 {
		t.Fatalf("deleted message id = %d, want 42", messages.lastDeleteID)
	}
}
