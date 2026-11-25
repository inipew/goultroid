# Goultroid Unified System Management — AI Implementation Plan

**Status:** U5 COMPLETED  
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

Completed work & verification evidence:
- **Unified Readiness & Aggregate Health:** `Runtime.Health(ctx)` now correctly accounts for critical vs optional components. When any critical component returns `HealthUnhealthy` or times out (`HealthUnknown`), `AggregateHealth.Ready` is set to `false`. When an optional component fails or returns `HealthUnhealthy`, `AggregateHealth.Status` degrades to `HealthDegraded` while `Ready` remains `true` (avoiding false readiness failures). `App.HealthContext(ctx)` now directly merges `rtHealth.Ready` into `health.Ready` and reconciles subsystem states without overwrite.
- **Elimination of Overlapping / Contradictory Subsystem Checks (F-01):** The database `resourceComponent` now implements a real context-aware health probe calling `db.PingContext(ctx)`. Dummy `resourceComponent` health is eliminated; ping failure reliably sets `health.Subsystems["database"] = "unhealthy"`, `health.Ready = false`, and `health.Status = "unhealthy"`. Fallback checks for `database`, `eventbus`, and `scheduler` in `App.HealthContext` only run when `a.runtime == nil`.
- **Bounded & Context-Aware Probing with Panic Recovery:** `Runtime.Health(ctx)` probes components concurrently with bounded channel collection. If a component reporter ignores context or blocks, `Runtime.Health` returns strictly within the caller's deadline; unfinished probes are preserved honestly as `HealthUnknown` with detail `probe-timeout`. Component panics are caught and contained with status `HealthUnhealthy` and detail `panic`. Pre-cancelled contexts return immediately with `probe-cancelled`.
- **Stable, Bounded, Sanitized Reason Identifiers (F-07):** Raw error strings (such as `err.Error()`) are strictly prevented from entering `health.Reasons`. `sanitizeHealthReason` normalizes details into lowercase kebab-case identifiers with maximum 48-char bounds. Database failures emit `database:ping-failed`, filesystem failures emit `filesystem:unavailable`, and component errors emit stable identifiers (`comp:timeout`, `comp:cancelled`, `comp:error`). Reasons are deduplicated deterministically.
- **Subsystem Operational Health Hardening (F-08):** `JobManager.Health` now checks `baseCtx`, admission state, and consecutive recovery failures (`recovery-failing`) without misclassifying intentional `UnknownEffect` quarantined occurrences as component failure. `Scheduler.Health` reports `not-running` upon engine stop.
- **Regression Suite Added & Passing:**
  - `TestRuntime_HealthSlowReporterBoundedAndPreservesUnknown`
  - `TestRuntime_HealthCriticalFailureMakesNotReady`
  - `TestRuntime_HealthOptionalFailureDegradesWithoutMakingNotReady`
  - `TestRuntime_HealthCancelledContextReportsHonestUnknown`
  - `TestRuntime_HealthReporterPanicIsContained`
  - `TestHealthContext_DatabaseFailureMakesAppUnreadyAndUnhealthy`
  - `TestHealthContext_DatabaseHealthNotOverwrittenByRuntime`
  - `TestHealthContext_RuntimeOptionalFailureDegradesWithoutMakingAppNotReady`
  - `TestHealthContext_ShutdownStateIsUnhealthyAndNotReady`
  - `TestJobManager_HealthAcceptingAndClosed`, `TestJobManager_HealthRecoveryFailing`, `TestJobManager_HealthContextCancelled`
  - `TestSchedulerEngine_HealthRunningAndStopped`
  - Full race test suite passes cleanly across all core packages.

