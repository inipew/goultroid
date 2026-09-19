package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type coordinatorStore struct {
	fakeStore
	outboxPolls atomic.Int32
}

func (s *coordinatorStore) ListPendingOutbox(context.Context, int) ([]OutboxEvent, error) {
	s.outboxPolls.Add(1)
	return nil, nil
}
func (s *coordinatorStore) MarkOutboxDelivered(context.Context, string) error { return nil }

func TestDurableCoordinatorOwnsRecoveryAndOutbox(t *testing.T) {
	store := &coordinatorStore{}
	pump := NewPersistencePump(1, 8)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())

	m := NewManager(newFakeTaskClient(), store, pump)
	m.SetOutboxSink(func(context.Context, OutboxEvent) error { return nil })
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Recovery + outbox are represented by one baseline Jobs worker. Retry
	// workers remain demand-driven and are accounted separately.
	if got := m.workersRemaining.Load(); got != 1 {
		t.Fatalf("baseline jobs workers=%d, want 1 durable coordinator", got)
	}

	deadline := time.Now().Add(time.Second)
	for store.outboxPolls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.outboxPolls.Load() == 0 {
		t.Fatal("startup outbox wake was not serviced")
	}

	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
