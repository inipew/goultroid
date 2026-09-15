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
	mu             sync.RWMutex
	registrationMu sync.Mutex
	client         tasks.Client
	store          Store
	pump           *PersistencePump
	definitions    map[string]JobDefinition
	handlers       map[string]Handler
	sequence       atomic.Uint64
	accepting      bool

	retryQueue       chan retryItem
	recoveryWake     chan struct{}
	stopCh           chan struct{}
	stopOnce         sync.Once
	baseCtx          context.Context
	baseCancel       context.CancelFunc
	wg               sync.WaitGroup
	done             chan struct{}
	tracked          map[string]*trackedOccurrence
	operationTimeout time.Duration
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

// attemptBinding bridges post-permit prepare and post-execution commit without
// exposing mutable persistence state through WorkSpec. The prepare operation
// resolves it exactly once, including when the worker stops waiting before the
// persistence pump finishes. Commit can therefore distinguish "no attempt was
// ever created" from an acknowledgement whose durable effect is uncertain.
type attemptBinding struct {
	once    sync.Once
	ready   chan struct{}
	mu      sync.RWMutex
	attempt *JobAttempt
	err     error
}

func newAttemptBinding() *attemptBinding {
	return &attemptBinding{ready: make(chan struct{})}
}

func (b *attemptBinding) resolve(attempt *JobAttempt, err error) {
	if b == nil {
		return
	}
	b.once.Do(func() {
		b.mu.Lock()
		b.attempt = attempt
		b.err = err
		b.mu.Unlock()
		close(b.ready)
	})
}

