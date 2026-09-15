package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

var ErrSubmitterNotRunning = errors.New("execution submitter is not running")

// LegacySubmitter is a one-way migration adapter. It translates the old
// closure-bearing Task API into immutable WorkSpec values. TaskEngine remains
// the only lifecycle/admission authority; this adapter keeps only ephemeral
// capability and callback bookkeeping.
type LegacySubmitter struct {
	engine   *taskengine.Engine
	registry *Registry
	ref      tasks.HandlerRef
	pools    []tasks.PoolID

	mu          sync.Mutex
	pending     map[tasks.TaskID]*legacyEntry
	generations map[tasks.ScopeOwner]uint64
	wake        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	running     bool
	sequence    atomic.Uint64
}

type legacyEntry struct {
	task       tasks.Task
	stopCancel func() bool
}

func NewLegacySubmitter(engine *taskengine.Engine, registry *Registry, pools []tasks.PoolID) (*LegacySubmitter, error) {
	if engine == nil || registry == nil {
		return nil, errors.New("task engine and execution registry are required")
	}
	if len(pools) == 0 {
		return nil, errors.New("at least one execution pool is required")
	}
	ref, err := tasks.NewHandlerRef("execution.ephemeral", 1)
	if err != nil {
		return nil, err
	}
	s := &LegacySubmitter{
		engine: engine, registry: registry, ref: ref, pools: append([]tasks.PoolID(nil), pools...),
		pending: make(map[tasks.TaskID]*legacyEntry), generations: make(map[tasks.ScopeOwner]uint64), wake: make(chan struct{}, 1),
	}
	descriptor := taskengine.HandlerDescriptor{
		Ref: ref, PayloadKind: "execution.ephemeral", PayloadVersions: []uint16{1}, MaxPayloadBytes: 512,
		AllowedPools: append([]tasks.PoolID(nil), pools...),
		AllowedClasses: []tasks.PriorityClass{
			tasks.PriorityInteractive, tasks.PriorityNormal, tasks.PriorityBackground, tasks.PriorityMaintenance,
		},
	}
	if err := registry.Register(descriptor, s.run); err != nil {
		return nil, fmt.Errorf("register ephemeral execution handler: %w", err)
	}
	return s, nil
}

func (s *LegacySubmitter) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if s.done != nil {
		return errors.New("execution submitter cannot be restarted")
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	s.running = true
	go s.reapLoop(s.ctx, s.done)
	return nil
}

func (s *LegacySubmitter) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.running {
		done := s.done
		s.mu.Unlock()
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
	s.running = false
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Submit waits only for TaskEngine's admission decision. It does not wait for a
// worker or completion, matching the replacement runtime contract.
func (s *LegacySubmitter) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	return s.submit(ctx, tasks.PoolID(poolName), task)
}

// TrySubmit has the same semantics in V2 because TaskEngine admission is
// bounded and never waits for a physical worker slot.
func (s *LegacySubmitter) TrySubmit(ctx context.Context, poolName string, task tasks.Task) error {
	return s.submit(ctx, tasks.PoolID(poolName), task)
}

func (s *LegacySubmitter) submit(ctx context.Context, pool tasks.PoolID, task tasks.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if task.Run == nil {
		return errors.New("task Run function cannot be nil")
	}
	if strings.TrimSpace(task.Owner) == "" {
		task.Owner = "runtime"
	}
	if strings.TrimSpace(task.ID) == "" {
		task.ID = fmt.Sprintf("compat:%d", s.sequence.Add(1))
	}
	taskID := tasks.TaskID(task.ID)
	owner := tasks.ScopeOwner("compat:" + task.Owner)
	scope, err := s.scope(owner)
	if err != nil {
		return err
	}
	payload, err := tasks.NewPayloadRef("execution.ephemeral", 1, []byte(task.ID))
	if err != nil {
		return err
	}
	class := tasks.PriorityNormal
	switch {
	case pool == "interactive" || task.Priority >= 100:
		class = tasks.PriorityInteractive
	case task.Priority < 0:
		class = tasks.PriorityBackground
	}
	cause := classifyCause(task.Name)
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: taskID, Scope: scope, QuotaOwner: tasks.QuotaOwner(task.Owner), Pool: pool,
		Class: class, Cause: cause, OrderingKey: task.CorrelationID,
		ExecutionTimeout: task.Timeout, Handler: s.ref, Input: payload,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return ErrSubmitterNotRunning
	}
	if _, exists := s.pending[taskID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("task %q already pending", task.ID)
	}
	entry := &legacyEntry{task: task}
	s.pending[taskID] = entry
	s.mu.Unlock()

	if _, err := s.engine.Submit(ctx, spec); err != nil {
		s.mu.Lock()
		delete(s.pending, taskID)
		s.mu.Unlock()
		return err
	}
	entry.stopCancel = context.AfterFunc(ctx, func() {
		_, _ = s.engine.Cancel(taskID, tasks.CancelCaller)
	})
	s.signal()
	return nil
}

