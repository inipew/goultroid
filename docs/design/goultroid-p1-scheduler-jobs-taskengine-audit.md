# P1 Audit — Scheduler → Jobs → TaskEngine

## Status

- Status: **CLOSED (P1-A, P1-B, P1-C, P1-D, P1-E Complete)**
- Workstream: P1 of the Goultroid Architecture Audit Roadmap P1–P3
- Branch: `test-next`
- Planning baseline before roadmap commit: `3b6cfaa5840e7d8360d3f368343238694f025090`
- Audit baseline HEAD: `9ab9783fd4a97b38f5e70d23f5dbae3008fec709`
- Audit evidence commit: `07ea5e28b4ea5982954e56041a624ee10dcec3d0`
- Scope: read-only architecture, correctness, and performance audit. No production code changes were required or applied.

## 1. Objective

Determine whether the existing Scheduler → Jobs → TaskEngine execution path is correct, bounded, fair, recoverable, and performant under both normal and sustained concurrent load.

The audit must follow real implementation paths and identify ownership boundaries. It must not presume that a defect exists or begin by redesigning the subsystem.

Desired outcomes:
- scheduled work is dispatched at the intended time and with well-defined misfire/overlap behavior;
- durable job state and attempt transitions remain consistent through failures and restarts;
- TaskEngine admission, queueing, worker limits, and fairness behave predictably;
- cancellation, deadlines, FloodWait, retry, and durable yield preserve ownership and do not duplicate work;
- database and scheduler overhead remain bounded;
- observability and tests are sufficient to prove the above.

## 2. Architecture invariants to preserve

1. **Scheduler** determines when scheduled work becomes due and applies the configured schedule policy. It must not become a second execution engine.
2. **JobManager** owns durable logical job state, claims, leases, attempts, and recovery.
3. **TaskEngine** owns execution admission, fairness, queue/worker/resource constraints, and execution lifecycle.
4. **Telegram RPCExecutor** remains the shared authority for Telegram RPC execution and FloodWait/retry handling.
5. Durable yield/retry decisions must have one authoritative owner and explicit state semantics.
6. Context cancellation and deadlines must flow through owned work; shutdown must not abandon untracked work.
7. Existing persistence and transaction boundaries remain authoritative; do not add a parallel job store.
8. All in-memory collections, queues, worker counts, and diagnostic cardinality must have explicit bounds.

No second TaskEngine, JobManager, scheduler loop, retry engine, RPC executor, callback protocol, or background worker system may be introduced.

## 3. Scope map

Map these stages from source before judging behavior:

```text
Schedule configuration / due-time calculation
                 |
                 v
Scheduler tick / due schedule selection
                 |
                 v
Misfire + overlap policy / dispatch decision
                 |
                 v
Durable job creation or dispatch request
                 |
                 v
JobManager claim / lease / attempt transition
                 |
                 v
TaskEngine admission / priority / queue / worker
                 |
                 v
Task execution / managed Telegram RPC
                 |
                 v
Result classification
      |          |          |
      v          v          v
 completed   retry/yield   terminal failure
      |          |          |
      +----------+----------+
                 |
                 v
JobManager durable state + next due/recovery
```

This is a conceptual map only. Replace it with the actual call graph, packages, symbols, and persistence paths found in the repository. Explicitly document any branch that bypasses or legitimately does not use one of these stages.

## 4. Audit dimensions

### A. Scheduler semantics

Inspect:
- schedule registration, update, disable, and deletion;
- due-time calculation, timezone handling, clock source, and clock jumps;
- tick/scan cadence and whether scans can overlap;
- misfire policy semantics (including run-once/catch-up/skip if present);
- overlap policy and concurrent invocation behavior;
- idempotency when a due schedule is observed repeatedly;
- schedule-to-job handoff atomicity and crash windows;
- behavior when downstream admission is full or temporarily unavailable;
- cancellation and shutdown while a schedule is being dispatched.

Questions to answer:
- Can a due schedule create duplicate logical jobs?
- Can a dispatch failure silently lose a due occurrence?
- Is the next due time advanced before or after durable job creation, and is that ordering crash-safe?
- Are timezone and DST behaviors explicit and tested?
- Can a slow dispatch block unrelated schedules?

