package jobs

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestTimingOwnedOccurrenceUntrackWakesSchedulerByOccurrenceOrigin(t *testing.T) {
	m := NewManager(nil, nil, nil)
	var wakes atomic.Int32
	m.SetScheduleWake(func() { wakes.Add(1) })

	m.tracked["regular"] = &trackedOccurrence{def: JobDefinition{ID: "feature:work"}}
	m.untrack("regular")
	if got := wakes.Load(); got != 0 {
		t.Fatalf("regular job produced scheduler wake: %d", got)
	}

	// Managed schedules keep the target definition ID. Timing ownership must
	// therefore survive independently from the definition naming convention.
	m.tracked["managed-scheduled"] = &trackedOccurrence{
		def: JobDefinition{ID: "managed-target-fails"}, timingOwned: true,
	}
	m.untrack("managed-scheduled")
	if got := wakes.Load(); got != 1 {
		t.Fatalf("managed scheduled occurrence wakes=%d, want 1", got)
	}

	m.tracked["periodic"] = &trackedOccurrence{
		def: JobDefinition{ID: "arbitrary-periodic-target"}, timingOwned: true,
	}
	m.untrack("periodic")
	if got := wakes.Load(); got != 2 {
		t.Fatalf("periodic occurrence wakes=%d, want 2", got)
	}
}

func TestTimingOwnedOccurrenceClassificationUsesOrigin(t *testing.T) {
	cases := []struct {
		name string
		occ  *JobOccurrence
		want bool
	}{
		{name: "manual", occ: &JobOccurrence{OccurrenceKey: "manual:feature:1"}, want: false},
		{name: "legacy_scheduler", occ: &JobOccurrence{OccurrenceKey: "sched:42:123"}, want: true},
		{name: "periodic", occ: &JobOccurrence{OccurrenceKey: "periodic:job:1:123"}, want: true},
		{name: "durable_schedule", occ: &JobOccurrence{ScheduleID: "sched:scheduled:42", OccurrenceKey: "opaque"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := timingOwnedOccurrence(tc.occ); got != tc.want {
				t.Fatalf("timingOwnedOccurrence()=%t, want %t", got, tc.want)
			}
		})
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

	m.tracked["occ:scheduler"] = &trackedOccurrence{def: JobDefinition{ID: "managed-target"}, timingOwned: true}
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

	m.tracked["occ:failed"] = &trackedOccurrence{def: JobDefinition{ID: "managed-failing-target"}, timingOwned: true}
	failed := &JobAttempt{ID: "attempt:3", OccurrenceID: "occ:failed", LeaseEpoch: 1}
	if err := m.commitAttemptResult(context.Background(), failed, tasks.TaskResult{Outcome: tasks.OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if got := wakes.Load(); got != 1 {
		t.Fatalf("non-terminal occurrence commit woke scheduler early: %d", got)
	}
}
