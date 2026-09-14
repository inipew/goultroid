from pathlib import Path

Path("internal/scheduler/engine_cancellation_test.go").write_text(r'''package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestEngineCancelDelegatesExecutionCancellationToTaskManager(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db.DB)
	tm := tasks.NewManager()
	engine := NewEngine(repo, nil, nil, nil, zap.NewNop())
	engine.taskMgr = tm

	created, err := repo.CreateScheduledJob(context.Background(), &ScheduledJob{
		ChatID:      42,
		PeerType:    "chat",
		ActionType:  ActionMessage,
		Payload:     "noop",
		NextRunAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
		Status:      JobStatusPending,
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	taskCtx, _, err := tm.Register(context.Background(), tasks.Task{
		ID:            "sched-cancel-test",
		Owner:         "scheduler",
		Name:          "test",
		CorrelationID: scheduledTaskCorrelation(created.ID),
		Run:           func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Cancel(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("scheduler cancellation did not cancel correlated task")
	}
}

func TestScheduledTaskCorrelationIsPerJob(t *testing.T) {
	if scheduledTaskCorrelation(1) == scheduledTaskCorrelation(2) {
		t.Fatal("distinct scheduled jobs must not share correlation IDs")
	}
}
''')
