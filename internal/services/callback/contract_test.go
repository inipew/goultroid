package callback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// TestContract_CallbackFiveScenarios verifies bug13 #42 contract:
// callback -> wrong user / wrong chat / expired / consumed / valid = correct code
func TestContract_CallbackFiveScenarios(t *testing.T) {
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	h := &mockHandler{namespace: "contract"}
	if err := router.Register(h); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 1. wrong user = rejected (ErrUnauthorized, alert)
	t.Run("wrong_user_rejected", func(t *testing.T) {
		token := store.StoreWithScope("secret", StateScope{UserID: 111}, 5*time.Minute)
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 1, UserID: 222, Data: EncodeCallbackData("contract", "view", token)}
		err := router.Dispatch(context.Background(), evt, svc)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
		if !svc.lastAnswerAlert {
			t.Fatalf("expected alert for unauthorized")
		}
		if svc.lastAnswerText == "" {
			t.Fatalf("expected user alert text")
		}
		if h.handled {
			t.Fatalf("handler must not run")
		}
		// metric tag verification via CallbackFailure is internal; ensure reject path sets alert
	})

	// 2. wrong chat = rejected
	t.Run("wrong_chat_rejected", func(t *testing.T) {
		token := store.StoreWithScope("x", StateScope{UserID: 111, ChatID: 999}, 5*time.Minute)
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 2, UserID: 111, ChatID: 888, Data: EncodeCallbackData("contract", "view", token)}
		err := router.Dispatch(context.Background(), evt, svc)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for chat mismatch, got %v", err)
		}
	})

	// 3. expired = rejected
	t.Run("expired_rejected", func(t *testing.T) {
		token := store.Store("short", 111, 10*time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 3, UserID: 111, Data: EncodeCallbackData("contract", "view", token)}
		err := router.Dispatch(context.Background(), evt, svc)
		if !errors.Is(err, ErrStateExpired) {
			t.Fatalf("expected ErrStateExpired, got %v", err)
		}
		if !svc.lastAnswerAlert {
			t.Fatalf("expected alert for expired")
		}
	})

	// 4. consumed = rejected
	t.Run("consumed_rejected", func(t *testing.T) {
		token := store.StoreWithScope("one", StateScope{UserID: 111, SingleUse: true}, 5*time.Minute)
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 4, UserID: 111, Data: EncodeCallbackData("contract", "view", token)}
		if err := router.Dispatch(context.Background(), evt, svc); err != nil {
			t.Fatalf("first dispatch should succeed, got %v", err)
		}
		h.handled = false
		svc2 := &recordingService{}
		err := router.Dispatch(context.Background(), evt, svc2)
		if !errors.Is(err, ErrStateNotFound) {
			t.Fatalf("expected ErrStateNotFound on replay, got %v", err)
		}
		if h.handled {
			t.Fatalf("handler must not run on consumed")
		}
	})

	// 5. valid = executed
	t.Run("valid_executed", func(t *testing.T) {
		h.handled = false
		token := store.Store("ok", 111, 5*time.Minute)
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 5, UserID: 111, Data: EncodeCallbackData("contract", "view", token)}
		err := router.Dispatch(context.Background(), evt, svc)
		if err != nil {
			t.Fatalf("valid dispatch failed: %v", err)
		}
		if !h.handled {
			t.Fatalf("handler must run for valid")
		}
	})

	// 6. handler missing = HandlerNotFound
	t.Run("handler_not_found", func(t *testing.T) {
		svc := &recordingService{}
		evt := &core.CallbackQueryEvent{QueryID: 6, UserID: 111, Data: EncodeCallbackData("missing", "act", "abc12345")}
		err := router.Dispatch(context.Background(), evt, svc)
		if !errors.Is(err, ErrHandlerNotFound) {
			t.Fatalf("expected ErrHandlerNotFound, got %v", err)
		}
	})
}

// TestContract_CallbackMetricTags ensures reject uses correct MetricTag per failure type.
func TestContract_CallbackMetricTags(t *testing.T) {
	// Verify CallbackFailure MetricTag mapping: expired->expired, unauthorized->unauthorized, etc.
	// Indirectly via router which uses reject() -> metrics.RecordCallback.
	// We use a recording metrics collector.
	m := &recordingMetrics{}
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	router.SetMetrics(m)
	h := &mockHandler{namespace: "metric"}
	_ = router.Register(h)

	token := store.StoreWithScope("s", StateScope{UserID: 1, ChatID: 100}, 5*time.Minute)
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{QueryID: 10, UserID: 1, ChatID: 999, Data: EncodeCallbackData("metric", "view", token)}
	_ = router.Dispatch(context.Background(), evt, svc)
	if m.lastTag != "unauthorized" {
		t.Errorf("expected metric tag unauthorized, got %q", m.lastTag)
	}
}

type recordingMetrics struct {
	lastTag string
}

func (r *recordingMetrics) RecordCommand(_ string, _ time.Duration, _ error)               {}
func (r *recordingMetrics) RecordCallback(tag string, _ time.Duration, _ error)            { r.lastTag = tag }
func (r *recordingMetrics) RecordSchedulerJob(_ int64, _ string, _ time.Duration, _ error) {}
func (r *recordingMetrics) RecordTelegramRequest(_ string, _ time.Duration, _ error)       {}
func (r *recordingMetrics) RecordInline(_ bool, _ int, _ time.Duration, _ error)           {}
func (r *recordingMetrics) RecordInlineCacheHit()                                          {}
func (r *recordingMetrics) RecordInlineCacheMiss()                                         {}
func (r *recordingMetrics) Snapshot() core.MetricsSnapshot                                 { return core.MetricsSnapshot{} }
func (r *recordingMetrics) Reset()                                                         {}
