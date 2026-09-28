package jobs

import (
	"context"
	"time"
)

// signalRecovery coalesces arbitrarily many recovery hints into one bounded wake.
func (m *Manager) signalRecovery() {
	m.mu.RLock()
	wake := m.recoveryWake
	m.mu.RUnlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// durableCoordinatorLoop owns all durable Jobs maintenance timing:
// startup/restart recovery, deferred-occurrence deadlines, outbox replay, and
// low-frequency crash-safety scans. Explicit wake sources remain independent,
// but one timer/goroutine owns their fallback deadlines.
func (m *Manager) durableCoordinatorLoop(done chan struct{}) {
	defer m.workerDone(done)

	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	now := time.Now()
	nextRecoverySafety := now.Add(recoveryInterval)
	nextOutboxSafety := now.Add(outboxSafetyInterval)

	for {
		m.mu.RLock()
		stopCh := m.stopCh
		baseCtx := m.baseCtx
		recoveryWake := m.recoveryWake
		outboxWake := m.outboxWake
		_, hasOutbox := m.store.(outboxStore)
		m.mu.RUnlock()
		if stopCh == nil || baseCtx == nil || recoveryWake == nil || outboxWake == nil {
			return
		}

		nextWake := nextRecoverySafety
		if hasOutbox && nextOutboxSafety.Before(nextWake) {
			nextWake = nextOutboxSafety
		}

		// Deferred retry timing is durable state and may be earlier than either
		// safety scan. A read failure does not create a tight loop; recovery safety
		// remains the fallback authority.
		if store, ok := m.store.(deferredDeadlineStore); ok {
			queryCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			due, found, err := store.EarliestDeferredOccurrenceDue(queryCtx, time.Now().UTC())
			cancel()
			if err == nil && found && due.Before(nextWake) {
				nextWake = due
			}
		}

		wait := time.Until(nextWake)
		if wait < 0 {
			wait = 0
		}
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(wait)
		}
		timerC = timer.C

		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case <-recoveryWake:
			m.runRecoveryPass(baseCtx)
		case <-outboxWake:
			if hasOutbox {
				m.drainOutbox(baseCtx)
			}
		case fired := <-timerC:
			utcNow := fired.UTC()
			// A timer firing can represent the deferred occurrence deadline, one
			// or both safety deadlines, or all of them. Recovery is cheap enough to
			// be the convergence owner whenever the nearest durable deadline fires.
			m.runRecoveryPass(baseCtx)
			if !utcNow.Before(nextRecoverySafety.UTC()) {
				nextRecoverySafety = fired.Add(recoveryInterval)
			}
			if hasOutbox && !utcNow.Before(nextOutboxSafety.UTC()) {
				m.drainOutbox(baseCtx)
				nextOutboxSafety = fired.Add(outboxSafetyInterval)
			}
		}
	}
}

func (m *Manager) runRecoveryPass(baseCtx context.Context) {
	m.mu.RLock()
	accepting := m.accepting
	m.mu.RUnlock()
	if !accepting {
		return
	}
	ctx, cancel := context.WithTimeout(baseCtx, recoveryTimeout)
	defer cancel()
	_, _ = m.Recover(ctx, recoveryScanLimit)
}

func occurrenceTerminal(state OccurrenceState) bool {
	switch state {
	case OccurrenceCompleted, OccurrenceFailed, OccurrenceCancelled:
		return true
	default:
		return false
	}
}

// Recover scans unresolved occurrences and converges each one. Repeated calls converge; limit bounds each scan.
func (m *Manager) Recover(ctx context.Context, limit int) (RecoverReport, error) {
	var report RecoverReport

	var candidates []RecoveryCandidate
	if store, ok := m.store.(recoveryCandidateStore); ok {
		var err error
		candidates, err = store.ListRecoveryCandidates(ctx, limit)
		if err != nil {
			return report, err
		}
	} else {
		unresolved, err := m.store.ListUnresolvedOccurrences(ctx, limit)
		if err != nil {
			return report, err
		}
		candidates = make([]RecoveryCandidate, 0, len(unresolved))
		for _, occ := range unresolved {
			if occ == nil {
				continue
			}
			candidates = append(candidates, RecoveryCandidate{Occurrence: *occ})
		}
	}

	for i := range candidates {
		candidate := &candidates[i]
		occ := &candidate.Occurrence
		report.Scanned++
		if occ.ReadyAt.After(time.Now().UTC()) {
			// Occurrence is deferred (waiting for FloodWait / durable backoff).
			continue
		}
		m.mu.RLock()
		_, activelyTracked := m.tracked[occ.ID]
		def, found := m.definitions[occ.JobID]
		handler := m.handlers[def.HandlerType]
		m.mu.RUnlock()
		// A tracked occurrence already has exactly one retry worker responsible
		// for observing its current ticket and deciding whether to finalize or
		// create the next attempt. Recovery must never race that owner: doing so
		// can lease two sequential attempts from the same terminal predecessor
		// and exceed the retry budget.
		if activelyTracked {
			report.Stale++
			continue
		}
		if !found || !def.Enabled || handler == nil {
			report.Orphaned++
			continue
		}
		summary := candidate.Summary
		if summary == nil {
			var err error
			summary, err = m.loadAttemptSummary(ctx, occ.ID)
			if err != nil {
				report.Stale++
				continue
			}
		}
		if summary.OccurrenceState != OccurrenceDispatched {
			report.Stale++
			continue
		}
		latest := &summary.Latest
		switch latest.State {
		case AttemptCompleted, AttemptFailed, AttemptTimedOut, AttemptCancelled, AttemptAbortedBeforeStart, AttemptDeferred:
		default:
			report.Stale++
			continue
		}
		if latest.State != AttemptDeferred {
			if semantics, typed := persistedAttemptSemantics(*latest); typed && !semantics.ShouldRetry() && !semantics.IsSuccess() {
				if ferr := m.store.FinalizeOccurrence(ctx, occ.ID, occurrenceStateForSemantics(semantics)); ferr != nil {
					report.Stale++
					continue
				}
				m.untrackOrWakeTimingOccurrence(occ)
				report.Finalized++
				continue
			}
		}
		if latest.State == AttemptDeferred && summary.Deferrals >= maxDeferrals(def.RetryPolicy) {
			if ferr := m.store.FinalizeOccurrence(ctx, occ.ID, OccurrenceFailed); ferr != nil {
				report.Stale++
				continue
			}
			m.untrackOrWakeTimingOccurrence(occ)
			report.Finalized++
			continue
		}
		retryUses := summary.RetryBudgetUses
		if retryUses >= maxAttempts(def.RetryPolicy) {
			if ferr := m.store.FinalizeOccurrence(ctx, occ.ID, OccurrenceFailed); ferr != nil {
				report.Stale++
				continue
			}
			m.untrackOrWakeTimingOccurrence(occ)
			report.Finalized++
			continue
		}
		if derr := m.driveAttempt(ctx, occ.ID, def, handler, timingOwnedOccurrence(occ)); derr != nil {
			report.Stale++
			continue
		}
		report.Redriven++
	}
	return report, nil
}
