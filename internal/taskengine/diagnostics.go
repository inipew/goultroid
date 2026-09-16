package taskengine

import "github.com/inipew/goultroid/internal/tasks"

// snapshotPoolRuntimeStats builds one internally-consistent pool snapshot from
// state owned by the TaskEngine run loop. Waiting is sourced from admission,
// while Running/Dispatching are sourced from the task registry so those states
// reflect the task lifecycle rather than physical worker liveness.
func (e *Engine) snapshotPoolRuntimeStats() map[tasks.PoolID]PoolRuntimeStats {
	pools := make(map[tasks.PoolID]PoolRuntimeStats, len(e.poolConcurrencies))
	for pool, maximum := range e.poolConcurrencies {
		waiting, waitingBytes := e.adm.PoolStats(pool)
		workers := runningCount(e.workerRunning[pool])
		idle := len(e.idleSlots[pool])
		pools[pool] = PoolRuntimeStats{
			Workers:      workers,
			MinWorkers:   e.poolMinWorkers[pool],
			MaxWorkers:   maximum,
			Idle:         idle,
			IdleWorkers:  idle,
			Waiting:      waiting,
			WaitingBytes: waitingBytes,
		}
	}

	for _, rec := range e.registry {
		if rec == nil {
			continue
		}
		stats, ok := pools[rec.spec.Pool]
		if !ok {
			continue
		}
		switch rec.state {
		case tasks.StateRunning:
			stats.Running++
		case tasks.StateDispatching:
			stats.Dispatching++
		default:
			continue
		}
		pools[rec.spec.Pool] = stats
	}
	return pools
}
