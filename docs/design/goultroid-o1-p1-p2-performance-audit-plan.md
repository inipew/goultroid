# O1 — P1 Runtime Performance Baseline & P2 TaskEngine Hot-Path Audit

## Status

- **Workstream:** O1 — Runtime Performance & Hot-Path Optimization
- **Scope approved:** P1 and P2 only
- **Baseline branch:** `test-next`
- **Baseline HEAD:** `f487e5a8988ca59e83e3928d009e47d7f2a864b2`
- **HEAD verification:** GitHub comparison of `f487e5a8988ca59e83e3928d009e47d7f2a864b2...test-next` returned `identical` (0 ahead / 0 behind).
- **Production code changes:** None in this audit plan.
- **CI:** Do not inspect or poll CI unless explicitly requested.

---

## 1. Objective

Establish a reproducible runtime baseline, then profile TaskEngine's hot paths against that baseline. The goal is to identify measurable CPU, allocation, contention, queueing, worker-churn, or latency costs before proposing optimizations.

This work is deliberately limited to:

1. **P1 — Runtime Performance Baseline**
2. **P2 — TaskEngine Hot-Path Profiling**

Do not expand into SQLite/JobManager, dispatcher/message processing, caches, background housekeeping, downloader, or other subsystems in this workstream.

## 2. Non-Negotiable Constraints

- Do not change production behavior, concurrency limits, queue capacities, pool defaults, timers, or runtime configuration during baseline collection.
- Do not add a second runtime, worker pool, scheduler, queue, executor, registry, or profiling service.
- Preserve TaskEngine as the execution/admission/fairness/resource authority.
- Preserve JobManager as durable logical work/attempt authority.
- Preserve the single-writer TaskEngine control-loop model unless profiling proves a specific bottleneck and a separate design is approved.
- Keep goroutines, queues, retained state, and instrumentation bounded.
- Never expose pprof or diagnostics on a public/untrusted interface. Reuse an existing authenticated/local-only diagnostics path if one exists; otherwise use OS process metrics and test/benchmark instrumentation.
- Do not infer a leak from a transient high-water goroutine count. Record idle, active, post-burst, and post-drain samples.
- Any proposed optimization must include a correctness/race test and a before/after benchmark or runtime measurement.
- Format all changed Go files with gofmt. Update tests whenever production logic or APIs change.

---

## 3. P1 — Runtime Performance Baseline

### 3.1 Measurement environment

Record before collecting data:

- Exact `test-next` HEAD and whether the working tree is clean.
- Go version, OS/kernel, architecture, CPU model, RAM, and `GOMAXPROCS`.
- Build mode (native binary/container), build flags, and relevant runtime configuration.
- Enabled plugins and external services that materially affect background activity.
- Whether the process has completed startup/recovery and reached steady state.
- Workload description, duration, request/task count, and concurrency.

Use the same binary, machine, configuration, and workload for all baseline comparisons. Do not change pool settings between runs.

### 3.2 Required runtime observations

Capture these dimensions:

| Dimension | Required observations |
|---|---|
| Process | RSS, virtual size, process CPU, thread count, uptime |
| Go runtime | goroutine count, heap alloc/in-use, heap objects, stack in-use, GC count and pause/CPU indicators where available |
| TaskEngine | active/queued tasks, per-pool active/idle/running workers, admission rejections, inbox pressure if observable |
| Completion/durability | queued/active/failure counters and worker count if exposed |
| Latency | submit/admission latency, task queue wait, execution duration, completion/durability wait where observable |
| Lifecycle | startup-to-ready time, quiesce/drain/shutdown duration, post-shutdown process exit |

Record source and collection method for every value. Mark unavailable metrics as unavailable; do not estimate them.

### 3.3 Workload matrix

Run each scenario against an unchanged build/configuration:

