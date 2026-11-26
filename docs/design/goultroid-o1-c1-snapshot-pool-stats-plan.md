# O1 — C1 Implementation Plan: Incremental Per-Pool Runtime Counters in `TaskEngine`

## Status

- **Workstream:** O1 — Runtime Performance & Hot-Path Optimization
- **Phase:** Gate C — C1 (`snapshotPoolRuntimeStats` incremental per-pool counters) & C2 (before/after benchmark + race/consistency verification)
- **Baseline branch / HEAD:** `test-next` (`0c1b63bce4012229720ffb55323b9324391add4e`)
- **Prerequisite:** P1 baseline and P2 audit (`docs/design/goultroid-o1-p1-p2-performance-audit-plan.md`) completed; P2 closed as audit.
- **Scope:** Strictly C1 + C2 only. Do **not** bundle `sendSubmitControl` channel pooling, `admitSubmit` allocation reductions, or callback/durability changes.

---

## 1. Problem Statement

`(*Engine).snapshotPoolRuntimeStats()` (`internal/taskengine/diagnostics.go`) currently iterates the entire `e.registry` map on every `Stats()` / `PoolStats()` control request to count tasks in `tasks.StateDispatching` and `tasks.StateRunning` per pool.

Because `e.registry` retains terminal records up to `ResultCapacity` and `snapshotPoolRuntimeStats()` runs on the single-writer coordinator `runLoop`, diagnostic polling scales as $O(|\text{registry}|)$ rather than $O(|\text{pools}|)$ and competes directly with task admission, dispatch, and completion on the coordinator.

## 2. Design & Invariants

Maintain incremental per-pool counters on `Engine`:

- `poolDispatching map[tasks.PoolID]int`
- `poolRunning map[tasks.PoolID]int`

Both maps are owned exclusively by the single-writer coordinator `runLoop` (no new mutexes, no atomics, no new goroutines, no new runtime/pool/request-reply mechanisms).

### 2.1 Centralized state-transition helper

All mutations of `rec.state` inside `internal/taskengine` (`engine_admission.go`, `engine_dispatch.go`, `engine_completion.go`, `durability.go`) go through:

```go
func (e *Engine) setTaskState(rec *taskRecord, next tasks.TaskState)
```

Invariants:
1. If `rec == nil` or `rec.state == next`, no counter changes occur (prevents double increment/decrement on idempotent/duplicate completion or cancel).
2. Leaving `tasks.StateDispatching` decrements `e.poolDispatching[rec.spec.Pool]`.
3. Leaving `tasks.StateRunning` decrements `e.poolRunning[rec.spec.Pool]`.
4. Entering `tasks.StateDispatching` increments `e.poolDispatching[rec.spec.Pool]`.
5. Entering `tasks.StateRunning` increments `e.poolRunning[rec.spec.Pool]`.
6. Terminal eviction (`delete(e.registry, id)`) only occurs on terminal records (never `StateDispatching` or `StateRunning`), and if a defensive cleanup ever removes a non-terminal record in tests/shutdown, `setTaskState` transitions it first.
7. `snapshotPoolRuntimeStats()` reads `e.poolRunning[pool]` and `e.poolDispatching[pool]` directly inside the `for pool, maximum := range e.poolConcurrencies` loop in $O(|\text{pools}|)$ time.

### 2.2 Test-fixture compatibility

Any unit test that constructs `taskRecord` literals directly into `e.registry` (e.g., `diagnostics_test.go`) must either initialize via `setTaskState` or be checked so `snapshotPoolRuntimeStats()` behavior remains consistent.

## 3. Validation Plan (C2)

1. **Apples-to-apples baseline before C1:**
   - Identical submit+wait workload **without** `Stats()`/`Snapshot()` poller vs **with** `Stats()`/`Snapshot()` poller at retained registry sizes **0**, **2,000**, and **10,000** (majority terminal records).
2. **Correctness & consistency tests:**
   - Unit test verifying `poolDispatching` / `poolRunning` match a full `e.registry` scan across `Queued -> Dispatching -> Running -> Completed/Failed/Cancelled/TimedOut/CommitPending -> Evict` (including duplicate completion and cancel-while-dispatching/running).
   - Full `go test -race ./internal/taskengine/...` and `go vet ./internal/taskengine/...`.
3. **After C1 measurement:**
   - Re-run the identical 0 / 2,000 / 10,000 before/after benchmarks and `go tool pprof` to verify `snapshotPoolRuntimeStats` overhead drops to $O(|\text{pools}|)$.
