package taskengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) sweepExpired(pool tasks.PoolID, now time.Time) {
	expired := e.adm.PopExpired(pool, now)
	for _, entry := range expired {
		rec, ok := e.registry[entry.Spec.ID]
		if !ok || rec.state != tasks.StateQueued {
			continue
		}
		rec.state = tasks.StateTimedOut
		rec.finishedAt = now
		rec.errorMsg = "queue deadline expired before execution"
		rec.result = tasks.TaskResult{TaskID: entry.Spec.ID, Outcome: tasks.OutcomeTimedOut, Cause: tasks.CauseQueueExpired, Disposition: execution.DispositionRetryable, FinishedAt: now, Failure: tasks.FailureInfo{Code: "queue_expired", Message: rec.errorMsg}}
		bounded, delta := e.boundResult(rec.result)
		rec.result = bounded
		rec.retainedBytes += delta
		e.retainedBytes += delta
		e.settleTerminal(rec)
	}
	e.evictExpiredTerminal(now)
}

// tryDispatch assigns queued work to idle physical slots.
func (e *Engine) tryDispatch(pool tasks.PoolID) {
	if e.rootCtx == nil || e.rootCtx.Err() != nil {
		return
	}
	e.sweepExpired(pool, time.Now().UTC())
	if waiting, _ := e.adm.PoolStats(pool); waiting > 0 && len(e.idleSlots[pool]) == 0 {
		e.spawnNextWorker(pool)
	}
	for len(e.idleSlots[pool]) > 0 {
		candidate, err := e.adm.SelectCandidateEligible(pool, e.resourcesAvailable)
		if err != nil {
			break
		}
		rec := e.registry[candidate.Spec.ID]
		if rec == nil || rec.state != tasks.StateQueued {
			continue
		}
		slotID := e.idleSlots[pool][0]
		e.idleSlots[pool] = e.idleSlots[pool][1:]
		e.workerIdleSince[pool][slotID] = time.Time{}
		e.dispatchEpoch++
		gen := e.poolGenerations[pool]
		permit := newPermit(pool, slotID, gen, rec.spec.ID, e.dispatchEpoch, nil)
		rec.permit = permit
		rec.dispatchEpoch = e.dispatchEpoch
		rec.poolGeneration = gen
		rec.state = tasks.StateDispatching
		e.reserveResources(rec.spec)

		taskCtx, cancel := context.WithCancel(e.rootCtx)
		rec.cancelFunc = cancel
		e.workerMailboxes[pool][slotID] <- workerAssignment{rec: rec, spec: candidate.Spec, permit: permit, taskCtx: taskCtx}
		if rec.cancelRequested {
			cancel()
		}
	}
	if waiting, _ := e.adm.PoolStats(pool); waiting > 0 && len(e.idleSlots[pool]) == 0 {
		e.spawnNextWorker(pool)
	}
}

func (e *Engine) spawnNextWorker(pool tasks.PoolID) bool {
	runningCount := 0
	for _, running := range e.workerRunning[pool] {
		if running {
			runningCount++
		}
	}
	if runningCount >= e.poolConcurrencies[pool] {
		return false
	}
	for slot, running := range e.workerRunning[pool] {
		if !running {
			if e.spawnWorker(pool, slot, e.rootCtx) {
				e.idleSlots[pool] = append(e.idleSlots[pool], slot)
				return true
			}
			return false
		}
	}
	return false
}

