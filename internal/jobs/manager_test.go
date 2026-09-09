package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	_ "modernc.org/sqlite"
)

type mockSubmitter struct {
	mu             sync.Mutex
	submittedTasks []tasks.Task
	submittedPools []string
	err            error
}

func (m *mockSubmitter) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.submittedTasks = append(m.submittedTasks, task)
	m.submittedPools = append(m.submittedPools, poolName)
	return nil
}

func TestJobManager_TriggerRollsBackWhenSubmitFails(t *testing.T) {
	mgr, submitter, repo := setupTestManagerWithDB(t)
	submitErr := errors.New("queue full")
	submitter.err = submitErr
	job := Job{ID: "rollback-job", Owner: "test", Type: "test", Run: func(context.Context) error { return nil }}
	if err := mgr.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Trigger(context.Background(), job.ID); !errors.Is(err, submitErr) {
		t.Fatalf("expected submit error, got %v", err)
	}
	got, _ := mgr.Get(job.ID)
	if got.State != StateRegistered || !got.LastRun.IsZero() {
		t.Fatalf("trigger state was not rolled back: %+v", got)
	}
	persisted, err := repo.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != StateRegistered || !persisted.LastRun.IsZero() {
		t.Fatalf("persisted trigger state was not rolled back: %+v", persisted)
	}
}

func TestJobManager_DuplicateIdempotencyDoesNotDeadlock(t *testing.T) {
	mgr, submitter, _ := setupTestManagerWithDB(t)
	idemp := &mockIdemp{claimed: map[string]bool{"duplicate": true}}
	mgr.SetIdempotencyManager(idemp)
	job := Job{ID: "duplicate-job", Owner: "test", Type: "test", IdempotencyKey: "duplicate", Run: func(context.Context) error { return errors.New("must not run") }}
	if err := mgr.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Trigger(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- submitter.submittedTasks[0].Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate idempotency path deadlocked")
	}
}

func (m *mockSubmitter) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.submittedTasks)
}

func setupTestManagerWithDB(t *testing.T) (*Manager, *mockSubmitter, *SQLiteRepository) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	submitter := &mockSubmitter{}
	mgr := NewManager(submitter, repo)
	return mgr, submitter, repo
}

