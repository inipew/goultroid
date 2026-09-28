# Goultroid Telegram Runtime Execution Audit — AI Session Plan

Status: **OPEN — E0 reproducer landed; E1 source fixes and E2 best-effort contract are implemented; R6 correctness fix + measurement harness landed; executable focused/acceptance runs and R5/R6 measurements remain pending**

Audit baseline:

- Branch: `test-next`
- Original baseline HEAD: `d709ac47` (`docs(telegram): add audit handoff plan`)
- Source revalidation HEAD: `ad42f4bf4725f42219ca5b93803ebe0faaf4cf22` on `test-next` (28 September 2026)
- Audit date: 28 September 2026
- Evidence: original source inspection and microbenchmarks, plus current source revalidation and targeted package tests. No production trace or representative scheduler load/profile for R5–R6.
- Related audits: [Telegram ingress and callback plan](goultroid-telegram-audit-ai-plan.md) and [Telegram runtime hardening plan](goultroid-telegram-runtime-hardening-ai-plan.md). Their command-identity, callback-claim, and Telegram transport findings are closed; this document covers execution and scheduling after ingress.

This handoff is for a later AI session. Treat confirmed code behavior, conditional failure paths, and unmeasured performance risks separately. Preserve TaskEngine and Jobs Manager as the existing execution and durable scheduling authorities.

## 1. Runtime map and existing strengths

```text
Telegram update → Dispatcher → TaskEngine admission → resource worker → Telegram RPC executor
                                      ↑
Scheduler → Jobs Manager attempt ─────┤
Delayed-action loop ───────────────────┘
```

- TaskEngine uses a single coordinator, bounded pool backlogs and retained results, admission accounting, lazy workers, and resource quotas (`internal/taskengine/engine.go`, `engine_config.go`, `engine_admission.go`, `engine_dispatch.go`). Callback delivery has explicit reservation (`delivery.go`).
- Jobs Manager persists definitions and schedules and supports recovery and leases (`internal/jobs/manager_recovery.go`, `manager_attempt.go`). Scheduler sleeps until a wake or deadline instead of polling at a fixed interval (`internal/scheduler/engine.go`).
- Telegram RPC execution centralizes rate limiting, retry classification, and FloodWait handling (`internal/telegram/rpc_executor.go`). These mechanisms are useful boundaries; the findings below do not establish that they are generally slow.

## 2. Findings and implementation targets

### R1 — Interval catch-up scales with missed ticks

**Confirmed code behavior; high priority.** Jobs Manager advances an interval schedule by repeatedly adding the interval until the next due time is after now (`internal/jobs/manager_schedule.go`). The compatibility projection in `internal/app/scheduled_action.go` uses the same pattern. A one-second interval after 30 days of downtime entails roughly 2.6 million loop iterations per schedule. This is a complexity estimate, not a measured outage. `validateSchedulePolicy` already rejects intervals below one second on new Jobs schedules; the fallback to one minute in `ProcessDueSchedules` does not make the loop constant-time.

**Target:** calculate the number of elapsed intervals in constant time, with explicit handling for invalid persisted intervals, duration/time arithmetic limits, and the rule that the resulting due time must be strictly after now. Keep the Jobs Manager result and compatibility projection consistent. Test exact boundaries, long downtime, and dates near representable limits without changing misfire or occurrence semantics.

### R2 — Scheduled-job activation can leave an executable schedule

**Conditional consistency defect; high priority.** `ScheduleOnce` and `ScheduleRecurring` create an initializing scheduled-job row, register a Jobs definition and schedule, then activate the scheduled-job row (`internal/scheduler/engine.go`). Saving the enabled Jobs schedule wakes the timing owner **before** activation. A due schedule can therefore become eligible while the API is still in progress. If activation fails, the error path attempts to delete the row but does not disable that schedule. `ActionJob` reuses a target definition; message/command actions register a wrapper definition. Registration or schedule-save failure can also leave a wrapper definition after row deletion. These paths matter when redesigned schedule cutover is active; no production occurrence has been observed.

**Target:** reproduce activation and schedule-save failures with fault-injecting stores, including an immediately due schedule racing activation, then ensure registration and activation behave as one recoverable unit. Choose compensation or an explicit recoverable state consistent with the existing persistence model. Cover failed compensation, already-materialized occurrences, process restart, and both `ActionJob` and wrapper-definition paths; `DisableSchedule` prevents future materialization but does not invalidate an existing occurrence. Do not claim atomicity across stores without a real transaction. Preserve the original error while reporting cleanup failure.

### R3 — Fractional recurring intervals are silently shortened

