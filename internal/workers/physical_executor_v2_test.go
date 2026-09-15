package workers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type testResolver struct {
	mu       sync.RWMutex
	handlers map[string]tasks.HandlerFunc
}

func (r *testResolver) ResolveHandler(ref tasks.HandlerRef) (tasks.HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[ref.Name()]
	return h, ok
}

type resultSink struct {
	started chan tasks.PhysicalPermit
	results chan tasks.TaskResult
}

func (s *resultSink) Started(permit tasks.PhysicalPermit, _ time.Time) error {
	s.started <- permit
	return nil
}
func (s *resultSink) Completed(_ tasks.PhysicalPermit, result tasks.TaskResult) error {
	s.results <- result
	return nil
}

func makeAssignment(t *testing.T, slot tasks.WorkerSlot, taskID tasks.TaskID, handlerName string, timeout time.Duration, epoch uint64) tasks.WorkerAssignment {
	t.Helper()
	scope, _ := tasks.NewScopeIdentity("scope:test", 1)
	handler, _ := tasks.NewHandlerRef(handlerName, 1)
	payload, _ := tasks.NewPayloadRef("test", 1, nil)
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: taskID, Scope: scope, QuotaOwner: "owner:test", Pool: slot.Pool,
		Class: tasks.PriorityNormal, Cause: tasks.CauseManual, Handler: handler,
		Input: payload, ExecutionTimeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := tasks.NewPhysicalPermit(slot.Pool, slot.WorkerID, slot.Generation, taskID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := tasks.NewWorkerAssignment(permit, spec)
	if err != nil {
		t.Fatal(err)
	}
	return assignment
}

func TestPhysicalExecutor_PanicIsContainedAndSlotRunsNextAttempt(t *testing.T) {
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"panic": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { panic("boom") },
		"ok":    func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
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
	sink := &resultSink{started: make(chan tasks.PhysicalPermit, 2), results: make(chan tasks.TaskResult, 2)}

	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "panic-task", "panic", time.Second, 1), sink); err != nil {
		t.Fatal(err)
	}
	first := <-sink.results
	if first.Outcome() != tasks.OutcomeFailed || first.Cause() != tasks.ResultCausePanic {
		t.Fatalf("unexpected panic result: outcome=%v cause=%v", first.Outcome(), first.Cause())
	}
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "ok-task", "ok", time.Second, 2), sink); err != nil {
		t.Fatal(err)
	}
	second := <-sink.results
	if second.Outcome() != tasks.OutcomeSucceeded {
		t.Fatalf("slot did not recover after panic: %v", second.Outcome())
	}
}

func TestPhysicalExecutor_RejectsWrongGeneration(t *testing.T) {
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{"ok": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil }}}
	executor, _ := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	_ = executor.Start(context.Background())
	defer executor.Stop(context.Background())
	slot := executor.Slots()[0]
	assignment := makeAssignment(t, slot, "task", "ok", time.Second, 1)
	permit, _ := tasks.NewPhysicalPermit(slot.Pool, slot.WorkerID, slot.Generation+1, "task", 1)
	wrong, _ := tasks.NewWorkerAssignment(permit, assignment.Spec())
	sink := &resultSink{started: make(chan tasks.PhysicalPermit, 1), results: make(chan tasks.TaskResult, 1)}
	if err := executor.Assign(context.Background(), wrong, sink); !errors.Is(err, ErrInvalidPermit) {
		t.Fatalf("Assign() error = %v want invalid permit", err)
	}
}

func TestPhysicalExecutor_OneAssignmentPerSlot(t *testing.T) {
	release := make(chan struct{})
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"block": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) {
			<-release
			return tasks.ResultRef{}, nil
		},
	}}
	executor, _ := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	_ = executor.Start(context.Background())
	defer executor.Stop(context.Background())
	slot := executor.Slots()[0]
	sink := &resultSink{started: make(chan tasks.PhysicalPermit, 2), results: make(chan tasks.TaskResult, 2)}
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "first", "block", 0, 1), sink); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "second", "block", 0, 2), sink); !errors.Is(err, ErrWorkerBusy) {
		t.Fatalf("second Assign() error = %v want busy", err)
	}
	close(release)
	<-sink.results
}

