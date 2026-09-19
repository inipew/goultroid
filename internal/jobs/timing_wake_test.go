package jobs

import (
	"sync/atomic"
	"testing"
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
