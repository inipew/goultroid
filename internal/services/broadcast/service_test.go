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
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentCount int32
	failCount int32
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if atomic.LoadInt32(&m.failCount) > 0 {
		atomic.AddInt32(&m.failCount, -1)
		return nil, errors.New("temporary error")
	}
	atomic.AddInt32(&m.sentCount, 1)
	return &tg.Message{ID: int(atomic.LoadInt32(&m.sentCount)), Message: text}, nil
}

func TestBroadcast_Success(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := broadcast.NewService(mockTG, zap.NewNop())

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

func TestBroadcast_Validation(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := broadcast.NewService(mockTG, zap.NewNop())
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
	mockTG := &mockTelegram{}
	svc := broadcast.NewService(mockTG, zap.NewNop())

	var targets []tg.InputPeerClass
	for i := 0; i < 20; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}

	req := broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "Long broadcast",
		Delay:   50 * time.Millisecond,
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
