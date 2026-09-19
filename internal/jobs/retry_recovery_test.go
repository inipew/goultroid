package jobs_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
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

func TestShortRetryDelayIsPersistedInsteadOfSleepingRetryWorker(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			if calls.Add(1) == 1 {
				return errTestFailure
			}
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 2, InitialDelay: 250 * time.Millisecond},
	)

	start := time.Now().UTC()
	ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:short-durable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	var occ *jobs.JobOccurrence
	for time.Now().Before(deadline) {
		occ, err = store.GetOccurrence(context.Background(), occurrenceID)
		if err == nil && occ.ReadyAt.After(start.Add(100*time.Millisecond)) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if occ == nil || !occ.ReadyAt.After(start.Add(100*time.Millisecond)) {
		t.Fatalf("short retry was not persisted as future ready_at: %+v", occ)
	}
	if n, err := store.CountAttempts(context.Background(), occurrenceID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("attempts=%d before durable deadline, want 1", n)
	}

	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceCompleted, 5*time.Second)
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestRetryHonorsConfiguredBackoff(t *testing.T) {
	var firstFailure time.Time
	var secondAttempt time.Time
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			if calls.Add(1) == 1 {
				firstFailure = time.Now()
				return errTestFailure
			}
			secondAttempt = time.Now()
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 2, InitialDelay: 150 * time.Millisecond},
	)
	_, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:retry-backoff")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceCompleted, 10*time.Second)
	if elapsed := secondAttempt.Sub(firstFailure); elapsed < 130*time.Millisecond {
		t.Fatalf("retry ran before configured backoff: %v", elapsed)
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
	// Startup recovery may win the race and already own the redrive. In that
	// case an explicit concurrent pass must report it stale rather than create a
	// duplicate attempt.
	if report.Scanned != 1 || report.Redriven+report.Stale != 1 {
		t.Fatalf("report=%+v, want one redrive or one already-owned occurrence", report)
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

// TestDeferredDurableRetry_FloodWait verifies that when a task encounters a Telegram
// FloodWait (RateLimitError), physical workers are released immediately, the occurrence
// is deferred in the store with a future ready_at, early recovery calls respect ready_at,
// and the task eventually completes after the wait window expires.
func TestDeferredDurableRetry_FloodWait(t *testing.T) {
	var calls atomic.Int32
	waitDuration := 200 * time.Millisecond

	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			if calls.Add(1) == 1 {
				return core.NewRateLimitError(waitDuration, errors.New("flood wait"))
			}
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2, InitialDelay: 10 * time.Millisecond, MaxDelay: 100 * time.Millisecond, BackoffMultiplier: 2},
	)

	start := time.Now()
	ticket, occID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:flood-wait")
	if err != nil {
		t.Fatal(err)
	}

	// First attempt resolves as failure promptly without blocking on the wait duration.
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != tasks.OutcomeFailed {
		t.Fatalf("first attempt outcome=%s, want failed", res.Outcome)
	}
	if res.Cause != tasks.CauseRateLimited || res.RetryAfter != waitDuration {
		t.Fatalf("rate-limit metadata lost: cause=%s retry_after=%s", res.Cause, res.RetryAfter)
	}
	if time.Since(start) >= waitDuration {
		t.Fatalf("first attempt should resolve immediately without waiting out flood wait: elapsed=%v", time.Since(start))
	}

	// Verify that the occurrence has been deferred into the future in the store.
	occ, err := store.GetOccurrence(context.Background(), occID)
	if err != nil {
		t.Fatal(err)
	}
	if !occ.ReadyAt.After(start) {
		t.Fatalf("occurrence ready_at %v should be deferred after %v", occ.ReadyAt, start)
	}
	latest, err := store.LatestAttempt(context.Background(), occID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred {
		t.Fatalf("first attempt state=%s, want deferred", latest.State)
	}
	if retryUses, err := store.CountRetryBudgetUses(context.Background(), occID); err != nil {
		t.Fatal(err)
	} else if retryUses != 0 {
		t.Fatalf("rate-limit deferral consumed retry budget: %d", retryUses)
	}

	// While still before readyAt, calling Recover must NOT redrive early.
	if time.Now().Before(occ.ReadyAt) {
		report, err := manager.Recover(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if report.Redriven != 0 {
			t.Fatalf("expected 0 redriven before ready_at, got %d", report.Redriven)
		}
	}

	// Eventually after waitDuration, recovery drives the second attempt and completes.
	pollOccurrenceState(t, store, occID, jobs.OccurrenceCompleted, 3*time.Second)

	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", calls.Load())
	}
}

func TestRepeatedFloodWaitsUseDeferralBudgetNotRetryBudget(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			if calls.Add(1) <= 2 {
				return core.NewRateLimitError(20*time.Millisecond, errors.New("flood wait"))
			}
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 3},
	)

	_, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:repeated-flood-wait")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceCompleted, 5*time.Second)

	attempts, err := store.CountAttempts(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("physical attempts=%d, want 3", attempts)
	}
	deferrals, err := store.CountDeferrals(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if deferrals != 2 {
		t.Fatalf("deferrals=%d, want 2", deferrals)
	}
	retryUses, err := store.CountRetryBudgetUses(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if retryUses != 1 {
		t.Fatalf("retry budget uses=%d, want only final successful execution", retryUses)
	}
}

func TestDeferralBudgetExhaustionFinalizesOccurrence(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			return core.NewRateLimitError(20*time.Millisecond, errors.New("persistent flood wait"))
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2},
	)

	_, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:deferral-budget")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceFailed, 5*time.Second)

	deferrals, err := store.CountDeferrals(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if deferrals != 2 {
		t.Fatalf("deferrals=%d, want configured cap 2", deferrals)
	}
	retryUses, err := store.CountRetryBudgetUses(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if retryUses != 0 {
		t.Fatalf("deferral exhaustion consumed ordinary retry budget: %d", retryUses)
	}
	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred || latest.Error == "" {
		t.Fatalf("terminal deferral diagnosis missing: %+v", latest)
	}
}

func TestCancellationWhileDeferredWinsOverDeadlineWake(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			calls.Add(1)
			return core.NewRateLimitError(150*time.Millisecond, errors.New("flood wait"))
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 5},
	)

	ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:cancel-deferred")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Cause != tasks.CauseRateLimited {
		t.Fatalf("cause=%s, want rate_limited", res.Cause)
	}
	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred {
		t.Fatalf("attempt state=%s, want deferred", latest.State)
	}
	if err := manager.CancelOccurrence(context.Background(), occurrenceID, "operator cancelled deferred work"); err != nil {
		t.Fatal(err)
	}

	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceCancelled, time.Second)
	time.Sleep(250 * time.Millisecond)
	if n, err := store.CountAttempts(context.Background(), occurrenceID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("cancelled deferred occurrence redrove after deadline: attempts=%d", n)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls=%d, want 1", calls.Load())
	}
}

