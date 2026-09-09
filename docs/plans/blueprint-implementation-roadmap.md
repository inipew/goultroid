# GoUltroid Blueprint Implementation Roadmap

Status: proposed implementation plan  
Source of truth: `docs/6_blueprint.md`  
Strategy: incremental strangler migration; no big-bang rewrite.

## 1. Outcome

Transform GoUltroid from a modular Telegram application into a managed runtime
where plugins consume scoped capabilities and cannot own global lifecycle,
unbounded concurrency, raw external clients, or untracked long-lived resources.

The target is the Blueprint's final architecture, adapted to the existing Go
codebase. Existing user-facing commands, SQLite data, addon compatibility, and
MTProto behaviour must remain compatible throughout the migration.

## 2. Current Baseline

Already available:

- centralized composition root in `internal/app` and explicit app lifecycle;
- plugin/module registration, generated module catalog, dependency guards, and
  reverse-order plugin shutdown;
- command middleware, permissions, dispatcher/update normalization, peer
  resolution, Telegram RPC policy, scheduler, settings, rate limits, storage,
  media/process/download services, and event bus;
- feature-owned migrations for much of the plugin surface;
- architecture tests that prevent feature-to-feature and feature-to-app imports.

Gaps against the Blueprint:

- no single `Runtime` facade/state model; lifecycle is still represented by
  `app.App` plus separate infrastructure objects;
- no resource registry/scope which owns every plugin subscription, job, task,
  goroutine, process, temporary file, or network session;
- no first-class bounded queue, task manager, worker pools, task ownership, or
  per-plugin concurrency/queue quota;
- scheduler can execute work but is not yet a declarative owned-job -> task ->
  queue -> worker pipeline;
- plugin capabilities describe product surfaces but do not yet gate every
  infrastructure operation;
- filesystem, HTTP, processes, secrets, locks, idempotency, health, diagnostics,
  audit, and notifications are fragmented or absent as runtime capabilities;
- `addon.go`, `scheduler.go`, `settings.go`, and `userlog.go` remain feature
  persistence in `internal/database`.

## 3. Non-Negotiable Invariants

1. Public command behaviour and existing database data survive every phase.
2. `cmd/goultroid` remains only the process entrypoint.
3. Plugin code never receives `*sql.DB`, raw app/runtime objects, or an
   unrestricted MTProto client.
4. Every long-lived operation has owner, ID, context, cancellation, cleanup,
   timeout where applicable, and diagnostic representation.
5. All queues are bounded; overload policy is explicit and observable.
6. Scheduler triggers tasks; it must not bypass worker, idempotency, or rate
   limit policy for heavy work.
7. Plugin disable stops intake first, then cancels and drains plugin-owned work
   before its dependencies are released.
8. Secrets never appear in logs, diagnostics, errors, or audit records.
9. New infrastructure is introduced behind contracts; legacy callers migrate
   feature by feature and are deleted only when coverage proves equivalence.
10. No package move merely to match the blueprint directory tree. A move must
    establish a real ownership or dependency boundary.

## 4. Target Package Shape

Names are intentionally pragmatic; exact names may change only through ADR.

```text
internal/
  runtime/          # Runtime facade, state machine, startup/shutdown plan
  lifecycle/        # component contracts and dependency-aware coordinator
  resource/         # ownership registry, scope, cleanup, leak verification
  events/           # event contracts and managed subscriptions (migrate core)
  queue/            # bounded queues and overload policies
  tasks/            # task state, retry, idempotency, task manager
  workers/          # named pools and per-owner quotas
  jobs/             # declarative jobs and execution records
  scheduler/        # scheduler adapter; submits jobs to tasks
  platform/
    storage/        # generic DB, transactions, migrations, namespaces
    network/        # scoped HTTP/WebSocket access
    filesystem/     # scoped data/cache/temp files
    process/        # tracked child processes
    secrets/        # secret references and redacted access
  health/           # readiness/liveness/component health
  diagnostics/      # runtime/plugin snapshots and leak reports
  idempotency/      # durable/ephemeral idempotency records
  locks/            # keyed locks/semaphores with ownership
  observability/    # metrics, tracing/correlation, audit, notifications
  services/         # reusable business capabilities
  telegram/         # MTProto boundary, normalized models, entity/RPC/media
  plugin/           # manifest, resolver, scope-aware manager
plugins/            # feature packages only
```