**Confirmed code behavior; medium priority.** `ScheduleRecurring` accepts intervals of at least one second, sets the first `NextRunAt` using the full duration, but stores `int64(interval.Seconds())` in `IntervalSeconds` (`internal/scheduler/engine.go`). `saveRedesignedSchedule` reconstructs later Jobs intervals from that integer. For example, a 1.9-second request gets an initial due time about 1.9 seconds later but a recurring interval of 1 second. Choose a documented contract: reject non-whole-second intervals or preserve their precision end to end. Add boundary tests and check persisted-schedule compatibility before changing representation.

### R4 — Delayed actions are lost if TaskEngine rejects submission

**Confirmed best-effort behavior; medium priority.** The delayed-action loop removes a due item and releases its reservation before calling TaskEngine `Submit`; a submission error only increments `submitFailures` (`internal/app/delayed_action.go`). The direct production caller is message deletion through `core.MessagesFacade.scheduleDelete`, used by `Respond` auto-delete and delayed reply deletion (`internal/core/response.go`, `internal/core/context_messages.go`). `Schedule` reports admission to the timer heap, not eventual TaskEngine admission or deletion. This facility is currently best effort and not restart durable; the audit does not infer a durability requirement for every delayed action.

**Target:** decide explicitly whether response auto-delete and delayed reply deletion require retry after transient admission failure. If they remain best effort, document that contract and expose final submission failure through health/metrics; if delivery is required, retain or requeue with a bounded policy, accounting, cancellation, and shutdown semantics. Add a focused admission-failure test; existing delayed-action tests cover normal, quiesce, and capacity cases (`internal/app/delayed_action_test.go`). A retry mechanism must not imply restart durability unless state is persisted.

### R5 — Scheduled actions can occupy scheduler workers while waiting

**Unmeasured performance risk.** A scheduled command with resource requirements submits a child TaskEngine job and waits on its ticket (`internal/app/scheduled_action.go`). Its wrapper Jobs definition uses the `scheduler` pool (`internal/scheduler/engine.go`), whose default concurrency is four (`internal/taskengine/engine_config.go`). Multiple waiting wrapper attempts can occupy that pool while child jobs wait for general, download, or media capacity. Resource-free commands execute directly in the wrapper; `ActionJob` schedules the target definition rather than a wrapper. No deadlock or production saturation was demonstrated.

**Target:** instrument scheduler-pool occupancy, child-job queue delay, and end-to-end attempt latency under representative contention. If the wait is material, change orchestration so waiting attempts do not consume scarce scheduler workers while preserving lease, cancellation, result, and retry semantics. Require before/after measurements.

### R6 — Settled-claim reconciliation repeats work across active claims

**Unmeasured performance risk with a conditional correctness edge.** The scheduler run loop calls `reconcileSettledClaims` on each pass (`internal/scheduler/engine.go`). It copies and walks the **legacy scheduled-job claims tracked in memory**, performing `GetScheduledJob` and usually `GetOccurrence` reads per claim; redesigned Jobs schedules are not entered in this map. Active claims also impose a 30-second safety wake. Work therefore grows with legacy claim count and wake frequency. The audit did not measure database load or show a live bottleneck. Separately, `reconcileClaim` currently untracks a claim when `GetScheduledJob` returns an error as well as when the row is missing; a transient read error may defer settlement until lease/recovery. That outcome needs fault-injection proof before being classified as an observed loss or duplicate.

**Target:** first inject a transient `GetScheduledJob` read failure and verify claim settlement/recovery semantics. Then record legacy claim count, reconciliation frequency, query count, and latency under representative batches. If the cost is material, reconcile only claims due for inspection or on relevant state transitions, with a bounded recovery sweep. Keep restart and missed-notification recovery intact.

## 3. Verification baseline and limits

At the original `d709ac47` audit baseline, `go test ./internal/taskengine ./internal/jobs ./internal/scheduler ./internal/app` passed; `go test ./internal/telegram` failed in dispatcher behavior cases. Those Telegram failures belong to the subsequently closed ingress/callback audit and are **not a current E0 blocker**. On revalidation HEAD `ad42f4bf4725f42219ca5b93803ebe0faaf4cf22`, `go test ./internal/taskengine ./internal/jobs ./internal/scheduler ./internal/app ./internal/telegram -count=1 -timeout=120s` passed all five packages (2.727s, 2.509s, 1.986s, 0.628s, and 0.546s). The full repository race suite was also run successfully earlier on this lineage at `b7332a44c7ad82f0194cf73554e0d579327eb4f8`; that is historical acceptance for the current baseline, not acceptance of future execution-plan changes.

Original short local microbenchmarks on Linux amd64, AMD Ryzen 7 5700U, with `-benchtime=100ms -count=1` reported:

