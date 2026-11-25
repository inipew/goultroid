# Goultroid Unified System Management — AI Implementation Plan

**Status:** U1 COMPLETE / AWAITING U2 APPROVAL  
**Target branch:** test-next  
**Design baseline HEAD:** 08dd3fc9809d02d232e07dd1ddc9d4453a64c071  
**Design date:** 2026-11-24  
**Scope:** Existing Runtime lifecycle/dependency graph, application composition and health, subsystem diagnostics, error/defer semantics, and bounded resource governance across TaskEngine, JobManager, Scheduler, Telegram RPCExecutor, persistence, and plugins.

> This is a design and implementation contract, not a claim that the proposed work has been implemented. Before each phase, fetch the latest remote test-next, pull fast-forward-only in the working copy, record the exact HEAD, inspect the current implementation and tests, and reconcile this plan with intervening commits. The baseline SHA is an audit anchor, never permission to overwrite newer work.

---

## 1. Executive decision

Goultroid should become easier to manage by making the existing system management model more coherent—not by introducing another manager.

The current repository already contains important management primitives:

- internal/runtime: component lifecycle contract, dependency graph, deterministic startup order, reverse shutdown, quiesce/drain/stop/forced-stop phases, aggregate runtime health, and shutdown reporting.
- internal/runtime/supervisor.go: bounded lifecycle-owned workers, panic recovery, restart budgets, backoff, and worker snapshots.
- internal/app/health.go: application readiness/degradation evaluation using diagnostics and selected bounded subsystem checks.
- internal/app/runtime_resources.go: composition-root dependency wrappers that preserve lifecycle capabilities.
- internal/taskengine: execution admission, bounded pools/queues, resource accounting, lifecycle and diagnostics.
- internal/jobs: durable job/occurrence/attempt lifecycle, retry/recovery coordination, and persistence.
- internal/scheduler: time-based orchestration and schedule reconciliation.
- Shared Telegram RPC policy/execution remains the authority for Telegram rate limits, FloodWait, and RPC retry behavior.

**Decision:** strengthen the existing internal/runtime.Runtime and internal/app.App composition/diagnostics boundaries. Do not add a new SystemCoordinator, GlobalManager, global registry, global event bus, execution engine, retry engine, or resource manager.

The intended outcome is a system that is:

1. **Easier to operate:** one coherent lifecycle and health view.
2. **Harder to break:** explicit ownership, dependency, error, and shutdown contracts.
3. **More tightly integrated:** subsystem health and pressure are visible through stable interfaces without sharing internal state.
4. **Resource-predictable:** bounded goroutines, queues, diagnostics, caches, timers, and retained data.
5. **Maintainable:** each subsystem keeps its own domain logic and exposes a small management surface.

---

## 2. Current architecture — verified baseline

### 2.1 Existing management plane

At baseline 08dd3fc9809d02d232e07dd1ddc9d4453a64c071, source inspection confirms:

| Existing area | Current responsibility | Design implication |
|---|---|---|
| internal/runtime/component.go | Component identity, dependencies, Start/Stop/Health; optional Quiescer, Drainer, ForcedStopper, CriticalComponent | Preserve and clarify this contract; do not replace it |
| internal/runtime/graph.go | Registration, dependency validation, cycle detection, deterministic topological order, reverse shutdown order | Reuse as the only component dependency graph |
| internal/runtime/runtime.go | Runtime state, startup/rollback, shutdown phases, aggregate health, shutdown report | Remains the lifecycle authority |
| internal/runtime/supervisor.go | Bounded worker registration, panic recovery, restart budget/backoff, worker health/snapshot | Keep distinct from TaskEngine; it supervises lifecycle-owned loops, not user work |
| internal/app/health.go | Application readiness, degradation reasons, diagnostics-based checks, Runtime health merge | Evolve this as the public application health projection |
| internal/app/runtime_resources.go | Composition wrappers for dependency and optional lifecycle interfaces | Keep composition wiring at app boundary |
| internal/taskengine/diagnostics.go | Snapshot of TaskEngine pool/worker/admission state | Publish through bounded snapshots; never expose mutable engine internals |

This is already a meaningful management foundation. The next work should close consistency gaps and verify end-to-end wiring rather than recreate these mechanisms.

### 2.2 Domain ownership that must remain separate