- **U2 Final Review & Hardening Outcome (Completed):**
  - **A. App health boundedness:** Eliminated synchronous blocking `os.Stat()` from `App.HealthContext()`. Replaced with `probeFilesystem(ctx, basePath)` which wraps the check in a non-blocking channel probe and strictly honors `ctx.Done()`. If filesystem probing hangs or the context expires, it returns `HealthUnknown` with reason `filesystem:probe-timeout` within deadline, avoiding hung calls on network/slow mounts. Verified via `TestProbeFilesystem_BoundedAndContextAware`.
  - **B. Reason & Identifier Sanitization:** Audited and hardened all dynamic reason sources across Runtime and App. Component names, plugin names, task names, and resource owner labels are passed through `sanitizeHealthReason` and `sanitizeHealthIdentifier`. Any untrusted strings (tokens, query parameters, path traversals `../../etc/passwd`, raw errors, non-alphanumerics) are stripped and normalized to kebab-case with a hard upper bound of 48 characters. Verified via `TestSanitizeHealthReason_UntrustedInputs` and `TestSanitizeHealthIdentifier_UntrustedInputs`.
  - **C. HealthUnknown Semantics Consistency:** Critical component timeouts or unknown states preserve `HealthUnknown` status with `Ready = false`, while aggregate status remains `HealthDegraded` (unless another component is unhealthy). Preserved honest unknown reporting without false escalation to unhealthy.
  - **D. Comprehensive Test Coverage & Hardening:**
    - `TestProbeFilesystem_BoundedAndContextAware`: Verifies filesystem probing respects context cancellations and timeouts (< 50ms).
    - `TestSanitizeHealthReason_UntrustedInputs` & `TestSanitizeHealthIdentifier_UntrustedInputs`: Verifies boundary limits and sanitization of malicious/dirty input strings.
    - `TestHealthContext_DependencyUnavailable`: Verifies EventBus created-not-started (`eventbus:not-started`, `HealthDegraded`) and EventBus closed (`eventbus:closed`, `HealthUnhealthy`).
    - `TestHealthContext_ConcurrentStateTransitions`: 4 background goroutines rapidly flipping lifecycle states while 4 concurrent goroutines call `App.HealthContext()`, asserting zero panics and valid status under `-race`.
    - `TestHealthContext_QueuePressure`: Verifies process runner concurrency saturation (`process:concurrency-saturated`), command concurrency saturation (`commands:concurrency-saturated`), telegram readiness integration (`telegram:not-ready`), EventBus dropped events (`eventbus:dropped-events`), and EventBus handler panics (`eventbus:handler-panics`).
  - **E. Clean Verification Outcomes:**
    - Modified package race tests (`./internal/runtime`, `./internal/app`, `./internal/telegram`): PASS.
    - Related subsystem race tests (`./internal/jobs`, `./internal/scheduler`, `./internal/taskengine`): PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).

- **U2 Final Verification — Resource Ownership & Bounded Probe Correction Outcome (Completed):**
  - **A. Filesystem health probe resource limits:** Audited `probeFilesystem()` and eliminated goroutine accumulation on hung storage/mounts. Storage owner state is queried in-memory first without I/O (`StorageHealth()`). When physical I/O is probed, active goroutines are bounded by a hard capacity limit of at most 1 active probe slot (`fsProbeSlot`). If a probe hangs or exceeds deadline, repeated subsequent health checks do not spawn goroutines, immediately returning `HealthUnknown` with `filesystem:probe-saturated`. Verified resource boundedness via `TestProbeFilesystem_ResourceBoundedWhenStuck` under `-race`.
  - **B. Command concurrency & resource ownership:** Audited command execution path from Dispatcher to TaskEngine. Removed duplicate manual counters (`commandCapacity`, `SetCommandCapacity`, `AddRunningCommands`) from Dispatcher. Real command execution capacity is derived directly from the admission owner (`TaskEngine.Stats().Pools["interactive"].MaxWorkers`), while active count reflects live execution in the worker handler (`d.runningCommands`). Handler guarantees release via `defer` across success, error, panic, and cancellation. Verified end-to-end integration via `TestHealthContext_QueuePressure` using a real TaskEngine and Dispatcher.
  - **C. Client readiness & test isolation:** Removed public `SignalReady()` method from `Client`, preventing artificial readiness bypasses outside of successful Telegram authentication. Replaced `NewClientWithDispatcher` with test-only constructor `NewClientForTesting(dispatcher, ready)` with immutable initial readiness state, ensuring production readiness is solely governed by Telegram connection and authentication completion in `c.raw.Run()`.
  - **D. Verification results:**
    - Fresh race tests on modified packages (`./internal/app`, `./internal/telegram`, `./internal/services/storage`, `./internal/runtime`): PASS.
    - Full repository race suite (`go test -race ./...`): PASS (100% across all packages).

- **U2 Final Correction — Non-Blocking Storage Health Snapshot (Completed):**
  - **A. Lock-Free Storage Health Snapshot:** Replaced `FileStorage.usage` `int64` with `atomic.Int64`. Completely eliminated mutex acquisition (`f.mu.RLock()`) from `StorageHealth()`, reading `f.usage.Load()` lock-free. `Put` checks `f.usage.Load()` under `mu.Lock()` for quota enforcement and updates `f.usage.Add(...)` after successful disk write. `Delete` updates `f.usage.Add(-size)` with clamp to 0. Even if a disk write hangs or blocks in filesystem I/O, `StorageHealth()` returns instantaneously without blocking.
  - **B. Subsystem Separation in App Health:** Separated `storage` (owner logical state / quota enforcement) and `filesystem` (physical mount accessibility) into independent keys in `health.Subsystems`. Physical filesystem probe results never overwrite a degraded storage quota status (`storage:quota-exceeded`). Fixed reason preservation so internal constant reason identifiers retain standard canonical formatting.
  - **C. Regression Test Coverage & Verification:**
    - `TestFileStorage_StorageHealthNonBlockingDuringTransaction`: verifies `store.StorageHealth()` returns immediately (< 1ms) while a concurrent `Put` holds the transaction mutex.
    - `TestFileStorage_UsageSnapshotConsistency`: verifies atomic usage accuracy across initialization, `Put`, and `Delete`.
    - `TestFileStorage_StorageHealthQuotaExceeded`: verifies degraded quota reporting.
    - `TestHealthContext_DoesNotWaitOnStorageMutex`: verifies `App.HealthContext()` completes within deadline (< 20ms) while storage `Put` is holding its transaction lock.
    - `TestHealthContext_StorageDegradedNotOverwrittenByFilesystemHealthy`: verifies degraded storage retains `health.Status = HealthDegraded` even if physical filesystem probe is healthy.
    - `TestHealthContext_StorageAndFilesystemCombinations`: parametric matrix testing combinations of storage health and filesystem probe states.
    - Fresh race tests on modified packages (`./internal/services/storage`, `./internal/app`, `./internal/runtime`, `./internal/telegram`): PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).

