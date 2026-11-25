package jobs_test

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

// P1-A: A materialized ready occurrence with no attempt must be recovered
// after process restart and submitted through TaskEngine.
func TestManagerRecoversReadyOccurrenceWithoutAttempt(t *testing.T) {
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
	var handlerCalled atomic.Int32
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(store), pump)
	if err := manager.RegisterHandler("test-ready", func(context.Context, jobs.JobDefinition) error {
		handlerCalled.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID:          "job-ready-crash",
		ScopeOwner:  "test:ready",
		QuotaOwner:  "test:ready",
		HandlerType: "test-ready",
		Pool:        "general",
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 3},
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Simulate an occurrence materialized in "ready" state, but the process
	// crashed before PrepareNextAttemptLease could run (0 attempts).
	occ := &jobs.JobOccurrence{
		ID:            "occ-ready-crash",
		JobID:         "job-ready-crash",
		OccurrenceKey: "sched:ready:crash-slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	// Verify occurrence is in "ready" state with 0 attempts before manager start.
	beforeOcc, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeOcc.State != jobs.OccurrenceReady {
		t.Fatalf("occurrence state=%s, want ready", beforeOcc.State)
	}
	count, err := store.CountAttempts(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("attempt count=%d, want 0", count)
	}

	// Start manager; startup recovery should detect the ready occurrence and redrive it.
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			if got := handlerCalled.Load(); got != 1 {
				t.Fatalf("handler calls=%d, want 1", got)
			}
			attempts, err := store.CountAttempts(ctx, occ.ID)
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 1 {
				t.Fatalf("attempt count=%d, want exactly 1 attempt created by recovery", attempts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	current, _ := store.GetOccurrence(ctx, occ.ID)
	t.Fatalf("startup recovery did not converge ready occurrence: %+v, handler calls=%d", current, handlerCalled.Load())
}

func TestReadyOccurrenceRecoveryIsIdempotentAcrossRepeatedPasses(t *testing.T) {
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
	var handlerCalled atomic.Int32
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(store), pump)
	if err := manager.RegisterHandler("test-idempotent", func(context.Context, jobs.JobDefinition) error {
		handlerCalled.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID:          "job-idempotent",
		ScopeOwner:  "test:idemp",
		QuotaOwner:  "test:idemp",
		HandlerType: "test-idempotent",
		Pool:        "general",
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 3},
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	occ := &jobs.JobOccurrence{
		ID:            "occ-idemp",
		JobID:         "job-idempotent",
		OccurrenceKey: "sched:idemp:slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	// First recovery pass redrives the ready occurrence
	report1, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover pass 1: %v", err)
	}
	if report1.Redriven != 1 {
		t.Fatalf("pass 1 redriven=%d, want 1", report1.Redriven)
	}

	// Immediate second recovery pass should see it actively tracked (stale)
	report2, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover pass 2: %v", err)
	}
	if report2.Redriven != 0 {
		t.Fatalf("pass 2 redriven=%d, want 0 (already tracked)", report2.Redriven)
	}

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Third recovery pass after completion should find nothing to redrive
	report3, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover pass 3: %v", err)
	}
	if report3.Redriven != 0 {
		t.Fatalf("pass 3 redriven=%d, want 0", report3.Redriven)
	}
	if got := handlerCalled.Load(); got != 1 {
		t.Fatalf("handler calls=%d, want 1", got)
	}
}

func TestConcurrentRecoveryCreatesAtMostOneFirstAttempt(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}

	store := jobsqlite.NewStore(db.DB)
	ctx := context.Background()
	def := &jobs.JobDefinition{
		ID:         "job-race",
		ScopeOwner: "test:race",
		QuotaOwner: "test:race",
		Pool:       "general",
		Enabled:    true,
	}
	if err := store.SaveDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{
		ID:            "occ-race",
		JobID:         "job-race",
		OccurrenceKey: "sched:race:slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	// Two callers race on PrepareNextAttemptLease on the same ready occurrence
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := store.PrepareNextAttemptLease(ctx, occ.ID, time.Minute)
			errs <- err
		}()
	}

	var successes, failures int
	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil {
			successes++
		} else {
			failures++
		}
	}

	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent prepare attempts: successes=%d, failures=%d; want exactly 1 success and 1 failure", successes, failures)
	}

	count, err := store.CountAttempts(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempt count=%d, want 1", count)
	}
}

func TestReadyOccurrenceWithMissingDefinitionIsObservable(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}

	store := jobsqlite.NewStore(db.DB)
	ctx := context.Background()
	def := &jobs.JobDefinition{
		ID:         "job-nonexistent",
		ScopeOwner: "test:orphan",
		QuotaOwner: "test:orphan",
		Pool:       "general",
		Enabled:    true,
	}
	if err := store.SaveDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{
		ID:            "occ-orphan",
		JobID:         "job-nonexistent",
		OccurrenceKey: "sched:orphan:slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	manager := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(store), nil)
	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if report.Orphaned != 1 {
		t.Fatalf("orphaned count=%d, want 1", report.Orphaned)
	}
	if report.Redriven != 0 {
		t.Fatalf("redriven count=%d, want 0", report.Redriven)
	}

	// Occurrence must remain durable and ready, not deleted or modified
	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceReady {
		t.Fatalf("occurrence state=%s, want ready", current.State)
	}
}