| Domain | Sole authority | Owns | Must not own |
|---|---|---|---|
| Application composition | internal/app.App | Constructing dependencies, wiring components, selecting startup order edges, application-level readiness | Job state, task execution, retry policy, plugin business logic |
| Lifecycle | internal/runtime.Runtime | Component lifecycle, dependency order, quiesce/drain/stop, shutdown budget/report | Physical task admission, durable attempts, Telegram RPC retry |
| Lifecycle worker supervision | Existing runtime.Supervisor | Small bounded long-lived maintenance loops and their restart policy | User jobs, arbitrary plugin work, second worker pool |
| Physical execution | TaskEngine | Admission, fairness, capacity, queueing, cancellation, execution and result delivery | Durable job semantics, schedule time, Telegram FloodWait policy |
| Durable logical work | JobManager | Definitions, occurrences, attempts, leases, retry/recovery, durable outbox convergence | Worker pool, wall-clock scheduling loop, Telegram RPC policy |
| Time orchestration | Scheduler | Due times, misfire/overlap policy, schedule claims and reconciliation | Job execution, generic retries, physical admission |
| Telegram transport policy | Shared RPCExecutor | Telegram request limits, FloodWait/defer, RPC retry classification and transport metrics | Durable job retries, task scheduling |
| Persistence | Existing repositories/stores | Transactions, schema, durable state and consistency | Lifecycle coordination, execution scheduling |
| Plugins/features | Feature-owned services | Feature semantics and bounded feature state | Private runtime, worker pool, retry engine, global registry |

A management view may aggregate snapshots from these owners. It must never become a second owner of their state.

### 2.3 Current known gaps to validate before coding

The following are **audit targets**, not confirmed defects unless a phase reproduces them on its fresh HEAD:

1. Runtime health uses a compact component-status map; verify whether it preserves enough stable reason/detail to diagnose optional component failures and dependency impact.
2. App health separately checks selected subsystems and merges Runtime health; verify whether overlapping checks can disagree, duplicate reasons, or accidentally perform expensive work.
3. Component health calls may be sequential and have no common explicit probe budget at the Runtime boundary; inspect current call sites and test slow/unresponsive health implementations.
4. App composition contains dependency wrappers and resource components; inventory whether dependency declarations are centralized, duplicated, or inconsistent with actual constructor wiring.
5. Worker supervision and TaskEngine both manage goroutines/workers for different purposes; verify their boundaries and ensure lifecycle loops are not accidentally submitted as ordinary TaskEngine work or vice versa.
6. Diagnostics are available in several owners; inventory whether the app-level snapshot is complete, consistent, bounded, and sufficiently useful without querying hot internal state.
7. Error classification across TaskEngine, JobManager, Scheduler, and RPCExecutor may not expose a consistent distinction between permanent failure, transient failure, deferred work, cancellation, admission rejection, and unknown external effect. Revalidate before proposing shared types.
8. Resource snapshots may use different units and ownership labels. Verify that goroutine counts, active work, queue bytes, retained media, cache cardinality, timers, and SQLite pressure can be interpreted without double-counting.
9. Shutdown reporting must distinguish a component that failed to stop from one whose context expired, was force-stopped, or was intentionally optional.
10. Readiness must distinguish “process is running” from “all critical dependencies are ready” and “some optional capability is degraded.”

Do not preemptively redesign any area that already meets the contract. Record evidence and classify each item as confirmed gap, existing adequate behavior, or not applicable.

---

## 3. Goals and acceptance criteria

### 3.1 Goals

- One canonical component lifecycle contract and one dependency graph.
- One deterministic application composition path.
- One bounded health/readiness projection with stable reason codes.
- A common operational vocabulary for outcomes and pressure, while preserving subsystem-specific error types and policies.
- A clear rule for which long-lived goroutines belong to Runtime/Supervisor and which finite work belongs to TaskEngine.
- Consistent, bounded diagnostics that help identify bottlenecks and degradation.
- Predictable quiesce, drain, stop, and emergency fencing.
- Low idle footprint; no new always-on management loop unless an existing owner demonstrably cannot satisfy a requirement.

### 3.2 Acceptance criteria

1. Every runtime-managed component is registered once, has a unique name, and declares only real dependencies.
2. Missing dependencies and cycles fail before any component starts.
3. Startup order is deterministic; rollback and shutdown stop dependents before dependencies.
4. Critical startup failure aborts startup; optional startup failure is visible as degraded and does not masquerade as healthy.
5. Health evaluation is bounded, read-only, and does not trigger recovery, retries, network calls, or resource creation.
6. Application readiness reports critical dependency failure separately from optional subsystem degradation.
7. Every diagnostic has a defined owner, unit, update semantics, and bounded cardinality.
8. Runtime/Supervisor owns only bounded lifecycle loops; TaskEngine remains the only physical execution authority for finite work.
9. JobManager and Scheduler retain their existing logical-work and timing responsibilities.
10. FloodWait/defer is not silently converted into a permanent failure or ordinary retry exhaustion.
11. Cancellation, admission rejection, dependency unavailability, and unknown external effect remain distinguishable.
12. Shutdown has one hard deadline, explicit phase outcomes, and no unbounded wait.
13. No unbounded maps, histories, queues, worker restarts, timers, goroutines, or error strings are introduced.
14. No user-facing command/plugin behavior changes as a side effect of management-plane work.
15. Every production behavior change has tests; every changed Go file is gofmt-formatted before commit.

