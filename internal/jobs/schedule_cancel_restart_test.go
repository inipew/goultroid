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

func TestCancelledScheduleOccurrenceStaysCancelledAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedule-cancel.db")
	before, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(ctx, before.DB); err != nil {
		t.Fatal(err)
	}
	store := jobsqlite.NewStore(before.DB)
	def := jobs.JobDefinition{ID: "cancel-restart-target", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "test", Pool: "general", Enabled: true}
	if err := store.SaveDefinition(ctx, &def); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Second)
	if err := store.SaveSchedule(ctx, &jobs.JobSchedule{
		ID: "cancel-restart-schedule", JobID: def.ID, Recurrence: "once", NextDueAt: due,
		Timezone: "UTC", MisfirePolicy: jobs.MisfireRunOnce, OverlapPolicy: jobs.OverlapForbid, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	occurrence, err := store.MaterializeDueSchedule(ctx, "cancel-restart-schedule", due.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if occurrence == nil {
		t.Fatal("schedule did not materialize an occurrence")
	}
	manual := &jobs.JobOccurrence{
		ID: "manual-target-occurrence", JobID: def.ID, OccurrenceKey: "manual:target",
		State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC(),
	}
	if err := store.MaterializeOccurrence(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableSchedule(ctx, "cancel-restart-schedule"); err != nil {
		t.Fatal(err)
	}
	manager := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(store), nil)
	if err := manager.CancelScheduleOccurrences(ctx, "cancel-restart-schedule", "schedule deleted"); err != nil {
		t.Fatal(err)
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}

	after, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if err := jobsqlite.InitSchema(ctx, after.DB); err != nil {
		t.Fatal(err)
	}
	reopened := jobsqlite.NewStore(after.DB)
	current, err := reopened.GetOccurrence(ctx, occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceCancelled {
		t.Fatalf("state after restart = %s, want cancelled", current.State)
	}
	manualAfter, err := reopened.GetOccurrence(ctx, manual.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manualAfter.State != jobs.OccurrenceReady {
		t.Fatalf("unrelated manual occurrence state = %s, want ready", manualAfter.State)
	}
	if _, err := reopened.PrepareNextAttemptLease(ctx, occurrence.ID, time.Minute); err == nil {
		t.Fatal("cancelled occurrence accepted a new attempt after restart")
	}
}

func TestFailedScheduledAttemptRecoversOnceAfterSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedule-recovery.db")
	before, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(ctx, before.DB); err != nil {
		t.Fatal(err)
	}
	store := jobsqlite.NewStore(before.DB)
	def := jobs.JobDefinition{ID: "restart-scheduled", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "restart", Pool: "general", RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 2}, Enabled: true}
	if err := store.SaveDefinition(ctx, &def); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Second)
	if err := store.SaveSchedule(ctx, &jobs.JobSchedule{ID: "restart-schedule", JobID: def.ID, Recurrence: "once", NextDueAt: due, Timezone: "UTC", MisfirePolicy: jobs.MisfireRunOnce, OverlapPolicy: jobs.OverlapForbid, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	occurrence, err := store.MaterializeDueSchedule(ctx, "restart-schedule", time.Now().UTC())
	if err != nil || occurrence == nil {
		t.Fatalf("materialize schedule: occurrence=%+v error=%v", occurrence, err)
	}
	attempt, err := store.PrepareNextAttemptLease(ctx, occurrence.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptFailed, nil, "simulated crash gap"); err != nil {
		t.Fatal(err)
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}

	after, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if err := jobsqlite.InitSchema(ctx, after.DB); err != nil {
		t.Fatal(err)
	}
	reopened := jobsqlite.NewStore(after.DB)
	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(ctx)
	engine := taskengine.NewEngine(taskengine.Config{Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1 << 20}}, ResultCapacity: 4})
	engine.SetCommitPump(pump)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(ctx)
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(reopened), pump)
	var runs atomic.Int32
	if err := manager.RegisterHandler("restart", func(context.Context, jobs.JobDefinition) error { runs.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := reopened.GetOccurrence(ctx, occurrence.ID)
		if err == nil && current.State == jobs.OccurrenceCompleted {
			attempts, err := reopened.CountAttempts(ctx, occurrence.ID)
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || runs.Load() != 1 {
				t.Fatalf("recovery attempts=%d handler runs=%d, want 2 attempts and 1 run", attempts, runs.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := reopened.GetOccurrence(ctx, occurrence.ID)
	t.Fatalf("scheduled occurrence did not recover after SQLite reopen: %+v", current)
}