func TestPhysicalExecutor_MapsExecutionTimeout(t *testing.T) {
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"wait": func(ctx context.Context, _ tasks.PayloadRef) (tasks.ResultRef, error) {
			<-ctx.Done()
			return tasks.ResultRef{}, ctx.Err()
		},
	}}
	executor, _ := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	_ = executor.Start(context.Background())
	defer executor.Stop(context.Background())
	slot := executor.Slots()[0]
	sink := &resultSink{started: make(chan tasks.PhysicalPermit, 1), results: make(chan tasks.TaskResult, 1)}
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "timeout", "wait", 5*time.Millisecond, 1), sink); err != nil {
		t.Fatal(err)
	}
	result := <-sink.results
	if result.Outcome() != tasks.OutcomeTimedOut || result.Cause() != tasks.ResultCauseExecutionTimeout {
		t.Fatalf("unexpected timeout result: outcome=%v cause=%v", result.Outcome(), result.Cause())
	}
}

func TestPhysicalExecutor_RepeatedStopStillWaitsForUncooperativeHandler(t *testing.T) {
	release := make(chan struct{})
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"block": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) {
			<-release
			return tasks.ResultRef{}, nil
		},
	}}
	executor, _ := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	slot := executor.Slots()[0]
	sink := &resultSink{started: make(chan tasks.PhysicalPermit, 1), results: make(chan tasks.TaskResult, 1)}
	if err := executor.Assign(context.Background(), makeAssignment(t, slot, "block", "block", 0, 1), sink); err != nil {
		t.Fatal(err)
	}
	<-sink.started

	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := executor.Stop(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop attempt %d error = %v want deadline exceeded", attempt+1, err)
		}
	}
	if err := executor.Start(context.Background()); err == nil {
		t.Fatal("executor restarted while previous generation was still stopping")
	}
	close(release)
	if err := executor.Stop(context.Background()); err != nil {
		t.Fatalf("final Stop() error = %v", err)
	}
	if err := executor.Start(context.Background()); err != nil {
		t.Fatalf("restart after settled stop failed: %v", err)
	}
	if got := executor.Slots()[0].Generation; got != slot.Generation+1 {
		t.Fatalf("worker generation = %d want %d", got, slot.Generation+1)
	}
	if err := executor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type reassignSink struct {
	executor *PhysicalExecutor
	second   tasks.WorkerAssignment
	done     chan error
	once     sync.Once
}

func (s *reassignSink) Started(tasks.PhysicalPermit, time.Time) error { return nil }

func (s *reassignSink) Completed(_ tasks.PhysicalPermit, _ tasks.TaskResult) error {
	s.once.Do(func() {
		s.done <- s.executor.Assign(context.Background(), s.second, s)
	})
	return nil
}

func TestPhysicalExecutor_SlotIsReusableWhenCompletionIsPublished(t *testing.T) {
	resolver := &testResolver{handlers: map[string]tasks.HandlerFunc{
		"ok": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
	}}
	executor, _ := NewPhysicalExecutor(map[tasks.PoolID]int{"general": 1}, resolver)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer executor.Stop(context.Background())
	slot := executor.Slots()[0]
	first := makeAssignment(t, slot, "first", "ok", time.Second, 1)
	second := makeAssignment(t, slot, "second", "ok", time.Second, 2)
	sink := &reassignSink{executor: executor, second: second, done: make(chan error, 1)}
	if err := executor.Assign(context.Background(), first, sink); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sink.done:
		if err != nil {
			t.Fatalf("reassign from completion callback failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback did not attempt reassign")
	}
}
