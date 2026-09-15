package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// Handler resolves a versioned job definition at execution time. It receives a
// value copy, never a mutable manager record.
type Handler func(context.Context, JobDefinition) error

// Store is the durable boundary for a job definition and each of its occurrences.
type Store interface {
	SaveDefinition(context.Context, *JobDefinition) error
	UpdateDefinitionCAS(context.Context, *JobDefinition, uint64) error
	MaterializeOccurrence(context.Context, *JobOccurrence) error
	PrepareAttemptLease(context.Context, string, string, time.Duration) (*JobAttempt, error)
	CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error
	FinalizeOccurrence(context.Context, string, OccurrenceState) error
	CancelOccurrence(context.Context, string, string) error
	GetOccurrence(context.Context, string) (*JobOccurrence, error)
	GetOccurrenceByKey(context.Context, string) (*JobOccurrence, error)
	CountAttempts(context.Context, string) (int, error)
	LatestAttempt(context.Context, string) (*JobAttempt, error)
	ListUnresolvedOccurrences(context.Context, int) ([]*JobOccurrence, error)
	DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error)
}

// Manager owns definitions and creates a distinct occurrence and TaskID for
// every trigger. Physical execution is exclusively delegated to TaskEngine.
type Manager struct {
	mu          sync.RWMutex
	client      tasks.Client
	store       Store
	pump        *PersistencePump
	definitions map[string]JobDefinition
	handlers    map[string]Handler
	sequence    atomic.Uint64
	accepting   bool

	retryQueue   chan retryItem
	recoveryWake chan struct{}
	stopCh       chan struct{}
	stopOnce     sync.Once
	baseCtx      context.Context
	baseCancel   context.CancelFunc
	wg           sync.WaitGroup
	tracked      map[string]*trackedOccurrence
}

// retryItem watches one submitted attempt for retry/recovery decisions.
type retryItem struct {
	occurrenceID string
	ticket       tasks.Ticket
}

// trackedOccurrence remembers the latest attempt driver of an occurrence.
type trackedOccurrence struct {
	def     JobDefinition
	handler Handler
	taskID  tasks.TaskID
}

const (
	retryQueueCap = 256
	retryWorkers  = 4

	// Automatic recovery is deliberately low-frequency as a safety scan; fast
	// convergence comes from bounded wake signals emitted on monitor overflow or
	// uncertain retry-driver errors.
	recoveryScanLimit = 256
	recoveryInterval  = 30 * time.Second
	recoveryTimeout   = 20 * time.Second
)

// RecoverReport summarizes one recovery scan over unresolved occurrences.
type RecoverReport struct {
	Scanned   int
	Redriven  int
	Finalized int
	Stale     int
	Orphaned  int
}

// Diagnostics is a read-only count of declarative job registrations.
type Diagnostics struct {
	Definitions int
	Handlers    int
	Accepting   bool
}

var _ runtime.Component = (*Manager)(nil)

func NewManager(client tasks.Client, store Store, pump *PersistencePump) *Manager {
	return &Manager{
		client: client, store: store, pump: pump,
		definitions: make(map[string]JobDefinition),
		handlers:    make(map[string]Handler),
		tracked:     make(map[string]*trackedOccurrence),
	}
}

func (m *Manager) Name() string           { return "jobs" }
func (m *Manager) Dependencies() []string { return []string{"taskengine"} }