func (b *attemptBinding) wait(ctx context.Context) (*JobAttempt, error) {
	if b == nil {
		return nil, errors.New("durable attempt binding is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-b.ready:
		b.mu.RLock()
		defer b.mu.RUnlock()
		return b.attempt, b.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const (
	retryQueueCap = 256
	retryWorkers  = 4

	// Automatic recovery is deliberately low-frequency as a safety scan; fast
	// convergence comes from bounded wake signals emitted on monitor overflow or
	// uncertain retry-driver errors.
	recoveryScanLimit       = 256
	recoveryInterval        = 30 * time.Second
	recoveryTimeout         = 20 * time.Second
	defaultOperationTimeout = 10 * time.Second
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

var (
	_ runtime.Component     = (*Manager)(nil)
	_ runtime.ForcedStopper = (*Manager)(nil)
)

func NewManager(client tasks.Client, store Store, pump *PersistencePump) *Manager {
	return &Manager{
		client:           client,
		store:            store,
		pump:             pump,
		definitions:      make(map[string]JobDefinition),
		handlers:         make(map[string]Handler),
		tracked:          make(map[string]*trackedOccurrence),
		operationTimeout: defaultOperationTimeout,
	}
}

func (m *Manager) Name() string           { return "jobs" }
func (m *Manager) Dependencies() []string { return []string{"taskengine"} }

func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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
		m.baseCtx, m.baseCancel = context.WithCancel(ctx)
	}
	if m.done == nil {
		m.done = make(chan struct{})
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
		done := m.done
		go func() {
			m.wg.Wait()
			close(done)
		}()
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

func (m *Manager) beginStop() {
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
}

func (m *Manager) ForceStop(context.Context) error {
	m.beginStop()
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.beginStop()
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
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
	if _, exists := m.handlers[handlerType]; exists {
		m.mu.Unlock()
		return fmt.Errorf("job handler already registered: %s", handlerType)
	}
	m.handlers[handlerType] = handler
	m.mu.Unlock()
	m.signalRecovery()
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

	// Serialize duplicate definition creation without holding m.mu across
	// storage I/O. ForceStop/Quiesce must always be able to acquire lifecycle
	// state even if a backend stalls while persisting a definition.
	m.registrationMu.Lock()
	defer m.registrationMu.Unlock()
	m.mu.RLock()
	_, exists := m.definitions[def.ID]
	_, handlerExists := m.handlers[def.HandlerType]
	m.mu.RUnlock()
	if exists {
		return fmt.Errorf("job definition already registered: %s", def.ID)
	}
	if !handlerExists {
		return fmt.Errorf("unknown job handler: %s", def.HandlerType)
	}
	regCtx, cancel := m.operationContext()
	err := m.store.SaveDefinition(regCtx, &def)
	cancel()
	if err != nil {
		return fmt.Errorf("save job definition: %w", err)
	}
	m.mu.Lock()
	m.definitions[def.ID] = def
	m.mu.Unlock()
	m.signalRecovery()
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
		if tr.scopeOwner != owner && tr.scopeOwner != "plugin:"+owner {
			continue
		}
		// Give every durable cancellation its own bounded operation context. A
		// large owner cannot let one slow row expire the shared context for all
		// subsequent rows. More importantly, do not forget an occurrence when
		// durable cancellation failed: recovery still owns that record.
		cancelCtx, cancel := m.operationContext()
		err := store.CancelOccurrence(cancelCtx, tr.occurrenceID, "owner cancelled")
		cancel()
		if err != nil {
			m.signalRecovery()
			continue
		}
		cancelled++
		m.untrack(tr.occurrenceID)
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

func (m *Manager) rootContext() context.Context {
	m.mu.RLock()
	ctx := m.baseCtx
	m.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (m *Manager) operationContext() (context.Context, context.CancelFunc) {
	m.mu.RLock()
	timeout := m.operationTimeout
	m.mu.RUnlock()
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	return context.WithTimeout(m.rootContext(), timeout)
}

func (m *Manager) nextTaskID(occurrenceID string) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf("task:%s:%d", occurrenceID, m.sequence.Add(1)))
}

// prepareAttempt is invoked by the fixed physical worker only after its permit
// has been consumed. The database operation itself runs on the dedicated
// persistence pump, keeping persistence concurrency bounded and independent of
// feature handlers while the reserved physical slot provides the ADR fencing
// point for execution leasing.
func (m *Manager) prepareAttempt(ctx context.Context, occurrenceID string, taskID tasks.TaskID, def JobDefinition, binding *attemptBinding) (*tasks.OccurrenceRef, error) {
	if m.pump == nil {
		err := errors.New("persistence pump is not configured")
		binding.resolve(nil, err)
		return nil, err
	}
	resCh, err := m.pump.Enqueue(ctx, func(opCtx context.Context) error {
		attempt, prepareErr := m.store.PrepareAttemptLease(opCtx, occurrenceID, string(taskID), leaseDurationFor(def))
		binding.resolve(attempt, prepareErr)
		return prepareErr
	})
	if err != nil {
		binding.resolve(nil, err)
		return nil, err
	}
	select {
	case err = <-resCh:
		if err != nil {
			return nil, err
		}
		attempt, bindErr := binding.wait(ctx)
		if bindErr != nil {
			return nil, bindErr
		}
		if attempt == nil {
			return nil, errors.New("persistence prepare completed without an attempt")
		}
		return &tasks.OccurrenceRef{
			JobID:        def.ID,
			OccurrenceID: tasks.OccurrenceID(occurrenceID),
			AttemptID:    tasks.AttemptID(attempt.ID),
			LeaseEpoch:   attempt.LeaseEpoch,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Manager) workSpecForOccurrence(def JobDefinition, handler Handler, occurrenceID string, taskID tasks.TaskID) tasks.WorkSpec {
	copyDef := def
	copyDef.Payload = append([]byte(nil), def.Payload...)
	binding := newAttemptBinding()
	return tasks.WorkSpec{
		ID:               taskID,
		Scope:            tasks.ScopeIdentity{Owner: copyDef.ScopeOwner, Generation: uint64(copyDef.Version)},
		QuotaOwner:       tasks.OwnerID(copyDef.QuotaOwner),
		Pool:             tasks.PoolID(copyDef.Pool),
		Class:            tasks.PriorityClass(copyDef.Class),
		ExecutionTimeout: copyDef.Timeout,
		HandlerRef:       copyDef.HandlerType,
		Input:            append([]byte(nil), copyDef.Payload...),
		Handler:          func(runCtx context.Context) error { return handler(runCtx, copyDef) },
		Prepare: func(prepareCtx context.Context, id tasks.TaskID) (*tasks.OccurrenceRef, error) {
			return m.prepareAttempt(prepareCtx, occurrenceID, id, copyDef, binding)
		},
		Commit: func(commitCtx context.Context, res tasks.TaskResult) error {
			attempt, err := binding.wait(commitCtx)
			if err != nil {
				return err
			}
			// No durable attempt means prepare failed before a lease existed. There
			// is deliberately nothing to commit and no execution retry budget was
			// consumed; the occurrence remains Ready for redrive.
			if attempt == nil {
				return nil
			}
			return m.store.CommitAttemptResult(commitCtx, attempt.ID, attempt.LeaseEpoch, attemptState(res.Outcome), nil, res.Failure.Message)
		},
	}
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
	occurrence := &JobOccurrence{ID: string(occurrenceID), JobID: jobID, OccurrenceKey: occurrenceKey, ScheduledFor: now, ReadyAt: now, State: OccurrenceReady}
	if err := m.store.MaterializeOccurrence(ctx, occurrence); err != nil {
		return nil, "", fmt.Errorf("materialize job occurrence: %w", err)
	}
	// Materialization is idempotent on occurrence_key and rewrites occurrence.ID
	// to the canonical identity when this logical run already exists.
	occurrenceID = tasks.OccurrenceID(occurrence.ID)
	if occurrence.State != OccurrenceReady {
		return nil, occurrence.ID, fmt.Errorf("occurrence %s is not ready for admission: %s", occurrence.ID, occurrence.State)
	}

	// Crucial ADR 0006 boundary: do NOT prepare an execution lease here. The
	// TaskEngine first performs fair admission and reserves a real physical
	// permit. WorkSpec.Prepare runs at the worker boundary before Started.
	taskID := m.nextTaskID(occurrence.ID)
	ticket, err := client.Submit(ctx, m.workSpecForOccurrence(definition, handler, occurrence.ID, taskID))
	if err != nil {
		// No durable attempt exists yet, so admission rejection cannot consume
		// retry budget. Keep the occurrence Ready and wake recovery/redrive.
		m.signalRecovery()
		return nil, occurrence.ID, err
	}
	m.track(occurrence.ID, definition, handler, taskID)
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
		case <-baseCtx.Done():
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

// watchAttempt waits for one execution intent's ticket and drives the
// retry/recovery protocol. A failed pre-start prepare may have zero durable
// attempts; such deferrals are retried without consuming execution budget.
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

	// Storage timeouts apply to one storage phase only. In particular, do not
	// carry a 10-second context across a potentially minutes-long retry backoff.
	ctx, cancel := m.operationContext()
	occ, err := m.store.GetOccurrence(ctx, item.occurrenceID)
	if err == nil {
		switch occ.State {
		case OccurrenceCancelled, OccurrenceCompleted, OccurrenceFailed, OccurrenceBlocked:
			cancel()
			m.untrack(item.occurrenceID)
			return
		}
	} else {
		m.signalRecovery()
	}
	if res.Outcome == tasks.OutcomeCompleted {
		cancel()
		m.untrack(item.occurrenceID)
		return
	}
	switch res.Outcome {
	case tasks.OutcomeFailed, tasks.OutcomeTimedOut, tasks.OutcomeCancelled,
		tasks.OutcomePanic, tasks.OutcomeAbortedBeforeStart:
		// Retryable physical/pre-start outcomes.
	default:
		cancel()
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	}
	attempts, err := m.store.CountAttempts(ctx, item.occurrenceID)
	if err != nil {
		cancel()
		m.signalRecovery()
		return
	}
	if attempts >= maxAttempts(tr.def.RetryPolicy) {
		err := m.store.FinalizeOccurrence(ctx, item.occurrenceID, OccurrenceFailed)
		cancel()
		if err != nil {
			m.signalRecovery()
			return
		}
		m.untrack(item.occurrenceID)
		return
	}
	cancel()

	var delay time.Duration
	if attempts == 0 {
		// Admission/prepare deferral is control-plane backpressure, not an
		// execution attempt. Use a small bounded retry delay rather than the
		// job's execution retry backoff/budget.
		delay = 200 * time.Millisecond
	} else {
		delay = retryDelay(tr.def.RetryPolicy, attempts)
	}
	if delay > 0 {
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

	// Re-read with a fresh operation context after backoff: cancellation wins
	// over retry, and a long backoff cannot poison the next storage operation.
	retryCtx, retryCancel := m.operationContext()
	defer retryCancel()
	if occ2, err := m.store.GetOccurrence(retryCtx, item.occurrenceID); err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
		return
	} else if occ2.State == OccurrenceCancelled || occ2.State == OccurrenceBlocked {
		m.untrack(item.occurrenceID)
		return
	}
	m.mu.RLock()
	accepting := m.accepting
	m.mu.RUnlock()
	if !accepting {
		return
	}
	if err := m.driveAttempt(retryCtx, item.occurrenceID, tr.def, tr.handler); err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
	}
}

// driveAttempt re-admits an execution intent. The durable attempt lease is
// intentionally NOT created here; WorkSpec.Prepare creates it only after a
// physical permit is granted by TaskEngine.
func (m *Manager) driveAttempt(ctx context.Context, occurrenceID string, def JobDefinition, handler Handler) error {
	nextTaskID := m.nextTaskID(occurrenceID)
	ticket, err := m.client.Submit(ctx, m.workSpecForOccurrence(def, handler, occurrenceID, nextTaskID))
	if err != nil {
		m.signalRecovery()
		return err
	}
	m.track(occurrenceID, def, handler, nextTaskID)
	m.enqueueRetry(retryItem{occurrenceID: occurrenceID, ticket: ticket})
	return nil
}

// Recover scans recoverable Ready/Dispatched occurrences and converges each
// one. Repeated calls converge; limit bounds each scan.
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

		// A Ready occurrence has no execution lease yet. Re-admit it directly;
		// this is the durable recovery path for overload/crash before prepare.
		if occ.State == OccurrenceReady {
			if derr := m.driveAttempt(ctx, occ.ID, def, handler); derr != nil {
				report.Stale++
				continue
			}
			report.Redriven++
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