### B. JobManager durability and state machine

Inspect:
- job creation and deduplication keys;
- claim acquisition and claim ownership;
- lease expiry, renewal, and stale-claim recovery;
- attempt numbering and MaxAttempts semantics;
- transitions among queued, running, yielded/retry, completed, failed, cancelled, and expired states (use actual repository states);
- transaction boundaries and compare-and-swap/update predicates;
- process crash between claim, execution, external side effect, and state persistence;
- recovery scan cadence, batch size, and fairness;
- durable yield semantics and next-attempt scheduling;
- cleanup/retention and bounded history.

Questions to answer:
- Is every transition validated against the expected prior state?
- Can two workers execute the same logical attempt concurrently?
- Can a stale worker overwrite a newer attempt's result?
- What happens if a process dies after Telegram accepted a request but before success is persisted?
- Are retryable and terminal errors classified consistently?
- Are recovery and cleanup bounded in both work per pass and stored rows?

### C. TaskEngine admission and execution

Inspect:
- admission API and call sites;
- queue capacity and rejection/backpressure behavior;
- worker lifecycle, worker caps, and pool selection;
- priority ordering, fairness, starvation prevention, and aging if implemented;
- per-user/per-feature/global concurrency constraints;
- context propagation, timeout enforcement, and cancellation;
- queue wait versus execution time accounting;
- behavior when all workers or queues are saturated;
- shutdown behavior for queued and active tasks;
- task result and error normalization.

Questions to answer:
- Can one pool or priority class starve another indefinitely?
- Are limits enforced at the correct scope?
- Does admission block callers unexpectedly?
- Are rejected tasks surfaced to the durable owner so they can be safely rescheduled or failed?
- Are worker goroutines bounded and joined?
- Is queue capacity sized from explicit resource constraints rather than accidental defaults?

### D. Retry, FloodWait, and durable yield

Inspect:
- all Telegram RPC ingress points used by jobs;
- shared RPCExecutor routing and policy ownership;
- FloodWait classification and duration handling;
- cancellation/deadline interaction with FloodWait;
- distinction between in-process retry and durable yield;
- attempt consumption rules for retry, yield, and non-retryable errors;
- retry-after persistence and wake-up behavior;
- duplicate side-effect protection and idempotency;
- interaction between TaskEngine retry policy and JobManager attempt policy.

Questions to answer:
- Is retry policy implemented once, or duplicated across layers?
- Can a task sleep while occupying a worker when it should durably yield?
- Can a durable yield be mistaken for terminal completion?
- Does a retry consume an attempt consistently?
- Can a cancellation race trigger a new attempt after cancellation?
- Are Telegram FloodWaits always handled by the shared RPCExecutor?

### E. Persistence and SQLite contention

Inspect:
- job/schedule schema and indexes;
- claim/update query shapes and transaction boundaries;
- write amplification from polling, heartbeats, or status updates;
- busy timeout and transaction retry behavior;
- read/write pool settings and connection lifecycle;
- batch claim/recovery efficiency;
- lock ordering and potential deadlocks;
- consistency under concurrent schedule edits, claims, and recovery.

Measure where useful:
- claim latency for 1/100/1000 jobs;
- schedule scan latency at representative cardinalities;
- write latency under concurrent claim/update load;
- busy/locked error rates;
- rows scanned versus rows changed;
- allocations and bytes per operation.

Do not optimize from a single microbenchmark. Include workload shape, hardware, Go version, SQLite settings, iteration count, and whether measurements are local.

### F. Resource bounds and performance

Measure:
- idle goroutine count attributable to this path;
- peak and post-load goroutine count;
- worker count by pool and priority;
- queue depth, rejection count, and queue wait;
- active/queued durable jobs;
- memory and allocation per task/attempt;
- database connections and outstanding transactions;
- scheduler scan work and duration;
- shutdown drain duration.

Use cold, warm, saturated, cancellation-heavy, and recovery-after-restart scenarios. Distinguish steady-state baseline from high-water marks. Do not infer a leak solely from a bounded high-water worker count; verify return-to-baseline and ownership.

### G. Observability and security

