package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	CommitAttemptDeferred(context.Context, string, uint64, time.Time, string) error
	FinalizeOccurrence(context.Context, string, OccurrenceState) error
	CancelOccurrence(context.Context, string, string) error
	GetOccurrence(context.Context, string) (*JobOccurrence, error)
	GetOccurrenceByKey(context.Context, string) (*JobOccurrence, error)
	CountAttempts(context.Context, string) (int, error)
	CountRetryBudgetUses(context.Context, string) (int, error)
	CountDeferrals(context.Context, string) (int, error)
	LatestAttempt(context.Context, string) (*JobAttempt, error)
	ListUnresolvedOccurrences(context.Context, int) ([]*JobOccurrence, error)
	DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error)
	DeferOccurrence(context.Context, string, time.Time) error
}

type outboxStore interface {
	ListPendingOutbox(context.Context, int) ([]OutboxEvent, error)
	MarkOutboxDelivered(context.Context, string) error
}

type deferredDeadlineStore interface {
	EarliestDeferredOccurrenceDue(context.Context, time.Time) (time.Time, bool, error)
}

type attemptSummaryStore interface {
	AttemptSummary(context.Context, string) (*AttemptSummary, error)
}

type nextAttemptLeaseStore interface {
	PrepareNextAttemptLease(context.Context, string, time.Duration) (*JobAttempt, error)
}

type definitionLoader interface {
	ListDefinitions(context.Context) ([]JobDefinition, error)
}

type scheduleStore interface {
	SaveSchedule(context.Context, *JobSchedule) error
	DisableSchedule(context.Context, string) error
	ListDueSchedules(context.Context, time.Time, int) ([]JobSchedule, error)
	EarliestScheduleDue(context.Context) (time.Time, bool, error)
	MaterializeDueSchedule(context.Context, string, time.Time) (*JobOccurrence, error)
	SkipDueSchedule(context.Context, string, time.Time) error
	CutoverActive(context.Context) (bool, error)
}

// OutboxSink accepts one durable job notification. Returning nil acknowledges
// it; errors leave the row pending for retry.
type OutboxSink func(context.Context, OutboxEvent) error

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
	started        bool

	retryQueue   chan retryItem
	recoveryWake chan struct{}
	outboxWake   chan struct{}
	outboxSink   OutboxSink
	scheduleWake func()
	stopCh       chan struct{}
	stopOnce     sync.Once
	baseCtx      context.Context
	baseCancel   context.CancelFunc
	wg           sync.WaitGroup
	done         chan struct{}
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
	Definitions int
	Handlers    int
	Accepting   bool
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

