package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

func TestEngine_WithWorkerManager(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")
	perms := core.NewPermissions(12345, nil)

	repo := NewSQLiteRepository(db.DB)
	engine := NewEngine(repo, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	workerMgr := workers.NewManager()
	taskMgr := tasks.NewManager()
	engine.SetWorkers(workerMgr, taskMgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := workerMgr.Start(ctx); err != nil {
		t.Fatalf("worker manager start: %v", err)
	}
	defer func() { _ = workerMgr.Stop(context.Background()) }()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine start: %v", err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	// Add a scheduled job
	now := time.Now().UTC()
	job := ScheduledJob{
		ChatID:          100,
		ActionType:      "message",
		Payload:         "hello from worker pool",
		IntervalSeconds: 0,
		NextRunAt:       now.Add(-time.Second),
		Status:          JobStatusPending,
	}
	created, err := repo.CreateScheduledJob(ctx, &job)
	if err != nil {
		t.Fatalf("create scheduled job: %v", err)
	}

	// Trigger processing
	engine.processDueJobs(ctx, now)

	// Wait for execution via worker pool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if svc.SentCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if svc.SentCount() == 0 {
		t.Fatalf("expected job to be executed by worker manager")
	}

	// Verify job is recorded in history as completed
	history, err := repo.GetJobHistory(ctx, created.ID, 1)
	if err != nil {
		t.Fatalf("get job history: %v", err)
	}
	if len(history) == 0 || !history[0].Success {
		t.Fatalf("expected completed job history entry, got %+v", history)
	}
}