### 3.3 Non-goals

- Replacing Runtime, TaskEngine, JobManager, Scheduler, RPCExecutor, SQLite, or plugin architecture.
- Building a distributed control plane or multi-process scheduler.
- Adding Prometheus/OpenTelemetry or an external monitoring dependency as a prerequisite.
- Creating a generic configuration database or global settings manager.
- Rewriting all errors into one universal error type.
- Running health probes continuously on a new polling goroutine.
- Making every optional plugin a Runtime component if it has no lifecycle requirement.
- Optimizing based on intuition without a measured bottleneck.

---

## 4. Proposed target model

```text
                       internal/app.App
             composition, dependency wiring, readiness
                              |
                    internal/runtime.Runtime
             lifecycle DAG, start/rollback/shutdown
                  /           |             \
          Components      Supervisor      Health
             |          lifecycle loops   snapshots
             |
   +---------+------------+-------------------+
   |         |            |                   |
TaskEngine JobManager  Scheduler          RPCExecutor
execution  durable     timing             Telegram policy
authority  work        authority          authority
   |         |            |                   |
   +---------+------------+-------------------+
                         |
                 Persistence / SQLite

Features and plugins use the existing public service boundaries.
They do not create private execution or lifecycle authorities.
```

### 4.1 Management plane vs data plane

**Management plane** answers:
- Is a component registered, started, ready, degraded, or stopping?
- Which dependencies must be ready first?
- What failed during startup or shutdown?
- Is capacity saturated or a durable backlog accumulating?
- Which owner is responsible for the current condition?

**Data plane** performs:
- command handling and plugin work;
- TaskEngine admission/execution;
- JobManager durable transitions;
- Scheduler due-time processing;
- Telegram RPC execution;
- persistence operations.

The management plane observes and coordinates lifecycle. It does not execute data-plane work or mutate domain state to “fix” a health result.

### 4.2 Component contract

Keep the existing runtime.Component contract as the starting point. Do not add methods to every component merely to satisfy one consumer. Add optional capability interfaces only when a concrete phase demonstrates a need and a consumer can use them without type assertions spread across the codebase.

Expected component lifecycle:

```text
Created
  -> Initializing
  -> Starting
  -> Running
  -> Quiescing
  -> Draining
  -> Stopping
  -> Stopped

Failure paths:
  -> Degraded (optional component / bounded worker failure)
  -> Failed (critical startup or unrecoverable lifecycle failure)
  -> ForcedStop (shutdown deadline exhausted; safety fence only)
```

This is a conceptual lifecycle view. Do not expand the Runtime state machine indiscriminately if existing state plus component health already expresses a state. Runtime lifecycle and component health are separate dimensions.

### 4.3 Dependency rules

- Declare dependencies based on required services, not package import relationships alone.
- A dependency edge means the dependency must be started before the dependent and stopped after it.
- Optional capabilities should not be modeled as hard dependencies unless startup truly cannot proceed without them.
- Dependencies must be declared once at composition time; do not infer them dynamically from health.
- Do not introduce hidden dependency lookups by global name or package-level singleton.
- A dependency becoming unhealthy after startup should affect readiness/degradation, not automatically trigger an uncontrolled restart cascade.
- Restart policy belongs to the existing owner that can prove restart safety. Runtime must not restart arbitrary components by default.

---

## 5. Health, readiness, and diagnostics contract

### 5.1 Separate lifecycle, health, and readiness

These concepts must not be conflated:

- **Lifecycle:** where a component is in Start/Stop.
- **Health:** whether it can currently perform its responsibility.
- **Readiness:** whether the application should accept new user-facing work.
- **Pressure:** whether it is operational but approaching a bounded capacity limit.

Example:
- Telegram transport disconnected: may be unready for Telegram work.
- Optional media preview storage unavailable: degraded, but commands not requiring media may remain available.
- TaskEngine queue near capacity: healthy but under pressure until an explicit threshold is crossed.
- JobManager recovery pass failed: degraded; it should not necessarily make unrelated command ingress unready.
- Runtime stopping: unready regardless of component-local health.

### 5.2 Stable health reason format

Prefer bounded stable reason identifiers, for example:

- runtime:not-running
- runtime:critical-component-unhealthy:<component>
- runtime:optional-component-degraded:<component>
- taskengine:admission-saturated
- jobs:recovery-failed
- jobs:oldest-ready-age-high
- scheduler:due-lag-high
- rpc:floodwait-deferred
- persistence:write-latency-high
- resources:owner-leak:<owner>

Reason strings must not include arbitrary user input, full payloads, tokens, peer identifiers, or unbounded raw error text. Preserve detailed errors in bounded logs/diagnostics with redaction.

### 5.3 Health probe rules