func TestConcurrentRecoveryPassesCreateOneDeferredRedrive(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "recovery-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
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

	store := jobsqlite.NewStore(db.DB)
	manager := jobs.NewManager(engine, store, pump)
	var calls atomic.Int32
	if err := manager.RegisterHandler("race", func(context.Context, jobs.JobDefinition) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID: "recovery-race", ScopeOwner: "test:race", QuotaOwner: "test:race",
		HandlerType: "race", Pool: "general",
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 3},
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	occ := &jobs.JobOccurrence{
		ID: "occ-recovery-race", JobID: "recovery-race", OccurrenceKey: "race:deferred",
		State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	first, err := store.PrepareAttemptLease(ctx, occ.ID, "task:occ-recovery-race:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitAttemptDeferred(ctx, first.ID, first.LeaseEpoch, time.Now().UTC().Add(-time.Millisecond), "due flood wait"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type recoveryResult struct {
		report jobs.RecoverReport
		err    error
	}
	results := make(chan recoveryResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			report, err := manager.Recover(ctx, 10)
			results <- recoveryResult{report: report, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	redriven := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent recovery failed: %v", result.err)
		}
		redriven += result.report.Redriven
	}
	if redriven != 1 {
		t.Fatalf("concurrent recovery redrives=%d, want exactly 1", redriven)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceCompleted {
		t.Fatalf("raced recovery did not converge: %+v", current)
	}
	if n, err := store.CountAttempts(ctx, occ.ID); err != nil {
		t.Fatal(err)
	} else if n != 2 {
		t.Fatalf("concurrent recovery created %d physical attempts, want 2 total", n)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls=%d, want one redrive execution", calls.Load())
	}
}

func TestRejectedUsageErrorDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			calls.Add(1)
			return core.NewUsageError("invalid scheduled arguments")
		},
		jobs.JobRetryPolicy{MaxAttempts: 5, InitialDelay: time.Millisecond},
	)
	_, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:typed-rejected")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceFailed, 5*time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls=%d, want 1 for rejected result", got)
	}
	if n, err := store.CountAttempts(context.Background(), occurrenceID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("attempts=%d, want 1 for rejected result", n)
	}
	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest.Result), `"disposition":"rejected"`) {
		t.Fatalf("persisted result metadata=%q, want rejected disposition", latest.Result)
	}
}

func TestExplicitPermanentFailureDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	permanent := errors.New("payload cannot ever be processed")
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			calls.Add(1)
			return execution.WithSemantics(permanent, execution.Semantics{
				Disposition: execution.DispositionPermanent,
				Code:        "bad_payload",
			})
		},
		jobs.JobRetryPolicy{MaxAttempts: 5, InitialDelay: time.Millisecond},
	)
	_, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:typed-permanent")
	if err != nil {
		t.Fatal(err)
	}
	pollOccurrenceState(t, store, occurrenceID, jobs.OccurrenceFailed, 5*time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls=%d, want 1 for permanent result", got)
	}
	if n, err := store.CountAttempts(context.Background(), occurrenceID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("attempts=%d, want 1 for permanent result", n)
	}
	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest.Result), `"disposition":"permanent"`) ||
		!strings.Contains(string(latest.Result), `"code":"bad_payload"`) {
		t.Fatalf("persisted result metadata=%q", latest.Result)
	}
}

func TestRecoverFinalizesPersistedPermanentFailureWithoutRedrive(t *testing.T) {
	var calls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			calls.Add(1)
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 5},
	)
	ctx := context.Background()
	occ := &jobs.JobOccurrence{
		ID: "occ-typed-permanent", JobID: "job-retry",
		OccurrenceKey: "manual:typed-recovery", State: jobs.OccurrenceReady,
		ReadyAt: time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.PrepareAttemptLease(ctx, occ.ID, "task:occ-typed-permanent:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"disposition":"permanent","code":"bad_payload"}`)
	if err := store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptFailed, metadata, "bad payload"); err != nil {
		t.Fatal(err)
	}

	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceFailed {
		t.Fatalf("occurrence state=%s, want failed; report=%+v", current.State, report)
	}
	if report.Redriven != 0 || report.Finalized != 1 {
		t.Fatalf("report=%+v, want finalized=1 redriven=0", report)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("handler calls=%d, permanent recovery must not redrive", got)
	}
	if n, err := store.CountAttempts(ctx, occ.ID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("attempts=%d, want 1", n)
	}
}