`internal/core` is not moved wholesale. Its stable value types and command
contracts are extracted one domain at a time; compatibility aliases are removed
only after callers migrate.

## 5. Delivery Sequence

### Phase 0 — Architecture decision record and executable baseline

Purpose: prevent the target design from becoming another aspirational document.

- Add ADRs for runtime ownership, plugin capability model, task/job semantics,
  persistence migration, and compatibility/deprecation policy.
- Add a package dependency matrix to architecture tests for each new boundary.
- Establish CI gates: `gofmt`, `go vet`, `go test ./...`, `go test -race` for
  runtime-critical packages, feature generator drift, and build.
- Capture baseline operational metrics: goroutine count, open resources,
  queue-free equivalent workload latency, scheduler job count, and command
  error/flood-wait rates.
- Define a test convention: no real internet; HTTP and process boundaries use
  controllable fakes; local listeners only when the test specifically verifies
  socket behaviour.

Exit criteria: ADRs approved, baseline recorded, and CI blocks boundary
violations and generated-file drift.

### Phase 1 — Runtime facade and lifecycle components

Purpose: create one observable owner without changing feature behaviour.

- Introduce `internal/runtime.Runtime` with states `created`, `initializing`,
  `starting`, `running`, `stopping`, `stopped`, and `failed`.
- Introduce a component interface: `Name`, `Dependencies`, `Start(ctx)`,
  `Stop(ctx)`, `Health()`; make start order a dependency graph rather than a
  hand-maintained sequence.
- Adapt `app.App` to construct and run Runtime; keep `App.Run`/`Shutdown` as
  compatibility façades until downstream callers migrate.
- Register existing DB, event bus, Telegram client/dispatcher, scheduler,
  settings worker, callback/inline stores, rate limiter, addons, plugins, and
  logger as components.
- Publish lifecycle events and a read-only state snapshot.
- Make construction failure rollback use the same reverse dependency order as
  normal shutdown.

Exit criteria: deterministic startup/shutdown graph tests; concurrent start and
shutdown tests; failure injection proves no started component is leaked.

### Phase 2 — Resource ownership, cleanup, and plugin scopes

Purpose: make the Blueprint's ownership invariant executable.

- Add `resource.Manager`, `Resource`, `Owner`, state, quotas, and snapshot API.
- Add `resource.Scope`: context cancellation, `Go`, `Defer`, `Track`, `Wait`,
  bounded cleanup, and idempotent close.
- Extend plugin metadata with stable ID/API version; create one scope per
  enabled plugin in `plugin.Manager`.
- Migrate plugin commands, message hooks, callback routes, event subscriptions,
  and scheduler registrations to scope-owned registrations.
- Register current addon runtimes, callback state, and process/media temporary
  resources where owners are known.
- On disable/shutdown: close plugin intake, unregister commands/hooks, cancel
  scope, wait with timeout, verify resources, then report residual leaks.
- Add `PluginState` (`discovered` through `disabled`/`failed`) and expose it in
  manager APIs without breaking the current `Plugin` interface initially.

Exit criteria: disabling a test plugin removes all its commands, hooks,
subscriptions, and tracked background work; leaked resources are named in a
diagnostic snapshot; no plugin code holds the manager lock while running.

### Phase 3 — Event contracts and managed delivery

Purpose: evolve the current event bus without using it as a job queue.

- Move event interfaces and bus implementation behind `internal/events` with
  compatibility aliases from `internal/core` during migration.
