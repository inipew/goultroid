# Goultroid Telegram Runtime Execution Audit — AI Session Plan

Status: **OPEN — audit documented; implementation and acceptance pending**

Audit baseline:

- Branch: `test-next`
- Baseline HEAD: `d709ac47` (`docs(telegram): add audit handoff plan`)
- Audit date: 28 September 2026
- Evidence: source inspection, targeted package tests, and short local microbenchmarks. No production trace, load test, p95/p99 measurement, or full race-suite result.
- Related audit: [Telegram ingress and callback plan](goultroid-telegram-audit-ai-plan.md). Its command-identity and callback-claim findings remain open; this document covers the execution path after ingress and the scheduler path.

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

**Confirmed code behavior; high priority.** Jobs Manager advances an interval schedule by repeatedly adding the interval until the next due time is after now (`internal/jobs/manager_schedule.go:140-149`). The compatibility projection in `internal/app/scheduled_action.go:95-99` uses the same pattern. A one-second interval after 30 days of downtime entails roughly 2.6 million loop iterations per schedule. This is a complexity estimate, not a measured outage.

**Target:** calculate the number of elapsed intervals in constant time, with explicit handling for zero/invalid intervals, integer overflow, and the rule that the resulting due time must be strictly after now. Keep the Jobs Manager result and compatibility projection consistent. Test exact boundaries, long downtime, and dates near representable limits.

### R2 — Scheduled-job activation can leave an executable schedule

**Conditional consistency defect; high priority.** `ScheduleOnce` and `ScheduleRecurring` create an initializing scheduled-job row, register a Jobs definition and schedule, then activate the scheduled-job row (`internal/scheduler/engine.go:409-479`, `484-534`). If activation fails, the error path removes the scheduled-job row but does not remove the Jobs schedule and definition. That can leave work eligible to run after the API returned an error. A production occurrence has not been observed.

**Target:** reproduce the activation failure with a fault-injecting store, then ensure registration and activation behave as one recoverable unit. Choose compensation or an explicit recoverable state consistent with the existing persistence model. Cover cleanup failures and restart recovery; do not claim atomicity across stores without a real transaction.

### R3 — Fractional recurring intervals are silently shortened

**Confirmed code behavior; medium priority.** `ScheduleRecurring` accepts intervals of at least one second but converts the duration with `int64(interval.Seconds())` (`internal/scheduler/engine.go:443-479`). For example, 1.9 seconds is stored as 1 second. Choose a documented contract: either reject non-whole-second intervals or preserve their precision end to end. Add a boundary test and check persisted-schedule compatibility before changing the representation.

### R4 — Delayed actions are lost if TaskEngine rejects submission

**Confirmed best-effort behavior; medium priority.** The delayed-action loop removes a due item and releases its reservation before calling TaskEngine `Submit`; a submission error only increments `submitFailures` (`internal/app/delayed_action.go:436-464`). The auto-delete response path uses this facility (`internal/core/context_messages.go:45-59`). This facility is currently best effort and not restart durable; the audit does not infer a durability requirement for every delayed action.

**Target:** define whether auto-delete and each other caller require retry after transient admission failure. For callers that do, retain or requeue the action with a bounded retry policy and clear shutdown semantics. Add a focused admission-failure test; existing delayed-action tests cover normal, quiesce, and capacity cases (`internal/app/delayed_action_test.go`). Document any caller that intentionally remains best effort.

### R5 — Scheduled actions can occupy scheduler workers while waiting

**Unmeasured performance risk.** A scheduled command with resource requirements submits a child TaskEngine job and waits on its ticket (`internal/app/scheduled_action.go:177-218`). The enclosing Jobs attempt runs in the `scheduler` pool (`internal/scheduler/engine.go:505-515`), whose default concurrency is four (`internal/taskengine/engine_config.go:64-69`). Multiple waiting attempts can occupy that pool while child jobs wait for general, download, or media capacity. No deadlock or production saturation was demonstrated.