- **U2 Test Hardening — Deterministic Non-Blocking & Quota Verification (Completed):**
  - **A. Deterministic Quota Full & Recovery Verification (`TestFileStorage_StorageHealthQuotaExceeded`):** Hardened quota test to perform an actual `Put()` writing physical data and metadata, configure capacity at 100% full, verify `StorageHealth()` returns `degraded` with reason `storage:quota-exceeded`, verify subsequent `Put()` operations are rejected with `storage.ErrQuotaExceeded`, verify `Delete()` recovers capacity, and verify subsequent `Put()` succeeds after recovery.
  - **B. Explicit Synchronization on Non-Blocking Storage Test (`TestFileStorage_StorageHealthNonBlockingDuringTransaction`):** Replaced sleep assumptions with explicit reader signaling (`readStarted`) confirming `Put()` has entered `Read()` while holding the transaction lock before `StorageHealth()` is evaluated. Added watchdog protection, clean reader unblocking, and waited on goroutine completion to eliminate goroutine leaks.
  - **C. Deterministic App Health Non-Blocking Test (`TestHealthContext_DoesNotWaitOnStorageMutex`):** Replaced sleep with explicit reader signaling to guarantee transaction lock contention, measured actual execution duration under a 500ms watchdog, asserted strict duration boundary (<= 50ms, actual < 1ms), and guaranteed clean termination of the `Put()` goroutine before test exit.
  - **D. Verification Results:**
    - `go test -count=1 -race ./internal/services/storage`: PASS (1.023s).
    - `go test -count=1 -race -run 'TestHealthContext' ./internal/app`: PASS (1.413s).
    - `go test -count=1 -race ./internal/app`: PASS (18.907s).
    - `go test -race ./...`: PASS (100% across all packages).
    - `go vet ./internal/app ./internal/services/storage`: PASS (0 warnings).
    - `gofmt -l` on all changed files: PASS (0 unformatted files).

**No new health polling loop.** Health is an on-demand projection of existing owner snapshots.

### Phase U3 — Diagnostics and resource observability

**Goal:** fill only high-value diagnostic gaps while preserving subsystem ownership.

Completed work & verification evidence:
- **U3-A — Subsystem Diagnostic Hardening & Final Correction (Completed):**
  - **Storage Diagnostics (F-U3-01):** Added `Diagnostics` struct and optional `DiagnosticsProvider` interface to package `storage`. Implemented lock-free, zero-allocation `Diagnostics()` on `FileStorage` exposing `Usage`, `MaxQuota`, and `UtilizationPercent`. Upgraded `MemoryStorage` to O(1) lock-free atomic usage accounting (`usage atomic.Int64`), eliminating full map iterations and mutex contention during diagnostics. Forwarded via `OwnerStorage` wrapper to preserve boundary encapsulation. Verified via `TestFileStorage_Diagnostics`, `TestMemoryStorage_Diagnostics`, and `TestMemoryStorage_ConcurrentPutDeleteDiagnostics`.
  - **TaskEngine Admission Rejections Counter (F-U3-03):** Added `admissionRejections atomic.Int64` to `Engine` to record every rejected submission at the admission boundary (unstarted/quiesced engine, cancellation prior to linearization, WorkSpec validation failure, duplicate task IDs, and queue/result capacity exhaustion). Documented counter semantics and verified no double counting across all rejection paths via `TestStats_AdmissionRejections_ExhaustivePaths`.
  - **PoolRuntimeStats Redundancy Cleanup (F-U3-05):** Formally documented `PoolRuntimeStats.IdleWorkers` as deprecated while maintaining exact legacy mirroring of `Idle` for caller backward compatibility.
  - **Bounded & Sanitized Diagnostics Error Strings (F-U3-02):** Implemented `SanitizeDiagnosticError` in `internal/runtime`, enforcing:
    1. A hard upper bound of 256 bytes for `WorkerSnapshot.LastError` (`Supervisor`) and `PeriodicTaskSnapshot.LastError` (`periodicCoordinator`) without splitting multi-byte UTF-8 runes.
    2. Targeted credential redaction for URL passwords (`://user:[REDACTED]@host`), Telegram bot tokens (`[REDACTED_TOKEN]`), and key-value secrets (`token=[REDACTED]`, `api_hash=[REDACTED]`), preserving safe and useful diagnostic errors without confusing truncation with sanitization.
    3. Deterministic synchronization in `TestSupervisor_SnapshotLastErrorBounded`, replacing arbitrary sleep with channel completion and graceful worker joining.
  - **Verification Results:**
    - Targeted race tests (`./internal/services/storage`, `./internal/taskengine`, `./internal/runtime`, `./internal/scheduler`): PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).
    - `go vet ./...`: PASS (0 warnings).
    - `gofmt -l .`: PASS (0 unformatted files).
