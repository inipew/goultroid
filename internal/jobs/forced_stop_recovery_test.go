package jobs_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestForcedStopUnknownEffectBlocksAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "forced-stop.db")
	before, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(ctx, before.DB); err != nil {
		t.Fatal(err)
	}
	store := jobsqlite.NewStore(before.DB)
	pump := jobs.NewPersistencePump(2, 32)
	if err := pump.Start(ctx); err != nil {
		t.Fatal(err)
	}
	engine := taskengine.NewEngine(taskengine.Config{Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
		"general": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1 << 20},
	}, ResultCapacity: 4})
	engine.SetCommitPump(pump)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	manager := jobs.NewManagerWithPorts(engine, jobsqlite.Ports(store), pump)
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.ForceStop(ctx)
		_ = engine.ForceStop(ctx)
		_ = pump.Stop(ctx)
		_ = before.Close()
	}()
	if err := manager.RegisterHandler("external", func(context.Context, jobs.JobDefinition) error {
		close(started)
		<-release // Model a handler whose external effect ignores cancellation.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	def := jobs.JobDefinition{ID: "forced-external", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "external", Pool: "general", RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 3}, Enabled: true}
	if err := manager.Register(def); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, occurrenceID, err := manager.SubmitOccurrence(ctx, def.ID, "sched:forced:slot")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled handler did not start")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = engine.ForceStop(stopCtx)
	cancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("force stop error = %v", err)
	}
	close(release)
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.LatestAttempt(ctx, occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != jobs.AttemptLeased {
		t.Fatalf("unknown-effect attempt state = %s, want leased", attempt.State)
	}
	if _, err := before.DB.ExecContext(ctx, `UPDATE job_attempts SET lease_until = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := pump.Stop(ctx); err != nil {
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
	recovery := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(reopened), nil)
	if err := recovery.RegisterHandler("external", func(context.Context, jobs.JobDefinition) error {
		t.Fatal("unknown effect replayed after forced stop")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Register(def); err != nil {
		t.Fatal(err)
	}
	report, err := recovery.Recover(ctx, 10)
	if err != nil || report.Blocked != 1 || report.Redriven != 0 {
		current, _ := reopened.GetOccurrence(ctx, occurrenceID)
		latest, _ := reopened.LatestAttempt(ctx, occurrenceID)
		t.Fatalf("forced-stop recovery = %+v, error = %v; occurrence=%+v attempt=%+v", report, err, current, latest)
	}
	current, err := reopened.GetOccurrence(ctx, occurrenceID)
	if err != nil || current.State != jobs.OccurrenceBlocked {
		t.Fatalf("forced-stop occurrence = %+v, error = %v", current, err)
	}
}
