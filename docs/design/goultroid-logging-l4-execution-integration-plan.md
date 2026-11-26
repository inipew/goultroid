# Goultroid Logging Architecture Overhaul — L4 Execution Pipeline Integration Plan

- **Phase:** L4 (Design Document — No Production Code Modified Yet)
- **Exact Baseline HEAD:** `dd585a7f5a947cca9e6c3f6735d5944a5972b201` (`origin/test-next`)
- **Master Plan:** [goultroid-logging-overhaul-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-overhaul-plan.md)
- **L1 Contract:** [goultroid-logging-l1-architecture-contract.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l1-architecture-contract.md)

---

## 1. Exact Baseline HEAD
- Repository: `https://github.com/inipew/goultroid`
- Branch: `test-next`
- Verified Commit: `dd585a7f5a947cca9e6c3f6735d5944a5972b201` (L0–L3 complete).

---

## 2. Actual Execution Pipeline Map

```text
Ingress / Request / Timer
   │
   ├─► internal/scheduler (Engine, periodicCoordinator)
   │      │  Due schedules, misfire policy, occurrence materialization
   │      ▼
   ├─► internal/jobs (Manager, PersistencePump, Recovery)
   │      │  Logical JobOccurrence -> Physical JobAttempt (lease, retry, deferral)
   │      ▼
   ├─► internal/taskengine (Engine: Admission -> Dispatch -> executeAssignment -> Completion)
   │      │  Pool fairness, resource permits, worker execution, panic/timeout/cancel
   │      ▼
   └─► internal/telegram (RPCExecutor.Do)
          │  HierarchicalRPCLimiter, FloodWait, retry/yield, MTProto RPC
          ▼
     Execution Result (tasks.TaskResult / execution.Semantics)
```

---

## 3. Existing Logger & Observability Inventory (L4-A)

| Component | Current Logger State | Current Gaps |
| --- | --- | --- |
| **`internal/taskengine`** | **None** (0 logger references) | Admission rejections, queue saturation, worker panics, timeouts, and execution failures are only returned via `TaskResult` / `RuntimeStats`; no structured error/panic logs. |
| **`internal/jobs`** | `*zap.Logger` via `Manager.SetLogger` (used only in `manager_recovery.go:151`) | `manager.go`, `manager_attempt.go`, `manager_retry.go`, `persistence_pump.go` emit zero structured events for logical job vs physical attempt transitions. |
| **`internal/scheduler`** | `*zap.Logger` in `Engine` (24 sites) and `periodicCoordinator` (5 sites) | Directly coupled to `go.uber.org/zap`; logs `job_id` / `task` without `correlation_id`, `operation`, or `error_category`. |
| **`internal/telegram/rpc_executor.go`** | **None** (only `RPCMetrics` counters) | No structured logging for FloodWait duration, retry decisions, rate-limit yield, or terminal RPC failures. |
| **`internal/app/wiring_core.go` & `wiring_services.go`** | Passes `logger.Named("jobs")` and raw `logger` to `scheduler.NewEngine` | `TaskEngine` and `RPCExecutor` receive no logger; `Scheduler` & `Jobs` still receive raw `*zap.Logger`. |

---

## 4. Event Ownership Matrix (Eliminating Duplicate Logging)

To prevent the same failure from being logged 3 times across `Scheduler`, `JobsManager`, and `TaskEngine`:

| Event / Transition | Single Authoritative Owner | Level | Notes |
| --- | --- | --- | --- |
| Schedule misfire, periodic admission rejection, claim/reconcile DB errors | `internal/scheduler` | `Warn` / `Error` | Scheduler owns schedule & timer decisions. |
| Logical job lifecycle (`job_cancelled`, `job_completed`, `job_blocked`, `recovery_pass_failed`) | `internal/jobs` | `Info` / `Warn` / `Error` | JobsManager owns `JobOccurrence` state transitions. |
| Physical attempt retry/deferral (`attempt_retry_scheduled`, `attempt_deferred`, `attempt_exhausted`) | `internal/jobs` | `Warn` / `Error` | Logged once per attempt outcome when `Manager` commits `TaskResult`. |
| Admission rejection, queue saturation, worker panic, unexpected timeout | `internal/taskengine` | `Warn` / `Error` | Logged only on abnormal outcomes (never on normal task completion). |
| FloodWait, local limiter yield, RPC retry, terminal RPC failure | `internal/telegram.RPCExecutor` | `Warn` / `Error` | Never logs successful RPCs at `Info`; never logs raw RPC args/payloads. |

---

## 5. Context & Correlation Propagation (L4-B)

