package broadcast

import (
	"testing"
	"time"
)

func TestProgressCoalescerRequiresStepAndInterval(t *testing.T) {
	start := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var got []BroadcastReport
	coalescer := newProgressCoalescer(func(report BroadcastReport) {
		got = append(got, report)
	}, 100, start)

	// 100 targets => 5-target minimum delta for at most ~20 count-driven
	// snapshots, but the time gate must also be satisfied.
	coalescer.Emit(BroadcastReport{Total: 100, Sent: 4}, start.Add(time.Second), false)
	coalescer.Emit(BroadcastReport{Total: 100, Sent: 5}, start.Add(time.Second), false)
	if len(got) != 0 {
		t.Fatalf("premature snapshots=%d", len(got))
	}

	coalescer.Emit(BroadcastReport{Total: 100, Sent: 5}, start.Add(2*time.Second), false)
	if len(got) != 1 || got[0].Sent != 5 || got[0].Duration != 2*time.Second {
		t.Fatalf("first snapshot=%+v", got)
	}

	coalescer.Emit(BroadcastReport{Total: 100, Sent: 10}, start.Add(3*time.Second), false)
	if len(got) != 1 {
		t.Fatalf("interval gate did not coalesce: %+v", got)
	}
	coalescer.Emit(BroadcastReport{Total: 100, Sent: 10}, start.Add(4*time.Second), false)
	if len(got) != 2 || got[1].Sent != 10 {
		t.Fatalf("second snapshot=%+v", got)
	}
}

func TestProgressCoalescerForcesTerminalAndDeduplicates(t *testing.T) {
	start := time.Unix(100, 0)
	var got []BroadcastReport
	coalescer := newProgressCoalescer(func(report BroadcastReport) {
		got = append(got, report)
	}, 1000, start)

	terminal := BroadcastReport{Total: 1000, Sent: 990, Failed: 10}
	coalescer.Emit(terminal, start.Add(500*time.Millisecond), true)
	coalescer.Emit(terminal, start.Add(time.Second), true)
	if len(got) != 1 {
		t.Fatalf("forced terminal duplicated: %+v", got)
	}
	if got[0].Duration != 500*time.Millisecond {
		t.Fatalf("terminal duration=%v", got[0].Duration)
	}

	canceled := terminal
	canceled.Canceled = true
	coalescer.Emit(canceled, start.Add(1500*time.Millisecond), true)
	if len(got) != 2 || !got[1].Canceled {
		t.Fatalf("changed terminal state was suppressed: %+v", got)
	}
}

func TestProgressCoalescerNilCallbackIsNoop(t *testing.T) {
	coalescer := newProgressCoalescer(nil, 10, time.Now())
	coalescer.Emit(BroadcastReport{Total: 10, Sent: 10}, time.Now(), true)
}
