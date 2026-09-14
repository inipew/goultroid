package workers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTryReserveExecutionHonorsPhysicalConcurrency(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := m.Stop(stopCtx); err != nil {
			t.Fatal(err)
		}
	}()

	var reservations []*ExecutionReservation
	for i := 0; i < 4; i++ {
		r, err := m.TryReserveExecution(PoolScheduler)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		reservations = append(reservations, r)
	}
	if _, err := m.TryReserveExecution(PoolScheduler); !errors.Is(err, ErrNoExecutionCapacity) {
		t.Fatalf("expected ErrNoExecutionCapacity, got %v", err)
	}

	reservations[0].Release()
	r, err := m.TryReserveExecution(PoolScheduler)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	r.Release()
	for _, reservation := range reservations[1:] {
		reservation.Release()
	}
}
