# Goultroid P1 Execution & Scheduler Audit — AI Implementation Plan

Status: **OPEN — source audit complete; cancellation fault reproduction, sustained R5, and representative R6 measurement pending**

Audit date: 8 October 2026 (Asia/Jakarta).
Repository: `inipew/goultroid`; branch: `test-next`.
Verified audit HEAD: `e91270f7fa0bcca70890b89826590a7854a125f9` — `style(pmpermit): format peer-aware origin helper`.
Audit method: GitHub source and design-plan inspection at the immutable HEAD. No repository checkout, fresh tests, profiler, sustained load, CI checks, code modifications, or production traces were performed in this audit.

Canonical historical plan: [goultroid-telegram-runtime-execution-ai-plan.md](goultroid-telegram-runtime-execution-ai-plan.md).
This is a **follow-up evidence and decision plan**, not a replacement for historical E0–E3 closure or a mandate to redesign TaskEngine.

## 1. Scope and authority boundaries

Trace:
- Direct legacy scheduler: `Scheduler.runLoop → ClaimDueScheduledJobs → Jobs.SubmitOccurrence → Jobs.driveAttempt → TaskEngine.Submit → durable CommitAttemptResult → reconciliation → scheduled_jobs settlement`.
- Redesigned schedule: `Scheduler.runLoop → Jobs.ProcessDueSchedules → SQLite MaterializeDueSchedule → Jobs.driveAttempt → TaskEngine.Submit → durable completion/outbox`.
- Resource-bearing scheduled command: `scheduler.action` wrapper in TaskEngine scheduler pool → child TaskEngine task in general/download/media-process → `Ticket.Wait(ctx)` → wrapper terminal result → Jobs durability.
- Independent in-memory periodic path: periodic coordinator → Jobs occurrence; inspect for wake pressure, but do not conflate with legacy tracked claims.
- Jobs durable maintenance: retry-monitor lane + single recovery/outbox/deferred-deadline coordinator.

Ownership invariant: **TaskEngine alone performs physical execution; Jobs Manager owns occurrence, attempt, lease, durable retry and recovery; Scheduler is the timing/compatibility owner; Telegram RPC goes through the shared executor.** Never introduce another execution loop, worker registry, retry engine, RPC executor, or downloader.

## 2. Source-verified baseline and existing acceptance

1. `internal/taskengine/engine_config.go:62-76` defaults: scheduler concurrency 4/backlog 100, download concurrency 3, media-process 2, general 8; all are zero-idle capable. These are defaults, not a measured live configuration.
2. `internal/app/scheduled_action.go:145-216`: resource-free commands execute in their scheduler wrapper; resource-bearing commands submit a child task, then call `ticket.Wait(ctx)`; a failed wait asks TaskEngine to cancel its child.
3. `internal/scheduler/engine.go:648-758`: wake-driven scheduler loop performs `reconcileSettledClaims` at the start of *every pass*. Legacy claims trigger a fallback safety deadline of 30 seconds; other wake/deadline paths may cause more passes.
4. `internal/scheduler/engine.go:910-963`: active tracked legacy claims each cause a scheduled-job read and, when relevant, an occurrence read. A transient `GetScheduledJob` error now returns **without untracking**; do not reopen the old read-error defect as if it remains present.
5. `internal/jobs/manager_attempt.go:166-212`: attempts prepare a durable lease before TaskEngine submission; submission rejection durably records aborted-before-start and signals recovery. TaskEngine Commit callback persists physical outcomes.
6. `internal/jobs/manager_recovery.go`: Jobs uses one coordinator for low-frequency recovery/outbox/deferred deadlines, with coalesced wakes and a bounded 256-candidate recovery scan.
7. `internal/jobs/manager_schedule.go`: interval catch-up is constant-time and unsupported persisted schedule policies are quarantined. Historical R1–R4 corrections and contracts should not be reimplemented.
8. Historical benchmarks at `706b1a0b` (29 September 2026): R5 four scheduler wrappers running and four child tasks queued under one blocker, but blocker released immediately (about 1.04–1.22 ms child queue p95 and 1.13–1.31 ms attempt p95); **not sustained contention**. R6 SQLite reconciliation: 100 active claims 7.19–7.75 ms/pass; 1,000 active claims 75.60–79.54 ms/pass; two reads/claim/pass. SQLite benchmark used in-memory DB, one connection and synthetic perpetually active claims. These are **not production measurements**.
9. Previous plan records package/race/vet/build/format passes on a prior checkout. They are historical and cannot be claimed for this HEAD.

