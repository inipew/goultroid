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
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	State       TaskState `json:"state"`
	Error       error     `json:"error,omitempty"`

	Run func(ctx context.Context) error `json:"-"`
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

// Execute runs the task using the provided base context, applying timeout and retries.
func (t *Task) Execute(parentCtx context.Context) error {
	if err := t.Validate(); err != nil {
		t.State = StateFailed
		t.Error = err
		return err
	}

	t.StartedAt = time.Now().UTC()
	t.State = StateRunning

	maxAttempts := t.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-parentCtx.Done():
			t.State = StateCancelled
			t.CompletedAt = time.Now().UTC()
			t.Error = parentCtx.Err()
			return parentCtx.Err()
		default:
		}

		ctx := parentCtx
		var cancel context.CancelFunc
		if t.Timeout > 0 {
			ctx, cancel = context.WithTimeout(parentCtx, t.Timeout)
		}

		err := func() (runErr error) {
			defer func() {
				if r := recover(); r != nil {
					runErr = fmt.Errorf("task panicked: %v", r)
				}
			}()
			return t.Run(ctx)
		}()

		if cancel != nil {
			cancel()
		}

		if err == nil {
			t.State = StateCompleted
			t.CompletedAt = time.Now().UTC()
			t.Error = nil
			return nil
		}

		lastErr = err
		if errors.Is(err, context.DeadlineExceeded) {
			t.State = StateTimedOut
		} else if errors.Is(err, context.Canceled) {
			t.State = StateCancelled
			t.CompletedAt = time.Now().UTC()
			t.Error = err
			return err
		}

		if attempt < maxAttempts && t.Retry.Delay > 0 {
			select {
			case <-time.After(t.Retry.Delay):
			case <-parentCtx.Done():
				t.State = StateCancelled
				t.CompletedAt = time.Now().UTC()
				t.Error = parentCtx.Err()
				return parentCtx.Err()
			}
		}
	}

	if t.State != StateTimedOut {
		t.State = StateFailed
	}
	t.CompletedAt = time.Now().UTC()
	t.Error = lastErr
	return lastErr
}
