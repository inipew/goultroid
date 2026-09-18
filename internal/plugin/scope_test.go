package plugin

import (
	"context"
	"sync"
	"testing"

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
