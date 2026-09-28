package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// Handler resolves a versioned job definition at execution time. It receives a
// value copy, never a mutable manager record.
type Handler func(context.Context, JobDefinition) error

// OutboxSink accepts one durable job notification. Returning nil acknowledges
// it; errors leave the row pending for retry.
type OutboxSink func(context.Context, OutboxEvent) error

// Manager owns definitions and creates a distinct occurrence and TaskID for
// every trigger. Physical execution is exclusively delegated to TaskEngine.
type Manager struct {
	mu             sync.RWMutex
	registrationMu sync.Mutex
	client         tasks.Client
	stores         StorePorts
	pump           *PersistencePump
	definitions    map[string]JobDefinition
	handlers       map[string]Handler
	sequence       atomic.Uint64
	accepting      bool
	started        bool

	retryQueue       chan retryItem
	retryMu          sync.Mutex
	retryRemaining   atomic.Int64
	retryQueued      atomic.Int64
	retryActive      atomic.Int64
	retryStopping    bool
	retryIdleTimeout time.Duration
	recoveryWake     chan struct{}
	outboxWake       chan struct{}
	outboxSink       OutboxSink
	scheduleWake     func()
	stopCh           chan struct{}
	stopOnce         sync.Once
	baseCtx          context.Context
	baseCancel       context.CancelFunc
	wg               sync.WaitGroup
	workersRemaining atomic.Int64
	doneOnce         sync.Once
	done             chan struct{}
	tracked          map[string]*trackedOccurrence
}

// retryItem watches one submitted attempt for retry/recovery decisions.
type retryItem struct {
	occurrenceID string
	ticket       tasks.Ticket
}

// trackedOccurrence remembers the latest attempt driver of an occurrence.
type trackedOccurrence struct {
	def         JobDefinition
	handler     Handler
	taskID      tasks.TaskID
	timingOwned bool
}

const (
	retryQueueCap    = 256
	retryWorkers     = 4
	retryIdleTimeout = 10 * time.Second

	// Automatic recovery is deliberately low-frequency as a safety scan; fast
	// convergence comes from bounded wake signals emitted on monitor overflow or
	// uncertain retry-driver errors.
	recoveryScanLimit     = 256
	recoveryInterval      = 30 * time.Second
	recoveryTimeout       = 20 * time.Second
	outboxBatchSize       = 100
	outboxDrainBatchLimit = 4
	outboxSafetyInterval  = 30 * time.Second
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
	RetryWorkerLimit    int
	RetryWorkers        int
	RetryQueued         int
	RetryActive         int
	TrackedOccurrences  int
	Definitions         int
	Handlers            int
	Accepting           bool
	DeferredOccurrences int
	RetainedDeferrals   int
	EarliestDeferredAt  time.Time
	DurableSnapshotOK   bool
}

var (
	_ runtime.Component     = (*Manager)(nil)
	_ runtime.ForcedStopper = (*Manager)(nil)
)

func cloneDefinition(def JobDefinition) JobDefinition {
	def.Payload = append([]byte(nil), def.Payload...)
	def.Resources = append([]tasks.ResourceRequirement(nil), def.Resources...)
	return def
}

func validateJobResources(resources []tasks.ResourceRequirement) error {
	seen := make(map[string]struct{}, len(resources))
	for _, requirement := range resources {
		if requirement.Name == "" || requirement.Name != strings.TrimSpace(requirement.Name) || requirement.Amount <= 0 {
			return errors.New("job resources require a trimmed name and positive amount")
		}
		if _, duplicate := seen[requirement.Name]; duplicate {
			return fmt.Errorf("duplicate job resource %q", requirement.Name)
		}
		seen[requirement.Name] = struct{}{}
	}
	return nil
}

