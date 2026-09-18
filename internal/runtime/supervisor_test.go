package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockReporter struct {
	mu      sync.Mutex
	reports []PanicReport
}

func (m *mockReporter) ReportPanic(report PanicReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, report)
}

func (m *mockReporter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reports)
}

func (m *mockReporter) last() PanicReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reports) == 0 {
		return PanicReport{}
	}
	return m.reports[len(m.reports)-1]
}

func TestSupervisor_NormalCleanCompletion(t *testing.T) {
	sup := NewSupervisor(WithSupervisorName("test-sup"), WithBaseBackoff(5*time.Millisecond))
	ran := atomic.Bool{}
	done := make(chan struct{})

	err := sup.Register(WorkerSpec{
		Name:    "clean-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			ran.Store(true)
			close(done)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker to run")
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	if !ran.Load() {
		t.Fatal("expected worker to have run")
	}

	snapshots := sup.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].State != WorkerStateStopped {
		t.Fatalf("expected state stopped, got %s", snapshots[0].State)
	}
	if snapshots[0].RestartCount != 0 {
		t.Fatalf("expected 0 restarts, got %d", snapshots[0].RestartCount)
	}
}

func TestSupervisor_NeverRestartPolicy_Failed(t *testing.T) {
	sup := NewSupervisor(WithBaseBackoff(5 * time.Millisecond))
	workerErr := errors.New("fatal worker failure")

	err := sup.Register(WorkerSpec{
		Name:    "fatal-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			return workerErr
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Wait briefly for worker to complete and fail
	time.Sleep(50 * time.Millisecond)

	snapshots := sup.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].State != WorkerStateFailed {
		t.Fatalf("expected state failed, got %s", snapshots[0].State)
	}
	if snapshots[0].LastError != workerErr.Error() {
		t.Fatalf("expected last error %q, got %q", workerErr.Error(), snapshots[0].LastError)
	}

	_ = sup.Stop(context.Background())
}

func TestSupervisor_RestartTransient_SuccessiveRestarts(t *testing.T) {
	sup := NewSupervisor(WithBaseBackoff(5 * time.Millisecond))
	attempts := atomic.Int32{}

	err := sup.Register(WorkerSpec{
		Name:          "transient-worker",
		Restart:       RestartTransient,
		MaxRestarts:   3,
		RestartWindow: 5 * time.Second,
		Run: func(ctx context.Context) error {
			val := attempts.Add(1)
			if val < 3 {
				return errors.New("temporary error")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Wait for 3 attempts and clean exit
	for i := 0; i < 50; i++ {
		if attempts.Load() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	if attempts.Load() != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", attempts.Load())
	}

	snapshots := sup.Snapshot()
	if snapshots[0].State != WorkerStateStopped {
		t.Fatalf("expected state stopped, got %s", snapshots[0].State)
	}
	if snapshots[0].RestartCount != 2 {
		t.Fatalf("expected 2 restarts, got %d", snapshots[0].RestartCount)
	}
}

func TestSupervisor_RestartTransient_ExhaustsBudget(t *testing.T) {
	sup := NewSupervisor(WithBaseBackoff(5 * time.Millisecond))
	attempts := atomic.Int32{}

	err := sup.Register(WorkerSpec{
		Name:          "flapping-worker",
		Restart:       RestartTransient,
		MaxRestarts:   2,
		RestartWindow: 5 * time.Second,
		Run: func(ctx context.Context) error {
			attempts.Add(1)
			return errors.New("flapping failure")
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Wait for attempts to exhaust budget
	for i := 0; i < 50; i++ {
		if attempts.Load() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)

	health := sup.Health(context.Background())
	if health.Status != HealthDegraded {
		t.Fatalf("expected supervisor health degraded, got %s (details: %s)", health.Status, health.Details)
	}

	snapshots := sup.Snapshot()
	if snapshots[0].State != WorkerStateDegraded {
		t.Fatalf("expected state degraded, got %s", snapshots[0].State)
	}

	_ = sup.Stop(context.Background())
}

func TestSupervisor_PanicCaptureAndReport(t *testing.T) {
	rep := &mockReporter{}
	sup := NewSupervisor(
		WithSupervisorName("panic-sup"),
		WithPanicReporter(rep),
		WithBaseBackoff(5*time.Millisecond),
	)

	panicked := atomic.Bool{}
	err := sup.Register(WorkerSpec{
		Name:          "panicky-worker",
		Restart:       RestartTransient,
		MaxRestarts:   2,
		RestartWindow: 5 * time.Second,
		Run: func(ctx context.Context) error {
			if !panicked.Swap(true) {
				panic("simulated worker panic")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	if rep.count() != 1 {
		t.Fatalf("expected 1 reported panic, got %d", rep.count())
	}
	last := rep.last()
	if last.Owner != "panic-sup" {
		t.Fatalf("expected owner 'panic-sup', got %q", last.Owner)
	}
	if last.Component != "panicky-worker" {
		t.Fatalf("expected component 'panicky-worker', got %q", last.Component)
	}
	if last.Value != "simulated worker panic" {
		t.Fatalf("expected panic value 'simulated worker panic', got %v", last.Value)
	}

	snapshots := sup.Snapshot()
	if snapshots[0].Panics != 1 {
		t.Fatalf("expected 1 recorded panic, got %d", snapshots[0].Panics)
	}
	if snapshots[0].State != WorkerStateStopped {
		t.Fatalf("expected state stopped, got %s", snapshots[0].State)
	}
}

func TestSupervisor_QuiesceRejectsNewWorkers(t *testing.T) {
	sup := NewSupervisor()
	if err := sup.Quiesce(context.Background()); err != nil {
		t.Fatalf("unexpected quiesce error: %v", err)
	}

	err := sup.Register(WorkerSpec{
		Name:    "new-worker",
		Restart: NeverRestart,
		Run:     func(ctx context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("expected register to fail on quiesced supervisor")
	}

	err = sup.Go("go-worker", func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("expected Go to fail on quiesced supervisor")
	}
}

func TestSupervisor_MaxWorkersEnforced(t *testing.T) {
	sup := NewSupervisor(WithMaxWorkers(2))

	for i := 0; i < 2; i++ {
		err := sup.Register(WorkerSpec{
			Name:    string(rune('a' + i)),
			Restart: NeverRestart,
			Run:     func(ctx context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("unexpected error registering worker %d: %v", i, err)
		}
	}

	err := sup.Register(WorkerSpec{
		Name:    "c",
		Restart: NeverRestart,
		Run:     func(ctx context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("expected error registering beyond max workers")
	}
}

func TestSupervisor_ComponentIntegrationWithRuntime(t *testing.T) {
	r := New()
	sup := NewSupervisor(WithSupervisorName("sys-sup"), WithBaseBackoff(5*time.Millisecond))

	ran := atomic.Bool{}
	err := sup.Register(WorkerSpec{
		Name:    "app-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			ran.Store(true)
			<-ctx.Done()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := r.Register(sup); err != nil {
		t.Fatalf("unexpected runtime register error: %v", err)
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("unexpected runtime start error: %v", err)
	}

	// Verify running
	time.Sleep(20 * time.Millisecond)
	if !ran.Load() {
		t.Fatal("expected worker to be running")
	}

	health := r.Health(context.Background())
	if health.Status != HealthHealthy {
		t.Fatalf("expected healthy runtime, got %s", health.Status)
	}

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected runtime stop error: %v", err)
	}

	snapshots := sup.Snapshot()
	if snapshots[0].State != WorkerStateStopped {
		t.Fatalf("expected worker stopped cleanly on runtime shutdown, got %s", snapshots[0].State)
	}
}

func TestSupervisor_StopConcurrentAndTimeout(t *testing.T) {
	sup := NewSupervisor(WithSupervisorName("concurrent-stop"))
	workerBlock := make(chan struct{})
	started := make(chan struct{})

	err := sup.Register(WorkerSpec{
		Name:    "blocking-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(started)
			<-workerBlock
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Wait until worker is actively running in its loop
	<-started

	// 1. Caller with immediate/short timeout should return context error without breaking supervisor
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := sup.Stop(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}

	// 2. Concurrent callers to Stop while worker is still running
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = sup.Stop(context.Background())
		}(i)
	}

	// Release the worker
	close(workerBlock)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent caller %d expected nil error, got: %v", i, err)
		}
	}

	// 3. Subsequent Stop call on already stopped supervisor
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("expected nil for stop on already stopped supervisor, got: %v", err)
	}
}