func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("jobs manager already started")
	}
	if m.client == nil || m.store == nil || m.pump == nil {
		return errors.New("jobs requires task client, durable store, and persistence pump")
	}
	if loader, ok := m.store.(definitionLoader); ok {
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
		for i := 0; i < retryWorkers; i++ {
			m.wg.Add(1)
			go m.retryLoop()
		}
		m.wg.Add(1)
		go m.recoveryLoop()
		if _, ok := m.store.(outboxStore); ok {
			m.wg.Add(1)
			go m.outboxLoop()
		}
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

// SetOutboxSink connects durable job notifications to application delivery.
func (m *Manager) SetOutboxSink(sink OutboxSink) {
	m.mu.Lock()
	m.outboxSink = sink
	m.mu.Unlock()
	m.signalOutbox()
}

// SetScheduleWake connects durable schedule mutations to the timing owner.
// The callback must be non-blocking; Scheduler uses a coalescing wake channel.
func (m *Manager) SetScheduleWake(wake func()) {
	m.mu.Lock()
	m.scheduleWake = wake
	m.mu.Unlock()
}

func (m *Manager) signalSchedule() {
	m.mu.RLock()
	wake := m.scheduleWake
	m.mu.RUnlock()
	if wake != nil {
		wake()
	}
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

func (m *Manager) outboxLoop() {
	defer m.wg.Done()
	// Delivery is wake-driven. The low-frequency ticker is only a crash/
	// uncertainty safety net for durable outbox rows that were committed before
	// an in-memory wake could be emitted.
	ticker := time.NewTicker(outboxSafetyInterval)
	defer ticker.Stop()
	for {
		m.mu.RLock()
		stopCh, baseCtx, wake := m.stopCh, m.baseCtx, m.outboxWake
		m.mu.RUnlock()
		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		m.drainOutbox(baseCtx)
	}
}

func (m *Manager) drainOutbox(base context.Context) {
	store, ok := m.store.(outboxStore)
	if !ok {
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
	err := m.store.SaveDefinition(regCtx, &def)
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
	if err := m.store.CancelOccurrence(ctx, occurrenceID, reason); err != nil {
		return err
	}
	m.signalOutbox()
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

// SubmitOccurrence admits one new occurrence through the store and into TaskEngine.
func (m *Manager) SubmitOccurrence(ctx context.Context, jobID string, occurrenceKey string) (tasks.Ticket, string, error) {
	m.mu.RLock()
	client := m.client
	definition, ok := m.definitions[jobID]
	handler := m.handlers[definition.HandlerType]
	accepting := m.accepting
	m.mu.RUnlock()
	if !accepting {
		return nil, "", errors.New("job admission is closed")
	}
	if !ok {
		return nil, "", fmt.Errorf("job definition not found: %s", jobID)
	}
	if handler == nil {
		return nil, "", fmt.Errorf("no handler registered for job definition %s (handler: %s)", jobID, definition.HandlerType)
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
	attempt, err := m.prepareNextAttemptLease(ctx, occurrence.ID, leaseDurationFor(definition))
	if err != nil {
		return nil, "", fmt.Errorf("prepare job attempt: %w", err)
	}
	taskID := tasks.TaskID(attempt.TaskID)
	copyDef := cloneDefinition(definition)
	m.track(occurrence.ID, copyDef, handler, taskID)
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
		m.persistAttemptResult(attempt, tasks.TaskResult{
			TaskID: taskID, Outcome: tasks.OutcomeAbortedBeforeStart,
			Cause: tasks.CausePersistenceFailure, FinishedAt: time.Now().UTC(),
			Failure: tasks.FailureInfo{Message: err.Error()},
		})
		return nil, "", err
	}
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
	if err := m.store.UpdateDefinitionCAS(ctx, &def, current.Revision); err != nil {
		return err
	}
	m.mu.Lock()
	m.definitions[def.ID] = cloneDefinition(def)
	m.mu.Unlock()
	return nil
}

func (m *Manager) PruneOccurrences(ctx context.Context, jobID string, before time.Time, limit int) (int64, error) {
	return m.store.DeleteTerminalOccurrences(ctx, jobID, before, limit)
}

func (m *Manager) SaveSchedule(ctx context.Context, schedule JobSchedule) error {
	if err := validateSchedulePolicy(schedule); err != nil {
		return err
	}
	store, ok := m.store.(scheduleStore)
	if !ok {
		return errors.New("job schedule store is not configured")
	}
	if err := store.SaveSchedule(ctx, &schedule); err != nil {
		return err
	}
	m.signalSchedule()
	return nil
}

func validateSchedulePolicy(schedule JobSchedule) error {
	if strings.TrimSpace(schedule.ID) == "" || strings.TrimSpace(schedule.JobID) == "" {
		return errors.New("schedule id and job id are required")
	}
	switch schedule.Recurrence {
	case "once":
		if schedule.Interval != 0 {
			return errors.New("one-shot schedule interval must be zero")
		}
	case "interval":
		if schedule.Interval < time.Second {
			return errors.New("recurring schedule interval must be at least one second")
		}
	default:
		return fmt.Errorf("unsupported recurrence %q", schedule.Recurrence)
	}
	if schedule.NextDueAt.IsZero() {
		return errors.New("schedule next due time is required")
	}
	tz := schedule.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid schedule timezone %q: %w", tz, err)
	}
	switch schedule.MisfirePolicy {
	case "", MisfireRunOnce, MisfireSkip:
	case MisfireCatchUpBounded:
		return errors.New("catch_up_bounded misfire policy is not supported")
	default:
		return fmt.Errorf("invalid misfire policy %q", schedule.MisfirePolicy)
	}
	switch schedule.OverlapPolicy {
	case "", OverlapForbid:
	case OverlapReplace:
		return errors.New("replace overlap policy is not supported")
	case OverlapAllowBounded:
		return errors.New("allow_bounded overlap policy is not supported")
	default:
		return fmt.Errorf("invalid overlap policy %q", schedule.OverlapPolicy)
	}
	return nil
}

// DisableSchedule atomically removes a schedule from timing ownership while
// retaining its durable definition and occurrence history for diagnostics.
func (m *Manager) DisableSchedule(ctx context.Context, scheduleID string) error {
	store, ok := m.store.(scheduleStore)
	if !ok {
		return errors.New("job schedule store is not configured")
	}
	if err := store.DisableSchedule(ctx, scheduleID); err != nil {
		return err
	}
	m.signalSchedule()
	return nil
}

func (m *Manager) ScheduleCutoverActive(ctx context.Context) (bool, error) {
	store, ok := m.store.(scheduleStore)
	if !ok {
		return false, nil
	}
	return store.CutoverActive(ctx)
}

func (m *Manager) EarliestScheduleDue(ctx context.Context) (time.Time, bool, error) {
	store, ok := m.store.(scheduleStore)
	if !ok {
		return time.Time{}, false, nil
	}
	return store.EarliestScheduleDue(ctx)
}

// ProcessDueSchedules materializes a bounded batch and delegates every
// physical attempt to TaskEngine. Scheduler calls this timing-only API.
func (m *Manager) ProcessDueSchedules(ctx context.Context, now time.Time, limit int) (int, error) {
	store, ok := m.store.(scheduleStore)
	if !ok {
		return 0, errors.New("job schedule store is not configured")
	}
	schedules, err := store.ListDueSchedules(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, schedule := range schedules {
		if err := validateSchedulePolicy(schedule); err != nil {
			// Quarantine persisted policies this binary cannot honor. Leaving the
			// row due would turn a configuration error into a tight scheduler loop.
			if disableErr := store.DisableSchedule(ctx, schedule.ID); disableErr != nil {
				return processed, fmt.Errorf("schedule %s policy invalid (%v), disable: %w", schedule.ID, err, disableErr)
			}
			return processed, fmt.Errorf("schedule %s: %w", schedule.ID, err)
		}
		nextDue := schedule.NextDueAt
		if schedule.Recurrence != "once" {
			interval := schedule.Interval
			if interval <= 0 {
				interval = time.Minute
			}
			nextDue = schedule.NextDueAt.Add(interval)
			for !nextDue.After(now) {
				nextDue = nextDue.Add(interval)
			}
			// A recurring slot is a misfire only after at least one complete
			// interval has elapsed. Skip advances timing ownership atomically
			// without consuming occurrence or attempt capacity.
			if schedule.MisfirePolicy == MisfireSkip && !now.Before(schedule.NextDueAt.Add(interval)) {
				if err := store.SkipDueSchedule(ctx, schedule.ID, nextDue); err != nil {
					return processed, err
				}
				processed++
				continue
			}
		}
		occurrence, err := store.MaterializeDueSchedule(ctx, schedule.ID, nextDue)
		if err != nil {
			return processed, err
		}
		processed++
		if occurrence == nil {
			continue
		}
		m.mu.RLock()
		definition, found := m.definitions[occurrence.JobID]
		handler := m.handlers[definition.HandlerType]
		m.mu.RUnlock()
		if !found || handler == nil {
			m.signalRecovery()
			continue
		}
		if err := m.driveAttempt(ctx, occurrence.ID, definition, handler); err != nil {
			m.signalRecovery()
		}
	}
	return processed, nil
}

func (m *Manager) track(occurrenceID string, def JobDefinition, handler Handler, taskID tasks.TaskID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tracked == nil {
		m.tracked = make(map[string]*trackedOccurrence)
	}
	m.tracked[occurrenceID] = &trackedOccurrence{def: cloneDefinition(def), handler: handler, taskID: taskID}
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
	safetyTicker := time.NewTicker(recoveryInterval)
	defer safetyTicker.Stop()

	var deadlineTimer *time.Timer
	var deadlineC <-chan time.Time
	stopDeadlineTimer := func() {
		if deadlineTimer == nil {
			deadlineC = nil
			return
		}
		if !deadlineTimer.Stop() {
			select {
			case <-deadlineTimer.C:
			default:
			}
		}
		deadlineC = nil
	}
	defer stopDeadlineTimer()

	rearmDeadline := func(baseCtx context.Context) {
		stopDeadlineTimer()
		store, ok := m.store.(deferredDeadlineStore)
		if !ok {
			return
		}
		queryCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
		due, found, err := store.EarliestDeferredOccurrenceDue(queryCtx, time.Now().UTC())
		cancel()
		if err != nil || !found {
			return
		}
		wait := time.Until(due)
		if wait < 0 {
			wait = 0
		}
		if deadlineTimer == nil {
			deadlineTimer = time.NewTimer(wait)
		} else {
			deadlineTimer.Reset(wait)
		}
		deadlineC = deadlineTimer.C
	}

	for {
		m.mu.RLock()
		stopCh := m.stopCh
		baseCtx := m.baseCtx
		wake := m.recoveryWake
		m.mu.RUnlock()
		if stopCh == nil || baseCtx == nil || wake == nil {
			return
		}

		rearmDeadline(baseCtx)
		select {
		case <-stopCh:
			return
		case <-baseCtx.Done():
			return
		case <-wake:
			m.runRecoveryPass(baseCtx)
		case <-deadlineC:
			m.runRecoveryPass(baseCtx)
		case <-safetyTicker.C:
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

func occurrenceTerminal(state OccurrenceState) bool {
	switch state {
	case OccurrenceCompleted, OccurrenceFailed, OccurrenceCancelled:
		return true
	default:
		return false
	}
}

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
	return m.store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, attemptState(res.Outcome), nil, res.Failure.Message)
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
			err := m.store.FinalizeOccurrence(finalizeCtx, item.occurrenceID, OccurrenceFailed)
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
		err := m.store.FinalizeOccurrence(finalizeCtx, item.occurrenceID, OccurrenceFailed)
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
		err := m.store.DeferOccurrence(deferCtx, item.occurrenceID, deferUntil)
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
	err = m.driveAttempt(driveCtx, item.occurrenceID, tr.def, tr.handler)
	driveCancel()
	if err != nil {
		m.untrack(item.occurrenceID)
		m.signalRecovery()
	}
}

// driveAttempt prepares the next attempt lease and submits its task, then re-arms the monitor.
func (m *Manager) driveAttempt(ctx context.Context, occurrenceID string, def JobDefinition, handler Handler) error {
	attempt, err := m.prepareNextAttemptLease(ctx, occurrenceID, leaseDurationFor(def))
	if err != nil {
		return err
	}
	nextTaskID := tasks.TaskID(attempt.TaskID)
	copyDef := cloneDefinition(def)
	commit := func(commitCtx context.Context, res tasks.TaskResult) error {
		return m.commitAttemptResult(commitCtx, attempt, res)
	}
	m.track(occurrenceID, copyDef, handler, nextTaskID)
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
		abortCtx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
		commitErr := m.store.CommitAttemptResult(abortCtx, attempt.ID, attempt.LeaseEpoch, AttemptAbortedBeforeStart, nil, err.Error())
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

// Recover scans unresolved occurrences and converges each one. Repeated calls converge; limit bounds each scan.
func (m *Manager) Recover(ctx context.Context, limit int) (RecoverReport, error) {
	var report RecoverReport
	unresolved, err := m.store.ListUnresolvedOccurrences(ctx, limit)
	if err != nil {
		return report, err
	}
	for _, occ := range unresolved {
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
		summary, err := m.loadAttemptSummary(ctx, occ.ID)
		if err != nil || summary.OccurrenceState != OccurrenceDispatched {
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
		if latest.State == AttemptDeferred && summary.Deferrals >= maxDeferrals(def.RetryPolicy) {
			if ferr := m.store.FinalizeOccurrence(ctx, occ.ID, OccurrenceFailed); ferr != nil {
				report.Stale++
				continue
			}
			m.untrack(occ.ID)
			report.Finalized++
			continue
		}
		retryUses := summary.RetryBudgetUses
		if retryUses >= maxAttempts(def.RetryPolicy) {
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
	ctx, cancel := context.WithTimeout(m.rootContext(), 10*time.Second)
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
	return cloneDefinition(def), ok
}

func (m *Manager) Diagnostics() Diagnostics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Diagnostics{Definitions: len(m.definitions), Handlers: len(m.handlers), Accepting: m.accepting}
}
