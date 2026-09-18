package broadcast_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentCount int32
	failCount int32
	sendDelay time.Duration
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if m.sendDelay > 0 {
		timer := time.NewTimer(m.sendDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if atomic.LoadInt32(&m.failCount) > 0 {
		atomic.AddInt32(&m.failCount, -1)
		return nil, errors.New("temporary error")
	}
	atomic.AddInt32(&m.sentCount, 1)
	return &tg.Message{ID: int(atomic.LoadInt32(&m.sentCount)), Message: text}, nil
}

func newBroadcastService(t *testing.T, telegram core.TelegramServicer) *broadcast.Service {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		DefaultPool: "general",
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 4, BacklogLimit: 128, PayloadBudget: 1 << 20},
		},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start task engine: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})
	svc := broadcast.NewService(telegram, zap.NewNop())
	svc.SetTasks(engine)
	return svc
}

func TestBroadcast_Success(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastService(t, mockTG)

	targets := []tg.InputPeerClass{
		&tg.InputPeerUser{UserID: 1},
		&tg.InputPeerUser{UserID: 2},
		&tg.InputPeerUser{UserID: 3},
	}

	var progressReports int32
	req := broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "Hello Broadcast!",
		Delay:   5 * time.Millisecond,
		Progress: func(report broadcast.BroadcastReport) {
			atomic.AddInt32(&progressReports, 1)
		},
	}

	ctx := context.Background()
	rep, err := svc.Broadcast(ctx, req)
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}

	if rep.Total != 3 || rep.Sent != 3 || rep.Failed != 0 {
		t.Errorf("unexpected report: %+v", rep)
	}
	if atomic.LoadInt32(&progressReports) != 3 {
		t.Errorf("expected 3 progress callbacks, got %d", progressReports)
	}
}

type rateLimitedTelegram struct {
	core.MockTelegramServicer
	calls int32
}

func (m *rateLimitedTelegram) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	atomic.AddInt32(&m.calls, 1)
	return nil, core.NewRateLimitError(30*time.Second, errors.New("telegram flood wait"))
}

func TestBroadcast_RateLimitIsNotRetriedLocally(t *testing.T) {
	mockTG := &rateLimitedTelegram{}
	svc := newBroadcastService(t, mockTG)

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
		Text:    "hello",
		Delay:   time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("broadcast should account for a per-target rate limit without failing the whole run: %v", err)
	}
	if got := atomic.LoadInt32(&mockTG.calls); got != 1 {
		t.Fatalf("expected exactly one SendMessage call and no local retry, got %d", got)
	}
	if rep == nil || rep.RateLimited != 1 || rep.Failed != 1 || rep.Sent != 0 {
		t.Fatalf("unexpected broadcast report: %+v", rep)
	}
}

func TestBroadcast_Validation(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastService(t, mockTG)
	ctx := context.Background()

	// Empty targets
	_, err := svc.Broadcast(ctx, broadcast.BroadcastRequest{Text: "Test"})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs on empty targets, got %v", err)
	}

	// Empty text
	_, err = svc.Broadcast(ctx, broadcast.BroadcastRequest{
		Targets: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs on empty text, got %v", err)
	}
}

func TestBroadcast_CancelActive(t *testing.T) {
	mockTG := &mockTelegram{sendDelay: 50 * time.Millisecond}
	svc := newBroadcastService(t, mockTG)

	var targets []tg.InputPeerClass
	for i := 0; i < 20; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}

	req := broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "Long broadcast",
	}

	ctx := context.Background()
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _ = svc.Broadcast(ctx, req)
	}()

	// Wait briefly for at least 1 message then cancel
	time.Sleep(20 * time.Millisecond)
	canceled := svc.CancelActive()
	if !canceled {
		t.Errorf("expected CancelActive to return true")
	}

	<-done
	sent := atomic.LoadInt32(&mockTG.sentCount)
	if sent >= 20 {
		t.Errorf("expected broadcast to cancel before sending all 20, sent: %d", sent)
	}
}


func TestBroadcast_BackpressuresInsteadOfDroppingOnTaskBacklog(t *testing.T) {
	mockTG := &mockTelegram{sendDelay: 10 * time.Millisecond}
	svc := newBroadcastService(t, mockTG)

	const targetCount = 200 // larger than the test engine backlog (128)
	targets := make([]tg.InputPeerClass, 0, targetCount)
	for i := 0; i < targetCount; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "bounded broadcast",
	})
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}
	if rep.Sent != targetCount || rep.Failed != 0 {
		t.Fatalf("temporary TaskEngine backlog saturation dropped targets: %+v", rep)
	}
	if got := atomic.LoadInt32(&mockTG.sentCount); got != targetCount {
		t.Fatalf("sent count = %d, want %d", got, targetCount)
	}
}
