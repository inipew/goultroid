package jobs_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
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


func TestManagerRestartWaitsForPersistedDeferredDeadline(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "jobs-restart.db")

	before, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(ctx, before.DB); err != nil {
		before.Close()
		t.Fatal(err)
	}
	beforeStore := jobsqlite.NewStore(before.DB)
	definition := jobs.JobDefinition{
		ID: "restart-deferred", ScopeOwner: "test:restart", QuotaOwner: "test:restart",
		HandlerType: "restart", Pool: "general", Class: string(tasks.PriorityNormal),
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2},
		Enabled: true, Version: 1,
	}
	if err := beforeStore.SaveDefinition(ctx, &definition); err != nil {
		before.Close()
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{
		ID: "occ-restart-deferred", JobID: definition.ID, OccurrenceKey: "restart:deferred",
		State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC(),
	}
	if err := beforeStore.MaterializeOccurrence(ctx, occ); err != nil {
		before.Close()
		t.Fatal(err)
	}
	attempt, err := beforeStore.PrepareAttemptLease(ctx, occ.ID, "task:occ-restart-deferred:1", time.Minute)
	if err != nil {
		before.Close()
		t.Fatal(err)
	}
	readyAt := time.Now().UTC().Add(time.Second)
	if err := beforeStore.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt, "persisted flood wait"); err != nil {
		before.Close()
		t.Fatal(err)
	}
	before.Close()

	after, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if err := jobsqlite.InitSchema(ctx, after.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(ctx); err != nil {
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
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(context.Background())

	store := jobsqlite.NewStore(after.DB)
	manager := jobs.NewManager(engine, store, pump)
	var calls atomic.Int32
	if err := manager.RegisterHandler("restart", func(context.Context, jobs.JobDefinition) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background())

	if time.Now().UTC().Before(readyAt) {
		if n, err := store.CountAttempts(ctx, occ.ID); err != nil {
			t.Fatal(err)
		} else if n != 1 {
			t.Fatalf("restart redrove before persisted ready_at: attempts=%d", n)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			n, countErr := store.CountAttempts(ctx, occ.ID)
			if countErr != nil {
				t.Fatal(countErr)
			}
			if n != 2 || calls.Load() != 1 {
				t.Fatalf("restart recovery attempts=%d handler_calls=%d, want exactly one redrive", n, calls.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := store.GetOccurrence(ctx, occ.ID)
	t.Fatalf("restart recovery did not converge deferred occurrence: %+v", current)
}
