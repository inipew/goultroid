package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	_ "modernc.org/sqlite"
)

// captureSvc records sent messages; err makes every send fail.
type captureSvc struct {
	core.MockTelegramServicer
	mu    sync.Mutex
	texts []string
	err   error
}

func (s *captureSvc) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.texts = append(s.texts, text)
	return &tg.Message{ID: len(s.texts), Message: text}, nil
}

func (s *captureSvc) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.texts)
}

type timingHarness struct {
	sched *Engine
	repo  *SQLiteRepository
	svc   *captureSvc
	jobs  *jobs.Manager
	store *jobsqlite.Store
	jobDB *sql.DB
}

func newTimingHarness(t *testing.T) *timingHarness {
	t.Helper()
	schedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	schedDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = schedDB.Close() })
	repo := NewSQLiteRepository(schedDB)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	jobDB, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobDB.Close() })
	if err := jobsqlite.InitSchema(context.Background(), jobDB.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pump.Stop(context.Background()) })
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general":   {Concurrency: 4, BacklogLimit: 50, PayloadBudget: 1 << 20},
			"scheduler": {Concurrency: 4, BacklogLimit: 50, PayloadBudget: 1 << 20},
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
	store := jobsqlite.NewStore(jobDB.DB)
	jobsMgr := jobs.NewManager(engine, store, pump)
	if err := jobsMgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobsMgr.Stop(context.Background()) })

	svc := &captureSvc{}
	if err := jobsMgr.RegisterHandler("scheduler.action", func(ctx context.Context, definition jobs.JobDefinition) error {
		id, err := strconv.ParseInt(strings.TrimPrefix(definition.ID, "scheduler:job:"), 10, 64)
		if err != nil {
			return err
		}
		job, err := repo.GetScheduledJob(ctx, id)
		if err != nil {
			return err
		}
		_, err = svc.SendMessage(ctx, &tg.InputPeerChat{ChatID: job.ChatID}, job.Payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sched := NewEngine(repo, nil)
	sched.SetTasks(engine)
	sched.SetJobsManager(jobsMgr)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Stop(context.Background()) })
	return &timingHarness{sched: sched, repo: repo, svc: svc, jobs: jobsMgr, store: store, jobDB: jobDB.DB}
}

func TestEngineStartRequiresExecutionDependencies(t *testing.T) {
	if err := NewEngine(nil, nil).Start(context.Background()); err == nil {
		t.Fatal("scheduler started without repository")
	}
	repo := NewSQLiteRepository(nil)
	sched := NewEngine(repo, nil)
	if err := sched.Start(context.Background()); err == nil {
		t.Fatal("scheduler started without task client")
	}
	sched.SetTasks(taskengine.NewEngine(taskengine.DefaultConfig))
	if err := sched.Start(context.Background()); err == nil {
		t.Fatal("scheduler started without jobs manager")
	}
}

func pollRowStatus(t *testing.T, h *timingHarness, chatID int64, wantCount int, timeout time.Duration) []ScheduledJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rows, err := h.repo.ListScheduledJobs(context.Background(), chatID)
		if err == nil && len(rows) == wantCount {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := h.repo.ListScheduledJobs(context.Background(), chatID)
	t.Fatalf("rows=%d, want %d", len(rows), wantCount)
	return nil
}

// E1: a due one-shot message flows claim -> occurrence -> action -> durable
// commit -> row deletion, with exactly one delivery.
func TestScheduledMessageExecutesOnceAndCompletes(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	job, err := h.sched.ScheduleOnce(ctx, 42, "chat", 0, time.Now().UTC().Add(-2*time.Second), ActionMessage, "hello")
	if err != nil {
		t.Fatal(err)
	}
	pollRowStatus(t, h, 42, 0, 10*time.Second)
	if h.svc.count() != 1 {
		t.Fatalf("deliveries=%d, want exactly 1", h.svc.count())
	}
	time.Sleep(300 * time.Millisecond)
	if h.svc.count() != 1 {
		t.Fatalf("duplicate delivery after settle: %d", h.svc.count())
	}
	unresolved, err := h.store.ListUnresolvedOccurrences(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("unresolved occurrences left: %d", len(unresolved))
	}
	_ = job
}

