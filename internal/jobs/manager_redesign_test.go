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
	if _, err := manager.SubmitOccurrence(context.Background(), "job-1", "manual:one"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	spec := client.spec
	client.mu.Unlock()
	if spec.Job == nil || spec.Job.AttemptID == "" || spec.Job.OccurrenceID == "" {
		t.Fatalf("durable identity missing from submitted work: %#v", spec.Job)
	}
	spec.OnComplete(tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted, FinishedAt: time.Now().UTC()})

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
	if _, err := manager.SubmitOccurrence(context.Background(), "job-fb", "manual:fb"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	spec := client.spec
	client.mu.Unlock()
	if spec.Job == nil || spec.Job.AttemptID == "" {
		t.Fatalf("durable identity missing: %#v", spec.Job)
	}

	spec.OnComplete(tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted, FinishedAt: time.Now().UTC()})

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