**Target:** instrument scheduler-pool occupancy, child-job queue delay, and end-to-end attempt latency under representative contention. If the wait is material, change orchestration so waiting attempts do not consume scarce scheduler workers while preserving lease, cancellation, result, and retry semantics. Require before/after measurements.

### R6 — Settled-claim reconciliation repeats work across active claims

**Unmeasured performance risk.** The scheduler run loop calls `reconcileSettledClaims` on each pass (`internal/scheduler/engine.go:577-683`). Reconciliation copies and walks active claims (`:835-847`) and performs `GetScheduledJob` and `GetOccurrence` reads for each (`:850` onward), with more work for terminal states. Work therefore grows with claim count and wake frequency. The audit did not measure database load or show a live bottleneck.

**Target:** record claim count, reconciliation frequency, query count, and latency under representative batches. If material, reconcile only claims due for inspection or on relevant state transitions, with a bounded recovery sweep. Keep restart and missed-notification recovery intact.

## 3. Verification baseline and limits

At the audit baseline, `go test ./internal/taskengine ./internal/jobs ./internal/scheduler ./internal/app` passed (package times: 2.719s, 2.535s, 2.020s, and 0.660s). `go test ./internal/telegram` failed in three `TestDispatcherBehaviorMatrix` subcases and `TestDispatcherBehaviorDurableClaimPrecedesCommandAdmission`: decision tasks appeared, while expected command/task/durable-claim effects did not. The root cause is undetermined. Diagnose those failures before attributing them to any runtime change; see the related ingress audit.

Short local microbenchmarks on Linux amd64, AMD Ryzen 7 5700U, with `-benchtime=100ms -count=1` reported:

| Benchmark | Time | Bytes/op | Allocs/op |
| --- | ---: | ---: | ---: |
| `BenchmarkIngressMessageDedupeUnique` | 196.7 ns/op | 5 | 0 |
| `BenchmarkDispatcherMessageRouteLookup` | 5.854 ns/op | 0 | 0 |
| `BenchmarkB1_TinyEphemeralTask` | 10,789 ns/op | 3,008 | 25 |

Commands: `go test ./internal/telegram -run '^$' -bench 'BenchmarkIngressMessageDedupeUnique|BenchmarkDispatcherMessageRouteLookup' -benchtime=100ms -count=1` and `go test ./internal/taskengine -run '^$' -bench '^BenchmarkB1_TinyEphemeralTask$' -benchtime=100ms -count=1`. These measure isolated operations, not Telegram end-to-end latency or system capacity. No load test, CPU profile, p95/p99 latency, or full race suite was run.

## 4. Implementation sequence

1. **E0 — Establish a reproducible baseline.** Re-run targeted tests, investigate the existing Telegram failures, and add fault injection and metrics needed to distinguish persistence defects from throughput concerns. Preserve failing names and output rather than weakening assertions.
2. **E1 — Correct schedule math and lifecycle.** Resolve R1, R2, and R3 with focused boundary, failure, and recovery tests. Verify the durable Jobs schedule and scheduled-job row cannot disagree after a reported scheduling failure.
3. **E2 — Define delayed-action delivery.** Resolve R4 per caller contract. Test TaskEngine admission failure, bounded retry, cancellation, and shutdown behavior where retries are required.
4. **E3 — Measure contention and reconciliation.** Profile R5 and R6 with representative schedule volume and resource contention. Implement changes only for a demonstrated cost and retain recovery and lease guarantees.

## 5. Acceptance and handoff

- Keep package boundaries checked by `internal/architecture`; do not add a second scheduler, queue, or raw Telegram execution path.
- Run focused tests for each change, then `go test -v -race ./...`, `go vet ./...`, and `go build -v ./cmd/goultroid` before a pull request. Report any pre-existing failures separately.
- Report before/after latency, throughput, allocations, and database queries for performance changes with workload and hardware stated. Do not use the microbenchmarks above as system-level performance claims.
- Record persistence compatibility, retry behavior, and any configuration or migration effect. Keep this plan open until its acceptance checks have fresh evidence.
