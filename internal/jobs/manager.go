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

// Store is the durable boundary for a job definition and each of its
// occurrences. Implementations fence attempts by lease epoch.
type Store interface {
	SaveDefinition(context.Context, *JobDefinition) error
	MaterializeOccurrence(context.Context, *JobOccurrence) error
	PrepareAttemptLease(context.Context, string, string, time.Duration) (*JobAttempt, error)
	CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error
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
}

// Diagnostics is a read-only count of declarative job registrations.
type Diagnostics struct {
	Definitions int
	Handlers    int
	Accepting   bool
}

var _ runtime.Component = (*Manager)(nil)

func NewManager(client tasks.Client, store Store, pump *PersistencePump) *Manager {
	return &Manager{client: client, store: store, pump: pump, definitions: make(map[string]JobDefinition), handlers: make(map[string]Handler)}
}

func (m *Manager) Name() string           { return "jobs" }
func (m *Manager) Dependencies() []string { return []string{"taskengine"} }
func (m *Manager) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil || m.store == nil || m.pump == nil {
		return errors.New("jobs requires task client, durable store, and persistence pump")
	}
	m.accepting = true
	return nil
}
func (m *Manager) Quiesce(context.Context) error {
	m.mu.Lock()
	m.accepting = false
	m.mu.Unlock()
	return nil
}
func (m *Manager) Drain(context.Context) error { return nil }
func (m *Manager) Stop(context.Context) error  { return m.Quiesce(context.Background()) }
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
	_, err := m.SubmitOccurrence(ctx, jobID, "")
	return err
}

// TryTrigger is retained as an admission-only spelling for timer producers.
// TaskEngine Submit itself never waits for a physical worker slot.
func (m *Manager) TryTrigger(ctx context.Context, jobID string) error { return m.Trigger(ctx, jobID) }

// CancelByOwner fences queued work through the scope identity. Definitions are
// immutable records; cancellation does not erase durable history.
func (m *Manager) CancelByOwner(owner string) int {
	m.mu.RLock()
	client := m.client
	definitions := make([]JobDefinition, 0, len(m.definitions))
	for _, definition := range m.definitions {
		definitions = append(definitions, definition)
	}
	m.mu.RUnlock()
	cancelled := 0
	for _, definition := range definitions {
		if definition.ScopeOwner == owner || definition.ScopeOwner == "plugin:"+owner {
			cancelled += client.CancelScope(tasks.ScopeIdentity{Owner: definition.ScopeOwner, Generation: uint64(definition.Version)}, tasks.CauseScopeClosed)
		}
	}
	return cancelled
}

func (m *Manager) SubmitOccurrence(ctx context.Context, jobID, occurrenceKey string) (tasks.Ticket, error) {
	m.mu.RLock()
	if !m.accepting {
		m.mu.RUnlock()
		return nil, errors.New("job admission is closed")
	}
	definition, found := m.definitions[jobID]
	handler := m.handlers[definition.HandlerType]
	client := m.client
	m.mu.RUnlock()
	if !found {
		return nil, fmt.Errorf("job definition not found: %s", jobID)
	}
	if handler == nil {
		return nil, fmt.Errorf("unknown job handler: %s", definition.HandlerType)
	}
	if !definition.Enabled {
		return nil, fmt.Errorf("job definition is disabled: %s", jobID)
	}
	sequence := m.sequence.Add(1)
	if occurrenceKey == "" {
		occurrenceKey = fmt.Sprintf("manual:%s:%d", jobID, sequence)
	}
	occurrenceID := tasks.OccurrenceID(fmt.Sprintf("occ:%s:%d:%d", jobID, time.Now().UTC().UnixNano(), sequence))
	taskID := tasks.TaskID(fmt.Sprintf("task:%s:1", occurrenceID))
	occurrence := &JobOccurrence{ID: string(occurrenceID), JobID: jobID, OccurrenceKey: occurrenceKey, ScheduledFor: time.Now().UTC(), ReadyAt: time.Now().UTC(), State: OccurrenceReady}
	if err := m.store.MaterializeOccurrence(ctx, occurrence); err != nil {
		return nil, fmt.Errorf("materialize job occurrence: %w", err)
	}
	leaseDuration := definition.Timeout + time.Minute
	if leaseDuration < time.Minute {
		leaseDuration = time.Minute
	}
	attempt, err := m.store.PrepareAttemptLease(ctx, occurrence.ID, string(taskID), leaseDuration)
	if err != nil {
		return nil, fmt.Errorf("prepare job attempt: %w", err)
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
		OnComplete: func(result tasks.TaskResult) {
			m.persistAttemptResult(attempt, result)
		},
	})
	if err != nil {
		m.persistAttemptResult(attempt, tasks.TaskResult{TaskID: taskID, Outcome: tasks.OutcomeAbortedBeforeStart, Cause: tasks.CausePersistenceFailure, FinishedAt: time.Now().UTC(), Failure: tasks.FailureInfo{Message: err.Error()}})
		return nil, err
	}
	return ticket, nil
}

func (m *Manager) persistAttemptResult(attempt *JobAttempt, result tasks.TaskResult) {
	if attempt == nil || m.store == nil {
		return
	}
	outcome := attemptState(result.Outcome)
	errText := result.Failure.Message
	commitFn := func(ctx context.Context) error {
		return m.store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, outcome, nil, errText)
	}

	if m.pump != nil {
		if _, err := m.pump.Enqueue(context.Background(), commitFn); err == nil {
			return
		}
	}

	// Fallback to direct background commit so durable completion evidence is never lost
	go func() {
		commitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = commitFn(commitCtx)
	}()
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
