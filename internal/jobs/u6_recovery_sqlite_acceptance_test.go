package jobs_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestU6_S04_DurableRecoveryAfterPersistenceFaultAndExpiredLeaseBoundary(t *testing.T) {
	t.Run("persistence failure triggers recovery convergence after store recovers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
			t.Fatal(err)
		}
		store := jobsqlite.NewStore(db.DB)
		ports, faultStore := u6WrapPortsWithFaultAttemptStore(jobsqlite.Ports(store))

		pump := jobs.NewPersistencePump(1, 16)
		if err := pump.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = pump.Stop(context.Background()) }()

		engine := taskengine.NewEngine(taskengine.Config{
			Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
				"general": {Concurrency: 1, BacklogLimit: 8, PayloadBudget: 1 << 20},
			},
			ResultCapacity: 16,
		})
		engine.SetCommitPump(pump)
		if err := engine.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = engine.Stop(context.Background()) }()

		manager := jobs.NewManagerWithPorts(engine, ports, pump)
		var runs atomic.Int32
		if err := manager.RegisterHandler("s04.handler", func(context.Context, jobs.JobDefinition) error {
			if runs.Add(1) == 1 {
				return errors.New("transient failure on attempt 1")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := manager.Register(jobs.JobDefinition{
			ID:          "s04-job",
			ScopeOwner:  "u6",
			QuotaOwner:  "u6",
			HandlerType: "s04.handler",
			Pool:        "general",
			Enabled:     true,
			RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 2},
		}); err != nil {
			t.Fatal(err)
		}
		if err := manager.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = manager.Stop(context.Background()) }()

		// Inject pre-write persistence fault for the first commit attempt.
		faultStore.SetCommitFault(u6FaultBeforeWrite, errors.New("injected disk unavailable"))
		ep, seq := faultStore.FaultToken()

		ticket, occID, err := manager.SubmitOccurrence(ctx, "s04-job", "u6:s04:fault")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := faultStore.WaitNextFault(ctx, ep, seq); err != nil {
			t.Fatalf("WaitNextFault: %v", err)
		}
		res, err := ticket.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res.Cause != tasks.CausePersistenceFailure {
			t.Fatalf("expected CausePersistenceFailure, got %+v", res)
		}

		// Storage recovers; drive recovery pass and verify convergence within MaxAttempts=2.
		faultStore.ClearCommitFault()

		// Lease is still AttemptLeased/AttemptRunning because commit failed pre-write.
		// Expire lease in DB so recovery treats it as unknown external effect OR commit failed attempt.
		// Here verify Recover converges without exceeding retry budget.
		if _, err := db.DB.ExecContext(ctx, `UPDATE job_attempts SET state = 'failed', lease_until = ? WHERE occurrence_id = ?`, time.Now().UTC().Add(-time.Minute), occID); err != nil {
			t.Fatal(err)
		}
		report, err := manager.Recover(ctx, 32)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Redriven == 0 && report.Scanned == 0 {
			t.Fatalf("expected Recover to scan/redrive occurrence, got %+v", report)
		}

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			occ, gerr := store.GetOccurrence(ctx, occID)
			if gerr == nil && occ.State == jobs.OccurrenceCompleted {
				if uses, uerr := store.CountRetryBudgetUses(ctx, occID); uerr != nil || uses > 2 {
					t.Fatalf("retry budget exceeded: uses=%d err=%v", uses, uerr)
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		occ, _ := store.GetOccurrence(ctx, occID)
		t.Fatalf("occurrence did not converge to Completed after recovery: %+v", occ)
	})

	t.Run("expired running lease is quarantined as blocked instead of blind re-execution", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
			t.Fatal(err)
		}
		store := jobsqlite.NewStore(db.DB)
		manager := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(store), nil)
		_ = manager.RegisterHandler("s04.blocked", func(context.Context, jobs.JobDefinition) error { return nil })
		_ = manager.Register(jobs.JobDefinition{
			ID: "s04-blocked-job", ScopeOwner: "u6", QuotaOwner: "u6",
			HandlerType: "s04.blocked", Pool: "general", Enabled: true,
			RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 3},
		})
		if err := store.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
			ID: "occ-expired", JobID: "s04-blocked-job", OccurrenceKey: "k-exp", State: jobs.OccurrenceReady,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PrepareAttemptLease(ctx, "occ-expired", "task-exp", time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.ExecContext(ctx, `UPDATE job_attempts SET lease_until = ? WHERE occurrence_id = 'occ-expired'`, time.Now().UTC().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}

		report, err := manager.Recover(ctx, 32)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Blocked != 1 {
			t.Fatalf("expected report.Blocked=1 for expired running lease, got %+v", report)
		}
		occ, err := store.GetOccurrence(ctx, "occ-expired")
		if err != nil || occ.State != jobs.OccurrenceBlocked {
			t.Fatalf("expected occ-expired to be quarantined in OccurrenceBlocked, got occ=%+v err=%v", occ, err)
		}
		blockedList, err := store.ListBlockedOccurrences(ctx, 10)
		if err != nil || len(blockedList) != 1 || blockedList[0].ID != "occ-expired" {
			t.Fatalf("expected occ-expired in ListBlockedOccurrences, got %+v err=%v", blockedList, err)
		}
	})
}