1. **Cold startup:** process start through ready state; capture startup duration and initial goroutine/thread/RSS observations.
2. **Steady idle:** after startup/recovery, no user workload for at least 5 minutes; sample at consistent intervals.
3. **Small burst:** short burst at low concurrency, followed by complete drain and an observation window.
4. **Normal mixed workload:** representative general/interactive/download/scheduler work at ordinary concurrency.
5. **Sustained workload:** stable arrival rate for at least 5 minutes, followed by drain.
6. **Peak bounded burst:** approach configured pool/backlog limits without intentionally exceeding safety limits; record rejections/backpressure.
7. **Shutdown:** quiesce, drain, stop, and verify process exit; record any workers or goroutines that remain until exit.

For each workload, report p50/p95/p99 where sample counts support percentiles. Include sample count and avoid percentiles for tiny samples.

### 3.4 Idle and post-burst acceptance observations

These are observations, not assumed pass thresholds:

- Does goroutine/thread/RSS return near the pre-burst steady-idle range after all tasks drain?
- Do adaptive workers retire according to configured idle timeouts?
- Do completion/durability workers retire when their queues become idle?
- Does retained task/result state remain bounded and trend down after expiry/eviction?
- Are there recurring timers, polling loops, or background workers active while the system is idle?
- Is there a repeatable monotonic increase across repeated identical burst/drain cycles?

A non-returning value is a lead for investigation, not proof of a leak. Compare repeated runs and account for allocator/GC high-water behavior.

### 3.5 P1 deliverables

- Environment and exact-HEAD record.
- Raw timestamped observations (sanitized; no secrets or personal message contents).
- Summary table for startup, idle, burst, sustained, drain, and shutdown.
- Graphs only where they reveal a trend; include the underlying sample data.
- Explicit limitations and unavailable metrics.
- P1 conclusion: baseline established / baseline incomplete, with evidence.

**Execution boundary:** Runtime measurements must be collected on the actual host/container running Goultroid. A repository-only review cannot truthfully supply live RSS, goroutine, CPU, or GC values.

---

## 4. P2 — TaskEngine Hot-Path Profiling

P2 begins after P1's environment and baseline are recorded. Profile only TaskEngine paths; do not broaden into other subsystems.

### 4.1 Code-path inventory

Audit and measure these paths:

1. `Submit` → request-envelope acquisition → bounded inbox handoff → coordinator decision → admission result.
2. Admission scheduling/fairness → assignment publication → physical worker start.
3. Worker execution → `opWorkerStarted` / `opWorkerCompleted` control events → idle-slot publication.
4. Cancellation (single task and scope) under queued and running load.
5. Result retention/credit release and terminal eviction.
6. Completion callback delivery queue and fallback behavior.
7. Durability acknowledgement lane queueing, worker activation/retirement, and stop behavior.
8. Adaptive worker scale-up/idle retirement and repeated burst/drain cycles.
9. Snapshot/stats/control requests competing with submit/completion traffic.
10. Quiesce/drain/shutdown while tasks and callbacks are in flight.

### 4.2 Initial source observations (not performance conclusions)

At the verified baseline HEAD:

- `internal/taskengine/engine.go` implements a single-writer coordinator loop and fixed per-slot physical worker goroutines.
- The control inbox is bounded; default configuration sets `InboxCapacity` to 2048.
- Default pool concurrency ceilings sum to 49 across general (8), interactive (32), download (3), media-process (2), and scheduler (4). These are configured ceilings, **not** a claim that 49 workers are continuously running.
- Default pools opt into `ZeroIdle`; their minimum physical worker count is therefore zero, with per-pool idle retirement timeouts.
- Worker generations receive distinct mailboxes to prevent retired workers from racing newly spawned generations.
- The durability lane is lazy: `start()` is passive and workers are created when pending work requires them. Its configured maximum is four workers.
- The coordinator handles submit, cancel, snapshot, result, stats, worker lifecycle, commit acknowledgement, pool/resource configuration, and lifecycle control requests on the same control loop.