func (e *Engine) applyPoolConfig(pool tasks.PoolID, cfg PoolEngineConfig) error {
	hardMax := len(e.workerMailboxes[pool])
	if hardMax == 0 {
		return fmt.Errorf("taskengine: unknown pool %s", pool)
	}
	if cfg.Concurrency <= 0 || cfg.Concurrency > hardMax || cfg.MinConcurrency < 0 || cfg.MinConcurrency > cfg.Concurrency {
		return errors.New("taskengine: invalid live pool bounds")
	}
	if cfg.IdleTimeout < 0 {
		return errors.New("taskengine: live pool idle timeout cannot be negative")
	}
	if cfg.BacklogLimit < 0 {
		return errors.New("taskengine: live pool backlog limit cannot be negative")
	}
	if cfg.PayloadBudget < 0 {
		return errors.New("taskengine: live pool payload budget cannot be negative")
	}
	cfg.MinConcurrency = effectivePoolMinimum(cfg, cfg.Concurrency)
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = e.poolIdleTimeouts[pool]
	}
	if err := e.adm.SetPoolConfig(pool, admission.PoolConfig{BacklogLimit: cfg.BacklogLimit, PayloadBudget: cfg.PayloadBudget}); err != nil {
		return err
	}
	e.config.Pools[pool] = cfg
	e.poolConcurrencies[pool] = cfg.Concurrency
	e.poolMinWorkers[pool] = cfg.MinConcurrency
	e.poolIdleTimeouts[pool] = cfg.IdleTimeout
	e.retireExcessWorkers(pool)
	now := time.Now().UTC()
	e.sweepIdleWorkers(now)
	for runningCount(e.workerRunning[pool]) < cfg.MinConcurrency {
		if !e.spawnNextWorker(pool) {
			break
		}
	}
	return nil
}

func runningCount(slots []bool) int {
	count := 0
	for _, running := range slots {
		if running {
			count++
		}
	}
	return count
}

func (e *Engine) retireExcessWorkers(pool tasks.PoolID) {
	maximum := e.poolConcurrencies[pool]
	for runningCount(e.workerRunning[pool]) > maximum && len(e.idleSlots[pool]) > 0 {
		slotID := e.idleSlots[pool][len(e.idleSlots[pool])-1]
		if !e.retireWorker(pool, slotID) {
			return
		}
	}
}

func (e *Engine) applyResourceCapacity(name string, capacity int64) error {
	if name == "" || capacity <= 0 {
		return errors.New("taskengine: resource name and positive capacity required")
	}
	if used := e.resourceUsed[name]; capacity < used {
		return fmt.Errorf("taskengine: resource %s currently uses %d", name, used)
	}
	e.resourceCapacity[name] = capacity
	for pool := range e.config.Pools {
		e.tryDispatch(pool)
	}
	return nil
}

func (e *Engine) resourcesAvailable(spec tasks.WorkSpec) bool {
	for _, requirement := range spec.Resources {
		if e.resourceUsed[requirement.Name]+requirement.Amount > e.resourceCapacity[requirement.Name] {
			return false
		}
	}
	return true
}

func (e *Engine) reserveResources(spec tasks.WorkSpec) {
	for _, requirement := range spec.Resources {
		e.resourceUsed[requirement.Name] += requirement.Amount
	}
}

func (e *Engine) releaseResources(spec tasks.WorkSpec) {
	for _, requirement := range spec.Resources {
		e.resourceUsed[requirement.Name] -= requirement.Amount
		if e.resourceUsed[requirement.Name] <= 0 {
			delete(e.resourceUsed, requirement.Name)
		}
	}
}

func (e *Engine) markWorkerIdle(pool tasks.PoolID, slotID int) {
	if slotID < 0 || slotID >= len(e.workerRunning[pool]) || !e.workerRunning[pool][slotID] {
		return
	}
	e.workerIdleSince[pool][slotID] = time.Now().UTC()
	for _, existing := range e.idleSlots[pool] {
		if existing == slotID {
			return
		}
	}
	e.idleSlots[pool] = append(e.idleSlots[pool], slotID)
	e.retireExcessWorkers(pool)
	for p := range e.config.Pools {
		e.tryDispatch(p)
	}
}

