package jobs_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type capturedClient struct {
	mu   sync.Mutex
	spec tasks.WorkSpec
}

func (c *capturedClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.spec = spec
	c.mu.Unlock()
	return capturedTicket{id: spec.ID}, nil
}

func (c *capturedClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *capturedClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *capturedClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type capturedTicket struct{ id tasks.TaskID }

func (t capturedTicket) TaskID() tasks.TaskID                         { return t.id }
func (capturedTicket) State() tasks.TaskState                         { return tasks.StateQueued }
func (capturedTicket) Done() <-chan struct{}                          { return nil }
func (capturedTicket) Result() (tasks.TaskResult, bool)               { return tasks.TaskResult{}, false }
func (capturedTicket) Wait(context.Context) (tasks.TaskResult, error) { return tasks.TaskResult{}, nil }

func TestManagerPersistsOccurrenceAttemptAndCompletion(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(1, 4)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())
	client := &capturedClient{}
	manager := jobs.NewManager(client, jobsqlite.NewStore(db.DB), pump)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterHandler("test", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{ID: "job-1", ScopeOwner: "plugin:test", QuotaOwner: "user:1", HandlerType: "test", Pool: "general"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-1", "manual:one"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	spec := client.spec
	client.mu.Unlock()
	if spec.Job == nil || spec.Job.AttemptID == "" || spec.Job.OccurrenceID == "" {
		t.Fatalf("durable identity missing from submitted work: %#v", spec.Job)
	}
	if spec.Commit == nil {
		t.Fatalf("durable commit hook missing from submitted work")
	}
	// Simulate the engine's durable commit path: physical completion followed
	// by exactly one Commit invocation.
	if err := spec.Commit(context.Background(), tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted, FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		var state string
		err = db.DB.QueryRow(`SELECT state FROM job_attempts WHERE id = ?`, spec.Job.AttemptID).Scan(&state)
		if err == nil && state == string(jobs.AttemptCompleted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt completion was not persisted, state=%q err=%v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
	var occurrenceState string
	if err := db.DB.QueryRow(`SELECT state FROM job_occurrences WHERE id = ?`, spec.Job.OccurrenceID).Scan(&occurrenceState); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if occurrenceState != string(jobs.OccurrenceCompleted) {
		t.Fatalf("occurrence state = %q, want completed", occurrenceState)
	}
}

func TestManagerPersistsOccurrenceAttempt_PumpFallback(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	// Start pump, then stop it so enqueue fails, triggering the fallback path
	pump := jobs.NewPersistencePump(1, 1)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pump.Stop(context.Background())

	client := &capturedClient{}
	manager := jobs.NewManager(client, jobsqlite.NewStore(db.DB), pump)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterHandler("fallback", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{ID: "job-fb", ScopeOwner: "plugin:test", QuotaOwner: "user:1", HandlerType: "fallback", Pool: "general"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-fb", "manual:fb"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	spec := client.spec
	client.mu.Unlock()
	if spec.Job == nil || spec.Job.AttemptID == "" {
		t.Fatalf("durable identity missing: %#v", spec.Job)
	}
	if spec.Commit == nil {
		t.Fatalf("durable commit hook missing")
	}

	// The commit hook is a pure store function: it persists even though the
	// pump is stopped, which is exactly what the engine's bounded direct
	// fallback relies on when the pump is saturated or down.
	if err := spec.Commit(context.Background(), tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted, FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		var state string
		err = db.DB.QueryRow(`SELECT state FROM job_attempts WHERE id = ?`, spec.Job.AttemptID).Scan(&state)
		if err == nil && state == string(jobs.AttemptCompleted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fallback attempt completion was not persisted, state=%q err=%v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestEngineBackedOccurrenceCommitsBeforeTicketResolves is the end-to-end
// proof that the durable commit protocol closes the crash window: with a real
// TaskEngine, real PersistencePump, and real sqlite store, the occurrence
// ticket resolves only after the attempt commit is durable, and the engine
// never reports success from memory alone.
func TestEngineBackedOccurrenceCommitsBeforeTicketResolves(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(2, 16)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20},
		},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	})
	engine.SetCommitPump(pump)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(context.Background())
	manager := jobs.NewManager(engine, jobsqlite.NewStore(db.DB), pump)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterHandler("e2e", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{ID: "job-e2e", ScopeOwner: "plugin:test", QuotaOwner: "user:1", HandlerType: "e2e", Pool: "general"}); err != nil {
		t.Fatal(err)
	}
	ticket, _, err := manager.SubmitOccurrence(context.Background(), "job-e2e", "manual:e2e")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ticket.Wait(ctx)
	if err != nil {
		t.Fatalf("ticket wait: %v", err)
	}
	if res.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("outcome=%s, want completed", res.Outcome)
	}
	// The ticket resolved, so the commit must already be durable: read the
	// store synchronously with no polling.
	var attemptState, occurrenceState string
	if err := db.DB.QueryRow(`SELECT state FROM job_attempts WHERE task_id = ?`, string(res.TaskID)).Scan(&attemptState); err != nil {
		t.Fatalf("attempt not durably committed at ticket resolution: %v", err)
	}
	if attemptState != string(jobs.AttemptCompleted) {
		t.Fatalf("attempt state=%q, want completed", attemptState)
	}
	var occurrenceID string
	if err := db.DB.QueryRow(`SELECT occurrence_id FROM job_attempts WHERE task_id = ?`, string(res.TaskID)).Scan(&occurrenceID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT state FROM job_occurrences WHERE id = ?`, occurrenceID).Scan(&occurrenceState); err != nil {
		t.Fatal(err)
	}
	if occurrenceState != string(jobs.OccurrenceCompleted) {
		t.Fatalf("occurrence state=%q, want completed", occurrenceState)
	}
}
