package jobs_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/tasks"
)

type pausedAdmissionClient struct {
	tasks.Client
	entered   chan struct{}
	release   chan struct{}
	mu        sync.Mutex
	cancelled tasks.TaskID
}

func (c *pausedAdmissionClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	close(c.entered)
	select {
	case <-c.release:
		return pausedAdmissionTicket{id: spec.ID}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *pausedAdmissionClient) Cancel(id tasks.TaskID, _ tasks.Cause) (tasks.CancelReceipt, error) {
	c.mu.Lock()
	c.cancelled = id
	c.mu.Unlock()
	return tasks.CancelReceipt{}, nil
}

type pausedAdmissionTicket struct{ id tasks.TaskID }

func (t pausedAdmissionTicket) TaskID() tasks.TaskID           { return t.id }
func (pausedAdmissionTicket) State() tasks.TaskState           { return tasks.StateQueued }
func (pausedAdmissionTicket) Done() <-chan struct{}            { return nil }
func (pausedAdmissionTicket) Result() (tasks.TaskResult, bool) { return tasks.TaskResult{}, false }
func (pausedAdmissionTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	<-ctx.Done()
	return tasks.TaskResult{}, ctx.Err()
}

func TestCancelOccurrenceWaitsForConcurrentAdmissionAndCancelsTask(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(1, 8)
	if err := pump.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(ctx)
	client := &pausedAdmissionClient{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(client.release)
	store := jobsqlite.NewStore(db.DB)
	manager := jobs.NewManagerWithPorts(client, jobsqlite.Ports(store), pump)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(ctx)
	if err := manager.RegisterHandler("paused", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID: "paused-admission", ScopeOwner: "paused-admission", QuotaOwner: "paused-admission",
		HandlerType: "paused", Pool: "general", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	submitDone := make(chan error, 1)
	go func() {
		_, _, err := manager.SubmitOccurrence(ctx, "paused-admission", "paused:one")
		submitDone <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not reach task client")
	}
	occ, err := store.GetOccurrenceByKey(ctx, "paused:one")
	if err != nil || occ == nil {
		t.Fatalf("materialized occurrence = %+v, err=%v", occ, err)
	}
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- manager.CancelOccurrence(ctx, occ.ID, "test cancellation") }()
	select {
	case err := <-cancelDone:
		t.Fatalf("cancellation returned before in-flight admission finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	client.release <- struct{}{}
	if err := <-submitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	cancelled := client.cancelled
	client.mu.Unlock()
	if cancelled == "" {
		t.Fatal("admitted task was not cancelled")
	}
	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceCancelled {
		t.Fatalf("occurrence state = %s, want cancelled", current.State)
	}
}