func (e *Engine) retireWorker(pool tasks.PoolID, slotID int) bool {
	runningCount := runningCount(e.workerRunning[pool])
	if runningCount <= e.poolMinWorkers[pool] {
		return false
	}
	index := -1
	for i, idle := range e.idleSlots[pool] {
		if idle == slotID {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	e.idleSlots[pool] = append(e.idleSlots[pool][:index], e.idleSlots[pool][index+1:]...)
	e.workerRunning[pool][slotID] = false
	e.workerIdleSince[pool][slotID] = time.Time{}
	if cancel := e.workerCancels[pool][slotID]; cancel != nil {
		cancel()
		e.workerCancels[pool][slotID] = nil
	}
	return true
}

func (e *Engine) sweepIdleWorkers(now time.Time) {
	for poolID := range e.config.Pools {
		minWorkers := e.poolMinWorkers[poolID]
		idleTimeout := e.poolIdleTimeouts[poolID]
		if idleTimeout <= 0 {
			idleTimeout = 10 * time.Second
		}
		running := runningCount(e.workerRunning[poolID])
		if running <= minWorkers {
			continue
		}
		for i := len(e.idleSlots[poolID]) - 1; i >= 0 && running > minWorkers; i-- {
			slotID := e.idleSlots[poolID][i]
			idleSince := e.workerIdleSince[poolID][slotID]
			if idleSince.IsZero() {
				continue
			}
			if now.Sub(idleSince) >= idleTimeout {
				if e.retireWorker(poolID, slotID) {
					running--
				}
			}
		}
	}
}

func (e *Engine) earliestRetirementDeadline(now time.Time) (time.Time, bool) {
	var earliest time.Time
	hasAny := false
	for poolID := range e.config.Pools {
		minWorkers := e.poolMinWorkers[poolID]
		idleTimeout := e.poolIdleTimeouts[poolID]
		if idleTimeout <= 0 {
			idleTimeout = 10 * time.Second
		}
		running := runningCount(e.workerRunning[poolID])
		if running <= minWorkers {
			continue
		}
		for _, slotID := range e.idleSlots[poolID] {
			idleSince := e.workerIdleSince[poolID][slotID]
			if idleSince.IsZero() {
				continue
			}
			deadline := idleSince.Add(idleTimeout)
			if !hasAny || deadline.Before(earliest) {
				earliest = deadline
				hasAny = true
			}
		}
	}
	return earliest, hasAny
}

// applyWorkerStarted is the fencing point for Dispatching -> Running.
func (e *Engine) applyWorkerStarted(id tasks.TaskID, grant *permit, startedAt time.Time) {
	rec, ok := e.registry[id]
	if !ok || rec.state != tasks.StateDispatching {
		return
	}
	if grant == nil || rec.permit != grant {
		return
	}
	if grant.generation != rec.poolGeneration || grant.dispatchEpoch != rec.dispatchEpoch {
		return
	}
	rec.state = tasks.StateRunning
	rec.startedAt = startedAt
}

// SetCommitPump installs the durable-commit transport. It must be called before Start.
func (e *Engine) SetCommitPump(p CommitPump) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runStarted {
		return
	}
	e.commitPump = p
}

// SetHandlerResolver installs the stable-reference resolver before Start.
func (e *Engine) SetHandlerResolver(resolver HandlerResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.runStarted {
		e.handlerResolver = resolver
	}
}

// ConfigurePool adjusts an adaptive pool within the startup hard maximum.
func (e *Engine) ConfigurePool(ctx context.Context, pool tasks.PoolID, cfg PoolEngineConfig) error {
	reply, err := e.sendControl(ctx, engineRequest{op: opConfigurePool, pool: pool, poolConfig: cfg})
	if err != nil {
		return err
	}
	return reply.err
}

// SetResourceCapacity adjusts a named reservation budget without allowing the
// new limit to fall below current usage.
func (e *Engine) SetResourceCapacity(ctx context.Context, name string, capacity int64) error {
	reply, err := e.sendControl(ctx, engineRequest{op: opSetResourceCapacity, resourceName: name, resourceCapacity: capacity})
	if err != nil {
		return err
	}
	return reply.err
}
