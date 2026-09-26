package callback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestContractResidualCallbackPolicy(t *testing.T) {
	router := NewRouter(zap.NewNop())

	noopSvc := &recordingService{}
	noop := &core.CallbackQueryEvent{QueryID: 1, UserID: 7, Data: []byte(ActionNoop)}
	if err := dispatchForTest(router, context.Background(), noop, noopSvc); err != nil {
		t.Fatalf("noop: %v", err)
	}
	if noopSvc.lastAnswerText != "" || noopSvc.lastAnswerAlert {
		t.Fatalf("noop answer text=%q alert=%v", noopSvc.lastAnswerText, noopSvc.lastAnswerAlert)
	}

	legacySvc := &recordingService{}
	legacy := &core.CallbackQueryEvent{QueryID: 2, UserID: 7, Data: []byte("retired-legacy-payload")}
	if err := dispatchForTest(router, context.Background(), legacy, legacySvc); !errors.Is(err, ErrHandlerNotFound) {
		t.Fatalf("retired payload err=%v", err)
	}
	if legacySvc.lastAnswerText != legacyExpiredText || legacySvc.lastAnswerAlert {
		t.Fatalf("retired answer text=%q alert=%v", legacySvc.lastAnswerText, legacySvc.lastAnswerAlert)
	}
}

func TestContractResidualCallbackMetricTag(t *testing.T) {
	metrics := &recordingMetrics{}
	router := NewRouter(zap.NewNop())
	router.SetMetrics(metrics)
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{QueryID: 3, UserID: 7, Data: []byte("unknown")}
	_ = dispatchForTest(router, context.Background(), evt, svc)
	if metrics.lastTag != "expired_legacy" || metrics.callbackCount != 1 {
		t.Fatalf("metric tag=%q count=%d", metrics.lastTag, metrics.callbackCount)
	}
}

type recordingMetrics struct {
	lastTag       string
	callbackCount int
}

func (*recordingMetrics) RecordCommand(string, time.Duration, error) {}
func (r *recordingMetrics) RecordCallback(tag string, _ time.Duration, _ error) {
	r.lastTag = tag
	r.callbackCount++
}
func (*recordingMetrics) RecordSchedulerJob(int64, string, time.Duration, error) {}
func (*recordingMetrics) RecordTelegramRequest(string, time.Duration, error)     {}
func (*recordingMetrics) RecordInline(bool, int, time.Duration, error)           {}
func (*recordingMetrics) RecordInlineCacheHit()                                  {}
func (*recordingMetrics) RecordInlineCacheMiss()                                 {}
func (*recordingMetrics) Snapshot() core.MetricsSnapshot                         { return core.MetricsSnapshot{} }
func (*recordingMetrics) Reset()                                                 {}
