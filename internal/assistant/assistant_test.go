package assistant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/assistant/client"
	"go.uber.org/zap"
)

func TestAssistantApp_LifecycleAndMetadata(t *testing.T) {
	app := assistant.NewApp(1234, "hash", "test_bot_token", zap.NewNop())

	if app.IsRunning() {
		t.Fatalf("expected app to not be running initially")
	}

	if app.Username() != "GoUltroidBot" {
		t.Fatalf("expected default username GoUltroidBot, got %s", app.Username())
	}

	if app.StartTime().After(time.Now()) {
		t.Fatalf("expected StartTime to be in the past")
	}

	// Setting owner authorization
	app.SetOwner(12345, func() []int64 { return []int64{67890} })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Stop when not running is a no-op
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("unexpected error on Stop: %v", err)
	}
}

func TestAssistantApp_EmptyTokenRequired(t *testing.T) {
	app := assistant.NewApp(1234, "hash", "", zap.NewNop())

	ctx := context.Background()
	err := app.Start(ctx)
	if !errors.Is(err, client.ErrBotTokenRequired) {
		t.Fatalf("expected ErrBotTokenRequired, got %v", err)
	}
}

func TestNewBotClient_Alias(t *testing.T) {
	app := assistant.NewBotClient(1234, "hash", "token", zap.NewNop())
	if app == nil {
		t.Fatalf("expected non-nil AssistantApp from NewBotClient alias")
	}
}