func (m *Manager) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil || m.store == nil || m.pump == nil {
		return errors.New("jobs requires task client, durable store, and persistence pump")
	}
	firstStart := m.retryQueue == nil
	if m.retryQueue == nil {
		m.retryQueue = make(chan retryItem, retryQueueCap)
	}
	if m.recoveryWake == nil {
		m.recoveryWake = make(chan struct{}, 1)
	}
	if m.stopCh == nil {
		m.stopCh = make(chan struct{})
	}
	if m.baseCtx == nil {
		m.baseCtx, m.baseCancel = context.WithCancel(context.Background())
	}
	if m.tracked == nil {
		m.tracked = make(map[string]*trackedOccurrence)
	}
	m.accepting = true
	if firstStart {
		for i := 0; i < retryWorkers; i++ {
			m.wg.Add(1)
			go m.retryLoop()
		}
		m.wg.Add(1)
		go m.recoveryLoop()
		// Startup recovery is a bounded wake, not a caller responsibility.
		select {
		case m.recoveryWake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *Manager) Quiesce(context.Context) error {
	m.mu.Lock()
	m.accepting = false
	m.mu.Unlock()
	return nil
}

func (m *Manager) Drain(context.Context) error { return nil }

func (m *Manager) Stop(ctx context.Context) error {
	_ = m.Quiesce(context.Background())
	m.stopOnce.Do(func() {
		m.mu.RLock()
		stopCh := m.stopCh
		cancel := m.baseCancel
		m.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if stopCh != nil {
			close(stopCh)
		}
	})
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Health(context.Context) runtime.ComponentHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.accepting {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "job admission is closed"}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

func (m *Manager) RegisterHandler(handlerType string, handler Handler) error {
	if handlerType == "" || handler == nil {
		return errors.New("job handler type and handler are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.handlers[handlerType]; exists {
		return fmt.Errorf("job handler already registered: %s", handlerType)
	}
	m.handlers[handlerType] = handler
	return nil
}

func (m *Manager) Register(def JobDefinition) error {
	if def.ID == "" || def.ScopeOwner == "" || def.QuotaOwner == "" || def.HandlerType == "" {
		return errors.New("job definition id, scope owner, quota owner, and handler type are required")
	}
	if def.Pool == "" {
		def.Pool = "general"
	}
	if def.Class == "" {
		def.Class = string(tasks.PriorityNormal)
	}
	if def.Version <= 0 {
		def.Version = 1
	}
	if !def.Enabled {
		def.Enabled = true
	}
	def.Payload = append([]byte(nil), def.Payload...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.definitions[def.ID]; exists {
		return fmt.Errorf("job definition already registered: %s", def.ID)
	}
	if _, exists := m.handlers[def.HandlerType]; !exists {
		return fmt.Errorf("unknown job handler: %s", def.HandlerType)
	}
	if err := m.store.SaveDefinition(context.Background(), &def); err != nil {
		return fmt.Errorf("save job definition: %w", err)
	}
	m.definitions[def.ID] = def
	return nil
}

// Trigger returns after admission. Completion belongs to the occurrence ticket.
func (m *Manager) Trigger(ctx context.Context, jobID string) error {
	_, _, err := m.SubmitOccurrence(ctx, jobID, "")
	return err
}

// TryTrigger is retained as an admission-only spelling for timer producers.
func (m *Manager) TryTrigger(ctx context.Context, jobID string) error { return m.Trigger(ctx, jobID) }

// CancelByOwner fences queued work through the scope identity and closes the
// durable record of every tracked occurrence under that owner.
func (m *Manager) CancelByOwner(owner string) int {
	m.mu.RLock()
	client := m.client
	store := m.store
	definitions := make([]JobDefinition, 0, len(m.definitions))
	for _, definition := range m.definitions {
		definitions = append(definitions, definition)
	}
	type pendingCancel struct {
		occurrenceID string
		scopeOwner   string
	}
	var tracked []pendingCancel
	for occID, tr := range m.tracked {
		tracked = append(tracked, pendingCancel{occurrenceID: occID, scopeOwner: tr.def.ScopeOwner})
	}
	m.mu.RUnlock()
	cancelled := 0
	for _, definition := range definitions {
		if definition.ScopeOwner == owner || definition.ScopeOwner == "plugin:"+owner {
			cancelled += client.CancelScope(tasks.ScopeIdentity{Owner: definition.ScopeOwner, Generation: uint64(definition.Version)}, tasks.CauseScopeClosed)
		}
	}
	for _, tr := range tracked {
		if tr.scopeOwner == owner || tr.scopeOwner == "plugin:"+owner {
			if err := store.CancelOccurrence(context.Background(), tr.occurrenceID, "owner cancelled"); err == nil {
				cancelled++
			}
			m.untrack(tr.occurrenceID)
		}
	}
	return cancelled
}

// CancelOccurrence durably cancels one occurrence and requests cancellation of its latest task.
func (m *Manager) CancelOccurrence(ctx context.Context, occurrenceID, reason string) error {
	m.mu.RLock()
	var taskID tasks.TaskID
	if tr, ok := m.tracked[occurrenceID]; ok {
		taskID = tr.taskID
	}
	m.mu.RUnlock()
	if err := m.store.CancelOccurrence(ctx, occurrenceID, reason); err != nil {
		return err
	}
	if taskID != "" {
		_, _ = m.client.Cancel(taskID, tasks.CauseUserCancel)
	}
	m.untrack(occurrenceID)
	return nil
}

func (m *Manager) untrack(occurrenceID string) {
	m.mu.Lock()
	delete(m.tracked, occurrenceID)
	m.mu.Unlock()
}

func (m *Manager) SubmitOccurrence(ctx context.Context, jobID, occurrenceKey string) (tasks.Ticket, string, error) {
	m.mu.RLock()
	if !m.accepting {
		m.mu.RUnlock()
		return nil, "", errors.New("job admission is closed")
	}
	definition, found := m.definitions[jobID]
	handler := m.handlers[definition.HandlerType]
	client := m.client
	m.mu.RUnlock()
	if !found {
		return nil, "", fmt.Errorf("job definition not found: %s", jobID)
	}
	if handler == nil {
		return nil, "", fmt.Errorf("unknown job handler: %s", definition.HandlerType)
	}
	if !definition.Enabled {
		return nil, "", fmt.Errorf("job definition is disabled: %s", jobID)
	}
	sequence := m.sequence.Add(1)
	if occurrenceKey == "" {
		occurrenceKey = fmt.Sprintf("manual:%s:%d", jobID, sequence)
	}
	now := time.Now().UTC()
	occurrenceID := tasks.OccurrenceID(fmt.Sprintf("occ:%s:%d:%d", jobID, now.UnixNano(), sequence))
	taskID := tasks.TaskID(fmt.Sprintf("task:%s:1", occurrenceID))
	occurrence := &JobOccurrence{ID: string(occurrenceID), JobID: jobID, OccurrenceKey: occurrenceKey, ScheduledFor: now, ReadyAt: now, State: OccurrenceReady}
	if err := m.store.MaterializeOccurrence(ctx, occurrence); err != nil {
		return nil, "", fmt.Errorf("materialize job occurrence: %w", err)
	}
	// Materialization is idempotent on occurrence_key and rewrites occurrence.ID
	// to the canonical identity when this logical run already exists.
	occurrenceID = tasks.OccurrenceID(occurrence.ID)
	taskID = tasks.TaskID(fmt.Sprintf("task:%s:1", occurrenceID))
	attempt, err := m.store.PrepareAttemptLease(ctx, occurrence.ID, string(taskID), leaseDurationFor(definition))
	if err != nil {
		return nil, "", fmt.Errorf("prepare job attempt: %w", err)
	}
	copyDef := definition
	copyDef.Payload = append([]byte(nil), definition.Payload...)
	ticket, err := client.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            tasks.ScopeIdentity{Owner: copyDef.ScopeOwner, Generation: uint64(copyDef.Version)},
		QuotaOwner:       tasks.OwnerID(copyDef.QuotaOwner),
		Pool:             tasks.PoolID(copyDef.Pool),
		Class:            tasks.PriorityClass(copyDef.Class),
		ExecutionTimeout: copyDef.Timeout,
		HandlerRef:       copyDef.HandlerType,
		Input:            append([]byte(nil), copyDef.Payload...),
		Job:              &tasks.OccurrenceRef{JobID: copyDef.ID, OccurrenceID: occurrenceID, AttemptID: tasks.AttemptID(attempt.ID), LeaseEpoch: attempt.LeaseEpoch},
		Handler:          func(runCtx context.Context) error { return handler(runCtx, copyDef) },
		Commit: func(commitCtx context.Context, res tasks.TaskResult) error {
			return m.store.CommitAttemptResult(commitCtx, attempt.ID, attempt.LeaseEpoch, attemptState(res.Outcome), nil, res.Failure.Message)
		},
	})
	if err != nil {
		m.persistAttemptResult(attempt, tasks.TaskResult{
			TaskID: taskID, Outcome: tasks.OutcomeAbortedBeforeStart,
			Cause: tasks.CausePersistenceFailure, FinishedAt: time.Now().UTC(),
			Failure: tasks.FailureInfo{Message: err.Error()},
		})
		return nil, "", err
	}
	m.track(occurrence.ID, copyDef, handler, taskID)
	m.enqueueRetry(retryItem{occurrenceID: occurrence.ID, ticket: ticket})
	return ticket, occurrence.ID, nil
}

func (m *Manager) GetOccurrence(ctx context.Context, occurrenceID string) (*JobOccurrence, error) {
	return m.store.GetOccurrence(ctx, occurrenceID)
}
func (m *Manager) OccurrenceByKey(ctx context.Context, occurrenceKey string) (*JobOccurrence, error) {
	return m.store.GetOccurrenceByKey(ctx, occurrenceKey)
}
func (m *Manager) LatestAttempt(ctx context.Context, occurrenceID string) (*JobAttempt, error) {
	return m.store.LatestAttempt(ctx, occurrenceID)
}

func (m *Manager) UpdateDefinition(ctx context.Context, def JobDefinition) error {
	if def.ID == "" {
		return errors.New("job definition id is required")
	}
	m.mu.RLock()
	current, found := m.definitions[def.ID]
	m.mu.RUnlock()
	if !found {
		return fmt.Errorf("job definition not found: %s", def.ID)
	}
	def.Revision = current.Revision
	if err := m.store.UpdateDefinitionCAS(ctx, &def, current.Revision); err != nil {
		return err
	}
	m.mu.Lock()
	m.definitions[def.ID] = def
	m.mu.Unlock()
	return nil
}

func (m *Manager) PruneOccurrences(ctx context.Context, jobID string, before time.Time, limit int) (int64, error) {
	return m.store.DeleteTerminalOccurrences(ctx, jobID, before, limit)
}

func (m *Manager) track(occurrenceID string, def JobDefinition, handler Handler, taskID tasks.TaskID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tracked == nil {
		m.tracked = make(map[string]*trackedOccurrence)
	}
	m.tracked[occurrenceID] = &trackedOccurrence{def: def, handler: handler, taskID: taskID}
}

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

// enqueueRetry hands an attempt to the bounded monitor pool. Queue saturation
// is never silent: durability remains authoritative and the recovery loop is
// woken to converge the occurrence once the attempt becomes terminal.
func (m *Manager) enqueueRetry(item retryItem) {
	m.mu.RLock()
	queue := m.retryQueue
	m.mu.RUnlock()
	if queue == nil {
		m.signalRecovery()
		return
	}
	select {
	case queue <- item:
	default:
		m.signalRecovery()
	}
}

func (m *Manager) retryLoop() {
	defer m.wg.Done()
	for {
		m.mu.RLock()
		queue := m.retryQueue
		stopCh := m.stopCh
		baseCtx := m.baseCtx
		m.mu.RUnlock()
		if queue == nil || stopCh == nil {
			return
		}
		select {
		case <-stopCh:
			return
		case item := <-queue:
			m.watchAttempt(baseCtx, item)
		}
	}
}

// recoveryLoop owns startup/restart convergence and provides a low-frequency
// safety scan. Overflow/error paths only wake this one bounded goroutine.
func (m *Manager) recoveryLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	for {
		m.mu.RLock()
		stopCh := m.stopCh
		baseCtx := m.baseCtx
		wake := m.recoveryWake
		m.mu.RUnlock()
		if stopCh == nil || baseCtx == nil || wake == nil {
			return
		}
		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case <-wake:
			m.runRecoveryPass(baseCtx)
		case <-ticker.C:
			m.runRecoveryPass(baseCtx)
		}
	}
}