func classifyCause(name string) tasks.SubmissionCause {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(name, "command:"):
		return tasks.CauseInteractiveCommand
	case strings.HasPrefix(name, "callback:"):
		return tasks.CauseCallback
	case strings.HasPrefix(name, "inline:"):
		return tasks.CauseInline
	case strings.HasPrefix(name, "periodic:"):
		return tasks.CausePeriodic
	case strings.HasPrefix(name, "maintenance:"):
		return tasks.CauseMaintenance
	default:
		return tasks.CauseManual
	}
}

func (s *LegacySubmitter) scope(owner tasks.ScopeOwner) (tasks.ScopeIdentity, error) {
	s.mu.Lock()
	generation := s.generations[owner]
	if generation == 0 {
		generation = 1
		s.generations[owner] = generation
	}
	s.mu.Unlock()
	return tasks.NewScopeIdentity(owner, generation)
}

// CloseOwner fences the current compatibility generation, cancels all accepted
// work in it, then advances the local generation for future reload work.
func (s *LegacySubmitter) CloseOwner(owner string) (int, error) {
	scopeOwner := tasks.ScopeOwner("compat:" + owner)
	s.mu.Lock()
	generation := s.generations[scopeOwner]
	if generation == 0 {
		generation = 1
	}
	s.generations[scopeOwner] = generation + 1
	s.mu.Unlock()
	scope, err := tasks.NewScopeIdentity(scopeOwner, generation)
	if err != nil {
		return 0, err
	}
	return s.engine.CloseScope(scope)
}

func (s *LegacySubmitter) run(ctx context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
	taskID := tasks.TaskID(string(payload.Data()))
	s.mu.Lock()
	entry := s.pending[taskID]
	s.mu.Unlock()
	if entry == nil {
		return tasks.ResultRef{}, fmt.Errorf("ephemeral task %q is no longer registered", taskID)
	}
	return tasks.ResultRef{}, entry.task.Run(ctx)
}

func (s *LegacySubmitter) reapLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	var timer *time.Timer
	for {
		s.mu.Lock()
		hasPending := len(s.pending) > 0
		s.mu.Unlock()
		if !hasPending {
			select {
			case <-ctx.Done():
				s.cancelPending()
				return
			case <-s.wake:
				continue
			}
		}
		if timer == nil {
			timer = time.NewTimer(20 * time.Millisecond)
		} else {
			timer.Reset(20 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			s.cancelPending()
			return
		case <-s.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		s.reapTerminal()
	}
}

func (s *LegacySubmitter) reapTerminal() {
	s.mu.Lock()
	ids := make([]tasks.TaskID, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		snapshot, ok := s.engine.Snapshot(id)
		if !ok || !snapshot.State().Terminal() {
			continue
		}
		result, ready, err := s.engine.ConsumeResult(id)
		if err != nil || !ready {
			continue
		}
		s.finish(id, resultError(result))
	}
}

func (s *LegacySubmitter) finish(id tasks.TaskID, resultErr error) {
	s.mu.Lock()
	entry := s.pending[id]
	if entry != nil {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if entry == nil {
		return
	}
	if entry.stopCancel != nil {
		entry.stopCancel()
	}
	if entry.task.OnComplete != nil {
		entry.task.OnComplete(resultErr)
	}
}

func resultError(result tasks.TaskResult) error {
	switch result.Outcome() {
	case tasks.OutcomeSucceeded:
		return nil
	case tasks.OutcomeCancelled:
		return context.Canceled
	case tasks.OutcomeTimedOut, tasks.OutcomeExpired:
		return context.DeadlineExceeded
	default:
		message := result.Failure().Message
		if message == "" {
			message = fmt.Sprintf("task failed with outcome %d", result.Outcome())
		}
		return errors.New(message)
	}
}

func (s *LegacySubmitter) cancelPending() {
	s.mu.Lock()
	ids := make([]tasks.TaskID, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_, _ = s.engine.Cancel(id, tasks.CancelShutdown)
	}
	s.reapTerminal()
}

func (s *LegacySubmitter) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