- Health must be read-only and side-effect free.
- A health call must not create a worker, trigger a retry, wake a scheduler, recover an attempt, ping Telegram, or mutate durable state.
- External checks must have a strict caller-supplied timeout and must be limited to checks whose operational value justifies the cost.
- Prefer reading subsystem snapshots already maintained by the owning subsystem.
- A slow optional health reporter must not stall the entire health response. Consider per-component bounded probe execution only if tests show sequential probing is a real latency problem; do not add goroutine-per-probe.
- Report stale/unavailable snapshots honestly rather than pretending they are healthy.
- Avoid high-frequency health polling. Health is an on-demand projection unless a current owner already maintains the required counters.

### 5.4 Diagnostics snapshot

Create or refine a stable app-level snapshot composed from owner snapshots. It should include, where already available and meaningful:

| Area | Useful signals | Owner |
|---|---|---|
| Runtime | state, uptime, component statuses, startup failures, last shutdown phase/error | Runtime |
| Supervisor | registered/running/degraded workers, restart count, panic count | Supervisor |
| TaskEngine | pool workers/min/max/idle, running, dispatching, waiting, waiting bytes, admission rejects, queue wait | TaskEngine |
| JobManager | ready occurrences, active attempts, deferred work, blocked/unknown-effect work, recovery result and age | JobManager |
| Scheduler | due count/lag, claim count, renew failures, reconciliation result | Scheduler |
| RPCExecutor | FloodWait/defer count and duration, limiter wait, retry class, RPC latency/failure | RPCExecutor |
| Persistence | transaction latency, busy/locked errors, write queue/backlog if present | Repository/store owner |
| Media/resources | active retained bytes, temp dirs, resource leases by owner | Media/resource owner |
| Process | active/max capacity, queue pressure, timeout/failure | Process runner |

Rules:
- Do not invent metrics if no owner currently maintains them.
- Do not add expensive SQL scans to a health endpoint.
- Prefer gauges/counters/histograms with fixed labels and bounded cardinality.
- Never use user ID, chat ID, URL, job ID, task ID, or arbitrary error text as metric labels.
- Do not retain an unbounded event history to answer “what happened”; use existing logs plus bounded last-error metadata/counters.
- Keep units explicit: bytes, count, duration, timestamp, ratio.
- Snapshot values are observational and may be slightly stale; document that rather than locking all subsystems into one global transaction.

### 5.5 Readiness policy

Define criticality at composition time:
- Critical component startup failure prevents the application from reaching ready.
- Optional component startup failure marks degraded and records a stable reason.
- Runtime running does not automatically mean application ready.
- A degraded optional feature should not block unrelated userbot capabilities.
- Saturation should only remove readiness if accepting more work would violate correctness or the configured overload contract. Otherwise report pressure and let the owning admission controller reject/defer.

Do not make a single boolean “healthy” the only signal. Preserve status, ready, reasons, and subsystem state.

---

## 6. Error and outcome semantics across subsystem boundaries

The goal is a shared vocabulary, not a universal error implementation.

### 6.1 Required semantic categories

| Category | Meaning | Expected behavior |
|---|---|---|
| Permanent | Invalid request, unsupported operation, or non-recoverable business failure | Do not retry automatically |
| Transient | Temporary dependency/system failure | Retry only under the owning retry budget/deadline |
| Deferred | Work cannot proceed now but remains valid (e.g. FloodWait, capacity window) | Yield/defer; do not burn permanent-failure budget |
| Cancelled | Caller, owner, or shutdown cancelled the work | Stop promptly; do not classify as business failure |
| AdmissionRejected | Work was not accepted by the execution owner | Return rejection; do not pretend an attempt ran |
| DependencyUnavailable | Required service is not ready | Fail/defer at the boundary according to operation semantics |
| UnknownEffect | External side effect may have happened but confirmation is missing | Fence/quarantine; never blindly replay |

### 6.2 Ownership of classification

- Telegram RPCExecutor classifies Telegram RPC/FloodWait outcomes and applies transport policy.
- TaskEngine reports admission, execution, cancellation, timeout, and terminal task outcomes.
- JobManager translates execution outcomes into durable attempt/retry/defer/blocked transitions.
- Scheduler handles due-time and misfire/overlap decisions, not generic operation retry.
- App/Runtime maps component health to application readiness; it must not reinterpret a job attempt or RPC failure.

Do not add a second retry policy to make errors look uniform.

### 6.3 FloodWait and deferred work

A known design risk from earlier audits is that a typed rate-limit/defer outcome can be flattened into a generic failure message before durable retry accounting. This is an **audit candidate requiring fresh verification**, not a confirmed current regression.

The implementation phase must verify:
- FloodWait is represented as a typed deferred outcome across RPCExecutor → task/job boundary.
- A known defer does not consume the same budget as a permanent/failed execution attempt unless explicitly specified.
- Retry budget and defer/yield budget are distinct where semantics require it.
- Caller deadline and shutdown cancellation remain authoritative.
- The durable next-eligible time is not earlier than Telegram's required wait.
- No per-job timer or goroutine is introduced; existing JobManager/Scheduler wake mechanisms remain authoritative.