func (m *Manager) runRecoveryPass(baseCtx context.Context) {
	ctx, cancel := context.WithTimeout(baseCtx, recoveryTimeout)
	defer cancel()
	_, _ = m.Recover(ctx, recoveryScanLimit)
}

func maxAttempts(policy JobRetryPolicy) int {
	if policy.MaxAttempts <= 0 {
		return 1
	}
	return policy.MaxAttempts
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

func leaseDurationFor(def JobDefinition) time.Duration {
	leaseDuration := def.Timeout + time.Minute
	if leaseDuration < time.Minute {
		leaseDuration = time.Minute
	}
	return leaseDuration
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	occ, err := m.store.GetOccurrence(ctx, item.occurrenceID)
	if err == nil {
		switch occ.State {
		case OccurrenceCancelled, OccurrenceCompleted, OccurrenceFailed:
			m.untrack(item.occurrenceID)
			return
		}
	} else {
		m.signalRecovery()
	}
	if res.Outcome == tasks.OutcomeCompleted {
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
	attempts, err := m.store.CountAttempts(ctx, item.occurrenceID)
	if err != nil {
		m.signalRecovery()
		return
	}
	if attempts >= maxAttempts(tr.def.RetryPolicy) {
		if err := m.store.FinalizeOccurrence(ctx, item.occurrenceID, OccurrenceFailed); err != nil {
			m.signalRecovery()
			return
		}
		m.untrack(item.occurrenceID)
		return
	}
	if delay := retryDelay(tr.def.RetryPolicy, attempts); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case <-timer.C:
		}
	}
	// Re-read after backoff: cancellation wins over retry.
	if occ2, err := m.store.GetOccurrence(ctx, item.occurrenceID); err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	} else if occ2.State == OccurrenceCancelled {
		m.untrack(item.occurrenceID)
		return
	}
	if err := m.driveAttempt(ctx, item.occurrenceID, tr.def, tr.handler); err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
	}
}