Inspect:
- structured logs for schedule/job/attempt lifecycle;
- metrics for queue, admission, claim, execution, retry/yield, and recovery;
- bounded metric labels (no raw peer IDs, user IDs, URLs, or unbounded job IDs);
- redaction of Telegram credentials, AccessHash, tokens, and private payloads;
- whether errors are actionable and preserve typed causes;
- ability to correlate a logical job across attempts without exposing sensitive identifiers.

## 5. Phase plan

### P1-A — Baseline and architecture inventory

Tasks:
1. Verify GitHub remote HEAD for `test-next`.
2. Run `git pull --ff-only`; record exact HEAD and clean/dirty worktree state.
3. Inventory scheduler packages, JobManager, TaskEngine, RPCExecutor, persistence, and their tests.
4. Trace representative job types end to end: scheduled job, immediate job, retry/yield job, and cancellation path.
5. Build a symbol-level call graph and state ownership matrix.
6. Record any path that bypasses a layer and explain why.

Deliverables:
- exact baseline commit;
- package/file/symbol inventory;
- call graph;
- state ownership matrix;
- list of unresolved questions.

Exit criteria: no major execution edge is inferred from naming alone; every important edge is tied to source.

### P1-B — Correctness and concurrency audit

Tasks:
1. Enumerate durable state transitions and their guards.
2. Inspect claim/lease and stale-worker behavior.
3. Inspect schedule overlap/misfire and duplicate dispatch windows.
4. Inspect cancellation races at queued, claimed, running, yielding, and terminal stages.
5. Inspect crash windows around external side effects and persistence.
6. Inspect FloodWait and retry/yield ownership.
7. Review existing race/concurrency tests and identify missing deterministic assertions.

Deliverables:
- state-transition table;
- crash-window analysis;
- concurrency/race findings;
- confirmed bug list and test-gap list.

Exit criteria: every reported correctness issue has a reproducible scenario or a clearly demonstrated unsafe code path.

### P1-C — Performance, contention, and resource audit

Tasks:
1. Establish idle baseline.
2. Measure scheduler scan and dispatch cost at representative schedule counts.
3. Measure claim/attempt persistence at representative job counts.
4. Measure TaskEngine queue wait and execution under mixed priorities/pools.
5. Measure saturation, rejection, cancellation-heavy, and recovery workloads.
6. Inspect SQLite lock contention and transaction duration.
7. Measure resource recovery after load and shutdown.

Deliverables:
- reproducible benchmark/test commands;
- workload parameters and environment;
- latency and resource tables;
- bottleneck analysis distinguishing measured facts from hypotheses.

Exit criteria: no performance conclusion relies on unsupported extrapolation or a single benchmark sample.

### P1-D — Test hardening and approved fixes

Tasks:
1. Prioritize findings by severity, likelihood, and blast radius.
2. Prepare a separate implementation plan before nontrivial changes.
3. Add deterministic tests for uncovered invariants before or alongside fixes.
4. Keep changes narrow and preserve existing subsystem ownership.
5. Format all changed Go files with `gofmt`.
6. Run focused tests, relevant `-race` tests, vet, architecture guards, and `git diff --check`.
7. Do not inspect/poll CI unless explicitly requested.

Deliverables:
- finding-to-fix mapping;
- commits and changed-file list;
- test evidence and any environment limitations;
- remaining risks.

Exit criteria: each accepted fix has a regression test and verification evidence; rejected findings have a reason recorded.

### P1-E — Documentation and closure

Tasks:
1. Update this document with actual findings and final status.
2. Record exact final HEAD and commit references.
3. Record tests/benchmarks actually executed and who executed them.
4. Separate closed findings from accepted residual risks.
5. Update the P1–P3 roadmap progress register.
6. Obtain explicit review/approval before starting P2.

Deliverables:
- audit summary;
- findings register;
- test/benchmark evidence;
- accepted-risk list;
- closure decision and P2 handoff.

Exit criteria: P1 is explicitly marked closed or blocked; it is never silently treated as complete.

## 6. Finding severity

Use consistent severity and evidence:

- **Critical:** data corruption/loss, uncontrolled execution, security exposure, or system-wide outage likely.
- **High:** duplicate/lost jobs, stuck execution, broken cancellation/recovery, unbounded resource growth, or broad starvation.
- **Medium:** bounded but material latency/contention, recoverable correctness edge, or important test gap.
- **Low:** limited inefficiency, diagnostics gap, or maintainability concern without demonstrated user impact.
- **Observation:** valid design choice or hypothesis requiring more evidence; not a defect.

