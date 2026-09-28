package jobs

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func (m *Manager) ensureRetryWorkersLocked() {
	m.mu.RLock()
	queue := m.retryQueue
	stopCh := m.stopCh
	baseCtx := m.baseCtx
	done := m.done
	m.mu.RUnlock()
	if m.retryStopping || queue == nil || stopCh == nil || baseCtx == nil || done == nil || baseCtx.Err() != nil {
		return
	}
	select {
	case <-stopCh:
		return
	default:
	}

	target := int(m.retryActive.Load() + m.retryQueued.Load())
	if target < 1 {
		target = 1
	}
	if target > retryWorkers {
		target = retryWorkers
	}
	running := int(m.retryRemaining.Load())
	for running < target {
		m.retryRemaining.Add(1)
		m.workersRemaining.Add(1)
		running++
		m.wg.Add(1)
		go m.retryLoop(done)
	}
}

func (m *Manager) retryWorkerDone(done chan struct{}) {
	m.wg.Done()
	m.retryMu.Lock()
	m.retryRemaining.Add(-1)
	if !m.retryStopping && m.retryQueued.Load() > 0 {
		m.ensureRetryWorkersLocked()
	}
	m.retryMu.Unlock()
	if m.workersRemaining.Add(-1) == 0 {
		m.doneOnce.Do(func() { close(done) })
	}
}

// enqueueRetry hands an attempt to the bounded monitor pool. Queue saturation
// is never silent: durability remains authoritative and the recovery loop is
// woken to converge the occurrence once the attempt becomes terminal.
func (m *Manager) enqueueRetry(item retryItem) {
	m.retryMu.Lock()
	if m.retryStopping {
		m.retryMu.Unlock()
		m.signalRecovery()
		return
	}
	m.mu.RLock()
	queue := m.retryQueue
	m.mu.RUnlock()
	if queue == nil {
		m.retryMu.Unlock()
		m.signalRecovery()
		return
	}
	m.retryQueued.Add(1)
	select {
	case queue <- item:
		m.ensureRetryWorkersLocked()
		m.retryMu.Unlock()
	default:
		m.retryQueued.Add(-1)
		m.retryMu.Unlock()
		m.signalRecovery()
	}
}

func (m *Manager) retryLoop(done chan struct{}) {
	defer m.retryWorkerDone(done)

	m.retryMu.Lock()
	idleTimeout := m.retryIdleTimeout
	m.retryMu.Unlock()
	if idleTimeout <= 0 {
		idleTimeout = retryIdleTimeout
	}
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()

	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idleTimeout)
	}

	for {
		m.mu.RLock()
		queue := m.retryQueue
		stopCh := m.stopCh
		baseCtx := m.baseCtx
		m.mu.RUnlock()
		if queue == nil || stopCh == nil || baseCtx == nil {
			return
		}
		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case item := <-queue:
			m.retryActive.Add(1)
			m.retryQueued.Add(-1)
			m.watchAttempt(baseCtx, item)
			m.retryActive.Add(-1)
			resetTimer()
		case <-timer.C:
			if m.retryQueued.Load() == 0 {
				return
			}
			timer.Reset(idleTimeout)
		}
	}
}

func maxAttempts(policy JobRetryPolicy) int {
	if policy.MaxAttempts <= 0 {
		return 1
	}
	return policy.MaxAttempts
}

// maxDeferrals is intentionally derived from the existing execution budget
// when not configured explicitly. This keeps legacy definitions bounded
// without freezing a new tuning constant before the occupancy benchmarks.
// The +1 preserves one durable redrive even for MaxAttempts=1.
func maxDeferrals(policy JobRetryPolicy) int {
	if policy.MaxDeferrals > 0 {
		return policy.MaxDeferrals
	}
	return maxAttempts(policy) + 1
}

func retryDelay(policy JobRetryPolicy, attemptsMade int) time.Duration {
	if policy.InitialDelay <= 0 {
		return 0
	}
	mult := policy.BackoffMultiplier
	if mult <= 0 {
		mult = 1
	}
	delay := float64(policy.InitialDelay)
	for i := 1; i < attemptsMade; i++ {
		delay *= mult
	}
	if policy.MaxDelay > 0 && delay > float64(policy.MaxDelay) {
		delay = float64(policy.MaxDelay)
	}
	return time.Duration(delay)
}

