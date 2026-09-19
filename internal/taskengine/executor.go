package taskengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func executeAssignment(ctx context.Context, spec tasks.WorkSpec, grant *permit, onStarted func(time.Time)) (result tasks.TaskResult) {
	defer grant.release()
	result.TaskID = spec.ID
	if spec.Job != nil {
		result.AttemptID = spec.Job.AttemptID
	}
	defer func() {
		result.FinishedAt = time.Now().UTC()
		if recovered := recover(); recovered != nil {
			result.Outcome = tasks.OutcomePanic
			result.Cause = tasks.CausePanic
			result.Failure = tasks.FailureInfo{Message: fmt.Sprintf("panic in task handler: %v", recovered)}
		}
	}()

	if err := grant.use(spec); err != nil {
		result.Outcome = tasks.OutcomeAbortedBeforeStart
		result.Cause = tasks.CauseLeaseLost
		result.Failure = tasks.FailureInfo{Message: err.Error()}
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Outcome = tasks.OutcomeCancelled
		result.Cause = tasks.CauseShutdown
		result.Failure = tasks.FailureInfo{Message: err.Error()}
		return result
	}
	if !spec.QueueDeadline.IsZero() && !time.Now().Before(spec.QueueDeadline) {
		result.Outcome = tasks.OutcomeTimedOut
		result.Cause = tasks.CauseQueueExpired
		return result
	}
	if spec.Handler == nil {
		result.Outcome = tasks.OutcomeAbortedBeforeStart
		result.Failure = tasks.FailureInfo{Message: tasks.ErrUnknownHandler.Error()}
		return result
	}

	runCtx := ctx
	if spec.ExecutionTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.ExecutionTimeout)
		defer cancel()
	}
	if spec.Job != nil {
		runCtx = execution.WithMetadata(runCtx, execution.Metadata{CanDurablyYield: true})
	}
	result.StartedAt = time.Now().UTC()
	if onStarted != nil {
		onStarted(result.StartedAt)
	}
	err := spec.Handler(runCtx)
	var rateLimit interface {
		RateLimitWait() time.Duration
	}
	switch {
	case err == nil:
		result.Outcome = tasks.OutcomeCompleted
		result.Cause = tasks.CauseNone
	case errors.As(err, &rateLimit):
		wait := rateLimit.RateLimitWait()
		if wait < 0 {
			wait = 0
		}
		result.Outcome = tasks.OutcomeFailed
		result.Cause = tasks.CauseRateLimited
		result.RetryAfter = wait
		result.Failure = tasks.FailureInfo{Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		result.Outcome = tasks.OutcomeTimedOut
		result.Cause = tasks.CauseTimeout
		result.Failure = tasks.FailureInfo{Message: err.Error()}
	case errors.Is(err, context.Canceled):
		result.Outcome = tasks.OutcomeCancelled
		result.Cause = tasks.CauseUserCancel
		result.Failure = tasks.FailureInfo{Message: err.Error()}
	default:
		result.Outcome = tasks.OutcomeFailed
		result.Cause = tasks.CauseNone
		result.Failure = tasks.FailureInfo{Message: err.Error()}
	}
	return result
}
