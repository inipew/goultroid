package jobs

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestTimingOwnedOccurrenceUntrackWakesSchedulerOnlyForTimingDefinitions(t *testing.T) {
	m := NewManager(nil, nil, nil)
	var wakes atomic.Int32
	m.SetScheduleWake(func() { wakes.Add(1) })

	m.tracked["regular"] = &trackedOccurrence{def: JobDefinition{ID: "feature:work"}}
	m.untrack("regular")
	if got := wakes.Load(); got != 0 {
		t.Fatalf("regular job produced scheduler wake: %d", got)
	}

	m.tracked["scheduled"] = &trackedOccurrence{def: JobDefinition{ID: "scheduler:job:42"}}
	m.untrack("scheduled")
	if got := wakes.Load(); got != 1 {
		t.Fatalf("scheduled job wakes=%d, want 1", got)
	}

	m.tracked["periodic"] = &trackedOccurrence{def: JobDefinition{ID: "periodic:runtime:cleanup"}}
	m.untrack("periodic")
	if got := wakes.Load(); got != 2 {
		t.Fatalf("periodic job wakes=%d, want 2", got)
	}
}

type timingCommitWakeStore struct {
	Store
}

func (*timingCommitWakeStore) CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error {
	return nil
}

func TestTimingOwnedDurableTerminalCommitWakesSchedulerBeforeUntrack(t *testing.T) {
	m := NewManager(nil, &timingCommitWakeStore{}, nil)
	var wakes atomic.Int32
	m.SetScheduleWake(func() { wakes.Add(1) })

	m.tracked["occ:scheduler"] = &trackedOccurrence{def: JobDefinition{ID: "scheduler:job:42"}}
	attempt := &JobAttempt{ID: "attempt:1", OccurrenceID: "occ:scheduler", LeaseEpoch: 1}
	if err := m.commitAttemptResult(context.Background(), attempt, tasks.TaskResult{Outcome: tasks.OutcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if got := wakes.Load(); got != 1 {
		t.Fatalf("durable scheduler occurrence commit wakes=%d, want 1", got)
	}

	m.tracked["occ:regular"] = &trackedOccurrence{def: JobDefinition{ID: "feature:work"}}
	regular := &JobAttempt{ID: "attempt:2", OccurrenceID: "occ:regular", LeaseEpoch: 1}
	if err := m.commitAttemptResult(context.Background(), regular, tasks.TaskResult{Outcome: tasks.OutcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if got := wakes.Load(); got != 1 {
		t.Fatalf("regular durable commit produced scheduler wake: %d", got)
	}

	m.tracked["occ:failed"] = &trackedOccurrence{def: JobDefinition{ID: "scheduler:job:43"}}
	failed := &JobAttempt{ID: "attempt:3", OccurrenceID: "occ:failed", LeaseEpoch: 1}
	if err := m.commitAttemptResult(context.Background(), failed, tasks.TaskResult{Outcome: tasks.OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if got := wakes.Load(); got != 1 {
		t.Fatalf("non-terminal occurrence commit woke scheduler early: %d", got)
	}
}
