package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

type mockDAGComponent struct {
	name   string
	deps   []string
	start  func(ctx context.Context) error
	stop   func(ctx context.Context) error
	health runtime.ComponentHealth
}

func (m *mockDAGComponent) Name() string                   { return m.name }
func (m *mockDAGComponent) Dependencies() []string         { return m.deps }
func (m *mockDAGComponent) Start(ctx context.Context) error {
	if m.start != nil {
		return m.start(ctx)
	}
	return nil
}
func (m *mockDAGComponent) Stop(ctx context.Context) error {
	if m.stop != nil {
		return m.stop(ctx)
	}
	return nil
}
func (m *mockDAGComponent) Health(ctx context.Context) runtime.ComponentHealth {
	return m.health
}

func TestRuntimeLifecycle_DAGExecutionAndRollback(t *testing.T) {
	var mu sync.Mutex
	var startOrder []string
	var stopOrder []string

	r := runtime.New()

	// c1 depends on nothing
	c1 := &mockDAGComponent{
		name: "db",
		start: func(ctx context.Context) error {
			mu.Lock()
			startOrder = append(startOrder, "db")
			mu.Unlock()
			return nil
		},
		stop: func(ctx context.Context) error {
			mu.Lock()
			stopOrder = append(stopOrder, "db")
			mu.Unlock()
			return nil
		},
	}

	// c2 depends on c1
	c2 := &mockDAGComponent{
		name: "cache",
		deps: []string{"db"},
		start: func(ctx context.Context) error {
			mu.Lock()
			startOrder = append(startOrder, "cache")
			mu.Unlock()
			return nil
		},
		stop: func(ctx context.Context) error {
			mu.Lock()
			stopOrder = append(stopOrder, "cache")
			mu.Unlock()
			return nil
		},
	}

	// c3 depends on c2 and fails
	c3 := &mockDAGComponent{
		name: "failing_worker",
		deps: []string{"cache"},
		start: func(ctx context.Context) error {
			return errors.New("worker initialization failed")
		},
	}

	_ = r.Register(c1)
	_ = r.Register(c2)
	_ = r.Register(c3)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Starting should fail on c3, triggering rollback of c2 then c1
	err := r.Start(ctx)
	if err == nil {
		t.Fatalf("expected error from failing component, got nil")
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify startup order was db, cache
	if len(startOrder) != 2 || startOrder[0] != "db" || startOrder[1] != "cache" {
		t.Errorf("unexpected start order before failure: %v", startOrder)
	}

	// Verify reverse rollback order was cache, db
	if len(stopOrder) != 2 || stopOrder[0] != "cache" || stopOrder[1] != "db" {
		t.Errorf("unexpected rollback order: %v", stopOrder)
	}
}

type mockTaskSubmitter struct {
	mu        sync.Mutex
	submitted []tasks.Task
}

func (s *mockTaskSubmitter) Submit(ctx context.Context, poolName string, t tasks.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitted = append(s.submitted, t)
	return nil
}

func (s *mockTaskSubmitter) getSubmitted() []tasks.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]tasks.Task, len(s.submitted))
	copy(res, s.submitted)
	return res
}

func TestJobsReconciliation_RecoveryPoliciesOnRestart(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	repo := jobs.NewSQLiteRepository(db.DB)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	now := time.Now().UTC()
	pastTime := now.Add(-30 * time.Minute)

	// 1. Job with RunImmediately recovery policy (was due in the past)
	immediateJob := &jobs.Job{
		ID:             "job-immediate",
		Owner:          "system",
		Type:           "cleanup",
		Schedule:       "@every 1h",
		RecoveryPolicy: jobs.RecoveryRunImmediately,
		Pool:           "general",
		NextRun:        pastTime,
		LastRun:        pastTime,
		State:          jobs.StateScheduled,
	}
	if err := repo.Save(ctx, immediateJob); err != nil {
		t.Fatalf("Save immediateJob failed: %v", err)
	}

	// 2. Job with Skip recovery policy (was due in the past)
	skipJob := &jobs.Job{
		ID:             "job-skip",
		Owner:          "system",
		Type:           "report",
		Schedule:       "@every 1h",
		RecoveryPolicy: jobs.RecoverySkip,
		Pool:           "general",
		NextRun:        pastTime,
		LastRun:        pastTime,
		State:          jobs.StateScheduled,
	}
	if err := repo.Save(ctx, skipJob); err != nil {
		t.Fatalf("Save skipJob failed: %v", err)
	}

	// 3. Restart: Instantiate JobManager with task submitter and reconcile
	submitter := &mockTaskSubmitter{}
	mgr := jobs.NewManager(submitter, repo)

	mgr.RegisterHandler("cleanup", func(ctx context.Context, job *jobs.Job) error {
		return nil
	})
	mgr.RegisterHandler("report", func(ctx context.Context, job *jobs.Job) error {
		return nil
	})

	if err := mgr.LoadAndReconcile(ctx); err != nil {
		t.Fatalf("LoadAndReconcile failed: %v", err)
	}

	// Verify that the immediate job was submitted for immediate execution
	submitted := submitter.getSubmitted()
	var foundImmediate bool
	for _, task := range submitted {
		if strings.Contains(task.Name, "job-immediate") {
			foundImmediate = true
		}
	}
	if !foundImmediate {
		t.Errorf("expected job-immediate to be submitted, got: %v", submitted)
	}
}

func TestTaskWorkerManager_SaturationAndBackpressure(t *testing.T) {
	wm := workers.NewManager()
	ctx := context.Background()

	if err := wm.Start(ctx); err != nil {
		t.Fatalf("failed to start worker manager: %v", err)
	}
	defer func() { _ = wm.Stop(ctx) }()

	pool, ok := wm.Get("general")
	if !ok {
		t.Fatalf("general pool not found")
	}

	capacity := pool.Stats().Concurrency
	if capacity <= 0 {
		capacity = 4
	}

	blockCh := make(chan struct{})
	var startedCount sync.WaitGroup
	startedCount.Add(capacity)

	for i := 0; i < capacity; i++ {
		task := tasks.Task{
			ID:    "task-blocking",
			Owner: "system",
			Run: func(taskCtx context.Context) error {
				startedCount.Done()
				<-blockCh
				return nil
			},
		}
		if err := wm.Submit(ctx, "general", task); err != nil {
			t.Fatalf("failed to submit task %d: %v", i, err)
		}
	}

	// Wait for all workers to be occupied
	startedCount.Wait()

	// Metrics should show all workers busy
	stats := pool.Stats()
	if stats.Busy != capacity {
		t.Errorf("expected %d busy workers, got %d", capacity, stats.Busy)
	}

	// Release workers
	close(blockCh)
}
