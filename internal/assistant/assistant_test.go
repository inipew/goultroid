package assistant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/runtime"
	"go.uber.org/zap"
)

func TestAssistantApp_LifecycleAndMetadata(t *testing.T) {
	app := assistant.NewApp(1234, "hash", "test_bot_token", zap.NewNop())

	if app.IsRunning() {
		t.Fatalf("expected app to not be running initially")
	}

	if app.Username() != "" {
		t.Fatalf("expected no Assistant username before readiness, got %q", app.Username())
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

func TestAssistantAppP6HLifecycleDeclaresDrainDependenciesAndQuiesce(t *testing.T) {
	app := assistant.NewApp(1234, "hash", "token", zap.NewNop())

	deps := make(map[string]bool)
	for _, dep := range app.Dependencies() {
		deps[dep] = true
	}
	for _, required := range []string{"dispatcher", "settings", "taskengine"} {
		if !deps[required] {
			t.Fatalf("assistant missing lifecycle dependency %q: %v", required, app.Dependencies())
		}
	}
	if _, ok := any(app).(runtime.Quiescer); !ok {
		t.Fatal("AssistantApp does not expose runtime.Quiescer")
	}
}