Evidence links at audit SHA:
- [Scheduler loop and reconciliation](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/scheduler/engine.go)
- [Scheduler compatibility repository](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/scheduler/repository.go)
- [Scheduled action adapter](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/app/scheduled_action.go)
- [Jobs recovery](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/jobs/manager_recovery.go)
- [Jobs attempt and commit](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/jobs/manager_attempt.go)
- [R5 harness](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/app/runtime_execution_e3_r5_test.go)
- [R6 SQLite harness](https://github.com/inipew/goultroid/blob/e91270f7fa0bcca70890b89826590a7854a125f9/internal/scheduler/runtime_execution_e3_benchmark_test.go)

## 3. Audit findings — confirmed code behavior versus conditional hypotheses

### C1 — Cancel can leave stores inconsistent if disabling the redesigned schedule fails

**Source-confirmed ordering; conditional consistency defect. Prioritize a deterministic reproducer before any optimization.**

`internal/scheduler/engine.go:608-633` calls `DeleteScheduledJob` first, `Jobs.DisableSchedule` second, and returns immediately if disabling fails. The cancellation of active TaskEngine scope/legacy tracked occurrence happens *after* that second call. Deleting the compatibility row does not itself disable the independent `job_schedules` row; redesigned timing calls `Jobs.EarliestScheduleDue` and `ProcessDueSchedules`. Thus a failed disable can leave a deleted compatibility row with an enabled durable schedule and can skip active-task cancellation. Actual user-visible duplicate or side effect has **not** been demonstrated.

Reproducer: enable redesigned schedule mode, inject `DisableSchedule` failure after successful compatibility deletion, check both stores and actual due eligibility. Include `ActionJob` and scheduler-owned wrapper, one-shot and recurring cases, and a restart. Then test failure in the opposite direction, ambiguous commit, concurrent due materialization, and retrying cancellation. Demand an explicit recovery/compensation protocol rather than claiming unprovided cross-store atomicity. Do not destroy an already materialized occurrence's recovery history merely to hide inconsistent state.

### C2 — Cancel of already-materialized redesigned ActionJob occurrence

**Unresolved behavioral contract / coverage gap; not yet classified as a bug.**

The same `Cancel` code cancels a scope owned by `scheduler:job:<id>` and a `trackedClaim` only when present. The tracked-claim map belongs to legacy rows; redesigned `ActionJob` points to its caller-owned definition (with its own scope). Disabling the schedule prevents future due slots but does not inherently cancel an already materialized occurrence. Determine whether the user-facing command promises “stop future schedules” or “also stop active execution”; then test queued, active, terminal, and restart states without cancelling unrelated occurrences of the shared ActionJob definition.

Acceptance should express the chosen semantics explicitly and preserve fencing. Do not infer exactly-once Telegram delivery from durable attempt uniqueness: remote effects can be ambiguous on timeout/crash.

### R5 — Scheduler worker occupied while child waits

**Confirmed occupation in synthetic four-wrapper test; live starvation and long-term cost unmeasured.**

Default scheduler pool concurrency is four. When all four wrappers await resource-bearing children, an independent scheduler wrapper can queue. The previous harness releases its resource blocker immediately, so short observed delays cannot justify either “safe” or “critical” labels for sustained production. Test sustained blockers and mixed traffic without changing wrapper architecture first.

### R6 — Reconcile all active legacy claims on every runLoop wake

**Source-confirmed linear read path; performance significance unmeasured.**

Each legacy tracked claim is checked on every scheduler pass; the base 30-second safety wake is not a maximum frequency. With high wake activity, this can repeat scheduled-job plus occurrence reads long before settlement. The prior 1,000-claim SQLite benchmark is synthetic and does not establish actual claim cardinality or wake cadence. Current transient read error does **not** untrack; retain that regression.

### S1 — Recovery, cancellation and settling under pressure

**Acceptance gap, not a demonstrated defect.**

Review interleavings across `Jobs.Recover`, `watchAttempt`, lease expiry, stale TaskID, shutdown/quiesce and scheduler reconciliation. Current code has bounded recovery batches, tracked-owner exclusion and writer-fenced lease operations. Test these invariants under concurrent load rather than describing them as broken.

## 4. Mandatory execution discipline

- **Refresh `test-next` HEAD before every phase**. Record full SHA, commit message, new relevant changes and whether the audit baseline still applies. Never silently work from the 8 October audit SHA if HEAD moves.
- **Audit first, no production Go changes** until a reproducer or representative measurement supports a narrow fix. Do not redesign TaskEngine by default.
- Before **every commit changing Go**, run `gofmt` on all affected Go files; verify `gofmt -l` is empty, `git diff --check` is clean, and run relevant focused tests. Adjust affected tests/fixtures and run targeted race tests when concurrency code changes. Never claim a test passed unless it actually ran.
- **Never check, poll or report CI** unless explicitly requested by the user.
- One Jobs durable authority, one TaskEngine execution authority, one Telegram RPC executor. No second registry, worker system, retry scheduler, unmanaged goroutine-per-attempt, polling loop or unbounded cache/metrics.
- Keep state and cardinality bounded, idle close to zero, timeout/cancel semantics preserved, and restart/reload safety explicit. Preserve migration compatibility and shared-SQLite versus alternate-store behavior.
- Make small phase-specific changes; update this document with exact commits, tested commands, results, limitations, and unresolved items. Do not close a phase based only on source inspection.
- Use production-like SQLite file-backed/WAL and actual connection settings for representative cost tests; retain the existing single-connection in-memory benchmark as an isolated comparative control.

## 5. Proposed phases and gates

### P1-A — Current HEAD and execution topology inventory [OPEN]

Read the old runtime-execution plan Sections 6 and R5/R6 acceptance, then inspect only changed call paths:
- `internal/scheduler/{engine,repository,registration_*}.go`
- `internal/jobs/{manager,manager_attempt,manager_retry,manager_recovery,manager_schedule}.go` and `internal/jobs/sqlite`
- `internal/taskengine`, `internal/admission`, and `internal/app/scheduled_action.go`
- production wiring for schedule-cutover mode, DB pool, Job retry/handlers, TaskEngine pool overrides, and shutdown ordering.

Deliver an authority/ownership diagram, observed versus configured defaults, the legacy/redesigned split, and an invariant matrix. No coding.

### P1-B — Cancellation failure and in-flight semantics [OPEN / correctness gate]

1. Reproduce C1 with a deterministic fail-on-disable Jobs store after compatibility deletion.
2. Record post-failure `scheduled_jobs`, `job_schedules`, `job_occurrences`, `job_attempts`, TaskEngine scope state; restart and verify whether execution can still become eligible.
3. Test successful and failed cancellation of `ActionJob` (shared target) and wrapper, queued/running/terminal occurrence, plus disable failure and concurrent materialization.
4. If demonstrated, implement the **smallest** compensation/order/recovery change consistent with shared/alternate stores, and add failpoint + restart regressions before declaring a fix.
5. Distinguish stop-future-slots contract from stop-inflight contract. Do not cancel unrelated shared target invocations.

Gate: no executable orphan schedule after a confirmed successful cancellation; on uncertain errors report partial state and guarantee deterministic convergence; respect declared in-flight semantics and durable attempt fencing.

### P1-C — R5 sustained-contention acceptance [OPEN / measurement gate]

Start from `TestRuntimeExecutionE3_ResourceScheduledCommandOccupiesSchedulerPoolWhileChildQueued` and existing benchmark. Add a controlled *sustained* blocker and mixed workload:
- 1, 2, 4, 8 resource-bearing wrappers; download and media saturation; resource-free scheduled command control; direct `ActionJob` control.
- Hold saturation for bounded windows (e.g., 5, 15, 30 seconds), observe independent scheduler work, then release and observe catch-up and settling. Use cancellation-aware barriers to avoid flaky wall-clock sleeps.
- Measure scheduler occupied workers and backlog, child admitted/queued/running, child queue p50/p95/p99, wrapper attempt p50/p95/p99, unrelated scheduled action tail latency, memory/goroutine settled, cancellation/timeout outcome.
- Repeat in cold and warmed pools; compare configured default pool sizes and a realistic mixed workload.

Decision: if all four scheduler workers are blocked for material periods and independent due work misses an agreed service budget, prototype **one** minimal orchestration change and compare before/after. Otherwise document the measured safe envelope and leave code unchanged. Never substitute an extra executor or detached retry worker.

### P1-D — R6 claim cardinality and wake-frequency acceptance [OPEN / measurement gate]

Keep historical 1/100/1,000-claim benchmarks as controls. Measure with representative records and wake sources:
- Modes: zero claims, realistic low/mid/high *legacy* claim counts, cutover/redesigned-only, mixed schedules.
- Track `trackedClaimCount` distribution, wakes/pass by source, total reconciliation passes/sec, `GetScheduledJob` and `GetOccurrence` calls/sec, DB query p50/p95, wall time/pass, application CPU, lock waits and pool waits.
- Compare idle, due-burst, active long task, frequent completion signals, steady-state, settling.
- File-backed SQLite/WAL production-like connection pool; never extrapolate one-connection `:memory:` microbenchmark directly to a live DB.
- Inject transient read error and cancellation while a reconciliation pass is in flight. Retain recovery safety and no-drop-on-read-error tests.

Decision: calculate cost as **live claims × actual passes × reads/claim**, and compare to measured CPU/DB budgets. If material, add due-only or transition-driven bounded reconciliation plus a recovery sweep through the existing timing owner; no unbounded per-claim timers, watchers, or new polling goroutine. Benchmark before/after.

### P1-E — Restart, lease, shutdown and resource acceptance [OPEN]

Test restart at: prepared schedule, materialized occurrence, leased attempt, child queued, child running, Commit uncertain, cancel in progress, deferred retry, forced stop. Verify Jobs durable convergence, lease fencing, cancellation semantics, no extra logical occurrence from the same slot, bounded worker/queue/resource accounting and eventual idle settling. Separate repeated remote Telegram side effects caused by ambiguous commits from guaranteed durable-ID dedupe; mock RPC externally as needed.

Require focused unit tests, targeted race, recovery simulation, and only then broader `go test -race ./...`, `go vet ./...` and build for code-changing phases. Do **not** check CI.

### P1-F — Decision and closeout [OPEN]

For each of C1/C2/R5/R6/S1, record one of: FIXED with reproducer, MEASURED ACCEPTABLE with operating envelope, DEFERRED with explicit risk/owner, or NOT REPRODUCED with exact evidence. Include before/after metrics and cost of fixes. Close only after P1-B correctness and P1-C/D sustained measurements are complete, with P1-E appropriate to actual changes.

## 6. Focused commands for a local checkout (not executed during this audit)

~~~sh
git fetch origin test-next
git rev-parse HEAD
git log -1 --format='%H %s'
go test ./internal/scheduler ./internal/jobs ./internal/jobs/sqlite ./internal/taskengine ./internal/app ./internal/admission -count=1
go test ./internal/scheduler -run 'TestRuntimeExecution|TestCancel' -count=1
go test ./internal/app -run 'TestRuntimeExecutionE3' -count=1
go test ./internal/scheduler -run '^$' -bench '^BenchmarkRuntimeExecutionE3_ReconcileSettledClaims(SQLite)?$' -benchtime=500ms -count=3
go test ./internal/app -run '^$' -bench '^BenchmarkRuntimeExecutionE3_ResourceScheduledCommandContention$' -benchtime=5x -count=3
~~~

Commands above are suggested existing baseline runs, **not** sufficient sustained R5/R6 acceptance. Extend only the targeted harness once an explicit measurement design is agreed. For Go-changing work, run `gofmt -w` on each changed Go file before its commit, check `gofmt -l` and `git diff --check`, then adjust and run affected tests and targeted race tests. Do not poll CI.

## 7. Next AI session fast-start

1. Refresh and record real `test-next` HEAD + commit message.
2. Read this document Sections 3–5 and prior runtime-execution plan's R5/R6 closure note (29 September); do not reopen historical R1–R4 absent new evidence.
3. Start **P1-B cancellation fault reproduction** before performance refactoring; test source-level C1 hypothesis and stop-future versus in-flight ActionJob behavior.
4. Then perform P1-C sustained contention and P1-D representative wake/SQLite cost measurement. No speculative TaskEngine redesign.
5. Do not change production code without a deterministic repro or measured bottleneck. Keep the existing single-authority boundaries, `gofmt` discipline, adjusted tests, and no CI checks.