// watchAttempt waits for one attempt's ticket and drives the retry protocol.
func (m *Manager) watchAttempt(baseCtx context.Context, item retryItem) {
	m.mu.RLock()
	stopCh := m.stopCh
	tr, tracked := m.tracked[item.occurrenceID]
	m.mu.RUnlock()
	if !tracked {
		return
	}
	res, werr := item.ticket.Wait(baseCtx)
	if werr != nil {
		return // Manager is stopping.
	}
	select {
	case <-stopCh:
		return
	default:
	}
	if res.Cause == tasks.CausePersistenceFailure {
		// TaskEngine could not prove the durable acknowledgement. Never turn an
		// uncertain physical outcome into a new retry/finalization decision; the
		// store and recovery protocol remain authoritative.
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	}
	if res.Outcome == tasks.OutcomeCompleted {
		// Durable tickets resolve successfully only after CommitAttemptResult
		// acknowledgement, so re-reading job_occurrences here is redundant.
		m.untrack(item.occurrenceID)
		return
	}

	stateCtx, stateCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
	summary, err := m.loadAttemptSummary(stateCtx, item.occurrenceID)
	stateCancel()
	if err != nil {
		// The monitor cannot make a safe retry decision without durable state.
		// Drop in-memory ownership so recovery can become authoritative.
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	}
	if occurrenceTerminal(summary.OccurrenceState) {
		m.untrack(item.occurrenceID)
		return
	}

	semantics := res.Semantics()
	if !semantics.ShouldRetry() && !semantics.IsSuccess() {
		finalizeCtx, finalizeCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
		err := m.stores.Occurrences.FinalizeOccurrence(finalizeCtx, item.occurrenceID, occurrenceStateForSemantics(semantics))
		finalizeCancel()
		if err != nil {
			m.signalRecovery()
			return
		}
		m.untrack(item.occurrenceID)
		return
	}

	switch res.Outcome {
	case tasks.OutcomeFailed, tasks.OutcomeTimedOut, tasks.OutcomeCancelled,
		tasks.OutcomePanic, tasks.OutcomeAbortedBeforeStart:
		// Retryable physical outcomes.
	default:
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	}
	if res.Cause == tasks.CauseRateLimited {
		if summary.Deferrals >= maxDeferrals(tr.def.RetryPolicy) {
			finalizeCtx, finalizeCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
			err := m.stores.Occurrences.FinalizeOccurrence(finalizeCtx, item.occurrenceID, OccurrenceFailed)
			finalizeCancel()
			if err != nil {
				m.signalRecovery()
				return
			}
			m.untrack(item.occurrenceID)
			return
		}
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	}

	retryUses := summary.RetryBudgetUses
	if retryUses >= maxAttempts(tr.def.RetryPolicy) {
		finalizeCtx, finalizeCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
		err := m.stores.Occurrences.FinalizeOccurrence(finalizeCtx, item.occurrenceID, OccurrenceFailed)
		finalizeCancel()
		if err != nil {
			m.signalRecovery()
			return
		}
		m.untrack(item.occurrenceID)
		return
	}

	delay := retryDelay(tr.def.RetryPolicy, retryUses)
	if delay > 0 {
		deferUntil := time.Now().UTC().Add(delay)
		deferCtx, deferCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
		err := m.stores.Occurrences.DeferOccurrence(deferCtx, item.occurrenceID, deferUntil)
		deferCancel()
		if err != nil {
			m.signalRecovery()
			m.untrack(item.occurrenceID)
			return
		}
		m.untrack(item.occurrenceID)
		// Every positive backoff is durable timing state. The single recovery
		// coordinator owns the nearest-deadline timer, so retry workers never
		// sleep while waiting for a per-occurrence delay.
		m.signalRecovery()
		return
	}

	// Zero-delay retries may continue immediately. PrepareNextAttemptLease is
	// the final writer-fenced cancellation/state check, so a second occurrence
	// read here only adds a race window and one DB round-trip.
	m.mu.RLock()
	accepting := m.accepting
	m.mu.RUnlock()
	if !accepting {
		return
	}
	driveCtx, driveCancel := context.WithTimeout(m.rootContext(), 10*time.Second)
	err = m.driveAttempt(driveCtx, item.occurrenceID, tr.def, tr.handler, tr.timingOwned)
	driveCancel()
	if err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
	}
}