- Add subscription owner, priority, delivery mode, cancellation, middleware,
  and observability metadata.
- Define event classes: external Telegram updates, internal lifecycle events,
  and domain events. Document which are best-effort vs durable.
- Preserve current bounded asynchronous best-effort path; reserve synchronous
  durable dispatch for small in-process consistency work only.
- Add partition key/order support where needed (initially per-chat/per-user),
  rather than imposing global order.
- Normalize all dispatcher update types into stable internal events before
  plugins receive them; raw MTProto remains adapter-only except a privileged
  capability later.

Exit criteria: unsubscribe/close race tests, ordering tests per partition,
backpressure metrics, handler panic isolation, and no raw Telegram update type
in a plugin public contract.

### Phase 4 — Queue, task manager, worker pools, and overload control

Purpose: replace ad-hoc goroutines and feature-local concurrency with controlled
execution.

- Add named bounded queues with explicit policies: reject, block-with-context,
  drop, coalesce, and priority. Record depth, capacity, rejects, and latency.
- Add `Task` contract: ID, owner, name, priority, timeout, retry policy,
  idempotency key, state transitions, timestamps, and error classification.
- Add task manager that owns task cancellation/draining by plugin owner.
- Add separate pools at minimum: event dispatch, general task, download, and
  media/process. Do not combine heavyweight media/download work with commands.
- Add per-plugin maximum queued and concurrent task quota; begin in observe-only
  mode, then enforce conservative defaults.
- Migrate downloader, media, OCR, quote rendering, broadcast, and addon IPC
  work first; retain synchronous command paths only for bounded short work.
- Replace every new `go` statement in feature packages with `scope.Go` or task
  submission; inventory and migrate existing long-lived goroutines.

Exit criteria: saturation cannot grow memory unboundedly; disabling a plugin
cancels its queued/running work; media load cannot starve commands; race and
shutdown tests cover queued/running/cancelled/timeout task states.

### Phase 5 — Job manager and persistent scheduler pipeline

Purpose: implement `scheduler -> job -> task -> queue -> worker`.

- Define declarative `Job` and `JobExecution` models: owner, type, payload,
  schedule, timeout, retry, recovery policy, next run, and idempotency key.
- Add a job manager on top of the current scheduler engine. Scheduler only
  triggers; handlers become task factories.
- Add storage migration for job definitions/executions and adoption tests for
  existing scheduler rows.
- Implement recovery policies (`run_immediately`, `skip`, `recalculate`) and
  leases/idempotency for restart and overlap protection.
- Migrate scheduler plugin, reminders (when introduced), PM-permit expiry,
  cleanup routines, and recurring maintenance jobs.
- Scope-register every plugin job and cancel/disable it on plugin shutdown.

Exit criteria: fresh and upgraded SQLite databases produce identical effective
schedules; restart tests show no duplicate execution; heavy scheduled job is
observably queued rather than executed in scheduler goroutine.

### Phase 6 — Capability-scoped platform boundaries

Purpose: prevent plugins from bypassing runtime policy.

- Expand manifest from descriptive metadata to declared capabilities,
  dependencies, conflicts, config schema, quotas, storage namespace, migration
  version, and API version.
- Build `plugin.Context` from a scope and capability gate; do not expose global
  runtime or unscoped services.
- Create scoped storage namespaces; finish moving the four remaining legacy DB
  surfaces (`addon`, `scheduler`, `settings`, `userlog`) to owner repositories.
- Add filesystem service with plugin data/cache/temp roots, path validation,
  ownership registration, orphan cleanup, and disk limits.
- Replace direct plugin HTTP clients with network service: timeout, SSRF policy,
  retry, rate/concurrency quota, metrics, and owner context. Add WebSocket only
  when a feature needs it.
- Wrap process runner in a tracked manager with allowlists, cancellation,
  output limits, ownership, and cleanup. Migrate ffmpeg/yt-dlp/addon execution.
