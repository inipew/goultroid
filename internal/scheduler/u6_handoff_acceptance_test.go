package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestU6_S05_SchedulerMisfireRunOnceAndNoStorm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	store := jobsqlite.NewStore(db.DB)
	pump := jobs.NewPersistencePump(1, 32)
	if err := pump.Start(ctx); err != nil {
		t.Fatalf("pump.Start: %v", err)
	}
	defer func() { _ = pump.Stop(context.Background()) }()

	taskEng := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"scheduler": {Concurrency: 2, MinConcurrency: 1, BacklogLimit: 16, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 16,
	})
	taskEng.SetCommitPump(pump)
	if err := taskEng.Start(ctx); err != nil {
		t.Fatalf("taskEng.Start: %v", err)
	}
	defer func() { _ = taskEng.Stop(context.Background()) }()

	manager := jobs.NewManagerWithPorts(taskEng, jobsqlite.Ports(store), pump)
	var execCount atomic.Int32
	executedCh := make(chan struct{}, 4)
	if err := manager.RegisterHandler("s05.handler", func(context.Context, jobs.JobDefinition) error {
		execCount.Add(1)
		select {
		case executedCh <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID:          "s05-job",
		ScopeOwner:  "scheduler",
		QuotaOwner:  "scheduler",
		HandlerType: "s05.handler",
		Pool:        "scheduler",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("manager.Start: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	sched := NewEngine(nil, nil)
	sched.SetTasks(taskEng)
	sched.SetJobsManager(manager)

	now := time.Now().UTC()
	// Seed a schedule that missed ~2 hours of 1-minute runs.
	pastDue := now.Add(-2 * time.Hour)
	if err := manager.SaveSchedule(ctx, jobs.JobSchedule{
		ID:            "s05-misfire-sched",
		JobID:         "s05-job",
		Recurrence:    "interval",
		Interval:      time.Minute,
		Timezone:      "UTC",
		NextDueAt:     pastDue,
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       true,
		Revision:      1,
	}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}

	processed := sched.processRedesignedSchedules(ctx, now)
	if processed != 1 {
		t.Fatalf("processed schedules = %d, want 1 (MisfireRunOnce must not storm)", processed)
	}

	select {
	case <-executedCh:
	case <-ctx.Done():
		t.Fatalf("misfired schedule did not execute: %v", ctx.Err())
	}

	// Second pass at the same timestamp must not re-fire missed slots.
	if processedAgain := sched.processRedesignedSchedules(ctx, now); processedAgain != 0 {
		t.Fatalf("second pass processed = %d, want 0 (NextDueAt must advance past now)", processedAgain)
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution for MisfireRunOnce after 2h downtime, got %d", got)
	}
}