func TestReadyOccurrenceWithDisabledDefinitionDoesNotExecute(t *testing.T) {
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
	var handlerCalled atomic.Int32
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(store), pump)
	if err := manager.RegisterHandler("test-disabled", func(context.Context, jobs.JobDefinition) error {
		handlerCalled.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Save disabled definition directly in store before Manager.Start loads it
	jobDef := jobs.JobDefinition{
		ID:          "job-disabled",
		ScopeOwner:  "test:disabled",
		QuotaOwner:  "test:disabled",
		HandlerType: "test-disabled",
		Enabled:     false,
	}
	if err := store.SaveDefinition(ctx, &jobDef); err != nil {
		t.Fatal(err)
	}

	occ := &jobs.JobOccurrence{
		ID:            "occ-disabled",
		JobID:         "job-disabled",
		OccurrenceKey: "sched:disabled:slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	// Manager.Start loads definitions from store, preserving Enabled = false
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(ctx)

	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if report.Orphaned != 1 {
		t.Fatalf("orphaned count=%d, want 1", report.Orphaned)
	}
	if handlerCalled.Load() != 0 {
		t.Fatal("disabled job handler was executed")
	}
}

func TestReadyOccurrenceRecoveryDoesNotReplayBlockedUnknownEffect(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}

	store := jobsqlite.NewStore(db.DB)
	var handlerCalled atomic.Int32
	manager := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(store), nil)
	if err := manager.RegisterHandler("test-blocked", func(context.Context, jobs.JobDefinition) error {
		handlerCalled.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	jobDef := jobs.JobDefinition{
		ID:          "job-blocked",
		ScopeOwner:  "test:blocked",
		QuotaOwner:  "test:blocked",
		HandlerType: "test-blocked",
		Enabled:     true,
	}
	if err := manager.Register(jobDef); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	occ := &jobs.JobOccurrence{
		ID:            "occ-blocked",
		JobID:         "job-blocked",
		OccurrenceKey: "sched:blocked:slot",
		State:         jobs.OccurrenceReady,
		ReadyAt:       time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	// Prepare attempt with short lease; PrepareNextAttemptLease updates occurrence to 'dispatched'
	a1, err := store.PrepareNextAttemptLease(ctx, occ.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// Recover should block the expired leased attempt
	report, err := manager.Recover(ctx, 50)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if report.Blocked != 1 {
		t.Fatalf("blocked count=%d, want 1", report.Blocked)
	}
	if report.Redriven != 0 {
		t.Fatalf("redriven count=%d, want 0 (must not replay unknown effect)", report.Redriven)
	}

	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceBlocked {
		t.Fatalf("occurrence state=%s, want blocked", current.State)
	}
	_ = a1
}

func TestRedesignedScheduleRecoversAfterMaterializationCrash(t *testing.T) {
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
	var handlerCalled atomic.Int32
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(store), pump)
	if err := manager.RegisterHandler("sched-handler", func(context.Context, jobs.JobDefinition) error {
		handlerCalled.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	jobDef := jobs.JobDefinition{
		ID:          "job-sched-crash",
		ScopeOwner:  "test:sched",
		QuotaOwner:  "test:sched",
		HandlerType: "sched-handler",
		Enabled:     true,
	}
	if err := manager.Register(jobDef); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	sched := &jobs.JobSchedule{
		ID:            "sched-crash-1",
		JobID:         jobDef.ID,
		Recurrence:    "interval",
		Interval:      time.Hour,
		NextDueAt:     now.Add(-time.Minute),
		Timezone:      "UTC",
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       true,
	}
	if err := store.SaveSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	// Materialize occurrence atomically advancing schedule, simulating crash right after SQLite commit
	occ, err := store.MaterializeDueSchedule(ctx, sched.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if occ == nil {
		t.Fatal("expected occurrence from MaterializeDueSchedule, got nil")
	}

	// Record next due at after materialization
	schedAfterMat, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	savedNextDue := schedAfterMat.NextDueAt

	// Verify occurrence is in ready state with 0 attempts
	attemptsBefore, err := store.CountAttempts(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attemptsBefore != 0 {
		t.Fatalf("attempts before start = %d, want 0", attemptsBefore)
	}

	// Start manager; startup recovery must find and redrive this occurrence
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.GetOccurrence(ctx, occ.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			if got := handlerCalled.Load(); got != 1 {
				t.Fatalf("handler called %d times, want 1", got)
			}
			attemptsAfter, err := store.CountAttempts(ctx, occ.ID)
			if err != nil {
				t.Fatal(err)
			}
			if attemptsAfter != 1 {
				t.Fatalf("attempts after recovery = %d, want 1", attemptsAfter)
			}
			schedFinal, err := store.GetSchedule(ctx, sched.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !schedFinal.NextDueAt.Equal(savedNextDue) {
				t.Fatalf("schedule next due changed from %v to %v", savedNextDue, schedFinal.NextDueAt)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	current, _ := store.GetOccurrence(ctx, occ.ID)
	t.Fatalf("redesigned schedule occurrence did not complete: %+v", current)
}
