# Goultroid Logging Overhaul — L4 Execution Pipeline Plan (L4-A – L4-D)

- **Status:** Active
- **Baseline HEAD:** `76a5f0df3c0049d640e9147302ffd7067250239d` (`origin/test-next`)
- **Predecessor:** [goultroid-logging-boundary-revalidation-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-boundary-revalidation-plan.md)
- **Reference:** [goultroid-logging-l4-execution-integration-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l4-execution-integration-plan.md)

---

## 1. Exact Baseline HEAD
- Repository: `https://github.com/inipew/goultroid`
- Branch: `test-next`
- Verified Commit: `76a5f0df3c0049d640e9147302ffd7067250239d`

---

## 2. Verified Zap Inventory in `jobs` and `scheduler`

| Package | File | Current Zap Usage |
| --- | --- | --- |
| **`internal/jobs`** | `internal/jobs/manager.go` | `logger *zap.Logger` field + `SetLogger(logger *zap.Logger)` |
| **`internal/jobs`** | `internal/jobs/manager_recovery.go` | 1 direct `logger.Warn("job recovery pass failed", ...)` call (`zap.String`, `zap.Int`, `zap.Error`) |
| **`internal/scheduler`** | `internal/scheduler/engine.go` | `logger *zap.Logger` field + `NewEngine(db, logger *zap.Logger)` + **24** direct Zap calls |
| **`internal/scheduler`** | `internal/scheduler/periodic_timer.go` | `logger *zap.Logger` field + `newPeriodicCoordinator(logger *zap.Logger)` + **5** direct Zap calls |
| **`internal/taskengine`** | *(all)* | **0** logger references |
| **`internal/telegram`** | `rpc_executor.go` | **0** logger references (`RPCMetrics` only) |

---

## 3. Event Mapping & Correlation Flow

```text
Ingress / Scheduler Tick
  └─► logging.WithCorrelationID(ctx, id)
        ├─► internal/scheduler (L4-B)
        │     Events: scheduler_started, scheduler_stopped, periodic_admission_rejected,
        │             schedule_misfire_skipped, schedule_claim_failed, schedule_reconcile_failed
        ├─► internal/jobs (L4-A)
        │     Events: job_recovery_failed, attempt_retry_scheduled, attempt_deferred,
        │             attempt_exhausted, occurrence_blocked, occurrence_cancelled
        ├─► internal/taskengine (L4-C)
        │     Events: task_admission_rejected, task_panic_recovered, task_execution_failed,
        │             task_timeout
        └─► internal/telegram.RPCExecutor (L4-D)
              Events: rpc_flood_wait, rpc_rate_limit_yield, rpc_retry_scheduled,
                      rpc_failed
```

Every event emits only relevant, low-cardinality fields: `component`, `operation`, `event`, `correlation_id` (and `causation_id` when child work is spawned), `error_category`, `retryable`, and `duration_ms` (where applicable), with all errors sanitized via `logging.Error(err)`.

---

## 4. Subphase Sequence (L4-A – L4-D)

| Subphase | Target | Key Deliverables |
| --- | --- | --- |
| **L4-A** | `internal/jobs` | Replace `*zap.Logger` / `SetLogger` with `logging.Logger` (default `logging.Nop()`); log logical occurrence vs physical attempt transitions (`attempt_retry_scheduled`, `attempt_deferred`, `attempt_exhausted`, `occurrence_blocked`, `job_recovery_failed`); update `internal/app/wiring_core.go` and `internal/jobs/*_test.go`; zero Zap imports in `internal/jobs`. |
| **L4-B** | `internal/scheduler` | Replace `*zap.Logger` in `Engine` & `periodicCoordinator` with `logging.Logger` (default `logging.Nop()`); migrate all 29 Zap sites to standard schema + `correlation_id` propagation; update `internal/app/wiring_services.go` and `internal/scheduler/*_test.go`; zero Zap imports in `internal/scheduler`. |
| **L4-C** | `internal/taskengine` | Add `Logger logging.Logger` to `taskengine.Config` (default `logging.Nop()`); align `internal/core/correlation.go` with `internal/logging`; log admission rejection, panic recovery, timeout, and internal handler failure without per-loop spam; add hot-path benchmarks. |
| **L4-D** | `internal/telegram` (`RPCExecutor`) | Add `Logger logging.Logger` to `RPCExecutorConfig` (default `logging.Nop()`); log FloodWait, durable limiter yield, retry, and terminal RPC failure without raw args/payloads or per-success spam; add benchmarks. |

---

## 5. Architectural Constraints & Verification Criteria

- **Constraints:**
  - TaskEngine remains sole owner of admission, fairness, execution, and resource accounting.
  - JobManager remains sole owner of durable logical work and physical attempts.
  - Scheduler creates no new executor or retry engine.
  - FloodWait & retry remain solely in `telegram.RPCExecutor`.
  - No new goroutines, logging queues, registries, or SQLite queries/migrations.
  - Default logger is always `logging.Nop()`.
- **Verification per subphase:**
  - Zero `go.uber.org/zap` imports in migrated packages (`go list -f '{{.Imports}}'`).
  - `gofmt`, `go test -race`, `go vet`, `internal/architecture`, `go build ./cmd/goultroid`.
  - Regression tests for key events, correlation propagation, and error redaction.
  - Hot-path benchmarks in L4-C and L4-D.

---

## 6. Performance & API Compatibility Risks

- **API compatibility:** `jobs.Manager.SetLogger` and `scheduler.NewEngine` currently accept `*zap.Logger`. Changing them to `logging.Logger` requires updating in-repo wiring (`internal/app/wiring_*.go`) and test callers in the same subphase.
- **Performance:** `logging.Nop()` default ensures zero overhead when unconfigured; active loggers only emit on transitions/abnormalities (never on normal task/RPC success or polling ticks).
