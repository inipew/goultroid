package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

var errTestFailure = errors.New("test handler failure")

// engineBackedManager wires a real engine, pump, and sqlite store for
// retry/recovery tests.
func engineBackedManager(t *testing.T, handler jobs.Handler, policy jobs.JobRetryPolicy) (*jobs.Manager, *jobsqlite.Store, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pump.Stop(context.Background()) })
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 4, BacklogLimit: 50, PayloadBudget: 1 << 20},
		},
		ResultCapacity:      50,
		MaxTerminalRetained: 50,
		DecisionTimeout:     5 * time.Second,
	})
	engine.SetCommitPump(pump)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	store := jobsqlite.NewStore(db.DB)
	manager := jobs.NewManager(engine, store, pump)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	if err := manager.RegisterHandler("h", handler); err != nil {
		t.Fatal(err)
	}
	def := jobs.JobDefinition{
		ID: "job-retry", ScopeOwner: "plugin:test", QuotaOwner: "user:1",
		HandlerType: "h", Pool: "general", RetryPolicy: policy,
	}
	if err := manager.Register(def); err != nil {
		t.Fatal(err)
	}
	return manager, store, db
}

func pollOccurrenceState(t *testing.T, store *jobsqlite.Store, occurrenceID string, want jobs.OccurrenceState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		occ, err := store.GetOccurrence(context.Background(), occurrenceID)
		if err == nil && occ.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	occ, err := store.GetOccurrence(context.Background(), occurrenceID)
	t.Fatalf("occurrence %s state=%+v err=%v, want %s", occurrenceID, occ, err, want)
}

// D3: a failed first attempt is retried within budget and the occurrence
// completes; exactly two attempts exist.
func TestRetrySucceedsAfterFailure(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			if calls.Add(1) == 1 {
				return errTestFailure
			}
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 3, InitialDelay: 10 * time.Millisecond, MaxDelay: 100 * time.Millisecond, BackoffMultiplier: 2},
	)
	ticket, _, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:retry-ok")
	if err != nil {
		t.Fatal(err)
	}
	// The first ticket resolves with the physical failure; the retry proceeds
	// in the background.
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	occ, err := store.GetOccurrenceByKey(context.Background(), "manual:retry-ok")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occ.ID, jobs.OccurrenceCompleted, 10*time.Second)
	n, err := store.CountAttempts(context.Background(), occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("attempts=%d, want exactly 2", n)
	}
}

// D3: exhaustion finalizes the occurrence as failed with one attempt per
// budget slot.
func TestRetryExhaustsAndFinalizes(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return errTestFailure },
		jobs.JobRetryPolicy{MaxAttempts: 2, InitialDelay: time.Millisecond},
	)
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:retry-out"); err != nil {
		t.Fatal(err)
	}
	occ, err := store.GetOccurrenceByKey(context.Background(), "manual:retry-out")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occ.ID, jobs.OccurrenceFailed, 10*time.Second)
	n, err := store.CountAttempts(context.Background(), occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("attempts=%d, want exactly 2 (budget)", n)
	}
}

// D4: cancelling the occurrence stops the retry driver: no second attempt is
// ever leased and the record stays cancelled.
func TestCancelOccurrenceStopsRetry(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return errTestFailure },
		jobs.JobRetryPolicy{MaxAttempts: 5, InitialDelay: 20 * time.Millisecond},
	)
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:cancel-me"); err != nil {
		t.Fatal(err)
	}
	occ, err := store.GetOccurrenceByKey(context.Background(), "manual:cancel-me")
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the first attempt to exist, then cancel before the retry fires.
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, _ := store.CountAttempts(context.Background(), occ.ID)
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first attempt never leased")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := manager.CancelOccurrence(context.Background(), occ.ID, "test cancel"); err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occ.ID, jobs.OccurrenceCancelled, 10*time.Second)
	time.Sleep(200 * time.Millisecond)
	n, err := store.CountAttempts(context.Background(), occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cancelled occurrence grew %d attempts, want 1", n)
	}
}

// D5: recovery re-drives an occurrence whose attempts are terminal but whose
// record never closed (crashed monitor / restart gap).
func TestRecoverRedrivesTerminalUnresolved(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return nil },
		jobs.JobRetryPolicy{MaxAttempts: 3},
	)
	ctx := context.Background()
	// Craft the crash gap directly: failed attempt committed, occurrence left
	// dispatched (exactly what a monitor crash between commit and finalize
	// would leave behind).
	occ := &jobs.JobOccurrence{ID: "occ-crash", JobID: "job-retry", OccurrenceKey: "manual:crash-gap", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	a1, err := store.PrepareAttemptLease(ctx, "occ-crash", "task:occ-crash:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitAttemptResult(ctx, a1.ID, a1.LeaseEpoch, jobs.AttemptFailed, nil, "boom"); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != 1 || report.Redriven != 1 {
		t.Fatalf("report=%+v, want scanned=1 redriven=1", report)
	}
	pollOccurrenceState(t, store, "occ-crash", jobs.OccurrenceCompleted, 10*time.Second)
}

// D5: recovery never touches occurrences with live (non-terminal) attempts:
// unknown effect must not be retried blindly.
func TestRecoverLeavesStaleLeases(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return nil },
		jobs.JobRetryPolicy{MaxAttempts: 3},
	)
	ctx := context.Background()
	occ := &jobs.JobOccurrence{ID: "occ-stale", JobID: "job-retry", OccurrenceKey: "manual:stale", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareAttemptLease(ctx, "occ-stale", "task:occ-stale:1", time.Minute); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stale != 1 || report.Redriven != 0 {
		t.Fatalf("report=%+v, want stale=1 redriven=0", report)
	}
	n, err := store.CountAttempts(ctx, "occ-stale")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stale occurrence grew %d attempts", n)
	}
}

// D6: concurrent duplicate triggers collapse onto one logical run.
func TestConcurrentDuplicateTriggerSingleRun(t *testing.T) {
	manager, store, db := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return nil },
		jobs.JobRetryPolicy{MaxAttempts: 1},
	)
	const racers = 8
	type outcome struct{ err error }
	results := make(chan outcome, racers)
	for i := 0; i < racers; i++ {
		go func() {
			_, _, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:dedup")
			results <- outcome{err}
		}()
	}
	succeeded := 0
	for i := 0; i < racers; i++ {
		if (<-results).err == nil {
			succeeded++
		}
	}
	var rows int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM job_occurrences WHERE occurrence_key = 'manual:dedup'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("duplicate triggers materialized %d runs", rows)
	}
	if succeeded != 1 {
		t.Fatalf("succeeded=%d, want exactly 1 (single-flight lease)", succeeded)
	}
	_ = store
}