- **U3-B — Aggregation Consolidation & Durable Observability (Completed):**
  - **Runtime Status & Startup Failures (F-U3-04):** Added `Runtime.Diagnostics()` returning `RuntimeDiagnostics` with `ComponentSnapshot` and `StartupFailureSnapshot`. Exposes authoritative component state (`starting`, `running`, `failed`, `registered`, `stopping`, `stopped`), status (`healthy`, `degraded`, `failed`), and sanitized startup failure errors via `SanitizeDiagnosticError`. Projected into `DiagnosticsSnapshot.RuntimeComponents` and `DiagnosticsSnapshot.StartupFailures` without duplicating state or running unmanaged goroutines.
  - **U3-B Final Correction (Lifecycle Mapping & Deterministic Tests):**
    - Component lifecycle states strictly mapped: `initializing`/`starting` -> `starting`, `running` -> `running`, `stopping` -> `stopping`, `stopped` -> `stopped`, `failed` -> `failed`.
    - Unstarted registered components retain `state: "registered"` and `status: ""` (omitted in JSON), preventing false "healthy" reporting before verification.
    - Startup failures consistently distinguished: optional failures report `state: "failed"`, `status: "degraded"`; critical failures report `state: "failed"`, `status: "failed"`.
    - Incremental thread-safe updates to `startedComps` and `failedComps` inside `Runtime.Start()` under `r.mu` ensure real-time visibility during startup and preserve critical failure causes.
    - Added deterministic test suite: `TestRuntime_Diagnostics` (registered before start not healthy), `TestRuntime_DiagnosticsStartingState` (channel-synchronized mid-start visibility), `TestRuntime_DiagnosticsStoppingState` (channel-synchronized mid-stop and stopped states), `TestRuntime_DiagnosticsCriticalStartupFailure` (distinguishing critical vs unreached components and credential redaction), and `TestDurableDiagnostics_ContextDeadlineRespected` (SQLite protocol deadline and cancellation enforcement with 0 sleep).
  - **PersistencePanics Deduplication (F-U3-06):** Removed duplicate `PersistencePanics` counter from root `DiagnosticsSnapshot`, retaining `PersistencePumpStats.Panics` (`snapshot.PersistencePump.Panics`) as the single authoritative metric.
  - **ReadyOccurrences Durable Metric (F-U3-07):** Added `ReadyOccurrences int` to `DurableDiagnostics` (`domain.go`), `Diagnostics` (`manager.go`), and `DurableDiagnostics` SQLite query (`store.go`). Evaluates `COUNT(*) WHERE state = 'dispatched' AND ready_at <= ?` using index `idx_job_occ_state(state, ready_at)`. Verified that future `ready_at`, deferred attempts, blocked occurrences, and terminal occurrences are not counted as ready via `TestDurableDiagnostics_ReadyOccurrences`.
  - **Verification Results:**
    - Targeted race tests (`./internal/runtime`, `./internal/app`, `./internal/jobs`, `./internal/jobs/sqlite`): PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).
    - `go vet ./...`: PASS (0 warnings).
    - `gofmt -l .`: PASS (0 unformatted files).