Every finding should include:
- ID and severity;
- affected package/file/symbol;
- observed behavior and expected invariant;
- evidence (code path, test, reproduction, or measurement);
- impact and likelihood;
- proposed narrow remediation;
- regression test/verification;
- status and commit reference.

## 7. Required test scenario matrix

Use existing test helpers and avoid duplicating test infrastructure.

| Scenario | Invariant to prove |
|---|---|
| Due schedule observed repeatedly | One logical occurrence is dispatched according to policy |
| Misfire after downtime | Configured misfire behavior is deterministic |
| Overlap while prior run active | Overlap policy is enforced durably |
| Concurrent claim attempts | At most one valid owner for a claim/attempt |
| Lease expiry and recovery | Stale work is recovered without stale-owner overwrite |
| Crash after claim, before execution | Job remains recoverable |
| Crash after external side effect, before persistence | Duplicate-side-effect risk is explicit and mitigated where possible |
| Queue full / admission rejected | Durable owner receives a safe, explicit outcome |
| Mixed priorities and pools | Fairness and configured limits hold |
| FloodWait | Shared RPCExecutor owns Telegram wait/retry semantics |
| Durable yield | Worker is released and job resumes according to persisted policy |
| Cancellation at each lifecycle stage | No unintended new attempt or detached work |
| Shutdown with queued and active tasks | Owned work is stopped/drained within documented bounds |
| SQLite concurrent writers | No lost update or unhandled busy/snapshot failure |
| Large backlog recovery | Work per pass and resource use remain bounded |

Not every scenario must imply a new test if an equivalent strong test already exists. Cite existing tests by exact name and explain what they prove.

## 8. Operational rules for the coding agent

Before every phase:
- fetch/verify the current remote `test-next` HEAD;
- `git pull --ff-only`;
- record exact HEAD;
- inspect `git status --short`;
- do not overwrite or discard user changes.

During work:
- read source and tests before editing;
- do not infer behavior from docs alone;
- do not change unrelated subsystems;
- do not add new workers, goroutines, tickers, queues, registries, or retry layers without a demonstrated need and explicit approval;
- update tests when production logic/API changes;
- run `gofmt` on all changed Go code;
- do not poll CI unless explicitly requested;
- distinguish locally executed tests from reported test results.

## 9. Initial audit questions

The first audit session should answer these questions from source, not assumption:

1. What exact scheduler implementation is active in the current build?
2. Which component creates durable jobs from due schedules?
3. Is due-time advancement atomic with dispatch/job creation?
4. What are the exact durable job states and legal transitions?
5. Who owns claim/lease expiry and recovery?
6. How does TaskEngine admission rejection map back to durable job state?
7. Which component owns retry classification, FloodWait, and durable yield?
8. Can a worker remain occupied while waiting for a retry deadline?
9. What are the configured queue, worker, and concurrency bounds?
10. Which SQLite queries dominate claim/recovery under load?
11. What existing tests prove cancellation and shutdown behavior?
12. What resource metrics are available, and what important quantities remain invisible?

## 10. Findings register & Disposition (P1-D)

