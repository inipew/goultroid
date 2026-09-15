package workers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type rejectingStartSink struct {
	completed chan tasks.TaskResult
}

func (s *rejectingStartSink) Started(tasks.PhysicalPermit, time.Time) error {
	return errors.New("stale start grant")
}

func (s *rejectingStartSink) Completed(_ tasks.PhysicalPermit, result tasks.TaskResult) error {
	s.completed <- result
	return nil
}

func TestPhysicalExecutor_RejectedStartNeverRunsHandler(t *testing.T) {
	var executions atomic.Int32
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"guarded": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) {
			executions.Add(1)
			return tasks.ResultRef{}, nil
		},
	}}
	executor, err := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer executor.Stop(context.Background())

	slot := executor.Slots()[0]
	sink := &rejectingStartSink{completed: make(chan tasks.TaskResult, 2)}
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "guarded-task", "guarded", time.Second, 1), sink); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-sink.completed:
		if result.Outcome() != tasks.OutcomeAbortedBeforeStart || result.Cause() != tasks.ResultCauseInvalidPermit {
			t.Fatalf("rejected-start result = outcome=%v cause=%v", result.Outcome(), result.Cause())
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not publish terminal result after rejected start")
	}
	if executions.Load() != 0 {
		t.Fatalf("handler executed %d times after start rejection", executions.Load())
	}
	select {
	case extra := <-sink.completed:
		t.Fatalf("worker published duplicate terminal result: %+v", extra)
	default:
	}
}