- **U3-C — Diagnostics Consistency & Resource Observability (Completed):**
  - **Per-Owner Resource Breakdown (F-U3-08 & U3-C.1):**
    - Extended `ResourceDiagnostics` with `ByOwner []ResourceOwnerDiagnostics`, where each owner entry includes `Owner` string, `TotalActive int`, `CountsByType map[string]int`, and `Leaked int`.
    - Projections use the canonical owner (`*resource.Manager`) without GlobalResourceManager, ResourceRegistry, or polling goroutines.
    - Preserved bounded cardinality (`DefaultMaxTrackedResources = 16 * 1024`), fixed-cardinality type taxonomy, and deterministic sorting by `Owner`.
  - **Storage Diagnostics on App Snapshot (U3-C.2):**
    - Integrated `Storage storage.Diagnostics` into `DiagnosticsSnapshot`.
    - Direct access via `a.storage` (with fallback to `a.media.Storage()`), querying `Diagnostics()` from `storage.DiagnosticsProvider`.
    - 100% lock-free, zero-allocation, atomic O(1) query with zero I/O and zero SQL scans.
    - Updated `HealthContext()` to evaluate `a.storage` independently while maintaining strict separation between in-memory storage health and physical filesystem mount health.
  - **Runtime/App Lifecycle & Uptime Semantics (U3-C.3):**
    - Runtime is the authoritative source of truth for execution uptime: `a.runtime.Uptime()` is projected directly into `DiagnosticsSnapshot.Uptime`.
    - Strict lifecycle semantics defined:
      - `StateCreated`, `StateInitializing`, `StateStarting`: Uptime is strictly `0` (never negative, never ticking during construction or initialization).
      - `StateRunning`, `StateStopping`: Uptime measures active duration `time.Since(startTime)`, clamped to `0` in case of clock jitter.
      - `StateStopped`, `StateFailed`: Uptime is frozen at `stopTime.Sub(startTime)` for runtimes that ran, or `0` for failed startups that never ran.
    - App marks `startTime` strictly when entering `lifecycleRunning` (not at construction/wiring time).
  - **Snapshot Safety & Immutability (U3-C.4):**
    - Guaranteed that diagnostics snapshots do not expose mutable internal references:
      - Defensive copy of `ByOwner` and `CountsByType` in `ResourceDiagnostics`.
      - Defensive copy of `res.Metadata` in `plugin.Scope.Resources()` and `PluginDiagnostics`.
      - Defensive copy of `TaskEngine.Pools` and `TaskEngine.Resources`.
      - Defensive copy of `ShutdownReport.Phases` and `ShutdownReport.Errors` slices in `Runtime.LastShutdownReport()` (catatan: defensive copy diterapkan pada backing slice `[]error`, bukan deep copy dari underlying objek error).
    - Mutating slices or maps on a returned snapshot does not corrupt internal state or future snapshots.
  - **Performance Benchmarks & Profiling:**
    - `BenchmarkApp_Diagnostics_Idle`: ~19.4 µs/op, 1904 B/op, 8 allocs/op.
    - `BenchmarkApp_Diagnostics_Active`: ~25.2 µs/op, 4496 B/op, 32 allocs/op.
    - `BenchmarkApp_Diagnostics_BoundaryOwners` (64 distinct owners): ~85.2 µs/op, 49.9 KB/op, 342 allocs/op.
    - `BenchmarkRuntime_Diagnostics` (10 components): ~1.65 µs/op, 1376 B/op, 7 allocs/op.
    - Biaya pemanggilan `Diagnostics()` terisolasi pada operasi snapshot diagnostics dan dibuktikan melalui benchmark di atas (alokasi dan latensi pengambilan data diagnostik); benchmark ini merefleksikan overhead snapshotting dan tidak mengklaim ketiadaan lock contention pada hot path operasional (Dispatcher, TaskEngine admission, RPCExecutor) yang memerlukan profiling contention terpisah.
  - **Verification Results:**
    - Targeted race tests (`./internal/app`, `./internal/runtime`, `./internal/plugin`, `./internal/services/storage`): PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).
    - `go vet ./...`: PASS (0 warnings).
    - `gofmt -l .`: PASS (0 unformatted files).

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

- **U4 Implementation & Semantic Integrity Outcome (Completed):**
  - **F-U4-01 — TaskEngine Admission Rejection & Retry Budget Integrity:**
    - Audited all SQLite retry budget accounting queries: `CountRetryBudgetUses`, `AttemptSummary`, and `ListRecoveryCandidates`.
    - Updated query filters across all three locations to `state NOT IN ('deferred', 'aborted_before_start')` instead of `state != 'deferred'`.
    - Admission rejection (e.g. `ReasonPoolBacklogFull`, `ReasonOwnerQueueFull`) records the attempt as `AttemptAbortedBeforeStart` for audit trails and lease fencing, but does NOT count as an execution retry attempt or consume the retry budget.
    - Occurrences whose previous attempt was aborted before start remain eligible for automatic recovery re-drive without premature occurrence failure.
    - Verified via store unit tests (`TestStore_AdmissionRejectionDoesNotConsumeRetryBudget`) and manager end-to-end recovery tests (`TestAdmissionRejectionDoesNotExhaustRetryBudgetOrFinalizeOccurrence`).
  - **F-U4-02 — RPC Cancellation Precedence Over Error Class:**
    - Audited `RPCFailure.ExecutionSemantics()`.
    - Prioritized context cancellation (`errors.Is(f.Err, context.Canceled)`) immediately after ambiguous failure evaluation and strictly *before* error class checks (`RPCFloodWait` / `RPCTransient`).
    - Context cancellation during an RPC call returns `execution.DispositionCancelled` (`code: "context_cancelled"`), ensuring caller cancellation is never misclassified as a retryable flood wait or transient error.
    - Verified via `TestRPCFailureExecutionSemantics`.
  - **F-U4-03 — Shutdown vs Caller Cancellation Distinction:**
    - Updated TaskEngine worker execution signature: `executeAssignment(taskCtx, engineCtx, spec, grant, onStarted)`.
    - Passing engine context (`workerCtx`) enables deterministic discrimination between caller/user cancellation (`tasks.CauseUserCancel`) and engine shutdown/drain (`tasks.CauseShutdown`).
    - Interrupted attempts during shutdown preserve lease-expiry quarantine instead of being prematurely marked user-cancelled.
    - Verified via `TestEngine_ExecutionShutdownVsUserCancel`.
  - **F-U4-04 — Semantic RetryAfter & Disposition Integrity:**
    - In TaskEngine `executeAssignment`, ensured that semantic `RetryAfter` is evaluated only for retryable results (`semantics.Disposition == execution.DispositionRetryable && semantics.RetryAfter > 0`).
    - Permanent failure results (`execution.DispositionPermanent`) or non-retryable results carrying `RetryAfter` metadata retain their permanent disposition and metadata, without entering rate limited / deferral flow (`CauseRateLimited`).
    - Verified via `TestEngine_SemanticRetryAfterClassification`.
  - **F-U4-05 — Context Cancellation Precedence in TaskEngine:**
    - In TaskEngine `executeAssignment`, placed `case errors.Is(err, context.Canceled)` strictly before `errors.As(err, &rateLimit)` and `case errors.Is(err, context.DeadlineExceeded)`.
    - Guaranteed that true context cancellations are immediately classified as `OutcomeCancelled` with `DispositionCancelled`, and never deferred or scheduled for rate limit retry.
    - Verified via `TestEngine_CancellationPrecedesRateLimit`.
  - **Verification Results:**
    - Targeted race tests on `./internal/telegram`, `./internal/taskengine`, `./internal/jobs`, `./internal/jobs/sqlite`: PASS.
    - Full repository race test suite (`go test -race ./...`): PASS (100% across all packages).
    - `go vet ./...`: PASS (0 warnings).
    - `gofmt -l .`: PASS (0 unformatted files).


