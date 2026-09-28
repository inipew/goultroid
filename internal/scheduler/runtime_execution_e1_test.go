package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRuntimeExecutionE1_RejectsFractionalRecurringInterval(t *testing.T) {
	engine := NewEngine(&executionLifecycleRepository{}, nil)
	_, err := engine.ScheduleRecurring(context.Background(), 1, "chat", 0, 1500*time.Millisecond, ActionMessage, "hello")
	if err == nil || !strings.Contains(err.Error(), "whole-second precision") {
		t.Fatalf("error = %v, want whole-second precision rejection", err)
	}
}