| Benchmark | Time | Bytes/op | Allocs/op |
| --- | ---: | ---: | ---: |
| `BenchmarkIngressMessageDedupeUnique` | 196.7 ns/op | 5 | 0 |
| `BenchmarkDispatcherMessageRouteLookup` | 5.854 ns/op | 0 | 0 |
| `BenchmarkB1_TinyEphemeralTask` | 10,789 ns/op | 3,008 | 25 |

Commands: `go test ./internal/telegram -run '^$' -bench 'BenchmarkIngressMessageDedupeUnique|BenchmarkDispatcherMessageRouteLookup' -benchtime=100ms -count=1` and `go test ./internal/taskengine -run '^$' -bench '^BenchmarkB1_TinyEphemeralTask$' -benchtime=100ms -count=1`. These historical measurements cover isolated ingress/task operations, not scheduler contention, reconciliation query load, Telegram end-to-end latency, or system capacity. No representative R5/R6 load test, CPU profile, or scheduler p95/p99 latency has been recorded.

## 4. Implementation sequence

1. **E0 — Establish execution-specific regression contracts.** The current five-package baseline is green. Add fault injection for R2 activation/save/compensation and R6 transient reconciliation reads, plus measurements that distinguish persistence defects from throughput concerns. Do not reopen the closed Telegram ingress tests without a new failure.
2. **E1 — Correct schedule math and lifecycle.** Resolve R1, R2, and R3 with focused boundary, failure, and restart-recovery tests. Verify that a reported scheduling failure cannot leave an executable Jobs schedule, and document any retained definition or recoverable state.
3. **E2 — Define delayed-action delivery.** Resolve R4 per actual deletion caller contract. Test TaskEngine admission failure, bounded retry if chosen, cancellation, and shutdown. Explicitly retain best-effort semantics if that is the intended business policy.
4. **E3 — Validate reconciliation correctness, then measure costs.** Prove R6 behavior under transient repository-read failure. Profile R5 and R6 with representative schedule volume and resource contention. Implement performance changes only for a demonstrated cost and retain recovery and lease guarantees.

## 5. Acceptance and handoff

- Keep package boundaries checked by `internal/architecture`; do not add a second scheduler, queue, or raw Telegram execution path.
- Run focused tests for each change, then `go test -race ./... -count=1 -timeout=180s`, `go vet ./...`, and `go build ./cmd/goultroid` before closure. Report exact new failures separately. The prior green suite does not substitute for a post-change run.
- Report before/after latency, throughput, allocations, and database queries for performance changes with workload and hardware stated. Do not use the microbenchmarks above as system-level performance claims.
- Record persistence compatibility, retry behavior, and any configuration or migration effect. Keep this plan open until its acceptance checks have fresh evidence.


## 6. Implementation session update — 28 September 2026

### Refreshed baseline and post-handoff drift

The implementation session refreshed `test-next` before editing. The exact starting HEAD was:

- `deaac6ec66fe1dfe7b7291031b8a32a545b8add9` — `test(scheduler): reproduce runtime execution faults`
- parent: `072663386cc41606bf2a73f921698658fc6e9fd0` — the prior execution-audit handoff baseline

The only post-handoff change was `internal/scheduler/runtime_execution_e0_test.go`. Source reconciliation confirmed that R1-R4 still existed, R5 remained measurement-only, and R6 still untracked a claim on a transient `GetScheduledJob` error. The earlier Telegram command-identity, callback-claim/ACK, dispatcher, and RPC hardening work was not reopened.

### E0 — reproducer/fault-injection contract

`deaac6ec66fe1dfe7b7291031b8a32a545b8add9` provides fault-injection coverage for:

- definition-registration failure and initializing-row cleanup;
- redesigned schedule persistence failure;
- a due redesigned schedule being visible before compatibility-row activation;
- activation failure and compensation;
- ActionJob ownership: compensation must not delete a caller-owned target definition;
- compensation failure preserving the primary error;
- transient `GetScheduledJob` failure retaining the tracked claim for later settlement.

These are regression contracts, not evidence that the production implementation already passed them at that commit.

### E1 — R1/R2/R3 correctness implementation

Implementation commit:

- `5b7c2a5857f05c3f18231e0eb248f093df8b2c6a` — `fix(scheduler): harden schedule execution lifecycle`

Follow-up recovery hardening:

- `aa910c50d14b59075039446f097c6c4fbbf1d68f` — `fix(scheduler): preserve ambiguous schedule recovery`

Implemented source behavior:

