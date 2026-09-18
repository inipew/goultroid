package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/resource"
)

type mockPanicReporter struct {
	mu      sync.Mutex
	reports []core.PanicReport
}

func (m *mockPanicReporter) ReportPanic(report core.PanicReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, report)
}

func (m *mockPanicReporter) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reports)
}

func (m *mockPanicReporter) Last() core.PanicReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reports) == 0 {
		return core.PanicReport{}
	}
	return m.reports[len(m.reports)-1]
}

func TestScope_Go_PanicRecovery(t *testing.T) {
	mgr := resource.NewManager()
	scope := NewScopeWithManager(context.Background(), "test-plugin", mgr)
	reporter := &mockPanicReporter{}
	scope.SetPanicReporter(reporter)

	panicked := make(chan struct{})
	err := scope.Go(func(ctx context.Context) {
		close(panicked)
		panic("simulated plugin panic")
	})
	if err != nil {
		t.Fatalf("unexpected error starting goroutine: %v", err)
	}

	<-panicked
	// Close waits for all goroutines to exit (via wg.Done())
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if scope.ActiveGoroutines() != 0 {
		t.Fatalf("expected 0 active goroutines, got %d", scope.ActiveGoroutines())
	}
	if scope.Panics() != 1 {
		t.Fatalf("expected 1 panic, got %d", scope.Panics())
	}
	if reporter.Count() != 1 {
		t.Fatalf("expected 1 reported panic, got %d", reporter.Count())
	}
	last := reporter.Last()
	if last.Owner != "test-plugin" {
		t.Fatalf("expected owner 'test-plugin', got %q", last.Owner)
	}
	if last.Value != "simulated plugin panic" {
		t.Fatalf("expected panic value 'simulated plugin panic', got %v", last.Value)
	}
	if len(last.Stack) == 0 {
		t.Fatal("expected non-empty stack trace in panic report")
	}
}

func TestScope_Go_NormalExecution(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	done := make(chan struct{})

	err := scope.Go(func(ctx context.Context) {
		close(done)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	<-done
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if scope.ActiveGoroutines() != 0 {
		t.Fatalf("expected 0 active goroutines, got %d", scope.ActiveGoroutines())
	}
	if scope.Panics() != 0 {
		t.Fatalf("expected 0 panics, got %d", scope.Panics())
	}
}

func TestScope_Go_LimitExceeded(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	// Block goroutines
	blocker := make(chan struct{})
	defer close(blocker)

	for i := 0; i < DefaultMaxScopeGoroutines; i++ {
		err := scope.Go(func(ctx context.Context) {
			<-blocker
		})
		if err != nil {
			t.Fatalf("unexpected error at %d: %v", i, err)
		}
	}

	// 65th goroutine should fail admission
	err := scope.Go(func(ctx context.Context) {})
	if err == nil {
		t.Fatal("expected error on exceeding max goroutines limit")
	}
}

func TestScope_Defer_ExecutionOrder(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	var order []int

	_ = scope.Defer(func() { order = append(order, 1) })
	_ = scope.Defer(func() { order = append(order, 2) })
	_ = scope.Defer(func() { order = append(order, 3) })

	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if len(order) != 3 || order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Fatalf("expected cleanups in LIFO order [3, 2, 1], got %v", order)
	}
}

func TestScope_Defer_PanicReported(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	reporter := &mockPanicReporter{}
	scope.SetPanicReporter(reporter)

	secondRan := false
	_ = scope.Defer(func() {
		secondRan = true
	})
	_ = scope.Defer(func() {
		panic("cleanup panic")
	})

	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if !secondRan {
		t.Fatal("expected second cleanup to run even after first panicked")
	}
	if reporter.Count() != 1 {
		t.Fatalf("expected 1 reported panic, got %d", reporter.Count())
	}
	last := reporter.Last()
	if last.Owner != "test-plugin" {
		t.Fatalf("expected owner 'test-plugin', got %q", last.Owner)
	}
	if last.Component != "plugin.Scope.Close" {
		t.Fatalf("expected component 'plugin.Scope.Close', got %q", last.Component)
	}
	if last.Value != "cleanup panic" {
		t.Fatalf("expected value 'cleanup panic', got %v", last.Value)
	}
}


func TestScope_TrackingFailsClosedWhenGlobalResourceCapacityIsFull(t *testing.T) {
	mgr := resource.NewManagerWithLimit(1)
	scope := NewScopeWithManager(context.Background(), "plugin:test", mgr)
	if err := scope.Track(Resource{ID: "first", Type: resource.TypeJob}); err != nil {
		t.Fatal(err)
	}
	if err := scope.Track(Resource{ID: "second", Type: resource.TypeJob}); err == nil {
		t.Fatal("expected scoped resource registration to fail closed")
	}
	if got := len(scope.Resources()); got != 1 {
		t.Fatalf("scope retained resource rejected by global manager: %d", got)
	}
}

func TestScope_GoFailsClosedWhenGlobalResourceCapacityIsFull(t *testing.T) {
	mgr := resource.NewManagerWithLimit(1)
	if err := mgr.Register(resource.Resource{ID: "occupied", Owner: "other", Type: resource.TypeJob}); err != nil {
		t.Fatal(err)
	}
	scope := NewScopeWithManager(context.Background(), "plugin:test", mgr)
	ran := make(chan struct{}, 1)
	if err := scope.Go(func(context.Context) { ran <- struct{}{} }); err == nil {
		t.Fatal("expected goroutine admission to fail when tracking capacity is exhausted")
	}
	select {
	case <-ran:
		t.Fatal("goroutine executed without resource tracking")
	default:
	}
	if got := scope.ActiveGoroutines(); got != 0 {
		t.Fatalf("failed goroutine admission leaked active count: %d", got)
	}
}

func TestScope_DeferContextReceivesDeadline(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	observed := make(chan struct{}, 1)
	if err := scope.DeferContext(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); ok {
			observed <- struct{}{}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scope.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	default:
		t.Fatal("context-aware cleanup did not receive caller deadline")
	}
}

func TestScope_CloseDeadlineBoundsBlockingLegacyCleanup(t *testing.T) {
	scope := NewScope(context.Background(), "test-plugin")
	release := make(chan struct{})
	if err := scope.Defer(func() { <-release }); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := scope.Close(ctx)
	elapsed := time.Since(start)
	close(release)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("blocking legacy cleanup escaped close deadline: %v", elapsed)
	}
}
