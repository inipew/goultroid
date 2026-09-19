package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type coordinatorTaskClient struct{}

func (coordinatorTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return nil, nil
}
func (coordinatorTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (coordinatorTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (coordinatorTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type coordinatorStore struct {
	outboxPolls atomic.Int32
}

func (*coordinatorStore) SaveDefinition(context.Context, *JobDefinition) error { return nil }
func (*coordinatorStore) UpdateDefinitionCAS(context.Context, *JobDefinition, uint64) error {
	return nil
}
func (*coordinatorStore) MaterializeOccurrence(context.Context, *JobOccurrence) error { return nil }
func (*coordinatorStore) PrepareAttemptLease(context.Context, string, string, time.Duration) (*JobAttempt, error) {
	return nil, nil
}
func (*coordinatorStore) CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error {
	return nil
}
func (*coordinatorStore) CommitAttemptDeferred(context.Context, string, uint64, time.Time, string) error {
	return nil
}
func (*coordinatorStore) FinalizeOccurrence(context.Context, string, OccurrenceState) error {
	return nil
}
func (*coordinatorStore) CancelOccurrence(context.Context, string, string) error { return nil }
func (*coordinatorStore) GetOccurrence(context.Context, string) (*JobOccurrence, error) {
	return nil, nil
}
func (*coordinatorStore) GetOccurrenceByKey(context.Context, string) (*JobOccurrence, error) {
	return nil, nil
}
func (*coordinatorStore) CountAttempts(context.Context, string) (int, error)         { return 0, nil }
func (*coordinatorStore) CountRetryBudgetUses(context.Context, string) (int, error)  { return 0, nil }
func (*coordinatorStore) CountDeferrals(context.Context, string) (int, error)        { return 0, nil }
func (*coordinatorStore) LatestAttempt(context.Context, string) (*JobAttempt, error) { return nil, nil }
func (*coordinatorStore) ListUnresolvedOccurrences(context.Context, int) ([]*JobOccurrence, error) {
	return nil, nil
}
func (*coordinatorStore) DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error) {
	return 0, nil
}
func (*coordinatorStore) DeferOccurrence(context.Context, string, time.Time) error { return nil }

func (s *coordinatorStore) ListPendingOutbox(context.Context, int) ([]OutboxEvent, error) {
	s.outboxPolls.Add(1)
	return nil, nil
}
func (*coordinatorStore) MarkOutboxDelivered(context.Context, string) error { return nil }

func TestDurableCoordinatorOwnsRecoveryAndOutbox(t *testing.T) {
	store := &coordinatorStore{}
	pump := NewPersistencePump(1, 8)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())

	m := NewManager(coordinatorTaskClient{}, store, pump)
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