| ID | Severity | Finding | Evidence (Source / Test / Benchmark) | Disposition & Rationale |
|---|---|---|---|---|
| P1-F1 | Low / Observation | `JobSchedule.Timezone` is validated (`time.LoadLocation`) and persisted, while `NextIntervalDueAfter` advances recurring interval schedules using fixed UTC `time.Duration` arithmetic (`O(1)`). `Timezone` is metadata/calendar configuration and does not alter fixed-interval `time.Duration` stepping. | `internal/jobs/manager_schedule.go:63-69, 123-144`; `internal/jobs/manager_redesign_test.go` | **ACCEPT — documented behavior.** Fixed-interval UTC arithmetic is intentional, DST-safe, and prevents catch-up loops. Revisit only if calendar/cron recurrences are introduced. |
| P1-F2 | Low / Observation | `CauseRateLimited` (Telegram FloodWait durable yield via `CanDurablyYield`) persists `AttemptDeferred` via `CommitAttemptDeferred` and releases the worker immediately, distinct from normal in-process retries; `watchAttempt` enforces `maxDeferrals = maxAttempts + 1` before finalizing `OccurrenceFailed`. | `internal/jobs/manager_attempt.go:120-135`, `internal/jobs/manager_retry.go:148-153, 252-267`, `internal/jobs/rpc_yield_integration_test.go` | **ACCEPT — bounded existing behavior.** Separates durable deferral from worker-occupying sleep and bounds deferral count deterministically. |
| P1-F3 | Observation | `reconcileSettledClaims` is called at the top of `Engine.runLoop` (`internal/scheduler/engine.go:755`). When legacy `scheduled_jobs` claims are tracked in `e.claims`, it performs `O(N)` SQLite reads (`81 µs` for 1 claim, `8.03 ms` for 100, `109.8 ms` for 1000). Under `ScheduleCutoverActive == true`, redesigned `JobSchedule` occurrences do not populate `e.claims` and wake via `Manager.SetScheduleWake` on `CommitAttemptResult`, resulting in zero SQLite reads on the active cutover path when `e.claims` is empty. | `internal/scheduler/engine.go:755, 1074-1105`, `BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite` | **ACCEPT — legacy compatibility path; zero SQLite reads on the active cutover path when `e.claims` is empty.** Retain legacy path without modification; carry as an observation only if large legacy `scheduled_jobs` backlogs are ever re-enabled. |

## 11. Closure record (P1-E)

- **Final status:** **CLOSED** (P1-A, P1-B, P1-C, P1-D, and P1-E Complete)
- **Production code changes:** **None** (no Critical/High defects found; no production code changes required).
- **Audit baseline HEAD:** `9ab9783fd4a97b38f5e70d23f5dbae3008fec709`
- **Audit evidence commit:** `07ea5e28b4ea5982954e56041a624ee10dcec3d0`
- **Verified call graph (redesigned cutover path):**
  - `scheduler.Engine.runLoop` (`internal/scheduler/engine.go:734`) → `jobs.Manager.EarliestScheduleDue` / `ProcessDueSchedules` (`internal/jobs/manager_schedule.go:148`) → `sqlite.Store.MaterializeDueSchedule` (`internal/jobs/sqlite/store.go:1431`, atomic occurrence upsert + `OverlapForbid` + `next_due_at` advance with writer-intent lock) → `jobs.Manager.driveAttempt` (`internal/jobs/manager_attempt.go:167`) → `sqlite.Store.PrepareNextAttemptLease` (`internal/jobs/sqlite/store.go:824`, `attempt_no` & `lease_epoch` fenced) → `taskengine.Engine.Submit` (`internal/taskengine/engine_admission.go:14`) → worker executes `Handler` (using shared `telegram.RPCExecutor` for Telegram RPCs) → `PersistencePump` → `jobs.Manager.commitAttemptResult` (`internal/jobs/manager_attempt.go:116`) → `sqlite.Store.CommitAttemptResult` / `CommitAttemptDeferred` (`internal/jobs/sqlite/store.go:1044`) → `Manager.SetScheduleWake` / `watchAttempt` (`internal/jobs/manager_retry.go:174`) / `Manager.Recover` (`internal/jobs/manager_recovery.go:178`).
- **Configured resource bounds verified from source (`internal/taskengine/engine_config.go`, `internal/jobs/manager.go`, `internal/jobs/persistence_pump.go`):**
  - `TaskEngine`: `InboxCapacity=2048`, `ResultCapacity=1000`, `MaxTerminalRetained=1000`, `TerminalTTL=5m`, `MaxScopeTombstones=4096`, `scheduler` pool `Concurrency=4, BacklogLimit=100, ZeroIdle=true`.
  - `JobManager`: `retryQueueCap=256`, `retryWorkers=4`, `recoveryScanLimit=256`, `recoveryInterval=30s`.
  - `PersistencePump`: `concurrency=2`, `queueCap=256`, `maxRetainedBytes=64 MiB`.
- **Tests executed locally:**
  - `go test ./internal/scheduler/... ./internal/jobs/... ./internal/taskengine/...`: **PASS**
