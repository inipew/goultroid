package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func (m *Manager) loadAttemptSummary(ctx context.Context, occurrenceID string) (*AttemptSummary, error) {
	if store, ok := m.store.(attemptSummaryStore); ok {
		return store.AttemptSummary(ctx, occurrenceID)
	}

	// Compatibility fallback for alternate/test stores. Production SQLite uses
	// the single-round-trip AttemptSummary fast path above.
	occ, err := m.store.GetOccurrence(ctx, occurrenceID)
	if err != nil {
		return nil, err
	}
	summary := &AttemptSummary{
		OccurrenceState: occ.State,
		ReadyAt:         occ.ReadyAt,
	}
	if occurrenceTerminal(occ.State) {
		return summary, nil
	}
	latest, err := m.store.LatestAttempt(ctx, occurrenceID)
	if err != nil {
		return nil, err
	}
	summary.Latest = *latest
	if summary.AttemptCount, err = m.store.CountAttempts(ctx, occurrenceID); err != nil {
		return nil, err
	}
	if summary.RetryBudgetUses, err = m.store.CountRetryBudgetUses(ctx, occurrenceID); err != nil {
		return nil, err
	}
	if summary.Deferrals, err = m.store.CountDeferrals(ctx, occurrenceID); err != nil {
		return nil, err
	}
	return summary, nil
}

func (m *Manager) prepareNextAttemptLease(ctx context.Context, occurrenceID string, leaseDuration time.Duration) (*JobAttempt, error) {
	if store, ok := m.store.(nextAttemptLeaseStore); ok {
		return store.PrepareNextAttemptLease(ctx, occurrenceID, leaseDuration)
	}

	// Compatibility fallback for alternate/test stores. Production SQLite
	// computes attempt_no and TaskID inside one fenced transaction.
	attempts, err := m.store.CountAttempts(ctx, occurrenceID)
	if err != nil {
		return nil, err
	}
	taskID := fmt.Sprintf("task:%s:%d", occurrenceID, attempts+1)
	return m.store.PrepareAttemptLease(ctx, occurrenceID, taskID, leaseDuration)
}

type attemptResultMetadata struct {
	Disposition execution.Disposition `json:"disposition,omitempty"`
	Code        string                `json:"code,omitempty"`
}

func encodeAttemptResultMetadata(res tasks.TaskResult) []byte {
	// Completed/cancelled attempts already have authoritative durable state and
	// never need failure disposition to decide a future redrive.
	if res.Outcome == tasks.OutcomeCompleted || res.Outcome == tasks.OutcomeCancelled {
		return nil
	}
	semantics := res.Semantics()
	if semantics.Disposition == "" {
		return nil
	}
	data, err := json.Marshal(attemptResultMetadata{Disposition: semantics.Disposition, Code: semantics.Code})
	if err != nil {
		return nil
	}
	return data
}

func persistedAttemptSemantics(attempt JobAttempt) (execution.Semantics, bool) {
	if len(attempt.Result) == 0 {
		return execution.Semantics{}, false
	}
	var metadata attemptResultMetadata
	if err := json.Unmarshal(attempt.Result, &metadata); err != nil || metadata.Disposition == "" {
		return execution.Semantics{}, false
	}
	return execution.Semantics{Disposition: metadata.Disposition, Code: metadata.Code}, true
}

func occurrenceStateForSemantics(semantics execution.Semantics) OccurrenceState {
	if semantics.Disposition == execution.DispositionCancelled {
		return OccurrenceCancelled
	}
	return OccurrenceFailed
}

func abortedTaskResult(taskID tasks.TaskID, err error) tasks.TaskResult {
	semantics := execution.SemanticsOf(err)
	return tasks.TaskResult{
		TaskID:      taskID,
		Outcome:     tasks.OutcomeAbortedBeforeStart,
		Cause:       tasks.CauseAdmissionRejected,
		Disposition: semantics.Disposition,
		FinishedAt:  time.Now().UTC(),
		Failure:     tasks.FailureInfo{Code: semantics.Code, Message: err.Error()},
	}
}