- **Helpers Used:** `logging.NewCorrelationID()`, `logging.WithCorrelationID()`, `logging.CorrelationID()`, `logging.WithCausationID()`, `logging.CausationID()`, `logging.ContextFields(ctx)` (and `core.WithCorrelationID` / `core.GetCorrelationID` aligned to the same context key or bridged cleanly).
- **Rules:**
  1. **Single logical operation = one `correlation_id`:** Created at ingress (or scheduler tick/periodic trigger when no upstream context exists) and preserved across `TaskEngine.Submit(ctx, ...)`, `JobsManager`, and `RPCExecutor.Do(ctx, ...)`.
  2. **Retries preserve `correlation_id`:** `JobAttempt` retries and `RPCExecutor` retries reuse the occurrence's `correlation_id` (and may attach `causation_id` for child/follow-up operations).
  3. **Restart recovery without DB schema migration:** Store optional `correlation_id` in existing JSON metadata (`attemptResultMetadata` / `JobDefinition.Payload` envelope where applicable, or generate a new recovery `correlation_id` per recovered occurrence without adding SQLite columns or migrations).
  4. **Never substitute IDs:** `job_id`, `occurrence_id`, `attempt_no`, `task_id`, and Telegram `msg.ID` are logged as their own bounded fields—never as `correlation_id`.

---

## 6. TaskEngine Integration (L4-C)

- **Injection:** Add `Logger logging.Logger` to `taskengine.Config` (or `EngineOption`, defaulting to `logging.Nop()`).
- **Logged Events (abnormalities & lifecycle only):**
  - Admission rejected / queue saturated (`Warn`, with `pool`, `class`, `error_category`, `correlation_id`).
  - Worker panic recovered in `executeAssignment` (`Error`, with `logging.Panic(...)`, `task_id`, `correlation_id`).
  - Execution timeout / cancellation / lease lost (`Warn` on abnormal lease loss or timeout, `Debug` on expected user/shutdown cancellation).
  - Unexpected handler failure (`Error` when `Disposition == DispositionInternal`).
- **Strictly Excluded:** No logging on queue polling, worker idle wake/sleep, permit acquisition, or normal task completion at `Info`. Fairness and admission policies remain untouched.

---

## 7. JobManager Integration (L4-D)

- **Migration:** Replace `*zap.Logger` / `SetLogger(*zap.Logger)` on `jobs.Manager` with `logging.Logger` (`SetLogger(logging.Logger)` / `WithLogger`, defaulting to `logging.Nop()`).
- **Logical vs Physical Separation:**
  - **Logical (`JobOccurrence`):** `occurrence_created` (`Debug`), `occurrence_completed` (`Debug`), `occurrence_cancelled` (`Info`), `occurrence_blocked` (`Error`).
  - **Physical (`JobAttempt`):** `attempt_retry_scheduled` (`Warn`, with `attempt_no`, `retry_after`, `error_category`), `attempt_deferred` (`Warn`, FloodWait/resource deferral), `attempt_exhausted` (`Error`), `job_recovery_failed` (`Warn`, replacing raw Zap in `manager_recovery.go`).
- **No duplicate SQLite queries:** Log using fields already in memory during `commitAttemptResult`, `scheduleRetry`, and `recover`.

---

## 8. Scheduler Integration (L4-D)

- **Migration:** Migrate `internal/scheduler/engine.go` and `internal/scheduler/periodic_timer.go` from `*zap.Logger` to `logging.Logger` (default `logging.Nop()`).
- **Standardization:**
  - Attach `logging.Component("scheduler")`, `logging.Operation(...)`, `logging.Event(...)`, `logging.ErrorCategoryField(...)`, and `logging.ContextFields(ctx)` to all 29 existing log sites.
  - Ensure each scheduler tick/materialization generates or propagates a `correlation_id` in `ctx` before calling `jobsMgr`.

---

## 9. RPCExecutor Integration (L4-E)

- **Injection:** Add `Logger logging.Logger` to `RPCExecutorConfig` (default `logging.Nop()`).
- **Logged Events:**
  - Telegram `FloodWait` (`Warn`, with `rpc_method`, `rpc_kind`, `flood_wait`, `will_retry`, `will_yield`, `correlation_id`).
  - Local rate-limiter rejection/yield (`Debug` or sampled `Warn` when durable work yields).
  - Retry decision (`Warn`, with `attempt`, `backoff`, `error_category`).
  - Terminal RPC failure or context cancellation (`Error` on terminal failure; `Debug` on caller cancellation, with `duration` and `error_category`).
- **Invariants:** `RPCExecutor` remains the sole owner of retry/FloodWait policy. Successful RPCs are never logged at `Info`/`Warn`.

---

## 10. Error Classification & Retry Semantics

All 4 packages use `logging.ClassifyError(err)` (and `execution.SemanticsOf(err)` where available) to emit consistent `error_category` and `retryable` fields:
- `FloodWait` / `ErrRateLimited` -> `telegram_rpc`, `retryable=true`
- `context.Canceled` -> `cancellation`, `retryable=false`
- `context.DeadlineExceeded` -> `cancellation`, `retryable=true`
- `ErrResourceLimit` / queue full -> `resource`, `retryable=true`
- SQLite busy/locked -> `persistence`, `retryable=true`

