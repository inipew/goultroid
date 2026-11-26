# Goultroid Logging Overhaul — L4 Corrective Review & Hardening Plan

- **Status:** Active (Design Only — No Production Code Modified)
- **Baseline HEAD:** `23b15465e1ac2000c1b4c08937aeb3e434d8e09f` (`origin/test-next`)
- **Predecessor:** [goultroid-logging-l4-execution-pipeline-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l4-execution-pipeline-plan.md)
- **Gate:** L5 remains **On Hold** until all **P2** findings below (L4-C1, L4-C2, L4-D1, L4-B1) and agreed **P3** (L4-A1) are resolved and verified.

---

## 1. Review Findings Summary

| ID | Priority | Component | Finding |
| --- | --- | --- | --- |
| **L4-C1** | **P2** | `internal/taskengine` (`engine_admission.go`) | `admitSubmit()` admission rejections (backpressure, resource unavailable, scope closed, result capacity full, retained memory budget) are not logged; `enqueueRequest()` errors are hardcoded as `CategoryResource` / `retryable=true` even when `context.Canceled` or `context.DeadlineExceeded`. |
| **L4-C2** | **P2** | `internal/taskengine` (`engine_state.go`, `engine_completion.go`) | `taskRecord` does not store `correlationID`, so `task_panic_recovered`, `task_timeout`, and `task_execution_failed` lack `correlation_id`. |
| **L4-D1** | **P2** | `internal/telegram` (`rpc_executor.go`) | Terminal `RPCFailure` returns in stale-peer, transient exhaustion, non-idempotent ambiguous mutation, max-elapsed/deadline backoff, and limiter denial paths do not emit `rpc_failed`. |
| **L4-B1** | **P2** | `internal/scheduler` (`engine.go`, `periodic_timer.go`) & `internal/jobs` (`manager_recovery.go`) | `logWithLevel()`, `periodicCoordinator`, and `runRecoveryPass()` do not attach/propagate per-operation or per-pass `correlation_id`. |
| **L4-A1** | **P3** | `internal/jobs` (`manager.go`, `manager_attempt.go`, `manager_retry.go`) | Planned `occurrence_blocked` and `occurrence_cancelled` operational events are not yet emitted on those terminal occurrence transitions. |

---

## 2. Corrective Remediation Plan (L4-C1 – L4-A1)

### L4-C1: Complete Admission Rejection Logging & Classification (`internal/taskengine`)
- Log `task_admission_rejected` at the **authoritative admission decision** (`admitSubmit` / `sendSubmitControl` reply path) when `rep.err != nil`, in addition to `enqueueRequest` / `rootCtx == nil` failures.
- Classify `err` via `logging.ClassifyError(err)` (with `CategoryResource` + `retryable=true` reserved for actual admission/capacity rejections like `tasks.AdmissionError`, and `CategoryCancellation` for `context.Canceled` / `DeadlineExceeded`).
- **Tests:** Add regression tests for backpressure/capacity rejection and `context.Canceled` classification.

### L4-C2: Correlation Propagation to Completion (`internal/taskengine`)
- Store `correlationID string` (already validated/bounded via `logging.CorrelationID(ctx)`) on `taskRecord` at admission time (never retain `context.Context`).
- Pass `rec.correlationID` into `logAbnormalCompletion(spec, rec.result, rec.correlationID)` so `task_panic_recovered`, `task_timeout`, and `task_execution_failed` emit `correlation_id`.
- **Tests:** Verify `correlation_id` appears on `task_panic_recovered`, `task_timeout`, and `task_execution_failed`.

### L4-D1: Unified Terminal RPC Failure Helper (`internal/telegram/rpc_executor.go`)
- Introduce one helper `logTerminalFailure(meta, class, attempt, startTime, cid, err)` emitting `rpc_failed` (`rpc_method`, `rpc_class`, `attempt`, `duration_ms`, `error_category`, `retryable=false`, `correlation_id`, `logging.Error(err)`).
- Invoke it on all terminal `RPCFailure` returns:
  - Stale peer refresh failure / unavailable / non-idempotent ambiguous stale peer
  - Transient max-attempts exhausted, non-idempotent ambiguous mutation, backoff > `MaxElapsed` / `deadline`, sleep error
  - Limiter reservation denied / exceeded `MaxElapsed` / exceeded deadline
  - Permanent failure
- Keep `rpc_flood_wait` (do not emit duplicate `rpc_failed` on durable FloodWait yield) and `rpc_cancelled` distinct.
- **Tests:** Add regression tests for transient exhaustion, non-idempotent ambiguous mutation, stale peer failure, and limiter denial.

### L4-B1: Correlation on Scheduler, Periodic Coordinator & Recovery Pass
- `internal/scheduler/engine.go`: Update `logWithLevel(ctx, ...)` (or accept `correlationID`) and mint a per-pass `correlation_id` in `process()` / `reconcile()` / `runRecoveryPass()` when no caller context ID is present.
- `internal/scheduler/periodic_timer.go`: Mint/propagate per-tick `correlation_id` in `periodicCoordinator` submission & rejection logs.
- `internal/jobs/manager_recovery.go`: Mint a per-pass `correlation_id` in `runRecoveryPass` and attach it to `job_recovery_failed`.
- **Tests:** Verify `correlation_id` presence on scheduler failure, periodic rejection, and job recovery failure logs.

### L4-A1: `occurrence_blocked` & `occurrence_cancelled` Events (`internal/jobs`)
- Emit `occurrence_cancelled` (at `Info`) and `occurrence_blocked` (at `Error`) at the authoritative occurrence finalization/quarantine transitions in `internal/jobs` (`FinalizeOccurrence` / `BlockOccurrence` call sites) with `job_id`, `occurrence_id`, and `correlation_id`.
- **Tests:** Verify `occurrence_cancelled` / `occurrence_blocked` emission.

---

## 3. Verification Criteria
- `gofmt` on all modified Go files.
- `go test -race ./internal/taskengine/... ./internal/telegram/... ./internal/scheduler/... ./internal/jobs/... ./internal/architecture/...`
- `go vet ./internal/taskengine/... ./internal/telegram/... ./internal/scheduler/... ./internal/jobs/... ./internal/app/...`
- `go build -v ./cmd/goultroid`
- Zero Zap imports in `internal/jobs`, `internal/scheduler`, and `internal/taskengine`.