func TestJobManager_RegisterAndTrigger(t *testing.T) {
	mgr, submitter, repo := setupTestManagerWithDB(t)

	job := Job{
		ID:             "job-1",
		Owner:          "plugin:reminder",
		Type:           "reminder",
		Schedule:       "@every 1h",
		Pool:           workers.PoolScheduler,
		RecoveryPolicy: RecoveryRunImmediately,
		Run: func(ctx context.Context) error {
			return nil
		},
	}

	if err := mgr.Register(job); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Verify persistence in repo
	persisted, err := repo.Get(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("expected job-1 in repo: %v", err)
	}
	if persisted.Owner != "plugin:reminder" {
		t.Errorf("expected owner plugin:reminder, got %s", persisted.Owner)
	}

	ctx := context.Background()
	if err := mgr.Trigger(ctx, "job-1"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	if submitter.Count() != 1 {
		t.Fatalf("expected 1 submitted task, got %d", submitter.Count())
	}

	if submitter.submittedPools[0] != workers.PoolScheduler {
		t.Errorf("expected task in pool %s, got %s", workers.PoolScheduler, submitter.submittedPools[0])
	}

	// Execute task to verify lifecycle state transition
	task := submitter.submittedTasks[0]
	if err := task.Run(ctx); err != nil {
		t.Fatalf("task execution failed: %v", err)
	}

	j, ok := mgr.Get("job-1")
	if !ok || j.State != StateCompleted {
		t.Errorf("expected job state completed, got %v", j.State)
	}
}

func TestJobManager_TypedHandler(t *testing.T) {
	mgr, submitter, _ := setupTestManagerWithDB(t)

	handled := false
	mgr.RegisterHandler("archive_cleanup", func(ctx context.Context, j *Job) error {
		handled = true
		return nil
	})

	job := Job{
		ID:             "j-archive",
		Owner:          "plugin:media",
		Type:           "archive_cleanup",
		Schedule:       "@daily",
		RecoveryPolicy: RecoverySkip,
	}

	if err := mgr.Register(job); err != nil {
		t.Fatalf("register typed job failed: %v", err)
	}

	ctx := context.Background()
	if err := mgr.Trigger(ctx, "j-archive"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	task := submitter.submittedTasks[0]
	if err := task.Run(ctx); err != nil {
		t.Fatalf("task run failed: %v", err)
	}

	if !handled {
		t.Errorf("expected archive_cleanup handler to be invoked")
	}
}

func TestJobManager_CancelAndCancelByOwner(t *testing.T) {
	mgr, _, repo := setupTestManagerWithDB(t)

	_ = mgr.Register(Job{
		ID:    "j1",
		Owner: "plugin:reminder",
		Run:   func(ctx context.Context) error { return nil },
	})
	_ = mgr.Register(Job{
		ID:    "j2",
		Owner: "plugin:reminder",
		Run:   func(ctx context.Context) error { return nil },
	})
	_ = mgr.Register(Job{
		ID:    "j3",
		Owner: "plugin:other",
		Run:   func(ctx context.Context) error { return nil },
	})

	cancelled := mgr.CancelByOwner("plugin:reminder")
	if cancelled != 2 {
		t.Errorf("expected 2 cancelled jobs, got %d", cancelled)
	}

	remaining := mgr.All()
	if len(remaining) != 1 || remaining[0].ID != "j3" {
		t.Errorf("expected only j3 remaining, got: %+v", remaining)
	}

	// Verify repo
	persisted, err := repo.ListByOwner(context.Background(), "plugin:reminder")
	if err != nil {
		t.Fatalf("repo check failed: %v", err)
	}
	if len(persisted) != 0 {
		t.Errorf("expected 0 jobs in repo for plugin:reminder, got %d", len(persisted))
	}
}

func TestJobManager_LoadAndReconcile(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	now := time.Now().UTC()
	// Insert 2 jobs into SQLite directly (simulating pre-restart state)
	// Job 1: Run immediately because it missed execution
	pastJob := &Job{
		ID:             "missed-job",
		Owner:          "test",
		Type:           "reconcile_test",
		Schedule:       "@hourly",
		RecoveryPolicy: RecoveryRunImmediately,
		NextRun:        now.Add(-10 * time.Minute),
		State:          StateRegistered,
	}
	// Job 2: Future execution
	futureJob := &Job{
		ID:             "future-job",
		Owner:          "test",
		Type:           "reconcile_test",
		Schedule:       "@hourly",
		RecoveryPolicy: RecoverySkip,
		NextRun:        now.Add(30 * time.Minute),
		State:          StateRegistered,
	}

	_ = repo.Save(context.Background(), pastJob)
	_ = repo.Save(context.Background(), futureJob)

	submitter := &mockSubmitter{}
	mgr := NewManager(submitter, repo)

	mgr.RegisterHandler("reconcile_test", func(ctx context.Context, j *Job) error {
		return nil
	})

	if err := mgr.LoadAndReconcile(context.Background()); err != nil {
		t.Fatalf("LoadAndReconcile failed: %v", err)
	}

	// missed-job should have been triggered immediately
	if submitter.Count() != 1 {
		t.Fatalf("expected 1 triggered job on reconcile, got %d", submitter.Count())
	}
	if submitter.submittedTasks[0].Name != "job:missed-job" {
		t.Errorf("unexpected task triggered: %v", submitter.submittedTasks[0].Name)
	}

	// future-job should be loaded in memory but not triggered yet
	if _, ok := mgr.Get("future-job"); !ok {
		t.Errorf("expected future-job loaded in memory")
	}

	diag := mgr.Diagnostics()
	if diag.Total != 2 {
		t.Errorf("expected 2 total jobs in diagnostics, got %d", diag.Total)
	}
}

type mockIdemp struct {
	claimed map[string]bool
}

func (m *mockIdemp) CheckAndSet(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if m.claimed[key] {
		return false, nil
	}
	m.claimed[key] = true
	return true, nil
}

func TestJobManager_IdempotencyDeduplication(t *testing.T) {
	mgr, submitter, _ := setupTestManagerWithDB(t)
	idemp := &mockIdemp{claimed: make(map[string]bool)}
	mgr.SetIdempotencyManager(idemp)

	job1 := Job{
		ID:             "job-dedup-1",
		Owner:          "test",
		Pool:           workers.PoolGeneral,
		IdempotencyKey: "idem-key-1",
		Run:            func(ctx context.Context) error { return nil },
	}
	if err := mgr.Register(job1); err != nil {
		t.Fatalf("register job1 failed: %v", err)
	}

	// Active duplicate with same IdempotencyKey should fail Register
	job2 := Job{
		ID:             "job-dedup-2",
		Owner:          "test",
		Pool:           workers.PoolGeneral,
		IdempotencyKey: "idem-key-1",
		Run:            func(ctx context.Context) error { return nil },
	}
	if err := mgr.Register(job2); err == nil {
		t.Fatal("expected error registering active duplicate idempotency key, got nil")
	}

	// Trigger job1 and execute
	ctx := context.Background()
	if err := mgr.Trigger(ctx, "job-dedup-1"); err != nil {
		t.Fatalf("trigger job1 failed: %v", err)
	}
	task := submitter.submittedTasks[0]
	if err := task.Run(ctx); err != nil {
		t.Fatalf("task run failed: %v", err)
	}

	// Second execution with same idempotency key in idemp manager should be skipped cleanly
	ranSecond := false
	jobSecond := Job{
		ID:             "job-dedup-second",
		Owner:          "test",
		Pool:           workers.PoolGeneral,
		IdempotencyKey: "idem-key-1", // already claimed
		Run:            func(ctx context.Context) error { ranSecond = true; return nil },
	}
	mgr.mu.Lock()
	mgr.jobs[jobSecond.ID] = &jobSecond
	mgr.mu.Unlock()

	if err := mgr.Trigger(ctx, "job-dedup-second"); err != nil {
		t.Fatalf("trigger second failed: %v", err)
	}
	taskSecond := submitter.submittedTasks[1]
	if err := taskSecond.Run(ctx); err != nil {
		t.Fatalf("taskSecond run failed: %v", err)
	}
	if ranSecond {
		t.Errorf("expected duplicate task execution to be skipped by idempotency manager")
	}
}

func TestJobManager_ReconciliationFailureHealth(t *testing.T) {
	_, _, repo := setupTestManagerWithDB(t)

	pastJob := &Job{
		ID:             "fail-job",
		Owner:          "test",
		Type:           "test_type",
		Schedule:       "@hourly",
		RecoveryPolicy: RecoveryRunImmediately,
		NextRun:        time.Now().UTC().Add(-10 * time.Minute),
		State:          StateRegistered,
	}
	_ = repo.Save(context.Background(), pastJob)

	// Create manager with nil submitter so Trigger fails during reconciliation
	mgr := NewManager(nil, repo)

	err := mgr.LoadAndReconcile(context.Background())
	if err == nil {
		t.Fatal("expected LoadAndReconcile to fail when trigger fails")
	}

	h := mgr.Health(context.Background())
	if h.Status != runtime.HealthDegraded {
		t.Errorf("expected health status degraded, got %s", h.Status)
	}
	if h.Error == nil {
		t.Errorf("expected non-nil error in ComponentHealth")
	}

	diag := mgr.Diagnostics()
	if diag.RecoveryFailures != 1 {
		t.Errorf("expected 1 recovery failure in diagnostics, got %d", diag.RecoveryFailures)
	}
}

func TestJobManager_CleanupTerminalRetention(t *testing.T) {
	mgr, submitter, repo := setupTestManagerWithDB(t)
	mgr.SetRetention(time.Millisecond, time.Hour)
	job := Job{ID: "expired-terminal", Owner: "test", Run: func(context.Context) error { return nil }}
	if err := mgr.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Trigger(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := submitter.submittedTasks[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := mgr.CleanupTerminal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get(job.ID); ok {
		t.Fatal("terminal job remained in memory after retention cleanup")
	}
	if _, err := repo.Get(context.Background(), job.ID); err == nil {
		t.Fatal("terminal job remained in repository after retention cleanup")
	}
}