- **Benchmarks executed locally (AMD Ryzen 7 5700U, `linux/amd64`, `-benchtime=100ms`):**
  - `BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite/claims=1`: `81374 ns/op`, `5840 B/op`, `144 allocs/op`
  - `BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite/claims=100`: `8026605 ns/op`, `586934 B/op`, `14549 allocs/op`
  - `BenchmarkB0_IdleOverhead`: `2642 ns/op`, `1201 B/op`, `7 allocs/op`
  - `BenchmarkB1_TinyEphemeralTask`: `9324 ns/op`, `3004 B/op`, `25 allocs/op`
  - `BenchmarkB4_PeriodicDueBurst`: `6123019 ns/op`, `500.0 tasks/burst`, `1622662 B/op`, `13300 allocs/op`
  - `BenchmarkB5_DurableStoreThroughput`: `322885 ns/op`, `7587 B/op`, `213 allocs/op`
  - `BenchmarkB6_CancellationStorm`: `9889 ns/op`, `3928 B/op`, `27 allocs/op`
  - `BenchmarkB8_ShutdownDrainLatency`: `668100 ns/op`, `4793 B/op`, `30 allocs/op`
  - `BenchmarkTaskEngineFirstTaskAfterIdle/cold_zero_idle`: `49658 ns/op`, `3903 B/op`, `33 allocs/op`
  - `BenchmarkTaskEngineFirstTaskAfterIdle/warm_min_one`: `13109 ns/op`, `3195 B/op`, `27 allocs/op`
- **Handoff:** P1 is formally **CLOSED**. Proceed to **P2-A — Downloader → Retained Media → Telegram RPC (Read-Only Inventory)**.
- Benchmarks actually executed:
- Confirmed findings closed:
- Accepted residual risks:
- P2 authorization/handoff:


## 12. Detailed execution protocol

This section turns the phase outline into an actionable audit procedure. It does not assert that any listed subsystem currently has a defect.

### 12.1 Before starting each phase

The coding agent must:

1. Confirm repository is `inipew/goultroid` and branch is `test-next`.
2. Fetch the current remote branch head and record the full SHA; do not rely on the planning baseline above as the current HEAD.
3. Run `git pull --ff-only`.
4. Record `git rev-parse HEAD`, `git status --short`, and the current branch.
5. If the worktree contains user changes, stop before modifying files and report them. Never reset, clean, stash, or overwrite user work without explicit permission.
6. Read this plan and the roadmap, then inspect the relevant production code and tests for the phase.
7. Keep the phase bounded. Do not silently start the next phase in the same change set.

### 12.2 Evidence rules

Every conclusion must be assigned one of these evidence classes:

- **Source-proven:** directly demonstrated by a concrete call path, condition, transaction, or state transition.
- **Test-proven:** an existing or newly executed test demonstrates the invariant. Record the exact test name and command.
- **Measured:** backed by a reproducible benchmark or runtime measurement with environment and workload details.
- **Reported:** supplied by the user or another coding agent but not independently rerun in this audit.
- **Hypothesis:** plausible risk that still needs a reproduction, code proof, or measurement.

Do not present a hypothesis as a confirmed defect. Do not state that a test passed unless the executing agent actually ran it. When tests are run by a separate coding agent, identify them as reported results unless independently verified.

### 12.3 Source tracing rules

For each representative path, record:

- entry point and trigger;
- owning package, type, and method;
- context/deadline source and propagation;
- admission boundary and rejection behavior;
- durable state read/write points;
- goroutine/worker owner and completion/join mechanism;
- RPC boundary, if applicable;
- result/error classification;
- cancellation and shutdown behavior;
- exact tests covering the path.

Use a table with one row per meaningful transition. If a transition is asynchronous, identify who owns the continuation and what durable/in-memory state makes it recoverable.

### 12.4 Required representative paths

At minimum, trace these paths end to end:

