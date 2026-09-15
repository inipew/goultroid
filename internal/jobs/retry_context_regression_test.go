package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type retryContextTicket struct{}

func (retryContextTicket) TaskID() tasks.TaskID   { return "task:occ:1" }
func (retryContextTicket) State() tasks.TaskState { return tasks.StateFailed }
func (retryContextTicket) Done() <-chan struct{}  { ch := make(chan struct{}); close(ch); return ch }
func (retryContextTicket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{TaskID: "task:occ:1", Outcome: tasks.OutcomeFailed}, true
}
func (retryContextTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return tasks.TaskResult{TaskID: "task:occ:1", Outcome: tasks.OutcomeFailed}, nil
}

type retryContextClient struct{}

func (retryContextClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return nil, errors.New("stop after proving fresh retry context")
}
func (retryContextClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (retryContextClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (retryContextClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type retryContextStore struct {
	getCalls    atomic.Int32
	secondFresh atomic.Bool
}

func (s *retryContextStore) SaveDefinition(context.Context, *JobDefinition) error { return nil }
func (s *retryContextStore) UpdateDefinitionCAS(context.Context, *JobDefinition, uint64) error {
	return nil
}
func (s *retryContextStore) MaterializeOccurrence(context.Context, *JobOccurrence) error { return nil }
func (s *retryContextStore) PrepareAttemptLease(context.Context, string, string, time.Duration) (*JobAttempt, error) {
	return &JobAttempt{ID: "attempt:occ:2", OccurrenceID: "occ", AttemptNo: 2, LeaseEpoch: 2}, nil
}
func (s *retryContextStore) CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error {
	return nil
}
func (s *retryContextStore) FinalizeOccurrence(context.Context, string, OccurrenceState) error {
	return nil
}
func (s *retryContextStore) CancelOccurrence(context.Context, string, string) error { return nil }
func (s *retryContextStore) GetOccurrence(ctx context.Context, id string) (*JobOccurrence, error) {
	call := s.getCalls.Add(1)
	if call == 2 && ctx.Err() == nil {
		s.secondFresh.Store(true)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &JobOccurrence{ID: id, JobID: "job", State: OccurrenceDispatched}, nil
}
func (s *retryContextStore) GetOccurrenceByKey(context.Context, string) (*JobOccurrence, error) {
	return nil, errors.New("unused")
}
func (s *retryContextStore) CountAttempts(ctx context.Context, _ string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 1, nil
}
func (s *retryContextStore) LatestAttempt(context.Context, string) (*JobAttempt, error) {
	return nil, errors.New("unused")
}
func (s *retryContextStore) ListUnresolvedOccurrences(context.Context, int) ([]*JobOccurrence, error) {
	return nil, nil
}
func (s *retryContextStore) DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error) {
	return 0, nil
}

func TestWatchAttemptRefreshesStorageContextAfterBackoff(t *testing.T) {
	store := &retryContextStore{}
	m := NewManager(retryContextClient{}, store, nil)
	m.operationTimeout = 5 * time.Millisecond
	m.stopCh = make(chan struct{})
	m.tracked["occ"] = &trackedOccurrence{
		def: JobDefinition{
			ID:          "job",
			ScopeOwner:  "plugin:test",
			QuotaOwner:  "user:1",
			HandlerType: "h",
			Pool:        "general",
			RetryPolicy: JobRetryPolicy{MaxAttempts: 3, InitialDelay: 25 * time.Millisecond},
		},
		handler: func(context.Context, JobDefinition) error { return nil },
		taskID:  "task:occ:1",
	}
	m.accepting = true

	m.watchAttempt(context.Background(), retryItem{occurrenceID: "occ", ticket: retryContextTicket{}})

	if got := store.getCalls.Load(); got < 2 {
		t.Fatalf("expected occurrence re-read after backoff, calls=%d", got)
	}
	if !store.secondFresh.Load() {
		t.Fatal("post-backoff occurrence read reused an expired storage context")
	}
}
