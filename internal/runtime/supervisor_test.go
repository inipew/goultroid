package runtime

import (
	"context"
	"errors"
	"strings"
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

func TestSupervisor_SnapshotLastErrorBounded(t *testing.T) {
	sup := NewSupervisor(WithBaseBackoff(5 * time.Millisecond))
	defer func() { _ = sup.Stop(context.Background()) }()

	longErrText := strings.Repeat("A very long error message that exceeds boundary limits! ", 20)
	if len(longErrText) <= 256 {
		t.Fatalf("test setup error: longErrText length %d <= 256", len(longErrText))
	}

	workerDone := make(chan struct{})
	err := sup.Register(WorkerSpec{
		Name:    "long-err-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			defer close(workerDone)
			return errors.New(longErrText)
		},
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Deterministic synchronization: wait for worker Run to return, then gracefully join
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run or return within deadline")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	snapshots := sup.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if len(snapshots[0].LastError) > 256 {
		t.Fatalf("LastError length = %d, want <= 256", len(snapshots[0].LastError))
	}
	if snapshots[0].LastError != longErrText[:256] {
		t.Fatalf("LastError does not match truncated prefix: %q", snapshots[0].LastError)
	}
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

func TestSupervisor_ForceStop_WorkerExitsBeforeDeadline(t *testing.T) {
	sup := NewSupervisor(WithSupervisorName("force-stop-clean"))
	started := make(chan struct{})

	err := sup.Register(WorkerSpec{
		Name:    "responsive-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sup.ForceStop(timeoutCtx); err != nil {
		t.Fatalf("expected clean ForceStop, got: %v", err)
	}

	if st := sup.State(); st != StateStopped {
		t.Fatalf("expected StateStopped, got: %s", st)
	}
}

func TestSupervisor_ForceStop_WorkerIgnoresCancellationAndEventuallyExits(t *testing.T) {
	sup := NewSupervisor(WithSupervisorName("force-stop-stuck"))
	started := make(chan struct{})
	workerBlock := make(chan struct{})

	err := sup.Register(WorkerSpec{
		Name:    "stuck-worker",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(started)
			// Ignores ctx.Done() deliberately
			<-workerBlock
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started

	// ForceStop with short deadline must time out and NOT report StateStopped
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = sup.ForceStop(timeoutCtx)
	if err == nil {
		t.Fatal("expected ForceStop timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "stuck-worker") && !strings.Contains(err.Error(), "1 worker(s)") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Must NOT claim StateStopped while worker is still running
	if st := sup.State(); st == StateStopped {
		t.Fatalf("supervisor must NOT report StateStopped while worker is still active; state=%s", st)
	}

	// Now unblock the worker and ensure supervisor eventually settles cleanly
	close(workerBlock)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("expected clean stop after worker completion, got: %v", err)
	}

	if st := sup.State(); st != StateStopped {
		t.Fatalf("expected StateStopped after worker exit, got: %s", st)
	}
}

func TestSupervisor_ConcurrentStopAndForceStop(t *testing.T) {
	sup := NewSupervisor(WithSupervisorName("concurrent-stop-force"))
	started := make(chan struct{})

	err := sup.Register(WorkerSpec{
		Name:    "worker-1",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = sup.Stop(context.Background())
		}(i)
	}
	for i := 3; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = sup.ForceStop(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d returned error: %v", i, err)
		}
	}

	if st := sup.State(); st != StateStopped {
		t.Fatalf("expected StateStopped, got: %s", st)
	}
}

func TestSupervisor_RegistrationCapacityAndPruning(t *testing.T) {
	sup := NewSupervisor(WithMaxWorkers(3))

	// 1. Register lifecycle worker with RestartTransient
	if err := sup.Register(WorkerSpec{
		Name:    "lifecycle-worker",
		Restart: RestartTransient,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Register one-shot worker that completes immediately
	worker2Done := make(chan struct{})
	if err := sup.Register(WorkerSpec{
		Name:    "oneshot-1",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(worker2Done)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Register one-shot worker 2
	worker3Done := make(chan struct{})
	if err := sup.Register(WorkerSpec{
		Name:    "oneshot-2",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(worker3Done)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Start supervisor
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-worker2Done
	<-worker3Done

	// Give time for workers to record clean completion
	for i := 0; i < 50; i++ {
		snaps := sup.Snapshot()
		stopped := 0
		for _, s := range snaps {
			if s.State == WorkerStateStopped {
				stopped++
			}
		}
		if stopped >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Capacity is currently full (3/3). Registering a new worker should trigger automatic pruning of stopped clean one-shot workers!
	err := sup.Register(WorkerSpec{
		Name:    "oneshot-3",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("expected successful registration after automatic pruning, got: %v", err)
	}

	// Lifecycle worker must NOT have been pruned!
	snaps := sup.Snapshot()
	foundLifecycle := false
	for _, s := range snaps {
		if s.Name == "lifecycle-worker" {
			foundLifecycle = true
		}
	}
	if !foundLifecycle {
		t.Fatal("lifecycle worker must not be pruned")
	}

	_ = sup.Stop(context.Background())
}

func TestSupervisor_Deregister(t *testing.T) {
	sup := NewSupervisor(WithMaxWorkers(5))
	workerDone := make(chan struct{})

	if err := sup.Register(WorkerSpec{
		Name:    "short-task",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(workerDone)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	blockWorkerStarted := make(chan struct{})
	blockWorker := make(chan struct{})
	if err := sup.Register(WorkerSpec{
		Name:    "running-task",
		Restart: NeverRestart,
		Run: func(ctx context.Context) error {
			close(blockWorkerStarted)
			<-blockWorker
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-workerDone
	<-blockWorkerStarted

	// Deregistering running worker must fail
	if err := sup.Deregister("running-task"); err == nil {
		t.Fatal("expected error deregistering running worker, got nil")
	}

	// Wait for short-task to be marked stopped
	for i := 0; i < 50; i++ {
		snaps := sup.Snapshot()
		for _, s := range snaps {
			if s.Name == "short-task" && s.State == WorkerStateStopped {
				goto ready
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
ready:
	// Deregistering stopped worker must succeed
	if err := sup.Deregister("short-task"); err != nil {
		t.Fatalf("expected successful deregister of stopped worker, got: %v", err)
	}

	// Verify short-task is no longer in snapshots
	for _, s := range sup.Snapshot() {
		if s.Name == "short-task" {
			t.Fatal("deregistered worker still present in snapshot")
		}
	}

	close(blockWorker)
	_ = sup.Stop(context.Background())
}