func (m *Manager) commitAttemptResult(ctx context.Context, attempt *JobAttempt, res tasks.TaskResult) error {
	if attempt == nil {
		return errors.New("job attempt is required for durable commit")
	}
	if res.Cause == tasks.CauseRateLimited {
		if res.FinishedAt.IsZero() {
			return errors.New("rate-limited task result is missing finished_at")
		}
		wait := res.RetryAfter
		if wait < 0 {
			wait = 0
		}
		return m.store.CommitAttemptDeferred(
			ctx,
			attempt.ID,
			attempt.LeaseEpoch,
			res.FinishedAt.UTC().Add(wait),
			res.Failure.Message,
		)
	}

	if err := m.store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, attemptState(res.Outcome), encodeAttemptResultMetadata(res), res.Failure.Message); err != nil {
		return err
	}

	// Completed/cancelled attempts atomically settle the occurrence in durable
	// storage. Wake the timing owner at that authoritative boundary instead of
	// waiting for the asynchronous retry monitor to observe the ticket and call
	// untrack(). The untrack wake remains a fallback for other terminal paths.
	if res.Outcome == tasks.OutcomeCompleted || res.Outcome == tasks.OutcomeCancelled {
		m.mu.RLock()
		tracked := m.tracked[attempt.OccurrenceID]
		wake := m.scheduleWake
		shouldWake := tracked != nil && tracked.timingOwned
		m.mu.RUnlock()
		if shouldWake && wake != nil {
			wake()
		}
	}
	return nil
}

func leaseDurationFor(def JobDefinition) time.Duration {
	leaseDuration := def.Timeout + time.Minute
	if leaseDuration < time.Minute {
		leaseDuration = time.Minute
	}
	return leaseDuration
}

// driveAttempt prepares the next attempt lease and submits its task, then re-arms the monitor.
func (m *Manager) driveAttempt(ctx context.Context, occurrenceID string, def JobDefinition, handler Handler, timingOwned bool) error {
	attempt, err := m.prepareNextAttemptLease(ctx, occurrenceID, leaseDurationFor(def))
	if err != nil {
		return err
	}
	nextTaskID := tasks.TaskID(attempt.TaskID)
	copyDef := cloneDefinition(def)
	commit := func(commitCtx context.Context, res tasks.TaskResult) error {
		return m.commitAttemptResult(commitCtx, attempt, res)
	}
	m.track(occurrenceID, copyDef, handler, nextTaskID, timingOwned)
	ticket, err := m.client.Submit(ctx, tasks.WorkSpec{
		ID:               nextTaskID,
		Scope:            tasks.ScopeIdentity{Owner: copyDef.ScopeOwner, Generation: uint64(copyDef.Version)},
		QuotaOwner:       tasks.OwnerID(copyDef.QuotaOwner),
		Pool:             tasks.PoolID(copyDef.Pool),
		Class:            tasks.PriorityClass(copyDef.Class),
		ExecutionTimeout: copyDef.Timeout,
		HandlerRef:       copyDef.HandlerType,
		Input:            append([]byte(nil), copyDef.Payload...),
		Resources:        definitionResources(copyDef),
		Job:              &tasks.OccurrenceRef{JobID: copyDef.ID, OccurrenceID: tasks.OccurrenceID(occurrenceID), AttemptID: tasks.AttemptID(attempt.ID), LeaseEpoch: attempt.LeaseEpoch},
		Handler: func(runCtx context.Context) error {
			return handler(runCtx, copyDef)
		},
		Commit: commit,
	})
	if err != nil {
		m.untrack(occurrenceID)
		abortResult := abortedTaskResult(nextTaskID, err)
		abortCtx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
		commitErr := m.store.CommitAttemptResult(abortCtx, attempt.ID, attempt.LeaseEpoch, AttemptAbortedBeforeStart, encodeAttemptResultMetadata(abortResult), err.Error())
		cancel()
		m.signalRecovery()
		if commitErr != nil {
			return fmt.Errorf("submit retry attempt: %w (persist abort: %v)", err, commitErr)
		}
		return err
	}
	m.enqueueRetry(retryItem{occurrenceID: occurrenceID, ticket: ticket})
	return nil
}

func definitionResources(def JobDefinition) []tasks.ResourceRequirement {
	return append([]tasks.ResourceRequirement(nil), def.Resources...)
}

// persistAttemptResult is used only when a lease was created but TaskEngine
// admission failed. Persist synchronously under a hard timeout: this path is
// already an error path, and bounded caller backpressure is preferable to an
// unbounded rescue goroutine or an occurrence left permanently dispatched.
func (m *Manager) persistAttemptResult(attempt *JobAttempt, result tasks.TaskResult) {
	if attempt == nil || m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
	err := m.store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, attemptState(result.Outcome), encodeAttemptResultMetadata(result), result.Failure.Message)
	cancel()
	// Whether commit succeeded or became uncertain, wake durable recovery. A
	// successful abort is immediately retryable; an uncertain one is revisited
	// by the periodic safety scan.
	_ = err
	m.signalRecovery()
}

func attemptState(outcome tasks.Outcome) AttemptState {
	switch outcome {
	case tasks.OutcomeCompleted:
		return AttemptCompleted
	case tasks.OutcomeTimedOut:
		return AttemptTimedOut
	case tasks.OutcomeCancelled:
		return AttemptCancelled
	case tasks.OutcomeAbortedBeforeStart:
		return AttemptAbortedBeforeStart
	default:
		return AttemptFailed
	}
}
