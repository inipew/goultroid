package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestDispatcherStopClosesAdmissionBeforeWait(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.mu.Lock()
	d.inFlight.Add(1)
	d.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- d.Stop(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for d.acceptingUpdates.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if d.acceptingUpdates.Load() {
		t.Fatal("dispatcher did not quiesce ingress")
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, &tg.Message{
		ID:      1,
		Message: ".noop",
		PeerID:  &tg.PeerChat{ChatID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	d.inFlight.Done()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish")
	}
}