- Add secret manager using references from config/environment; log access names
  only, never values. Migrate OCR and external API credentials.
- Restrict raw Telegram access to an explicit privileged capability; normal
  plugins use Telegram/message/entity service interfaces.

Exit criteria: architecture tests forbid `database/sql`, `net/http`, `os/exec`,
and unrestricted filesystem access in feature packages except approved adapters;
capability denial is testable; a stopped plugin leaves no temporary file or
child process.

### Phase 7 — Cross-cutting correctness: idempotency, locks, error, retries

Purpose: make retries and duplicate Telegram/scheduler input safe.

- Add typed error taxonomy and central retry policy with bounded exponential
  backoff/jitter, cancellation, flood-wait awareness, and retryable categories.
- Add idempotency manager with in-memory TTL mode first, then durable keys for
  side-effecting jobs and mutations. Define key sources per update/task/job.
- Add keyed lock/lease service with owner, timeout, context cancellation, and
  diagnostics; use it for per-user/per-chat mutations and job overlap.
- Move flood-wait handling, Telegram method classification, retries, and
  rate limits fully behind the Telegram boundary.
- Apply common middleware to commands, tasks, events, and network calls:
  recovery, correlation, logging/metrics, timeout, auth, rate limit,
  idempotency, and error mapping.

Exit criteria: duplicate-update, retry-after-partial-failure, flood-wait, and
concurrent mutation tests demonstrate one side effect; errors are actionable
without leaking credentials or raw implementation types.

### Phase 8 — Health, diagnostics, audit, notification, and CLI

Purpose: make runtime condition observable and operable.

- Add component health model with `healthy`, `degraded`, `unhealthy`, and
  `unknown`; implement readiness and liveness separately.
- Add diagnostics snapshots for runtime state, components, plugins, queues,
  workers, tasks, jobs, resources, Telegram, network, processes, and storage.
- Add leak policy: warn -> mark plugin degraded -> optional disable/force cleanup.
- Standardize correlation ID propagation from Telegram update through event,
  command, task, job, services, and outgoing Telegram request.
- Add audit service for moderation, permissions, settings, plugin lifecycle,
  secret access metadata, and sensitive operations.
- Add notification policy for plugin crashes, queue saturation, DB/Telegram
  degradation, flood waits, low disk, process crashes, and resource leaks.
- Expose safe read-only diagnostics via admin command and CLI; do not expose
  raw secrets, full message content, or arbitrary execution controls.

Exit criteria: an operator can identify a degraded component and plugin owner
from one snapshot; readiness blocks intake until critical components are ready;
audit records include actor/action/target/result/correlation ID.

### Phase 9 — Feature migration and new Blueprint features

Purpose: prove the runtime by moving real workloads, then add missing features.

Migration order:

1. Stateless plugins: ping, help, info, alive, wikipedia, quote.
2. Persistent light plugins: AFK, notes, filters, blacklist, sudo, settings.
3. Domain integrations: admin, PM permit, userlog, broadcast, forward.
4. Heavy/privileged plugins: downloader, media, OCR, voice, clone, addon.
5. New features: reminder (first persistent Job proof), welcome, anti-spam,
   auto-reply, then any feature validated by product requirements.

For each plugin:

- write a feature contract (inputs, state, services, capabilities, side
  effects, quotas, persistence, failure policy);
- declare manifest dependencies/capabilities/conflicts/config schema;
- migrate commands/hooks/jobs/tasks to `plugin.Context` and scope ownership;
- preserve or adopt DB migration history; test fresh and upgrade paths;
- add lifecycle, capability-denial, saturation, cancellation, and leak tests;
- remove the compatibility adapter only after production-equivalence checks.

Exit criteria: every enabled plugin is scope-owned and diagnostic-visible; all
new Blueprint features use runtime facilities rather than inventing local ones.

### Phase 10 — Legacy removal and release hardening

Purpose: finish only after migration is demonstrably complete.