### Phase U5 — Resource and lifecycle conformance

**Status:** COMPLETED  
**Final Accepted Implementation Baseline:** `505d400322baf75bcc0613f4f1a2110bedd938dd`  
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

- **Implementation Details (U5-B):**
  - **Supervisor Forced Shutdown (U5-B1):**
    - Implemented `runtime.ForcedStopper` on `Supervisor` (`ForceStop(ctx context.Context) error`).
    - Cancels all workers via context cancellation, bounds worker join wait with caller context/deadline without blocking indefinitely.
    - If forced join deadline expires with active workers, `Supervisor` accurately reports `StateStopping` (never falsely claiming `StateStopped`), returns a context deadline error, and lists active workers.
    - Safe background reaper joins exiting workers and transitions state to `StateStopped` once all workers finish.
  - **Worker Registration Semantics & Retention (U5-B2):**
    - Added `Deregister(name string) error` allowing explicit deregistration of stopped workers while rejecting running workers.
    - Added bounded capacity protection with automatic pruning (`PruneStopped()` and `pruneStoppedLocked()`) for clean one-shot workers (`NeverRestart && WorkerStateStopped && lastError == nil`) when worker capacity is reached.
    - Preserved diagnostic health history for lifecycle workers (`RestartTransient`) and failed/degraded workers.
  - **Voice Persistence Ownership (U5-B3):**
    - Eliminated all unmanaged background goroutines (`go func() { ... }()`) using `context.Background()` across `internal/voice/queue.go` and `internal/voice/player.go`.
    - Made `Enqueue`, `Dequeue`, `Clear`, and `persistSession` synchronous with explicit context propagation and bounded timeouts.
    - Guaranteed in-memory consistency: DB operations precede memory modifications so persistence errors abort changes cleanly without desynchronization.
  - **Resource Snapshot Verification on Shutdown (U5-B4):**
    - Added `ForceCleanupAllContext(ctx context.Context) []error` to `resource.Manager` to clean up eligible resources across all owners under bounded shutdown context.
    - Added `VerifyShutdownClean() error` to `resource.Manager` to inspect remaining active or leaked resources and format deterministic, sensitive-data-safe summaries (`owner:[type=count]`).
    - Integrated force-cleanup and leak verification into `App.performShutdown()`, deterministically combining cleanup errors and resource leaks into the final shutdown error.
    - Disallowed deleting untracked resources without cleanup callbacks during force cleanup to prevent false-clean snapshots.
  - **Repeated Lifecycle & Architecture Conformance Tests (U5-B5):**
    - Added `TestRepeatedLifecycle_StartStopRounds`, `TestRepeatedLifecycle_WorkerPanicAndRestart`, `TestRepeatedLifecycle_CancellationWhileWorkerActive`, `TestRepeatedLifecycle_ForceStopUncooperativeWorker`, and `TestRepeatedLifecycle_VoicePersistenceLifecycle` in `internal/app/repeated_lifecycle_test.go`.
    - Added architecture enforcement tests in `internal/architecture/system_management_u5_test.go` ensuring `internal/voice` contains zero detached `go` statements and `Supervisor` implements `ForcedStopper`.
  - **Verification & Closure Details (U5-C):**
    - **Voice Context & Persistence Integrity (U5-C1):**
      - Removed all fallback `context.Background()` instances across `internal/voice/queue.go` and `internal/voice/player.go`.
      - Enforced non-nil caller context across all queue and player operational methods (`NewQueue`, `Enqueue`, `Dequeue`, `Clear`, `Play`, `Pause`, `Resume`, `Skip`, `Stop`, `Leave`, `SetVolume`, `SetRepeatMode`, `GetOrCreateSession`, `Shutdown`), immediately returning `ErrNilContext` or `ctx.Err()`.
      - Guarded against post-shutdown operations via `s.stopped` and `s.stopCh`, ensuring no new backend calls or database queries can execute after `Shutdown()`.
      - Guaranteed honest error reporting: operations that succeeded on the Telegram/backend layer (e.g. `Play`, `Pause`, `Resume`, `SetVolume`) do not falsely claim rollback if persistence fails, but honestly report the persistence failure to the caller.
      - Updated `Service.Shutdown()` to capture and deterministically aggregate backend and persistence errors via `errors.Join`.
      - Added deterministic unit tests for nil/canceled contexts, repository errors, post-shutdown rejection, and state consistency.
    - **Strengthened Full App Lifecycle Tests (U5-C2):**
      - Added `TestRepeatedLifecycle_RealAppCompositionStartShutdownRounds` testing repeated startup/shutdown on full `App` composition roots built via `New(cfg)` with verified resource cleanup and settling.
      - Added `TestRepeatedLifecycle_LateCleanupCallbackDetected` confirming delayed cleanup callbacks are reliably trapped and reported without falsely reporting clean state.
      - Added `TestRepeatedLifecycle_VoiceShutdownNoLingeringOperations` ensuring all database queries finish before `Shutdown()` returns and in-flight operations drop to zero.
      - Added AST architecture test `TestU5_VoiceQueueAndPlayerHaveNoBackgroundContextFallbacks` ensuring zero `context.Background()` fallbacks in `queue.go` and `player.go`.
    - **In-flight Operation Coordination & Atomic Admission Control (U5-C Correction):**
      - Integrated atomic admission control (`beginOp(ctx)` / `release()`) in `voice.Service`.
      - Guarded all public operational methods (`Play`, `Pause`, `Resume`, `Skip`, `Stop`, `Leave`, `SetVolume`, `SetRepeatMode`, `GetOrCreateSession`) with atomic admission under `s.mu.Lock()`, atomically verifying `!s.stopped` and incrementing `s.activeOps`.
      - Immediate fail-closed admission rejection: new operations attempting admission after shutdown has begun are rejected immediately with `ErrServiceStopped`.
      - Graceful coordinated shutdown: `Shutdown(ctx)` sets `s.stopped = true`, signals `s.stopCh`, releases `s.mu`, and deterministically awaits completion of active operations bounded by caller context/deadline.
      - Honest timeout reporting: if shutdown deadline expires with operations still active, returns an error wrapping `context.DeadlineExceeded` with remaining active operation counts, without corrupted state.
      - Teardown synchronization: backend disconnection (`Stop`, `Leave`), in-memory session cleanup, and final session persistence execute strictly after all in-flight operations complete, guarded against concurrent shutdown calls via `s.shutdownMu`.
      - Added deterministic concurrency tests (`TestPlayerService_ShutdownWaitsForInFlightOperation`, `TestPlayerService_ShutdownDeadlineExceededOnStuckOperation`, and `TestRepeatedLifecycle_VoiceShutdownConcurrentInFlightOperationCoordination`).
    - **Retryable Voice Teardown & Shutdown Result Integrity (U5-C5):**
      - Differentiated lifecycle shutdown phases: shutdown initiated (`stopped`), active operations drained, and teardown completed (`drained`).
      - Preserved session teardown snapshot in `s.pendingTeardown` tracking granular progress per session (`stopped`, `left`, `persisted`).
      - Enforced strict teardown ordering (F-U5-C5-01): `backend.Stop()` must complete successfully before `backend.Leave()` is invoked. If `Stop()` fails, teardown for that session aborts without calling `Leave()` or persisting idle state, leaving the connection intact for `Stop()` retry.
      - Retried only uncompleted teardown steps upon subsequent `Shutdown()` invocations without duplicating previously completed side effects; `backend.Leave()` and idle state persistence execute strictly after `Stop()` succeeds.
      - Guaranteed `s.drained` is set to `true` strictly when all sessions have completely and successfully torn down.
      - Documented lifecycle boundary: `voice.Service.Shutdown()` is individually retryable, while the top-level `App.performShutdown()` and `Runtime` execution remain single-cycle bounded shutdowns (`stopOnce`).
      - Added `StopCh() <-chan struct{}` to provide explicit synchronization that shutdown admission has closed, eliminating timing jitter in concurrency tests.
      - Added deterministic unit tests for backend `Stop` retry without calling `Leave` on failure, backend `Leave` retry, idle session persistence retry, multiple sessions partial failure, and post-success idempotency.
  - **Verification Results:**
    - Targeted race tests on `./internal/runtime`, `./internal/voice/...`, `./plugins/voice/...`, `./internal/resource`, `./internal/app`, `./internal/architecture`: PASS.
    - Full repository race test suite (`go test -race ./...`): PASS.
    - `go vet ./...`: PASS (0 warnings).
    - `gofmt -l .`: PASS (0 unformatted files).
  - **U5 Closure & Lifecycle Boundary Summary:**
    - **U5-A — Read-only audit:** All lifecycle findings and resource risks investigated and confirmed resolved.
    - **U5-B — Initial hardening:** Implemented `runtime.ForcedStopper` on `Supervisor`, worker registration pruning, synchronous voice persistence with explicit context propagation, `resource.Manager` force cleanup & leak verification, and repeated lifecycle tests.
    - **U5-C — Conformance correction:** Eliminated `context.Background()` fallbacks, enforced persistence integrity, integrated atomic operation admission (`beginOp`/`release`), and bounded in-flight shutdown coordination.
    - **U5-C5 — Retryable teardown:** Granular teardown progress tracking (`sessionTeardownProgress`) and retry for `Stop`, `Leave`, and idle session persistence.
    - **F-U5-C5-01 — Strict teardown ordering:** `backend.Stop()` must succeed before `backend.Leave()` is invoked; failure of `Stop()` inhibits `Leave()` and idle persistence, keeping the voice connection intact for `Stop()` retry.
    - **Explicit Lifecycle Boundaries:**
      - Voice domain shutdown (`voice.Service.Shutdown`) features an individually retryable teardown mechanism.
      - Top-level `App.performShutdown()` and `Runtime` shutdown remain single-cycle bounded shutdowns via `stopOnce`. Retryable teardown in Voice does not mean the entire App or Runtime shutdown can be re-run or retried from scratch.
      - App lifecycle tests verify the real `New(cfg)` composition root, DAG components, and background services, but do not invoke full Telegram `Run()` involving live network connections or external authentication.
    - **Closure Decision:**
      - All audited U5 findings are accepted, fully covered by deterministic unit/concurrency tests, and verified under race detection.
      - No technical blockers remain for U5. Phase U5 is officially **COMPLETED**.
      - Final accepted implementation baseline: `505d400322baf75bcc0613f4f1a2110bedd938dd`.
      - Handoff to Phase U6: Ready for planning and failure acceptance matrix specification.

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
13. Supervisor crash loop, panic isolation, and restart backoff (U6-S13 extension).