- **R1:** Jobs interval advancement now uses `jobs.NextIntervalDueAfter`, a constant-time, phase-preserving calculation whose result is strictly after `now`. The compatibility projection uses the same helper. Arithmetic spans that saturate `time.Duration` fail closed rather than re-entering a work-proportional loop; the durable Jobs schedule is disabled on that invalid catch-up path.
- **R2:** redesigned schedules are prepared disabled, the compatibility row is activated, and only then is the redesigned schedule published enabled. This is a staged cross-store protocol, **not an atomic transaction**. Pre-publish failures compensate with bounded detached cleanup. Scheduler-owned wrapper definitions may be removed while the schedule is known non-executable; ActionJob target definitions are never deleted. If the final enable write returns an ambiguous error, compensation disables the schedule but retains the wrapper definition so a possibly materialized occurrence remains recoverable and its durable history is not deleted by definition FK cascade.
- **R3:** new recurring scheduler API calls now reject non-whole-second intervals. This matches the existing persistence contract (`scheduled_jobs.interval_seconds` and redesigned `job_schedules.interval_seconds`) instead of silently truncating later recurrences. Existing persisted integer-second schedules require no representation migration.
- **R6 correctness edge:** a transient `GetScheduledJob` error no longer untracks the in-memory legacy claim. A confirmed missing row or claim-token change can still release tracking.

New boundary tests cover exact interval slots, between-slot advancement, 30-day downtime, saturated time arithmetic, fractional recurring interval rejection, and ambiguous publish compensation retaining the wrapper.

### E2 — R4 delayed-action contract

Contract commit:

- `85aaf6c898ffe5b9940f7658bfa81ced6a772787` — `test(app): define delayed action best-effort failure`

The actual production callers are response auto-delete and delayed reply deletion. The chosen contract remains **best effort and non-durable**:

- `Schedule` confirms admission to the bounded timer heap, not eventual TaskEngine admission or Telegram deletion;
- once due, TaskEngine remains the only physical execution authority;
- a TaskEngine submission failure consumes that delayed item rather than creating a second retry engine;
- `submitFailures` and degraded component health are the terminal observability path;
- shutdown may drop pending timer-heap actions; no restart durability is claimed.

The E2 regression asserts timer admission can succeed while the due TaskEngine admission later fails, the deletion action does not run, retained pending accounting is released, and health reports the submission failure.

### E3 — R6 measurement harness and R5 hold

Measurement-harness commit:

- `b37907faa9a84d712dc38b2e74c838c505765395` — `test(scheduler): add claim reconciliation benchmark`

`BenchmarkRuntimeExecutionE3_ReconcileSettledClaims` runs representative 1/100/1000 active-claim batches and reports both `scheduled_reads/op` and `occurrence_reads/op`. No R6 performance optimization has been made from the source-level complexity estimate alone.

R5 is intentionally unchanged. The source still shows resource-bearing scheduled commands occupying the scheduler wrapper while waiting for the child TaskEngine ticket; resource-free commands run directly and ActionJob routes to the target Jobs definition. A representative executable measurement of scheduler-pool occupancy, child queue delay, and end-to-end attempt latency is still required before changing that orchestration.

### Verification status and environment limit

The source/diff was re-inspected after each pushed change and the newly added E2/E3 Go test files were passed through local `gofmt` before their commits. The first E1 production commit was pushed before a runnable repository checkout was available in this execution environment; therefore this document does **not** claim a successful post-change `gofmt -l`, focused `go test`, `git diff --check`, benchmark execution, race suite, vet, or build for the E1 lineage. The later formatting follow-up aligns the touched registration struct, but fresh executable verification remains mandatory.

Do not close this plan until a real checkout runs, at minimum:

```text
gofmt -w <all Go files changed by E0-E3>
gofmt -l <all Go files changed by E0-E3>   # must print nothing
git diff --check

go test ./internal/taskengine ./internal/jobs ./internal/scheduler ./internal/app ./internal/telegram -count=1 -timeout=120s
go test ./internal/scheduler -run 'TestRuntimeExecutionE0|TestRuntimeExecutionE1' -count=1
go test ./internal/app -run 'TestRuntimeExecutionE2' -count=1

go test ./internal/scheduler -run '^$' -bench '^BenchmarkRuntimeExecutionE3_ReconcileSettledClaims$' -benchtime=500ms -count=3
# Add/run an R5 representative contention measurement before any R5 orchestration change.

go test -race ./... -count=1 -timeout=180s
go vet ./...
go build ./cmd/goultroid
```

Any R5/R6 optimization remains gated on those fresh measurements. The historical ingress/TaskEngine microbenchmarks and the historical race pass remain background evidence only; they are not acceptance for this execution-plan lineage.