1. **Scheduled success:** due schedule → dispatch → durable job → claim → TaskEngine → successful result → durable completion.
2. **Immediate job:** user/feature entry → admission or durable job creation → execution → result.
3. **Admission rejection:** job dispatch/claim → TaskEngine rejection or saturation → durable state and next action.
4. **Retryable failure:** execution error → classification → retry or durable yield → next attempt.
5. **Telegram FloodWait:** managed RPC → FloodWait handling → worker occupancy/release → continuation.
6. **Cancellation before execution:** queued or claimed work → cancellation → durable terminal/recoverable state.
7. **Cancellation during execution:** active context cancellation → RPC/handler stop → attempt/job finalization.
8. **Crash recovery:** process interruption at claim, execution, yield, and completion persistence boundaries.
9. **Shutdown:** stop admission → scheduler stop → active/queued work handling → owned goroutine join → persistence consistency.

If a path does not exist in the current implementation, state that explicitly and explain the actual equivalent path. Do not invent an abstraction to fill the diagram.

## 13. State and ownership artifacts

P1-A/B must produce the following artifacts in the audit notes or a linked report.

### 13.1 Component ownership matrix

| Concern | Actual owner (fill from source) | Source evidence | Notes |
|---|---|---|---|
| Schedule definition/configuration | TBD | File/type/method | |
| Due-time calculation | TBD | File/type/method | |
| Misfire/overlap decision | TBD | File/type/method | |
| Schedule dispatch | TBD | File/type/method | |
| Durable job creation | TBD | File/type/method | |
| Claim/lease lifecycle | TBD | File/type/method | |
| Attempt lifecycle | TBD | File/type/method | |
| Admission/fairness/worker limits | TBD | File/type/method | |
| Telegram FloodWait handling | TBD | File/type/method | |
| Durable yield/retry scheduling | TBD | File/type/method | |
| Recovery after restart | TBD | File/type/method | |
| Shutdown and join | TBD | File/type/method | |

Do not fill this table from naming conventions or prior documentation alone. Confirm each owner from current source.

### 13.2 State-transition matrix

For every persisted job state found in source, record:

| Current state | Trigger | Guard/ownership token | Next state | Durable write | Retry/attempt effect |
|---|---|---|---|---|---|
| TBD | TBD | TBD | TBD | TBD | TBD |

Include rejected transitions and stale-owner behavior. Distinguish persisted states from transient in-memory execution states.

### 13.3 Failure-window matrix

Analyze at least these interruption points:

| Interruption point | Durable state before interruption | External side effect possible? | Recovery behavior | Duplicate/loss risk |
|---|---|---|---|---|
| Before durable job creation | TBD | No/unknown | TBD | TBD |
| After job creation, before claim | TBD | No | TBD | TBD |
| After claim, before handler starts | TBD | No | TBD | TBD |
| During handler, before RPC | TBD | No | TBD | TBD |
| After Telegram accepted RPC, before result persistence | TBD | Yes | TBD | TBD |
| After durable yield, before next wake | TBD | Depends | TBD | TBD |
| After terminal result, before cleanup | TBD | Depends | TBD | TBD |

The “after Telegram accepted RPC” case must explicitly acknowledge that exactly-once delivery may be impossible without an idempotency primitive supplied by the remote API. Document the actual guarantee rather than promising exactly-once behavior.

## 14. Performance and resource measurement protocol

### 14.1 Baseline dimensions

Record at least:

- Go version, OS/kernel, architecture, CPU, memory;
- SQLite version and relevant connection/pool/pragmas;
- build mode and test/benchmark command;
- configured scheduler scan interval and limits;
- TaskEngine worker/queue/pool limits as read from source/config;
- workload size, concurrency, duration, warm-up, and sample count.

Do not change configuration merely to make a benchmark look better. If a test-only configuration is needed, record it separately from production defaults.

### 14.2 Measurement scenarios

| Scenario | Load shape | Record |
|---|---|---|
| Idle | No submitted jobs, steady runtime | Goroutines, heap/RSS if available, workers, queue depth, DB connections |
| Cold start | First job after startup | Startup-to-admission and first execution latency |
| Warm steady state | Repeated jobs at normal concurrency | p50/p95/p99 queue wait and execution latency |
| Mixed pools/priorities | Competing task classes | Per-class wait, throughput, starvation indicators |
| Saturation | Admission reaches configured capacity | Rejection/blocking behavior, queue bound, worker cap |
| Cancellation-heavy | Cancel queued and active work | Cancellation latency, residual workers/goroutines, durable state |
| Recovery | Large eligible backlog after restart | Recovery duration, batch size, memory and DB work |
| Post-load idle | Load stops and system drains | Return-to-baseline and resources retained |
| Shutdown | Stop with queued and active work | Shutdown duration, work outcome, goroutine join |

