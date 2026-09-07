package callback

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type fakeInlineInteraction struct {
	answered    bool
	answerText  string
	answerAlert bool
	answerCount int
	edited      bool
	editText    string
}

func (f *fakeInlineInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	f.answered = true
	f.answerText = text
	f.answerAlert = alert
	f.answerCount++
	return nil
}

func (f *fakeInlineInteraction) Edit(ctx context.Context, target interaction.InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	f.edited = true
	f.editText = text
	return nil
}

func TestInlineTransaction_LifecycleAndAnswering(t *testing.T) {
	ctx := context.Background()
	fake := &fakeInlineInteraction{}
	target := interaction.NewInlineTarget(101, &tg.InputBotInlineMessageID{DCID: 1, ID: 2, AccessHash: 3}, 555)

	payload := ParsedPayload{Namespace: "test", Action: "action"}
	tx := NewInlineTransaction(101, 202, payload, target, fake)

	if tx.State() != StateReceived {
		t.Fatalf("expected StateReceived, got %v", tx.State())
	}

	// First answer should succeed
	err := tx.Answer(ctx, "hello", false)
	if err != nil {
		t.Fatalf("expected nil error on first answer, got %v", err)
	}
	if !tx.IsAnswered() {
		t.Fatalf("expected IsAnswered to be true")
	}
	if tx.State() != StateAnswered {
		t.Fatalf("expected StateAnswered, got %v", tx.State())
	}

	// Second answer should return ErrCallbackAlreadyAnswered
	err2 := tx.Answer(ctx, "hello again", false)
	if !errors.Is(err2, interaction.ErrCallbackAlreadyAnswered) {
		t.Fatalf("expected ErrCallbackAlreadyAnswered, got %v", err2)
	}

	// Edit should succeed
	err = tx.Edit(ctx, "updated text", nil)
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	if !fake.edited || fake.editText != "updated text" {
		t.Fatalf("expected edit with 'updated text'")
	}

	// Delete on inline target should return ErrUnsupportedTarget
	err = tx.Delete(ctx)
	if !errors.Is(err, interaction.ErrUnsupportedTarget) {
		t.Fatalf("expected ErrUnsupportedTarget on inline delete, got %v", err)
	}
}

func TestInlineTransaction_SingleFlightConcurrent(t *testing.T) {
	ctx := context.Background()
	fake := &fakeInlineInteraction{}
	target := interaction.NewInlineTarget(101, &tg.InputBotInlineMessageID{DCID: 1, ID: 2, AccessHash: 3}, 555)
	tx := NewInlineTransaction(101, 202, ParsedPayload{Namespace: "test", Action: "act"}, target, fake)

	var wg sync.WaitGroup
	var successCount int
	var alreadyAnsweredCount int
	var mu sync.Mutex

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := tx.Answer(ctx, "concurrent", false)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else if errors.Is(err, interaction.ErrCallbackAlreadyAnswered) {
				alreadyAnsweredCount++
			}
		}()
	}
	wg.Wait()

	if fake.answerCount != 1 {
		t.Fatalf("expected exactly 1 underlying RPC answer, got %d", fake.answerCount)
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successCount)
	}
	if alreadyAnsweredCount != 19 {
		t.Fatalf("expected 19 ErrCallbackAlreadyAnswered, got %d", alreadyAnsweredCount)
	}
}

func TestRouter_DispatchInline_FullPipeline(t *testing.T) {
	ctx := context.Background()
	logger := zap.NewNop()
	router := NewRouter(logger)
	metrics := core.NewDefaultMetricsTracker()
	router.SetMetricsCollector(metrics)

	executed := false
	router.RegisterInline("plugin", "click", func(ctx context.Context, tx *InlineTransaction) error {
		executed = true
		return tx.Answer(ctx, "clicked!", false)
	})

	fake := &fakeInlineInteraction{}
	target := interaction.NewInlineTarget(123, &tg.InputBotInlineMessageID{DCID: 1, ID: 2, AccessHash: 3}, 555)
	tx := NewInlineTransaction(123, 456, ParsedPayload{Namespace: "plugin", Action: "click"}, target, fake)

	err := router.DispatchInline(ctx, tx)
	if err != nil {
		t.Fatalf("dispatch inline failed: %v", err)
	}
	if !executed {
		t.Fatalf("expected inline handler to execute")
	}
	if tx.State() != StateCompleted {
		t.Fatalf("expected StateCompleted, got %v", tx.State())
	}
	if !fake.answered || fake.answerText != "clicked!" {
		t.Fatalf("expected answered with 'clicked!', got %q", fake.answerText)
	}

	snap := metrics.Snapshot()
	if snap.CallbackReceived == 0 {
		t.Fatalf("expected callback metrics to be recorded, got 0")
	}
}
