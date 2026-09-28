package jobs

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeExecutionE1_NextIntervalDueAfterBoundaries(t *testing.T) {
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{name: "exact first slot", now: base, want: base.Add(time.Second)},
		{name: "exact later slot", now: base.Add(time.Second), want: base.Add(2 * time.Second)},
		{name: "between slots", now: base.Add(1500 * time.Millisecond), want: base.Add(2 * time.Second)},
		{name: "long downtime", now: base.Add(30*24*time.Hour + 500*time.Millisecond), want: base.Add(30*24*time.Hour + time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextIntervalDueAfter(base, tc.now, time.Second)
			if err != nil {
				t.Fatalf("advance interval: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("next due = %v, want %v", got, tc.want)
			}
			if !got.After(tc.now) {
				t.Fatalf("next due %v is not strictly after now %v", got, tc.now)
			}
		})
	}
}

func TestRuntimeExecutionE1_NextIntervalDueAfterRejectsSaturatedSpan(t *testing.T) {
	_, err := NextIntervalDueAfter(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), time.Second)
	if err == nil || !strings.Contains(err.Error(), "exceeds time.Duration range") {
		t.Fatalf("error = %v, want saturated-span rejection", err)
	}
}
