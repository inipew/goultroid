package workers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Assignment represents a fully validated task dispatched to a physical worker slot (ADR 0006 §3.2).
type Assignment struct {
	Spec   tasks.WorkSpec
	Permit *Permit
	Ctx    context.Context
}

// ExecuteAssignment runs the physical execution boundary:
// applies timeout, isolates panics, records start/finish times, and returns an immutable TaskResult.
func ExecuteAssignment(a Assignment) (res tasks.TaskResult) {
	if a.Permit != nil {
		defer a.Permit.Release()
	}

	startedAt := time.Now().UTC()
	res.TaskID = a.Spec.ID
	if a.Spec.Job != nil {
		res.AttemptID = a.Spec.Job.AttemptID
	}
	res.StartedAt = startedAt

	defer func() {
		res.FinishedAt = time.Now().UTC()
		if r := recover(); r != nil {
			res.Outcome = tasks.OutcomePanic
			res.Cause = tasks.CausePanic
			res.Failure = tasks.FailureInfo{
				Message: fmt.Sprintf("panic in worker task: %v", r),
			}
		}
		if a.Spec.OnComplete != nil {
			a.Spec.OnComplete(res)
		}
	}()

	if a.Permit == nil {
		res.Outcome = tasks.OutcomeAbortedBeforeStart
		res.Cause = tasks.CauseLeaseLost
		res.Failure = tasks.FailureInfo{Message: "nil execution permit"}
		return res
	}

	if err := a.Permit.Use(); err != nil {
		res.Outcome = tasks.OutcomeAbortedBeforeStart
		res.Cause = tasks.CauseLeaseLost
		res.Failure = tasks.FailureInfo{Message: err.Error()}
		return res
	}

	parentCtx := a.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	if err := parentCtx.Err(); err != nil {
		res.Outcome = tasks.OutcomeCancelled
		res.Cause = tasks.CauseShutdown
		res.Failure = tasks.FailureInfo{Message: err.Error()}
		return res
	}

	execCtx := parentCtx
	if a.Spec.ExecutionTimeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(parentCtx, a.Spec.ExecutionTimeout)
		defer cancel()
	}

	var err error
	if a.Spec.Handler != nil {
		err = a.Spec.Handler(execCtx)
	}

	if err == nil {
		res.Outcome = tasks.OutcomeCompleted
		res.Cause = tasks.CauseNone
	} else if errors.Is(err, context.DeadlineExceeded) {
		res.Outcome = tasks.OutcomeTimedOut
		res.Cause = tasks.CauseTimeout
		res.Failure = tasks.FailureInfo{Message: err.Error()}
	} else if errors.Is(err, context.Canceled) {
		res.Outcome = tasks.OutcomeCancelled
		res.Cause = tasks.CauseUserCancel
		res.Failure = tasks.FailureInfo{Message: err.Error()}
	} else {
		res.Outcome = tasks.OutcomeFailed
		res.Cause = tasks.CauseNone
		res.Failure = tasks.FailureInfo{Message: err.Error()}
	}

	return res
}