- **U6 Closure & Acceptance Summary:**
  - **Status:** **COMPLETED** (test-only acceptance conformance; zero production code or API changes required).
  - **U6-A / U6-A1 — Read-only Audit & Failure Acceptance Matrix:** Mapped cross-subsystem ownership boundaries and finalized measurable acceptance criteria for `U6-S01` through `U6-S13`.
  - **U6-B1 — Deterministic Fixtures (`dd86d28f211bf5a981121dd6066b9238c8c3cae7`):** Added barrier synchronization (`u6ExecutionGate`), concurrency-safe sequential fault store wrapper (`u6FaultAttemptStore` with pre-write/post-write modes and epoch-scoped `WaitNextFault`), and external SQLite exclusive lock helper (`u6SQLiteLockHolder` verifying `SQLITE_BUSY`/`SQLITE_LOCKED`).
  - **U6-B2 — App Lifecycle, Health, & Resource Acceptance (`04c53ea9710cf73c23cc32cdb66dd984ef4589b5`):** Verified `U6-S01`, `U6-S02`, `U6-S08`, `U6-S09`, `U6-S10A`/`U6-S10B`, `U6-S11` (5 full App rounds with fixed goroutine tolerance), `U6-S12` (50 concurrent health probes bounded to 1 active `fsProbeSlot`), and `U6-S13` under `-race`.
  - **U6-B3 — TaskEngine, Scheduler, FloodWait, & Recovery Acceptance (`74324fce94a259ee27d99c223a088439f588e8b6`):** Verified `U6-S03A`/`U6-S03B` (pool backlog & retained-byte budget rejections), `U6-S04` (durable recovery & expired running lease quarantine), `U6-S05` (`MisfireRunOnce` no-storm handoff), `U6-S06` (`FloodWait` durable deferral, cancellation precedence, ambiguous permanent stop), and `U6-S07` (SQLite contention & `PersistencePump` backpressure) under `-race`.
  - **U6-B4 Verification Note:** All U6 acceptance packages (`internal/taskengine`, `internal/scheduler`, `internal/jobs`, `internal/jobs/sqlite`, `internal/runtime`, `internal/resource`, `internal/telegram`, `internal/architecture`) passed `go test -race` and `go vet`. `internal/app` passed `go test -race` (`49.596s`) prior to an unrelated `plugins/myxl/module.go` build error (`missing method GetTokenMaintenanceConfig`) present in the workspace during the final `./...` run.

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