---

## 7. Lifecycle and shutdown hardening

### 7.1 Startup

- Validate the entire dependency graph before starting anything.
- Start in deterministic topological order.
- Record start duration and outcome per component.
- Critical failure rolls back already-started components in reverse order.
- Optional failure remains visible in health and diagnostics.
- Do not mark a component started if its Start failed.
- Reconcile startup context cancellation with component-local cancellation and root Runtime context.

### 7.2 Quiesce and drain

The intended shutdown order remains:

1. Stop accepting new ingress/admission at the relevant owners.
2. Quiesce components in reverse dependency order according to the existing Runtime contract.
3. Drain accepted work under a shared deadline.
4. Stop components in reverse dependency order.
5. If graceful budget expires, call only explicit bounded ForcedStopper safety paths.
6. Cancel root lifecycle context and publish the final shutdown report.

Do not:
- call arbitrary blocking Stop after the hard deadline;
- create detached cleanup goroutines without an owner and join;
- let one component's private timeout exceed the application shutdown deadline;
- report a forced fence as a successful graceful drain;
- allow new work to enter after quiesce has completed.

### 7.3 Supervisor vs TaskEngine

Use existing Supervisor only for lifecycle-owned long-lived loops that need bounded panic recovery/restart behavior (for example, an existing maintenance loop where current ownership supports it).

Use TaskEngine for finite accepted work.

A component must not register an unbounded number of workers. Restart budgets, worker count, and backoff remain bounded. A supervisor worker must not spawn detached goroutines that outlive its context.

Before moving any existing loop into Supervisor, prove that doing so removes duplicate lifecycle ownership rather than simply moving the same goroutine to another abstraction.

---

## 8. Resource governance and low-idle-footprint rules

The management plane should observe resource use and enforce only its own bounded lifecycle limits. Resource capacity remains with the subsystem that owns the resource.

| Resource | Enforcement owner | Management visibility |
|---|---|---|
| Task queue, workers, task bytes | TaskEngine | pool snapshot, queue pressure, rejects |
| Durable attempts, recovery batch | JobManager | ready/active/deferred/blocked counts and oldest age |
| Schedule due batch and claim lease | Scheduler | due lag, claims, renewals |
| Telegram request rate | RPCExecutor | wait, FloodWait, retry outcomes |
| SQLite transactions/concurrency | persistence layer | latency, busy/locked errors, pool state |
| Media retention/temp files | media subsystem | bytes, active leases, cleanup failures |
| Lifecycle workers | Runtime Supervisor | worker count, restart budget, panics |
| Plugin resources | plugin scope/resource owner | active/leaked resource counts |

Hard constraints:
- No global semaphore that competes with TaskEngine or RPCExecutor.
- No second worker pool.
- No goroutine-per-health-check, job, timer, or metric.
- No unbounded diagnostics labels or error history.
- No duplicate cache of subsystem state in App.
- No global resource manager that tries to own all capacities.
- No idle polling loop when event-driven wakeups or existing bounded periodic owners suffice.
- Prefer explicit limits and overload behavior at the resource owner.

### 8.1 Resource budget review

For each long-lived goroutine, record:
- owner;
- purpose;
- start condition;
- cancellation source;
- maximum concurrent count;
- blocking operations/deadline;
- join point;
- restart policy (if any).

For each retained collection, record:
- maximum item count;
- maximum bytes where applicable;
- eviction/cleanup policy;
- owner;
- behavior on capacity exhaustion.

This inventory is required before adding any new long-lived management behavior.

---

## 9. Configuration and operational policy

Configuration remains owned by the subsystem that consumes it.

App/Runtime may validate cross-component invariants at startup, but must not become a global configuration store.

Examples of cross-component validation:
- Scheduler claim/lease bounds are compatible with the associated managed attempt lifecycle.
- JobManager recovery scan limits remain bounded and compatible with startup budget.
- TaskEngine pool capacity and queue byte limits are valid.
- Runtime shutdown budget is not shorter than mandatory safety fencing requirements.
- RPCExecutor rate/defer policy has a bounded maximum wait and respects parent cancellation.
- Media retained-byte limits are consistent with available storage policy.

Rules:
- Validate once at startup where possible.
- Error messages must name the owning setting and the violated invariant.
- Defaults remain local to the owning subsystem.
- Avoid duplicate config fields that represent the same policy.
- Do not silently clamp invalid values unless the existing config contract explicitly defines clamping.
- Do not introduce dynamic reconfiguration until a concrete runtime use case and safe atomic update model are established.

---

## 10. Implementation roadmap

The work is deliberately staged. Each phase has its own fresh HEAD, audit delta, tests, and commit. Do not combine all phases into one broad refactor.

