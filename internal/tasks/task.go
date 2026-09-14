package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskState represents the execution lifecycle of a single Task.
type TaskState string

const (
	StateCreated   TaskState = "created"
	StateAdmitted  TaskState = "admitted"
	StateQueued    TaskState = "queued"
	StateRunning   TaskState = "running"
	StateCompleted TaskState = "completed"
	StateFailed    TaskState = "failed"
	StateCancelled TaskState = "cancelled"
	StateTimedOut  TaskState = "timed_out"
)

// RetryPolicy defines how many times a failed task can be retried.
type RetryPolicy struct {
	MaxAttempts int           `json:"max_attempts"`
	Delay       time.Duration `json:"delay"`
}

// Task defines a discrete unit of work executed by a worker pool.
type Task struct {
	ID             string        `json:"id"`
	Owner          string        `json:"owner"`
	Name           string        `json:"name"`
	Priority       int           `json:"priority"`
	Timeout        time.Duration `json:"timeout"`
	Retry          RetryPolicy   `json:"retry"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	CorrelationID  string        `json:"correlation_id,omitempty"`

	CreatedAt   time.Time `json:"created_at"`
	AdmittedAt  time.Time `json:"admitted_at"`
	QueuedAt    time.Time `json:"queued_at"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	State       TaskState `json:"state"`
	Error       error     `json:"error,omitempty"`

	Run func(ctx context.Context) error `json:"-"`
	// OnComplete receives the terminal result, including panic and cancellation
	// before execution. Executors must invoke it exactly once for accepted work.
	OnComplete func(error) `json:"-"`
}

// Validate checks that required fields on the task are populated.
func (t *Task) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("task ID cannot be empty")
	}
	if strings.TrimSpace(t.Owner) == "" {
		return errors.New("task Owner cannot be empty")
	}
	if t.Run == nil {
		return errors.New("task Run function cannot be nil")
	}
	return nil
}

// Execute runs the task using the provided base context, applying timeout.
// A Task represents strictly one execution attempt; retries are handled
// by orchestrators (such as JobManager or Scheduler), not inside the physical task execution.
func (t *Task) Execute(parentCtx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task panicked: %v", r)
		}
		t.CompletedAt = time.Now().UTC()
		t.Error = err
		t.State = ResultState(err)
		if t.OnComplete != nil {
			t.OnComplete(err)
		}
	}()
	if err := t.Validate(); err != nil {
		return err
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if err := parentCtx.Err(); err != nil {
		return err
	}
	t.StartedAt = time.Now().UTC()
	t.State = StateRunning
	ctx := parentCtx
	if t.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parentCtx, t.Timeout)
		defer cancel()
	}
	return t.Run(ctx)
}

// ResultState maps a concrete attempt result to its terminal lifecycle state.
func ResultState(err error) TaskState {
	switch {
	case err == nil:
		return StateCompleted
	case errors.Is(err, context.DeadlineExceeded):
		return StateTimedOut
	case errors.Is(err, context.Canceled):
		return StateCancelled
	default:
		return StateFailed
	}
}
