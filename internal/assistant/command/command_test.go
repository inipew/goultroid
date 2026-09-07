package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"go.uber.org/zap"
)

type fakeInteraction struct {
	lastSentText   string
	lastSentMarkup tg.ReplyMarkupClass
}

func (f *fakeInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}
func (f *fakeInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	return nil
}
func (f *fakeInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}
func (f *fakeInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}
func (f *fakeInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}
func (f *fakeInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	f.lastSentText = text
	f.lastSentMarkup = markup
	return &tg.Message{ID: 100}, nil
}

func TestCommandRouter_Dispatch(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	startTime := time.Now().Add(-10 * time.Minute)
	command.AttachDefaultCommands(r, func() string { return "TestBot" }, func() time.Time { return startTime }, presentation.RenderScreen)

	fake := &fakeInteraction{}
	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	// 1. /start with bot suffix
	err := r.Dispatch(ctx, 12345, peer, "/start@TestBot", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /start: %v", err)
	}
	if fake.lastSentMarkup == nil {
		t.Fatalf("expected markup returned on /start")
	}

	// 2. /ping
	err = r.Dispatch(ctx, 12345, peer, "/ping", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /ping: %v", err)
	}
	if fake.lastSentText == "" {
		t.Fatalf("expected text reply on /ping")
	}

	// 3. /alive
	err = r.Dispatch(ctx, 12345, peer, "/alive", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /alive: %v", err)
	}
	if fake.lastSentText == "" {
		t.Fatalf("expected text reply on /alive")
	}

	// 4. Unknown command
	err = r.Dispatch(ctx, 12345, peer, "/unknown_cmd", fake)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("expected ErrUnknownCommand, got %v", err)
	}

	// 5. Non-command text should be silently ignored (return nil)
	err = r.Dispatch(ctx, 12345, peer, "hello bot", fake)
	if err != nil {
		t.Fatalf("expected non-command to return nil, got %v", err)
	}
}