### Phase U0 — Inventory and management contract audit

**Goal:** establish the real current state before changing APIs.

Inspect:
- Runtime component graph and registration sites;
- App composition root and dependency wrappers;
- all runtime.Component implementations;
- health and diagnostics source for TaskEngine, JobManager, Scheduler, RPCExecutor, persistence, media, process runner, plugin resources;
- Supervisor registrations and all long-lived goroutine owners;
- shutdown and forced-stop implementations;
- error/defer types across RPCExecutor → TaskEngine → JobManager.

Deliverables:
- component/dependency inventory;
- goroutine ownership inventory;
- diagnostic field/unit/owner inventory;
- health/readiness matrix (critical vs optional);
- error semantic flow map;
- confirmed gaps vs existing adequate behavior.

**Acceptance:** no source change required; findings have exact file/function evidence and no speculative bug claims.

**U0 Audit Outcome (Completed at baseline 08dd3fc9809d02d232e07dd1ddc9d4453a64c071):**
- Component map & dependency graph inventoried across 18 components;
- Duplicate/overridden dependency declarations identified between component implementations and `dependencyComponent` wrapper;
- Double Quiesce identified on `dispatcher` during shutdown;
- `dependencyComponent` does not implement `CriticalComponent`, dropping optional component behavior if wrapped;
- Optional component startup failure silently excluded from `startedComps` in `runtime.Start`, masking failure in `runtime.Health`;
- `App.HealthContext` health map overwritten by dummy `resourceComponent` health results;
- Caller startup context discarded in `App.startBackgroundServices`;
- Goroutine ownership verified: finite work strictly mapped to `TaskEngine`, long-lived loops bounded and lifecycle-owned;
- Error semantics verified: Telegram RPC FloodWait typed deferral and separate deferral budget in `JobManager` are functioning correctly;
- Full U0 report produced; awaiting user approval before proceeding to U1.

### Phase U1 — Lifecycle and dependency consistency

**Goal:** make composition deterministic and auditable without changing domain owners.

Potential work, only where U0 proves a gap:
- centralize component registration/dependency declarations in the existing App composition path;
- remove duplicated or contradictory dependency wiring;
- make optional component startup failure consistently visible;
- ensure registration is complete before Runtime.Start;
- verify rollback, repeated Stop, cancellation, and forced-stop paths;
- add architecture tests for one Runtime, one TaskEngine, one JobManager, one Scheduler, and one RPCExecutor per application composition.

**Do not** add another registry. The existing Runtime graph is the registry for lifecycle-managed components.

**Tests:**
- missing dependency/cycle rejection before any Start call;
- deterministic startup and reverse shutdown order;
- critical vs optional startup failure;
- rollback error aggregation;
- no duplicate registration;
- quiesce/drain/stop deadline behavior;
- repeated Stop and caller cancellation semantics.

**U1 Implementation Outcome (Completed):**
- **U1-A (F-02, F-05):** `Runtime` tracks startup failures separately in `failedComps` / `StartupFailures()`. Components that failed to start are never placed into `startedComps`, ensuring they are neither treated as running nor invoked during stop/drain. Optional component startup failure marks `Health()` as degraded and emits `runtime:optional-component-failed:<name>`. `dependencyComponent` now forwards `CriticalComponent` (`IsCritical() bool`).
- **U1-B (F-03):** Startup context is propagated to `Runtime.Start(ctx)` in `App.startBackgroundServices(ctx)`, allowing caller startup deadlines/cancellations to be observed, while preserving `appCtx` for the dispatcher root and isolating graceful teardown to `appShutdownTimeout`.
- **U1-C (F-04, F-06):** Duplicate manual `Dispatcher.Quiesce` removed from `App.performShutdown()`, restoring `Runtime` as the sole Quiesce authority. `dependencyComponent.Dependencies()` merges and deduplicates composition-level dependencies with intrinsic component dependencies, ensuring no required dependency is dropped.
- **U1-D:** Architecture tests added in `internal/architecture/system_management_u1_test.go` asserting exactly one Runtime, one TaskEngine, one JobManager, one Scheduler, and one Telegram Client/Executor per application composition, and verifying absence of forbidden global managers.
- **U1-E (Startup cancellation correctness):** `Runtime.Start()` now validates `ctx.Err()` both immediately after the component startup loop and within the critical section prior to `stateMachine.Transition(StateRunning)`. If the caller context expires or is cancelled during the last component's `Start()` (even when `comp.Start()` returns `nil`), all previously started components are rolled back, runtime transitions to `StateFailed` (never `StateRunning`), and the returned error preserves `errors.Is(err, context.Canceled)` or `errors.Is(err, context.DeadlineExceeded)`. Verified via `TestRuntime_StartupCancellationByLastComponentRollsBackAndFails` and `TestRuntime_StartupDeadlineExceededPostLoopRollsBack`.
- **U1-F (Dependency contract consistency):** Re-audited all `Dependencies()` implementations and formally documented the Additive Dependency Contract in `internal/app/runtime_resources.go`. Clarified the boundary between intrinsic package requirements (functional dependencies needed for standalone execution) and composition-level wiring edges (application root lifecycle ordering). Verified deterministic resolution (composition edges first, unlisted intrinsic edges appended), whitespace trimming, and deduplication. Added unit test `TestDependencyComponent_AdditiveDependencyContractRetainsIntrinsicDependencies`. Full test suite and race checks pass cleanly.