// driveAttempt prepares the next attempt lease and submits its task, then re-arms the monitor.
func (m *Manager) driveAttempt(ctx context.Context, occurrenceID string, def JobDefinition, handler Handler) error {
	attempts, err := m.store.CountAttempts(ctx, occurrenceID)
	if err != nil {
		return err
	}
	nextTaskID := tasks.TaskID(fmt.Sprintf("task:%s:%d", occurrenceID, attempts+1))
	attempt, err := m.store.PrepareAttemptLease(ctx, occurrenceID, string(nextTaskID), leaseDurationFor(def))
	if err != nil {
		return err
	}
	copyDef := def
	copyDef.Payload = append([]byte(nil), def.Payload...)
	commit := func(commitCtx context.Context, res tasks.TaskResult) error {
		return m.store.CommitAttemptResult(commitCtx, attempt.ID, attempt.LeaseEpoch, attemptState(res.Outcome), nil, res.Failure.Message)
	}
	ticket, err := m.client.Submit(ctx, tasks.WorkSpec{
		ID:               nextTaskID,
		Scope:            tasks.ScopeIdentity{Owner: copyDef.ScopeOwner, Generation: uint64(copyDef.Version)},
		QuotaOwner:       tasks.OwnerID(copyDef.QuotaOwner),
		Pool:             tasks.PoolID(copyDef.Pool),
		Class:            tasks.PriorityClass(copyDef.Class),
		ExecutionTimeout: copyDef.Timeout,
		HandlerRef:       copyDef.HandlerType,
		Input:            append([]byte(nil), copyDef.Payload...),
		Job:              &tasks.OccurrenceRef{JobID: copyDef.ID, OccurrenceID: tasks.OccurrenceID(occurrenceID), AttemptID: tasks.AttemptID(attempt.ID), LeaseEpoch: attempt.LeaseEpoch},
		Handler:          func(runCtx context.Context) error { return handler(runCtx, copyDef) },
		Commit:           commit,
	})
	if err != nil {
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		commitErr := m.store.CommitAttemptResult(abortCtx, attempt.ID, attempt.LeaseEpoch, AttemptAbortedBeforeStart, nil, err.Error())
		cancel()
		m.signalRecovery()
		if commitErr != nil {
			return fmt.Errorf("submit retry attempt: %w (persist abort: %v)", err, commitErr)
		}
		return err
	}
	m.track(occurrenceID, copyDef, handler, nextTaskID)
	m.enqueueRetry(retryItem{occurrenceID: occurrenceID, ticket: ticket})
	return nil
}