These facts define profiling targets only. They do not establish that any path is currently slow or defective.

### 4.3 Measurements and profiles

For each path, collect as applicable:

- Throughput (operations/tasks per second).
- p50/p95/p99 latency and sample count.
- CPU profile under a representative sustained workload.
- Allocation profile (allocs/op and bytes/op) for isolated benchmarkable paths.
- Mutex/block profiles where contention is suspected.
- Queue depth, enqueue wait, worker count/churn, and rejection counters.
- Goroutine count and stack categories at idle, peak, and post-drain.
- Control-loop service time and backlog/latency under mixed control and worker-completion traffic.

Profiles must be captured from a reproducible workload. Do not add a production profiling endpoint merely to obtain a profile. If the current application has no safe profile capture path, prefer package benchmarks and a temporary local-only test harness that is not shipped in production.

### 4.4 Benchmark coverage review

Before adding benchmarks, inventory existing TaskEngine benchmarks and tests. Reuse existing benchmark conventions and fixtures. If coverage is missing, propose a narrowly scoped benchmark/test addition for:

- warm and cold submit/admission;
- concurrent submitters against a bounded inbox;
- completion under mixed submit/control traffic;
- cancellation under queued/running load;
- adaptive worker scale-up and retirement;
- callback and durability lane activation, queue saturation, and drain.

Do not introduce brittle absolute timing assertions into unit tests. Benchmarks should report measurements; correctness tests should assert behavior and boundedness.

### 4.5 P2 investigation questions

- Is the single-writer coordinator CPU-bound, blocked on a slow transition, or simply receiving a burst of control events?
- Does the inbox handoff or coordinator service time dominate submit latency?
- Are request-pool reuse and clearing envelopes materially reducing allocations, or is another allocation source dominant?
- Does adaptive worker retirement cause avoidable goroutine churn for common burst patterns?
- Do completion/durability lanes create measurable scheduling or queueing delays?
- Do stats/snapshot/control requests add meaningful contention to task admission/completion?
- Are worker completion and idle-slot publication correctly bounded under cancellation and shutdown?
- Does post-burst goroutine/RSS behavior match the P1 idle baseline?

Do not answer these questions from source inspection alone; attach profile or benchmark evidence.

### 4.6 P2 deliverables

- TaskEngine call-path map with ownership and synchronization boundaries.
- Benchmark inventory and missing-coverage list.
- Profiles/benchmark outputs tied to exact HEAD, Go version, and workload.
- Ranked bottlenecks with evidence, impact, and confidence.
- Explicit non-findings and areas not measured.
- Optimization proposals separated from implementation; each proposal must state expected benefit, correctness risks, resource impact, and validation plan.

---

## 5. Phase Gates

### Gate A — P1 complete

P1 is complete only when the runtime environment, idle baseline, representative burst/sustained workload, post-drain behavior, and shutdown observations are recorded with reproducible evidence.

### Gate B — P2 complete

P2 is complete only when the TaskEngine paths have been profiled/benchmarked sufficiently to rank measured bottlenecks. Source-level suspicions alone do not close P2.

### Gate C — Optimization approval

No optimization is authorized by this audit plan. After P1/P2 results are reviewed, create a separate implementation plan for only the proven bottleneck(s). Preserve the baseline for before/after comparison.

---

## 6. Progress Register

| Phase | Status | Evidence |
|---|---|---|
| Repository HEAD verification | COMPLETE | `f487e5a8988ca59e83e3928d009e47d7f2a864b2`; `test-next` comparison identical |
| P1 — Runtime Performance Baseline | NOT STARTED | Requires runtime measurements on the deployed host |
| P2 — TaskEngine Hot-Path Profiling | SOURCE INVENTORY STARTED | Initial source observations in §4.2; profiling/benchmark evidence pending |
| Production optimization | OUT OF SCOPE | Requires separate review and approval |