### Phase U2 — Health/readiness unification

**Goal:** one coherent, bounded application health projection.

Potential work:
- reconcile Runtime aggregate health with App HealthSnapshot;
- define critical vs optional readiness impact;
- use stable bounded reason identifiers;
- avoid duplicate or contradictory subsystem checks;
- ensure health probes are read-only and bounded;
- document snapshot freshness and unknown/unavailable state;
- add tests for slow health reporter, optional failure, critical failure, dependency unavailable, queue pressure, and shutdown.

**No new health polling loop.** Health is an on-demand projection of existing owner snapshots.

### Phase U3 — Diagnostics and resource observability

**Goal:** fill only high-value diagnostic gaps while preserving subsystem ownership.

Potential work:
- standardize field names and units in the app-level snapshot;
- expose existing owner snapshots through narrow interfaces;
- add missing counters only where the owner can maintain them cheaply;
- use fixed-cardinality reason/class labels;
- make oldest-age and pressure metrics available without expensive scans;
- ensure diagnostics snapshots do not retain mutable references.

**Tests/benchmarks:**
- snapshot consistency under concurrent updates;
- bounded memory/cardinality;
- diagnostics call latency and allocations;
- no hot-path regression;
- no SQLite scan added to readiness.

### Phase U4 — Error/defer semantic integrity

**Goal:** preserve the meaning of outcomes across boundaries.

Audit and, if confirmed, fix:
- typed FloodWait/defer propagation;
- distinction between retryable failure and deferred work;
- admission rejection vs execution failure;
- cancellation vs failure;
- unknown external effect quarantine;
- durable next-eligible time and attempt budget behavior.

**Tests:**
- FloodWait does not become permanent failure;
- defer does not consume ordinary failure budget where contract says it must not;
- retry/defer deadlines preserve caller and shutdown cancellation;
- admission rejection does not create a false execution attempt;
- unknown effect remains fenced;
- no extra retry loop or worker is created.

### Phase U5 — Resource and lifecycle conformance

**Goal:** prove every long-lived resource has one bounded owner.

Potential work:
- remove duplicate lifecycle ownership where evidence shows overlap;
- cap supervisor worker registration and restart behavior;
- ensure workers join on shutdown;
- ensure TaskEngine retains all finite work;
- align shutdown budgets and safety fences;
- add architecture tests for forbidden second engines/managers/pools.

**Tests/benchmarks:**
- goroutine count returns to baseline after start/stop cycles;
- no goroutine leak on worker panic/restart/cancellation;
- bounded restart storms;
- idle memory/worker baseline;
- repeated lifecycle start/stop tests;
- resource snapshot returns to zero after cleanup.

### Phase U6 — End-to-end management acceptance

**Goal:** validate operational behavior across subsystem boundaries.

Scenarios:
1. Critical dependency fails during startup.
2. Optional subsystem fails while core userbot remains usable.
3. TaskEngine saturates and rejects admission explicitly.
4. JobManager has deferred work and recovery failure.
5. Scheduler accumulates due lag without unbounded catch-up.
6. Telegram returns FloodWait and work is deferred correctly.
7. SQLite becomes busy and operations remain bounded.
8. Plugin resource leaks are surfaced without exposing sensitive identifiers.
9. Shutdown begins during active finite work.
10. Shutdown deadline expires and explicit forced fences run.
11. Repeated startup/shutdown does not grow goroutines or retained memory.
12. Health remains bounded while a subsystem probe is slow.

Final output must include measured before/after data where performance is claimed. Do not claim performance improvement from architecture diagrams or code inspection alone.

---

## 11. Test strategy and failure injection

### 11.1 Required test layers

- Package unit tests for each changed owner.
- Runtime dependency/lifecycle tests.
- App composition and readiness tests.
- Cross-boundary integration tests for RPC defer and durable JobManager outcomes.
- Race tests for changed concurrency paths.
- Benchmarks for diagnostics and lifecycle hot paths where changed.
- Resource leak tests for repeated lifecycle cycles.

Do not run CI or inspect workflow status unless explicitly requested.

### 11.2 Failure injection matrix

