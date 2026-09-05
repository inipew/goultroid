package core

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultMetricsTracker_RecordAndSnapshot(t *testing.T) {
	tracker := NewDefaultMetricsTracker()

	// Record command executions
	tracker.RecordCommand("ping", 10*time.Millisecond, nil)
	tracker.RecordCommand("ping", 20*time.Millisecond, nil)
	tracker.RecordCommand("ping", 5*time.Millisecond, errors.New("ping failed"))

	tracker.RecordCommand("ban", 50*time.Millisecond, nil)

	// Record scheduler
	tracker.RecordSchedulerJob(1, "remind", 100*time.Millisecond, nil)
	tracker.RecordSchedulerJob(2, "remind", 150*time.Millisecond, errors.New("timeout"))

	// Record telegram
	tracker.RecordTelegramRequest("sendMessage", 25*time.Millisecond, nil)
	tracker.RecordTelegramRequest("deleteMessage", 30*time.Millisecond, errors.New("network"))

	snap := tracker.Snapshot()

	if snap.TotalCommands != 4 {
		t.Errorf("expected 4 total commands, got %d", snap.TotalCommands)
	}
	if snap.TotalErrors != 1 {
		t.Errorf("expected 1 command error, got %d", snap.TotalErrors)
	}

	pingStats, exists := snap.Commands["ping"]
	if !exists {
		t.Fatalf("expected stats for 'ping'")
	}
	if pingStats.TotalCalls != 3 {
		t.Errorf("expected 3 calls for 'ping', got %d", pingStats.TotalCalls)
	}
	if pingStats.Errors != 1 {
		t.Errorf("expected 1 error for 'ping', got %d", pingStats.Errors)
	}
	if pingStats.MinTime != 5*time.Millisecond {
		t.Errorf("expected min time 5ms, got %v", pingStats.MinTime)
	}
	if pingStats.MaxTime != 20*time.Millisecond {
		t.Errorf("expected max time 20ms, got %v", pingStats.MaxTime)
	}

	if snap.SchedulerJobsRun != 2 || snap.SchedulerJobsFail != 1 {
		t.Errorf("unexpected scheduler stats: run=%d, fail=%d", snap.SchedulerJobsRun, snap.SchedulerJobsFail)
	}
	if snap.TelegramRequests != 2 || snap.TelegramErrors != 1 {
		t.Errorf("unexpected telegram stats: reqs=%d, errs=%d", snap.TelegramRequests, snap.TelegramErrors)
	}

	// Test Reset
	tracker.Reset()
	snapAfterReset := tracker.Snapshot()
	if snapAfterReset.TotalCommands != 0 || len(snapAfterReset.Commands) != 0 {
		t.Errorf("expected empty stats after reset, got %+v", snapAfterReset)
	}
}