func TestU6_S07_SQLiteContentionAndPersistencePumpBackpressure(t *testing.T) {
	t.Run("SQLite exclusive lock causes BUSY/LOCKED and recovers after release", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		dbPath := filepath.Join(t.TempDir(), "u6-s07.db")
		// Use short busy_timeout for deterministic test failure under lock without 5s wait.
		db, err := database.Open(dbPath + "?_pragma=busy_timeout(50)")
		if err != nil {
			t.Fatalf("database.Open: %v", err)
		}
		defer db.Close()
		if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
			t.Fatalf("InitSchema: %v", err)
		}
		store := jobsqlite.NewStore(db.DB)
		if err := store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "s07-job", Enabled: true}); err != nil {
			t.Fatal(err)
		}

		lockHolder := acquireU6SQLiteExclusiveLock(t, dbPath, 2*time.Second)

		err = store.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
			ID: "s07-occ", JobID: "s07-job", OccurrenceKey: "k-s07", State: jobs.OccurrenceReady,
		})
		if err == nil {
			t.Fatal("expected SQLite write under exclusive lock to fail")
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) {
			t.Fatalf("expected *sqlite.Error, got %T: %v", err, err)
		}
		primary := sqliteErr.Code() & 0xff
		if primary != sqlite3.SQLITE_BUSY && primary != sqlite3.SQLITE_LOCKED {
			t.Fatalf("expected SQLITE_BUSY or SQLITE_LOCKED, got code=%d (primary=%d): %v", sqliteErr.Code(), primary, err)
		}

		lockHolder.Release(t)

		if err := store.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
			ID: "s07-occ", JobID: "s07-job", OccurrenceKey: "k-s07", State: jobs.OccurrenceReady,
		}); err != nil {
			t.Fatalf("expected SQLite write to succeed after lock release, got: %v", err)
		}
	})

	t.Run("PersistencePump queue and byte budget backpressure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		pump := jobs.NewPersistencePump(1, 2)
		pump.SetMaxRetainedBytes(1024)
		if err := pump.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = pump.Stop(context.Background()) }()

		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })

		// 1. Occupy the single worker.
		_, err := pump.EnqueueSized(ctx, 128, func(runCtx context.Context) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		})
		if err != nil {
			t.Fatalf("enqueue active op: %v", err)
		}
		<-started

		// 2. Oversized byte admission rejected immediately.
		if _, err := pump.EnqueueSized(ctx, 2048, func(context.Context) error { return nil }); !errors.Is(err, jobs.ErrPumpByteBudget) {
			t.Fatalf("expected ErrPumpByteBudget, got: %v", err)
		}

		// 3. Fill queue capacity=2, then 3rd queued item must return ErrPumpQueueFull.
		for i := 0; i < 2; i++ {
			if _, err := pump.EnqueueSized(ctx, 128, func(context.Context) error { return nil }); err != nil {
				t.Fatalf("enqueue queued item %d: %v", i, err)
			}
		}
		if _, err := pump.EnqueueSized(ctx, 128, func(context.Context) error { return nil }); !errors.Is(err, jobs.ErrPumpQueueFull) {
			t.Fatalf("expected ErrPumpQueueFull, got: %v", err)
		}

		releaseOnce.Do(func() { close(release) })
	})
}
