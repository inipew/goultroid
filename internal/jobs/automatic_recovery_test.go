package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

// D/P0: recovery is a Manager lifecycle responsibility. A crash-gap that
// exists before Start is redriven by the startup recovery wake without any
// external caller invoking Recover.
func TestManagerStartAutomaticallyRecoversCrashGap(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}

	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())

	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 16, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 16,
	})
	engine.SetCommitPump(pump)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(context.Background())

	store := jobsqlite.NewStore(db.DB)
	manager := jobs.NewManager(engine, store, pump)
	if err := manager.RegisterHandler("auto", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID: "auto-recover", ScopeOwner: "test:auto", QuotaOwner: "test:auto",
		HandlerType: "auto", Pool: "general",
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 2}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	occ := &jobs.JobOccurrence{
		ID: "occ-auto", JobID: "auto-recover", OccurrenceKey: "auto:startup",
		State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	a1, err := store.PrepareAttemptLease(ctx, occ.ID, "task:occ-auto:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitAttemptResult(ctx, a1.ID, a1.LeaseEpoch, jobs.AttemptFailed, nil, "crash gap"); err != nil {
		t.Fatal(err)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			n, countErr := store.CountAttempts(ctx, occ.ID)
			if countErr != nil {
				t.Fatal(countErr)
			}
			if n != 2 {
				t.Fatalf("attempts=%d, want startup recovery to create exactly one retry", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := store.GetOccurrence(ctx, occ.ID)
	t.Fatalf("startup recovery did not converge occurrence: %+v", current)
}