---

## 11. Security & Redaction

- All fields pass through `logging.Logger` (`zaplog.Adapter` + `Redactor`).
- `JobDefinition.Payload`, `JobAttempt.Result`, raw Telegram RPC request/response structs, peer access hashes, and phone numbers are **never** logged.
- Errors are sanitized via `logging.Error(err)`; panics via `logging.Panic(...)`.

---

## 12. Performance Constraints

- **Zero new goroutines, queues, or SQLite queries.**
- **Fast disabled-level check:** `logging.Logger` / `zaplog.Adapter` checks `z.Check(level, msg)` before converting fields.
- **No per-call correlation allocation:** `CorrelationID(ctx)` only reads `ctx.Value`; IDs are generated only at ingress/tick boundaries when absent.
- **Hot-path benchmarks:** Measure `TaskEngine.Submit` + `RPCExecutor.Do` before and after L4 integration to verify negligible allocation/latency overhead.

---

## 13. Planned File Changes (Implementation Phases L4-B – L4-F)

| File | Phase | Change |
| --- | --- | --- |
| `internal/core/correlation.go` | L4-B | Align/delegate `WithCorrelationID` / `GetCorrelationID` with `internal/logging` context keys so both packages share one `correlation_id`. |
| `internal/taskengine/engine_config.go`, `engine.go`, `engine_admission.go`, `executor.go`, `engine_completion.go` | L4-C | Add `Logger logging.Logger` to `Config`; log admission rejections, panics, and abnormal failures. |
| `internal/jobs/manager.go`, `manager_attempt.go`, `manager_retry.go`, `manager_recovery.go` | L4-D | Switch `SetLogger` to `logging.Logger`; log logical vs attempt transitions and recovery errors. |
| `internal/scheduler/engine.go`, `periodic_timer.go` | L4-D | Migrate 29 Zap sites to `logging.Logger` with standard schema + correlation context. |
| `internal/telegram/rpc_executor.go`, `client.go` | L4-E | Add `Logger` to `RPCExecutorConfig`; log FloodWait, retry, yield, and terminal failures. |
| `internal/app/wiring_core.go`, `wiring_services.go`, `wiring_telegram.go` | L4-C/D/E | Inject `opLogger.Named(...)` into `TaskEngine`, `JobsManager`, `Scheduler`, and `RPCExecutor`. |
| `*_test.go` & `p2_hotpath_benchmark_test.go` | L4-F | Correlation propagation tests, FloodWait/retry logging tests, panic redaction tests, and hot-path benchmarks. |

---

## 14. Test & Benchmark Plan (L4-F)

- **Cross-layer correlation test:** Verify `Ingress -> TaskEngine -> JobsManager -> RPCExecutor` preserves one `correlation_id` across retries.
- **Deduplication test:** Verify a single failed attempt does not emit duplicate `Error` logs from both `TaskEngine` and `JobsManager`.
- **RPCExecutor test:** Verify `FloodWait` and terminal RPC errors log `error_category=telegram_rpc` without leaking payloads.
- **Benchmarks:** Run `go test -bench=. -benchmem ./internal/taskengine/... ./internal/telegram/...` before and after integration.
- **Full checks:** `go test -race ./internal/core/... ./internal/taskengine/... ./internal/jobs/... ./internal/scheduler/... ./internal/telegram/... ./internal/app/... ./internal/architecture/...`, `go vet`, `gofmt`, `go build ./cmd/goultroid`.

---

## 15. Acceptance Criteria

- [ ] One logical operation is traceable end-to-end via `correlation_id` across `Scheduler`, `JobsManager`, `TaskEngine`, and `RPCExecutor`.
- [ ] Retries preserve `correlation_id` and distinguish logical job vs physical attempt.
- [ ] No duplicate error logging across layers; no per-RPC or per-loop spam.
- [ ] Zero Zap imports in `internal/taskengine`, `internal/jobs`, `internal/scheduler`, and `rpc_executor.go`.
- [ ] Zero new goroutines, queues, or SQLite queries.
- [ ] All race, architecture, vet, benchmark, and build checks pass.

---

## 16. Risks & Rollback Strategy

- **Risk:** Switching `jobs.Manager.SetLogger` / `scheduler.NewEngine` signatures could affect tests passing `*zap.Logger`.
  - *Mitigation:* Update in-repo test callers in the same phase; default `logging.Nop()` handles nil loggers safely.
- **Rollback:** Each subsystem (`TaskEngine`, `Jobs/Scheduler`, `RPCExecutor`) is wired independently in `internal/app/wiring_*.go` and can be reverted per package without touching L2/L3 foundations.