func TestCutoverExecutesFromRedesignedSchedule(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	if _, err := h.jobDB.ExecContext(ctx, `UPDATE execution_runtime_state SET mode = 'redesigned' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	job, err := h.sched.ScheduleOnce(ctx, 91, "chat", 0, time.Now().UTC().Add(100*time.Millisecond), ActionMessage, "redesigned")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.svc.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.svc.count() != 1 {
		schedule, scheduleErr := h.store.GetSchedule(ctx, redesignedScheduleID(job.ID))
		unresolved, _ := h.store.ListUnresolvedOccurrences(ctx, 20)
		active, modeErr := h.jobs.ScheduleCutoverActive(ctx)
		t.Fatalf("deliveries=%d, want exactly 1; schedule=%+v scheduleErr=%v unresolved=%+v cutover=%t modeErr=%v wake=%d", h.svc.count(), schedule, scheduleErr, unresolved, active, modeErr, len(h.sched.wakeChan))
	}
	schedule, err := h.store.GetSchedule(ctx, redesignedScheduleID(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Enabled {
		t.Fatal("one-shot redesigned schedule remained enabled")
	}
}

// E1: a failing one-shot exhausts the job budget and fails the row
// permanently; the scheduler itself never retries.
func TestScheduledFailureFailsRowPermanently(t *testing.T) {
	h := newTimingHarness(t)
	h.svc.err = errors.New("telegram down")
	ctx := context.Background()
	if _, err := h.sched.ScheduleOnce(ctx, 43, "chat", 0, time.Now().UTC().Add(-2*time.Second), ActionMessage, "hello"); err != nil {
		t.Fatal(err)
	}
	rows := pollRowStatus(t, h, 43, 1, 10*time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for rows[0].Status != JobStatusFailed {
		if time.Now().After(deadline) {
			t.Fatalf("status=%s, want failed", rows[0].Status)
		}
		time.Sleep(20 * time.Millisecond)
		rows, _ = h.repo.ListScheduledJobs(context.Background(), 43)
		if len(rows) != 1 {
			t.Fatalf("row vanished: %+v", rows)
		}
	}
	hist, err := h.repo.GetJobHistory(ctx, rows[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 {
		t.Fatalf("expected history entries")
	}
}

// E1: recurring rows advance past the fired slot after durable success.
func TestRecurringRowAdvancesAfterSuccess(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	job, err := h.sched.ScheduleRecurring(ctx, 44, "chat", 0, time.Hour, ActionMessage, "tick")
	if err != nil {
		t.Fatal(err)
	}
	// Force the slot due without waiting an hour.
	if err := h.repo.UpdateScheduledJobNextRun(ctx, job.ID, time.Now().UTC().Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	h.sched.notifyWake()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, _ := h.repo.ListScheduledJobs(ctx, 44)
		if len(rows) == 1 && rows[0].Status == JobStatusPending && rows[0].NextRunAt.After(time.Now().UTC()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recurring row never advanced: %+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.svc.count() != 1 {
		t.Fatalf("deliveries=%d, want 1", h.svc.count())
	}
}

// E1: misfire-skip advances an overdue row without executing the action.
func TestMisfireSkipAdvancesWithoutExecution(t *testing.T) {
	h := newTimingHarness(t)
	h.sched.SetMisfirePolicy(MisfireSkip)
	ctx := context.Background()
	job, err := h.sched.ScheduleRecurring(ctx, 45, "chat", 0, time.Hour, ActionMessage, "tick")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.UpdateScheduledJobNextRun(ctx, job.ID, time.Now().UTC().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h.sched.notifyWake()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, _ := h.repo.ListScheduledJobs(ctx, 45)
		if len(rows) == 1 && rows[0].Status == JobStatusPending && rows[0].NextRunAt.After(time.Now().UTC()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("skipped row never advanced: %+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.svc.count() != 0 {
		t.Fatalf("skipped misfire executed %d times", h.svc.count())
	}
}

// E1: cancellation deletes the row and durably closes the occurrence so no
// retry can resurrect it.
func TestCancelDeletesRowAndClosesOccurrence(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	job, err := h.sched.ScheduleRecurring(ctx, 46, "chat", 0, time.Hour, ActionMessage, "tick")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.UpdateScheduledJobNextRun(ctx, job.ID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	h.sched.notifyWake()
	// Wait until claimed (running) or settled, then cancel.
	time.Sleep(500 * time.Millisecond)
	if err := h.sched.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := h.repo.ListScheduledJobs(ctx, 46)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("cancelled row remains: %+v", rows)
	}
}

// E2: periodic ticks run repeatedly through JobManager occurrences with
// stats reconciled from durable settlement.
func TestPeriodicRunsThroughJobOccurrences(t *testing.T) {
	h := newTimingHarness(t)
	var calls atomic.Int32
	if err := h.sched.RegisterPeriodicTask("tick", 100*time.Millisecond, func(ctx context.Context) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			unresolved, _ := h.store.ListUnresolvedOccurrences(context.Background(), 20)
			h.sched.periodic.mu.Lock()
			heapLen := h.sched.periodic.heap.Len()
			entry := h.sched.periodic.entries["tick"]
			running := h.sched.periodic.running
			h.sched.periodic.mu.Unlock()
			t.Fatalf("periodic tick never ran: snapshots=%+v jobs=%+v unresolved=%+v heap=%d running=%t entry=%+v", h.sched.PeriodicTaskSnapshots(), h.jobs.Diagnostics(), unresolved, heapLen, running, entry)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stats converge once occurrences settle.
	deadline = time.Now().Add(5 * time.Second)
	for {
		snaps := h.sched.PeriodicTaskSnapshots()
		if len(snaps) == 1 && snaps[0].Runs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("periodic stats never converged: %+v", snaps)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := calls.Load()
	if err := h.sched.UnregisterPeriodicTask("tick"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if after := calls.Load(); after > before+1 {
		t.Fatalf("unregistered task kept running: %d -> %d", before, after)
	}
}

// E2: periodic retry is owned solely by the JobManager policy: a failing
// task with MaxAttempts=3 executes exactly 3 attempts, then the occurrence
// fails and stats record one failure.
func TestPeriodicRetryCollapsesToJobPolicy(t *testing.T) {
	h := newTimingHarness(t)
	var calls atomic.Int32
	if err := h.sched.RegisterPeriodicTaskWithOptions("flaky", 100*time.Millisecond,
		PeriodicTaskOptions{Owner: "runtime", MaxAttempts: 3, RetryDelay: 10 * time.Millisecond},
		func(ctx context.Context) error {
			calls.Add(1)
			return errors.New("flaky failure")
		}); err != nil {
		t.Fatal(err)
	}
	// Find the in-flight occurrence and wait for 3 fenced attempts.
	deadline := time.Now().Add(10 * time.Second)
	var occID string
	for {
		h.sched.periodic.mu.Lock()
		for _, entry := range h.sched.periodic.entries {
			if entry.Name == "flaky" && entry.OccurrenceID != "" {
				occID = entry.OccurrenceID
			}
		}
		h.sched.periodic.mu.Unlock()
		if occID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no periodic occurrence tracked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		n, err := h.store.CountAttempts(context.Background(), occID)
		if err == nil && n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempts did not reach budget 3 (n=%d err=%v)", n, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		occ, err := h.store.GetOccurrence(context.Background(), occID)
		if err == nil && occ.State == jobs.OccurrenceFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("occurrence never finalized as failed: %+v %v", occ, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 3 {
		t.Fatalf("task executed %d times, want exactly 3 (one retry engine)", calls.Load())
	}
}

func TestJobsScheduleMutationWakesIdleScheduler(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()

	// Register one definition so the durable schedule store accepts the row.
	if err := h.jobs.RegisterHandler("wake.test", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.Register(jobs.JobDefinition{
		ID: "wake-test-job", ScopeOwner: "test:wake", QuotaOwner: "test",
		HandlerType: "wake.test", Pool: "scheduler", Class: string(tasks.PriorityMaintenance),
		Timeout: time.Second, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Stop the loop so the coalesced wake remains observable in the channel.
	if err := h.sched.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-h.sched.wakeChan:
		default:
			goto drained
		}
	}
drained:
	if err := h.jobs.SaveSchedule(ctx, jobs.JobSchedule{
		ID: "wake-test-schedule", JobID: "wake-test-job", Recurrence: "once",
		Timezone: "UTC", NextDueAt: time.Now().UTC().Add(time.Hour),
		MisfirePolicy: jobs.MisfireRunOnce, OverlapPolicy: jobs.OverlapForbid,
		Enabled: true, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.sched.wakeChan:
	case <-time.After(time.Second):
		t.Fatal("durable Jobs schedule mutation did not wake Scheduler")
	}
}
