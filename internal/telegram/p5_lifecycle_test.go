package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestDispatcherDrainClosesIngressBeforeWaiting(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.inFlight.Add(1)
	go func() { time.Sleep(20 * time.Millisecond); d.inFlight.Done() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if d.acceptingUpdates.Load() {
		t.Fatal("dispatcher still accepting after Drain")
	}
	deps := d.Dependencies()
	found := false
	for _, dep := range deps {
		if dep == "taskengine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dispatcher dependencies=%v, want taskengine", deps)
	}
}