// NewManagerWithPorts constructs a Manager from responsibility-specific durable
// boundaries. Focused tests may provide only the ports they exercise; Start
// still requires the complete core definition/occurrence/attempt/recovery set.
func NewManagerWithPorts(client tasks.Client, stores StorePorts, pump *PersistencePump) *Manager {
	return &Manager{
		client: client, stores: stores, pump: pump,
		definitions: make(map[string]JobDefinition),
		handlers:    make(map[string]Handler),
		tracked:     make(map[string]*trackedOccurrence),
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
	if m.started {
		return errors.New("jobs manager already started")
	}
	if m.client == nil || !m.stores.coreReady() || m.pump == nil {
		return errors.New("jobs requires task client, durable store, and persistence pump")
	}
	if loader := m.stores.DefinitionLoader; loader != nil {
		loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		definitions, err := loader.ListDefinitions(loadCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("load job definitions: %w", err)
		}
		for _, definition := range definitions {
			if err := validateJobResources(definition.Resources); err != nil {
				return fmt.Errorf("load job definition %s resources: %w", definition.ID, err)
			}
			if _, exists := m.definitions[definition.ID]; exists {
				continue
			}
			m.definitions[definition.ID] = cloneDefinition(definition)
		}
	}
	firstStart := m.retryQueue == nil
	if m.retryQueue == nil {
		m.retryQueue = make(chan retryItem, retryQueueCap)
	}
	if m.recoveryWake == nil {
		m.recoveryWake = make(chan struct{}, 1)
	}
	if m.outboxWake == nil {
		m.outboxWake = make(chan struct{}, 1)
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
	m.started = true
	if firstStart {
		done := m.done
		m.doneOnce = sync.Once{}
		m.workersRemaining.Store(1)
		m.retryRemaining.Store(0)
		m.retryQueued.Store(0)
		m.retryActive.Store(0)
		m.retryMu.Lock()
		m.retryStopping = false
		if m.retryIdleTimeout <= 0 {
			m.retryIdleTimeout = retryIdleTimeout
		}
		m.retryMu.Unlock()
		m.wg.Add(1)
		go m.durableCoordinatorLoop(done)
		// Startup convergence and outbox replay are explicit wakes. The single
		// coordinator owns both safety deadlines and the deferred-occurrence
		// deadline, so Jobs keeps one durable background goroutine instead of two.
		select {
		case m.recoveryWake <- struct{}{}:
		default:
		}
		if m.stores.Outbox != nil {
			select {
			case m.outboxWake <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

// SetOutboxSink connects durable job notifications to application delivery.
func (m *Manager) SetOutboxSink(sink OutboxSink) {
	m.mu.Lock()
	m.outboxSink = sink
	m.mu.Unlock()
	m.signalOutbox()
}

func (m *Manager) signalOutbox() {
	m.mu.RLock()
	wake := m.outboxWake
	m.mu.RUnlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) workerDone(done chan struct{}) {
	m.wg.Done()
	if m.workersRemaining.Add(-1) == 0 {
		m.doneOnce.Do(func() { close(done) })
	}
}

func (m *Manager) drainOutbox(base context.Context) {
	store := m.stores.Outbox
	if store == nil {
		return
	}
	m.mu.RLock()
	sink := m.outboxSink
	m.mu.RUnlock()
	if sink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()

	for batch := 0; batch < outboxDrainBatchLimit; batch++ {
		events, err := store.ListPendingOutbox(ctx, outboxBatchSize)
		if err != nil || len(events) == 0 {
			return
		}
		for _, event := range events {
			if err := sink(ctx, event); err != nil {
				return
			}
			if err := store.MarkOutboxDelivered(ctx, event.ID); err != nil {
				return
			}
		}
		if len(events) < outboxBatchSize {
			return
		}
	}

	// More rows may remain after the bounded per-wake budget. Re-arm the
	// coalescing wake instead of waiting for the 30s crash-safety ticker.
	if ctx.Err() == nil {
		m.signalOutbox()
	}
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
		m.retryMu.Lock()
		m.retryStopping = true
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
		m.retryMu.Unlock()
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
	if err := validateJobResources(def.Resources); err != nil {
		return fmt.Errorf("invalid job definition resources: %w", err)
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
	def = cloneDefinition(def)

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
	regCtx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
	err := m.stores.Definitions.SaveDefinition(regCtx, &def)
	cancel()
	if err != nil {
		return fmt.Errorf("save job definition: %w", err)
	}
	m.mu.Lock()
	m.definitions[def.ID] = cloneDefinition(def)
	m.mu.Unlock()
	m.signalRecovery()
	return nil
}

// DeleteDefinition removes a definition that has not admitted any occurrence.
// It is used by schedule-registration compensation so a failed scheduling API
// does not leave a durable scheduler-owned wrapper behind. Callers must never
// use it for shared target definitions.
func (m *Manager) DeleteDefinition(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("job definition id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.registrationMu.Lock()
	defer m.registrationMu.Unlock()

	m.mu.RLock()
	_, exists := m.definitions[id]
	for _, tracked := range m.tracked {
		if tracked != nil && tracked.def.ID == id {
			m.mu.RUnlock()
			return fmt.Errorf("job definition %s has an active occurrence", id)
		}
	}
	m.mu.RUnlock()
	if !exists {
		return nil
	}

	store, ok := m.stores.Definitions.(interface {
		DeleteDefinition(context.Context, string) error
	})
	if !ok {
		return errors.New("job definition store does not support deletion")
	}
	if err := store.DeleteDefinition(ctx, id); err != nil {
		return fmt.Errorf("delete job definition %s: %w", id, err)
	}
	m.mu.Lock()
	delete(m.definitions, id)
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
	store := m.stores.Occurrences
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
	cancelCtx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
	defer cancel()
	cancelled := 0
	for _, definition := range definitions {
		if definition.ScopeOwner == owner || definition.ScopeOwner == "plugin:"+owner {
			cancelled += client.CancelScope(tasks.ScopeIdentity{Owner: definition.ScopeOwner, Generation: uint64(definition.Version)}, tasks.CauseScopeClosed)
		}
	}
	for _, tr := range tracked {
		if tr.scopeOwner == owner || tr.scopeOwner == "plugin:"+owner {
			if err := store.CancelOccurrence(cancelCtx, tr.occurrenceID, "owner cancelled"); err == nil {
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
	if err := m.stores.Occurrences.CancelOccurrence(ctx, occurrenceID, reason); err != nil {
		return err
	}
	m.signalOutbox()
	if taskID != "" {
		_, _ = m.client.Cancel(taskID, tasks.CauseUserCancel)
	}
	m.untrack(occurrenceID)
	return nil
}

func timingOwnedOccurrence(occurrence *JobOccurrence) bool {
	if occurrence == nil {
		return false
	}
	if occurrence.ScheduleID != "" {
		return true
	}
	return strings.HasPrefix(occurrence.OccurrenceKey, "sched:") ||
		strings.HasPrefix(occurrence.OccurrenceKey, "periodic:")
}

func (m *Manager) untrack(occurrenceID string) bool {
	m.mu.Lock()
	tracked, existed := m.tracked[occurrenceID]
	delete(m.tracked, occurrenceID)
	wake := m.scheduleWake
	shouldWake := existed && tracked != nil && tracked.timingOwned
	m.mu.Unlock()

	// Timing ownership belongs to the occurrence origin, not the target
	// definition name. This preserves immediate reconciliation for managed
	// schedules whose target definition has an arbitrary ID.
	if shouldWake && wake != nil {
		wake()
	}
	return shouldWake
}

func (m *Manager) untrackOrWakeTimingOccurrence(occurrence *JobOccurrence) {
	if occurrence == nil {
		return
	}
	if m.untrack(occurrence.ID) {
		return
	}
	// Recovery can finalize an occurrence after process restart with no in-memory
	// tracked entry. Persisted schedule_id/occurrence_key remains authoritative.
	if timingOwnedOccurrence(occurrence) {
		m.signalSchedule()
	}
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

// SubmitOccurrence admits one new occurrence through the store and into TaskEngine.
func (m *Manager) SubmitOccurrence(ctx context.Context, jobID string, occurrenceKey string) (tasks.Ticket, string, error) {
	m.mu.RLock()
	client := m.client
	definition, ok := m.definitions[jobID]
	handler := m.handlers[definition.HandlerType]
	accepting := m.accepting
	m.mu.RUnlock()
	if !accepting {
		return nil, "", execution.WithSemantics(
			errors.New("job admission is closed"),
			execution.Semantics{Disposition: execution.DispositionRetryable, Code: "job_admission_closed"},
		)
	}
	if !ok {
		return nil, "", execution.WithSemantics(
			fmt.Errorf("job definition not found: %s", jobID),
			execution.Semantics{Disposition: execution.DispositionPermanent, Code: "job_definition_not_found"},
		)
	}
	if handler == nil {
		return nil, "", execution.WithSemantics(
			fmt.Errorf("no handler registered for job definition %s (handler: %s)", jobID, definition.HandlerType),
			execution.Semantics{Disposition: execution.DispositionPermanent, Code: "job_handler_missing"},
		)
	}
	if !definition.Enabled {
		return nil, "", execution.WithSemantics(
			fmt.Errorf("job definition is disabled: %s", jobID),
			execution.Semantics{Disposition: execution.DispositionPermanent, Code: "job_definition_disabled"},
		)
	}
	sequence := m.sequence.Add(1)
	if occurrenceKey == "" {
		occurrenceKey = fmt.Sprintf("manual:%s:%d", jobID, sequence)
	}
	now := time.Now().UTC()
	occurrenceID := tasks.OccurrenceID(fmt.Sprintf("occ:%s:%d:%d", jobID, now.UnixNano(), sequence))
	occurrence := &JobOccurrence{ID: string(occurrenceID), JobID: jobID, OccurrenceKey: occurrenceKey, ScheduledFor: now, ReadyAt: now, State: OccurrenceReady}
	if err := m.stores.Occurrences.MaterializeOccurrence(ctx, occurrence); err != nil {
		return nil, "", fmt.Errorf("materialize job occurrence: %w", err)
	}
	// Materialization is idempotent on occurrence_key and rewrites occurrence.ID
	// to the canonical identity when this logical run already exists.
	occurrenceID = tasks.OccurrenceID(occurrence.ID)
	attempt, err := m.prepareNextAttemptLease(ctx, occurrence.ID, leaseDurationFor(definition))
	if err != nil {
		return nil, "", fmt.Errorf("prepare job attempt: %w", err)
	}
	taskID := tasks.TaskID(attempt.TaskID)
	copyDef := cloneDefinition(definition)
	m.track(occurrence.ID, copyDef, handler, taskID, timingOwnedOccurrence(occurrence))
	ticket, err := client.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            tasks.ScopeIdentity{Owner: copyDef.ScopeOwner, Generation: uint64(copyDef.Version)},
		QuotaOwner:       tasks.OwnerID(copyDef.QuotaOwner),
		Pool:             tasks.PoolID(copyDef.Pool),
		Class:            tasks.PriorityClass(copyDef.Class),
		ExecutionTimeout: copyDef.Timeout,
		HandlerRef:       copyDef.HandlerType,
		Input:            append([]byte(nil), copyDef.Payload...),
		Resources:        definitionResources(copyDef),
		Job:              &tasks.OccurrenceRef{JobID: copyDef.ID, OccurrenceID: occurrenceID, AttemptID: tasks.AttemptID(attempt.ID), LeaseEpoch: attempt.LeaseEpoch},
		Handler: func(runCtx context.Context) error {
			return handler(runCtx, copyDef)
		},
		Commit: func(commitCtx context.Context, res tasks.TaskResult) error {
			return m.commitAttemptResult(commitCtx, attempt, res)
		},
	})
	if err != nil {
		m.untrack(occurrence.ID)
		m.persistAttemptResult(attempt, abortedTaskResult(taskID, err))
		return nil, "", err
	}
	m.enqueueRetry(retryItem{occurrenceID: occurrence.ID, ticket: ticket})
	return ticket, occurrence.ID, nil
}

func (m *Manager) GetOccurrence(ctx context.Context, occurrenceID string) (*JobOccurrence, error) {
	return m.stores.Occurrences.GetOccurrence(ctx, occurrenceID)
}

func (m *Manager) OccurrenceByKey(ctx context.Context, occurrenceKey string) (*JobOccurrence, error) {
	return m.stores.Occurrences.GetOccurrenceByKey(ctx, occurrenceKey)
}

func (m *Manager) LatestAttempt(ctx context.Context, occurrenceID string) (*JobAttempt, error) {
	return m.stores.Attempts.LatestAttempt(ctx, occurrenceID)
}

func (m *Manager) UpdateDefinition(ctx context.Context, def JobDefinition) error {
	if def.ID == "" {
		return errors.New("job definition id is required")
	}
	if err := validateJobResources(def.Resources); err != nil {
		return fmt.Errorf("invalid job definition resources: %w", err)
	}
	m.mu.RLock()
	current, found := m.definitions[def.ID]
	m.mu.RUnlock()
	if !found {
		return fmt.Errorf("job definition not found: %s", def.ID)
	}
	def = cloneDefinition(def)
	def.Revision = current.Revision
	if err := m.stores.Definitions.UpdateDefinitionCAS(ctx, &def, current.Revision); err != nil {
		return err
	}
	m.mu.Lock()
	m.definitions[def.ID] = cloneDefinition(def)
	m.mu.Unlock()
	return nil
}

func (m *Manager) PruneOccurrences(ctx context.Context, jobID string, before time.Time, limit int) (int64, error) {
	return m.stores.Occurrences.DeleteTerminalOccurrences(ctx, jobID, before, limit)
}

func (m *Manager) track(occurrenceID string, def JobDefinition, handler Handler, taskID tasks.TaskID, timingOwned bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tracked == nil {
		m.tracked = make(map[string]*trackedOccurrence)
	}
	m.tracked[occurrenceID] = &trackedOccurrence{
		def: cloneDefinition(def), handler: handler, taskID: taskID, timingOwned: timingOwned,
	}
}

func (m *Manager) Definition(id string) (JobDefinition, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	def, ok := m.definitions[id]
	return cloneDefinition(def), ok
}

func (m *Manager) Diagnostics() Diagnostics {
	if m == nil {
		return Diagnostics{}
	}
	m.mu.RLock()
	diagnostics := Diagnostics{
		RetryWorkerLimit:   retryWorkers,
		RetryWorkers:       int(m.retryRemaining.Load()),
		RetryQueued:        int(m.retryQueued.Load()),
		RetryActive:        int(m.retryActive.Load()),
		TrackedOccurrences: len(m.tracked),
		Definitions:        len(m.definitions),
		Handlers:           len(m.handlers),
		Accepting:          m.accepting,
	}
	store := m.stores.Diagnostics
	m.mu.RUnlock()

	if durable := store; durable != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		snapshot, err := durable.DurableDiagnostics(ctx, time.Now().UTC())
		cancel()
		if err == nil {
			diagnostics.DeferredOccurrences = snapshot.DeferredOccurrences
			diagnostics.RetainedDeferrals = snapshot.RetainedDeferrals
			diagnostics.EarliestDeferredAt = snapshot.EarliestDeferredAt
			diagnostics.DurableSnapshotOK = true
		}
	}
	return diagnostics
}
