package taskengine

import "github.com/inipew/goultroid/internal/tasks"

// snapshotPoolRuntimeStats builds one internally-consistent pool snapshot from
// state owned by the TaskEngine run loop in O(|pools|). Waiting is sourced
// from admission, while Running/Dispatching are maintained incrementally on
// task state transitions so diagnostic polling never scans retained records.
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
			Running:      e.poolRunning[pool],
			Dispatching:  e.poolDispatching[pool],
			Waiting:      waiting,
			WaitingBytes: waitingBytes,
		}
	}
	return pools
}