| Failure | Expected behavior |
|---|---|
| Missing dependency | Runtime refuses startup before any component starts |
| Dependency cycle | Runtime refuses startup with deterministic diagnostic |
| Critical component Start error | Startup aborts and rolls back started components |
| Optional component Start error | App becomes degraded; unaffected critical features remain available |
| Health reporter blocks | Health request remains bounded; no unbounded probe goroutine |
| Supervisor worker panics | Panic recorded; restart only within configured bounded policy |
| Worker exceeds restart budget | Worker becomes degraded/failed and remains observable |
| TaskEngine admission full | Explicit admission rejection; no shadow queue |
| JobManager recovery error | Bounded observable failure; existing coordinator remains sole retry/wake owner |
| Telegram FloodWait | Typed defer/yield semantics preserved |
| SQLite busy | Bounded failure/defer according to owner policy |
| Shutdown context expires | Only explicit ForcedStopper paths run; report records deadline/forced path |
| Component ignores cancellation | Runtime does not wait indefinitely beyond hard deadline |
| Repeated Start/Stop | No duplicate workers, goroutine growth, or inconsistent terminal state |

---

## 12. Security, privacy, and operational safety

- Health and diagnostics must never expose auth keys, bot tokens, session strings, message bodies, download URLs containing secrets, or raw user-controlled payloads.
- Avoid chat/user/peer/job/task identifiers in metric labels.
- Error summaries must be bounded and sanitized.
- Panic stack traces may be retained in logs under existing policy; do not expose them in public health output.
- Health endpoints are read-only and must not grant control over components.
- Administrative actions must continue to use existing authorization boundaries.
- Management diagnostics must not reveal whether a protected peer or user exists beyond existing command authorization.

---

## 13. AI coding rules

Every implementation phase must follow these rules:

1. Fetch the latest remote test-next HEAD first; record the exact SHA.
2. Pull fast-forward-only before editing. If local state is unavailable or pull fails, stop and report that limitation; do not imply a local pull occurred.
3. Inspect the exact current source and tests for the phase. This plan is not a substitute for source revalidation.
4. Preserve user changes. Never reset, force-push, or overwrite concurrent work.
5. Keep the phase narrowly scoped. If evidence contradicts this plan, update the plan before coding.
6. Update tests whenever production behavior, function contracts, or APIs change.
7. Run gofmt on every changed Go file before commit; verify no changed Go file remains unformatted.
8. Run targeted tests and relevant race/benchmark checks locally when feasible.
9. Do not inspect/poll CI unless the user explicitly asks.
10. Do not add a new engine, manager, runtime, registry, worker pool, retry loop, timer owner, event bus, or global resource manager.
11. Do not add a new dependency unless justified by a concrete requirement and approved scope.
12. Report exact files changed, tests run, results, commit SHA, and remaining limitations.

### Required phase handoff

Each phase report must include:
- branch and exact source HEAD;
- audit findings confirmed/rejected;
- design changes and why existing abstractions were insufficient;
- files changed;
- tests/benchmarks run and exact results;
- gofmt status;
- resource impact (goroutines, allocations, retained bytes, idle work);
- commit SHA;
- next phase and unresolved risks.

---

## 14. Completion checklist

The management system is considered coherent only when all applicable items are true:

- [ ] Existing Runtime remains the sole lifecycle coordinator.
- [ ] Existing Runtime graph is the sole lifecycle dependency registry.
- [ ] App composition declares real dependencies once and deterministically.
- [ ] Critical and optional component failures have explicit readiness semantics.
- [ ] Health is bounded, read-only, and does not trigger recovery or external retries.
- [ ] App health is a consistent projection of owner snapshots.
- [ ] TaskEngine, JobManager, Scheduler, RPCExecutor, persistence, media, and plugins retain state ownership.
- [ ] Long-lived workers have one owner, cancellation, cap, and join path.
- [ ] Finite work always goes through TaskEngine.
- [ ] Durable work always goes through JobManager.
- [ ] Timing orchestration remains in Scheduler.
- [ ] Telegram FloodWait and RPC retry remain in the shared RPCExecutor.
- [ ] Deferred work is not confused with failed work.
- [ ] Unknown external effects remain fenced.
- [ ] Diagnostics have fixed cardinality and explicit units/owners.
- [ ] Shutdown obeys one hard deadline and reports forced fencing honestly.
- [ ] No duplicate runtime/engine/manager/pool/retry/event infrastructure exists.
- [ ] Idle resource footprint has not increased without measured justification.
- [ ] Targeted tests and race tests pass for changed paths.
- [ ] All changed Go code is gofmt-formatted.

---

## 15. Final architectural rule

The system should feel like one cohesive application to an operator, but remain a set of clearly bounded authorities internally.

**One lifecycle authority. One physical execution authority. One durable work authority. One timing authority. One Telegram RPC policy authority. One persistence truth per data domain.**

Integration comes from explicit contracts, dependency edges, snapshots, and typed outcomes—not from putting every responsibility into a global manager.

The preferred implementation is the smallest change that makes the existing owners easier to coordinate, observe, test, and operate.