// Recover scans unresolved occurrences and converges each one. Repeated calls converge; limit bounds each scan.
func (m *Manager) Recover(ctx context.Context, limit int) (RecoverReport, error) {
	var report RecoverReport
	unresolved, err := m.store.ListUnresolvedOccurrences(ctx, limit)
	if err != nil {
		return report, err
	}
	for _, occ := range unresolved {
		report.Scanned++
		m.mu.RLock()
		def, found := m.definitions[occ.JobID]
		handler := m.handlers[def.HandlerType]
		m.mu.RUnlock()
		if !found || !def.Enabled || handler == nil {
			report.Orphaned++
			continue
		}
		latest, err := m.store.LatestAttempt(ctx, occ.ID)
		if err != nil {
			report.Stale++
			continue
		}
		switch latest.State {
		case AttemptCompleted, AttemptFailed, AttemptTimedOut, AttemptCancelled, AttemptAbortedBeforeStart:
		default:
			report.Stale++
			continue
		}
		attempts, err := m.store.CountAttempts(ctx, occ.ID)
		if err != nil {
			report.Stale++
			continue
		}
		if attempts >= maxAttempts(def.RetryPolicy) {
			if ferr := m.store.FinalizeOccurrence(ctx, occ.ID, OccurrenceFailed); ferr != nil {
				report.Stale++
				continue
			}
			m.untrack(occ.ID)
			report.Finalized++
			continue
		}
		if derr := m.driveAttempt(ctx, occ.ID, def, handler); derr != nil {
			report.Stale++
			continue
		}
		report.Redriven++
	}
	return report, nil
}

// persistAttemptResult is used only when a lease was created but TaskEngine
// admission failed. Persist synchronously under a hard timeout: this path is
// already an error path, and bounded caller backpressure is preferable to an
// unbounded rescue goroutine or an occurrence left permanently dispatched.
func (m *Manager) persistAttemptResult(attempt *JobAttempt, result tasks.TaskResult) {
	if attempt == nil || m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := m.store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, attemptState(result.Outcome), nil, result.Failure.Message)
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

func (m *Manager) Definition(id string) (JobDefinition, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	def, ok := m.definitions[id]
	def.Payload = append([]byte(nil), def.Payload...)
	return def, ok
}

func (m *Manager) Diagnostics() Diagnostics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Diagnostics{Definitions: len(m.definitions), Handlers: len(m.handlers), Accepting: m.accepting}
}
