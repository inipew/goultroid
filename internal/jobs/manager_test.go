package jobs

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	_ "modernc.org/sqlite"
)

type mockSubmitter struct {
	mu             sync.Mutex
	submittedTasks []tasks.Task
	submittedPools []string
}

func (m *mockSubmitter) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submittedTasks = append(m.submittedTasks, task)
	m.submittedPools = append(m.submittedPools, poolName)
	return nil
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