- Remove compatibility aliases and legacy wiring one capability at a time.
- Delete obsolete `internal/core`/`internal/database` feature APIs only after
  callers and data migration tests are gone.
- Tighten architecture tests from allowlists to prohibitions.
- Run fault-injection tests for storage unavailable, Telegram reconnect/flood
  wait, queue saturation, plugin panic, process hang, shutdown during work,
  and corrupted/old database migration state.
- Produce upgrade guide, rollback procedure, resource/quota defaults, and
  operator runbook before declaring the runtime stable.

Exit criteria: zero legacy feature persistence under generic database package,
no unowned long-lived resource class, and all Definition-of-Done items in the
Blueprint have executable test or operational evidence.

## 6. Data Migration Rules

- Never rewrite historic integer migrations. New migrations use namespaced IDs
  and explicitly adopt equivalent legacy versions.
- Every persistence migration requires three tests: clean database, upgraded
  database, and interrupted/rolled-back transaction where supported.
- New job/idempotency/resource tables are additive first; read old state and
  write new state in a compatibility window; remove old columns/tables only in
  a later release with a documented rollback boundary.
- Persistent plugin data is owned by its plugin/service repository; runtime
  owns only generic storage primitives and migration execution.

## 7. Acceptance Matrix

| Capability | First phase | Final evidence |
|---|---:|---|
| Runtime lifecycle | 1 | dependency graph + failure rollback tests |
| Plugin resource ownership | 2 | disable/leak verification tests |
| Managed events | 3 | order, panic, backpressure tests |
| Queue/task/worker | 4 | saturation, cancellation, quota tests |
| Persistent jobs | 5 | restart/idempotency/upgrade tests |
| Scoped platform access | 6 | capability and boundary tests |
| Idempotency/locks/retry | 7 | duplicate/concurrency/flood-wait tests |
| Health/diagnostics/audit | 8 | snapshot/readiness/audit contract tests |
| Feature parity/new features | 9 | per-feature contract and upgrade tests |
| Legacy removal | 10 | architecture allowlist reaches zero |

## 8. Release Gates Per Phase

Required for every merge:

```text
gofmt -l .
go vet ./...
go test ./...
go test -race <runtime-critical packages>
go run ./tools/featuregen && git diff --exit-code -- internal/app/generated_modules.go
go build ./cmd/goultroid
```

Additionally require phase-specific tests, architecture guard updates, database
fresh/upgrade fixtures where persistence changes, and a documented migration
rollback assessment.

## 9. Sequencing Constraints

- Phase 2 must precede plugin enable/disable and resource quotas.
- Phase 4 must precede migration of heavy plugins and scheduler execution.
- Phase 5 requires Phase 4; jobs cannot call handlers directly.
- Phase 6 must precede strict capability enforcement, otherwise existing
  plugins would lose access without a supported replacement.
- Phase 7 must precede broad automatic retries or restart recovery.
- Phase 8 is introduced early in read-only form, but enforcement/escalation
  waits until ownership data is trustworthy.
- New features start only after the runtime capability they require exists.

## 10. Explicit Non-Goals for the First Runtime Release

- Dynamic loading of arbitrary untrusted Go code.
- Distributed queues, multi-process scheduler election, or remote secret
  backends before the single-process contracts are proven.
- Global total event ordering.
- Replacing SQLite, Gotd, or every existing service merely for directory purity.
- Moving all packages in one pull request.

## 11. Suggested First Three Implementation PRs

1. **Runtime component contract:** introduce state/component interfaces and wrap
   the existing app lifecycle without changing plugin APIs.
2. **Resource manager + plugin scope:** track command/hook/subscription cleanup
   and prove plugin disable leaves no registration behind.
3. **Bounded task/worker pilot:** migrate downloader/media as a separate queue
   and pool, with cancellation and metrics.

These PRs establish the ownership/execution backbone on which the rest of the
Blueprint can safely build.