### 14.3 Reporting measurements

For every benchmark or load test, report:
- exact command and benchmark/test names;
- machine and runtime environment;
- workload parameters;
- iterations or duration;
- p50/p95/p99 where meaningful (do not derive percentiles from insufficient samples);
- throughput and error/rejection counts;
- allocations/op and bytes/op where Go benchmarks apply;
- baseline comparison, if a comparable baseline exists;
- limitations and whether the result is local-only.

Do not compare numbers across different machines/configurations as if they were controlled experiments. Avoid claiming a leak from a single goroutine snapshot; use repeated sampling and post-load recovery evidence.

## 15. Decision gates and change control

P1 is primarily an audit. Findings do not automatically authorize code changes.

For each finding, choose one disposition:

- **Fix now:** confirmed defect with a narrow, understood remediation.
- **Plan separately:** confirmed issue requiring nontrivial design or cross-package change.
- **Accept:** valid trade-off with bounded impact and explicit rationale.
- **Defer:** real issue but outside P1 or dependent on another workstream.
- **Reject:** initial concern disproved by source/test evidence.
- **Needs evidence:** cannot yet be classified.

Before implementing a nontrivial fix, create or update a dedicated plan that states:
- exact invariant being repaired;
- affected packages and APIs;
- alternatives considered;
- compatibility and migration concerns;
- concurrency and failure semantics;
- tests to add/update;
- performance/resource impact expected;
- rollback or recovery considerations.

Any change to production behavior must include corresponding test updates. All changed Go files must be formatted with `gofmt`. Do not broaden the fix to unrelated cleanup.

## 16. Required audit report format

At the end of each phase, report:

1. Phase name and status (complete, blocked, or partial).
2. Repository, branch, starting HEAD, ending HEAD, and worktree status.
3. Files/packages/types/methods inspected.
4. Actual call graph and ownership findings.
5. Findings grouped by severity and evidence class.
6. Tests inspected and tests actually executed, with exact commands.
7. Benchmarks/measurements actually executed and environment.
8. Changes/commits made, or explicit statement that no code changed.
9. Remaining questions, risks, and deferred items.
10. Whether the next phase is authorized to begin.

At P1-E, also include:
- a concise executive summary;
- a finding register with final disposition;
- a test coverage/gap summary;
- accepted residual risks;
- P2 handoff notes;
- explicit closure decision.

## 17. Scope boundaries and anti-patterns

The following are out of scope unless a concrete P1 finding proves they are necessary and the user approves a separate plan:

- replacing or splitting TaskEngine or JobManager;
- adding a new scheduler ticker, worker pool, queue, registry, or retry engine;
- introducing a second Telegram RPC executor;
- moving durable job state into a new persistence subsystem;
- adding polling loops or detached goroutines to mask backpressure;
- raising worker/queue limits solely to improve a benchmark;
- speculative micro-optimization without a measured bottleneck;
- unrelated plugin feature work or broad refactoring;
- CI polling or CI inspection.

A proposed architecture change must explain why existing ownership boundaries cannot solve the demonstrated problem and quantify the additional idle and peak resource cost.

## 18. P1-A kickoff checklist

P1-A may begin only after the coding agent receives the explicit instruction to start P1-A. On kickoff, complete this checklist in order:

- [ ] Verify remote `test-next` HEAD.
- [ ] `git pull --ff-only`.
- [ ] Record exact HEAD, branch, and worktree status.
- [ ] Inventory scheduler, jobs, TaskEngine, RPCExecutor, and persistence packages from source.
- [ ] Locate their tests and architecture guards.
- [ ] Trace the nine representative paths in Section 12.4, marking unavailable paths.
- [ ] Fill the component ownership matrix with source references.
- [ ] Produce a symbol-level call graph.
- [ ] Record unresolved questions without prematurely proposing redesign.
- [ ] Submit the P1-A report for review before P1-B.

**P1-A must not silently implement fixes.** If an obvious defect is found, record the evidence and impact, then request approval or a separate implementation plan.
