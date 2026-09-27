# Goultroid Maintainability Boundary Refactor — AI Session Plan

Status: **IN PROGRESS — M1 CLOSED; M2 NEXT**

Audit baseline:

- Branch: `test-next`
- Baseline HEAD: `1f0077a8f90b7062c6c624daf415769dbf7b6942`
- Baseline commit: `fix(telegram): release command claims before task admission`
- Audit date: 27 September 2026

This document is the implementation handoff for the maintenance issue:

> Narrow interfaces by caller need and split oversized TaskEngine / Job Manager source by responsibility without changing execution ownership or production semantics.

## 1. User rules and hard constraints

The next AI session must preserve these rules:

1. Run `gofmt` **before every commit that changes Go code**.
2. Do **not** inspect, poll, or wait for CI unless explicitly requested by the user.
3. Do not create a second TaskEngine, Job Manager, execution registry, retry engine, or transport implementation.
4. TaskEngine remains the single execution authority for finite work.
5. Jobs remain durable orchestration over the existing TaskEngine, not a competing executor.
6. Keep caches, queues, retained state, and helper goroutines bounded.
7. Structural refactors must preserve runtime semantics first; API cleanup follows only after callers are migrated.
8. Prefer interfaces owned by the consumer and sized to the consumer's actual need.
9. Compatibility adapters must have an explicit exit condition and must not become permanent duplicate APIs.
10. No feature behavior, Telegram RPC semantics, scheduling semantics, retry policy, persistence semantics, or shutdown policy should change merely because files/interfaces are being split.

## 2. Confirmed audit findings

### 2.1 Telegram boundary is too broad

`internal/core/context.go` is currently about **1,103 lines**.

`core.TelegramServicer` contains **38 methods** spanning unrelated capability groups:

- message send/edit/delete/reaction/pin/forward;
- inline message edit;
- callback answers;
- inline query answers;
- download/upload/media delivery;
- moderation and admin mutations;
- peer/full-user/full-chat lookup;
- profile mutation;
- dialogs and contacts;
- block/unblock;
- bot-origin tracking.

The same file already exposes higher-level facades:

- `MessagesFacade` in `context_messages.go`;
- `AdminFacade` in `context_admin.go`;
- `MediaFacade` in `context_media.go`;
- `PeerFacade` in `context_peer.go`.

Those facades are a natural consumer boundary, but `Context` still stores:

```go
Svc TelegramServicer
```

so a test double for one message operation still has to satisfy a transport contract containing dozens of unrelated operations.

The optional `ContextualTelegramServicer` also combines contextual message send and contextual media send. Those are separable capabilities.

Concrete `internal/telegram/service.go` is itself about **1,806 lines**. This is adjacent maintenance debt, but it must be treated as source organization of the same `Service`, not as justification for another transport object.

### 2.2 TaskEngine is one correct owner but one oversized source file

`internal/taskengine/engine.go` is currently about **2,039 lines**.

The current design still has the important ADR 0006 invariant: one coordinator/run loop owns mutable execution state. Do **not** break that invariant merely to reduce file size.

The file currently combines at least these responsibilities:

- config/defaults/validation;
- internal task record and request/reply types;
- Engine state and constructor;
- lifecycle start/runtime-loop ownership;
- control inbox/request pooling;
- admission linearization;
- dispatch and worker inventory;
- resource accounting;
- worker start/completion application;
- cancellation and snapshots;
- terminal retention/eviction;
- drain/quiesce/stop;
- stats and health;
- public client/configuration API.

The package already has useful extracted files such as `executor.go`, `durability.go`, `delivery.go`, `accounting.go`, `shutdown.go`, and `diagnostics.go`. The remaining oversized `engine.go` should be split along the same state-machine responsibilities while keeping the same `Engine` and same run-loop ownership.

### 2.3 Jobs Manager combines several independent responsibilities

`internal/jobs/manager.go` is currently about **1,719 lines**.

It currently combines:

- lifecycle and runtime component behavior;
- handler/definition registration;
- occurrence creation and cancellation;
- schedule persistence/materialization;
- tracked occurrence bookkeeping;
- lazy retry workers and retry queue;
- durable coordinator/recovery wake loop;
- attempt summary/lease preparation;
- attempt watch/drive logic;
- retry semantics;
- result persistence/finalization;
- diagnostics.

This should remain one logical `Manager` state owner, but the source should be split so each responsibility can be tested and changed locally.

### 2.4 Jobs has an actual external boundary leak

`internal/plugin/context.go` currently exposes:

```go
Jobs() (*jobs.Manager, error)
TaskClient() (tasks.Client, error)
```

The Task boundary is already the better model: plugins receive a scoped `tasks.Client`, not `*taskengine.Engine`.

The Jobs boundary exposes the whole concrete manager and therefore lets a plugin see unrelated lifecycle, recovery, schedule, diagnostics, and mutation APIs. It also weakens capability separation because `CapJobs` / `CapScheduler` can currently lead to the same concrete manager.

This is a higher-value boundary fix than simply moving methods between files.

### 2.5 Jobs store boundary is also broad

The primary `jobs.Store` currently contains definition, occurrence, attempt, recovery, cancellation, pruning, and deferral operations in one interface. The code already contains narrower optional interfaces such as:

- `outboxStore`;
- `deferredDeadlineStore`;
- `durableDiagnosticsStore`;
- `attemptSummaryStore`;
- `nextAttemptLeaseStore`;
- `recoveryCandidateStore`;
- `definitionLoader`;
- `scheduleStore`.

This is evidence that consumer-specific store ports are already compatible with the architecture. The main store can be decomposed later without creating a second persistence implementation.

## 3. Design principles for this refactor

### 3.1 Narrow interfaces by consumer, not by producer

Do not create one new giant `TelegramCapabilities` interface that merely renames `TelegramServicer`.

A capability interface should exist because a consumer needs that exact operation set.

Examples:

- core message facade needs message operations;
- core admin facade needs moderation operations;
- core media facade needs media operations;
- core peer facade needs peer/query operations;
- dispatcher callback handling needs only callback answering;
- inline engine needs only inline answer/edit operations;
- origin classification needs only bot-origin tracking.

The concrete `telegram.Service` may implement all of them.

### 3.2 Preserve one state owner

Splitting source files is not permission to split mutable ownership.

For TaskEngine:

- one `Engine`;
- one coordinator/run loop;
- one task registry;
- one resource inventory;
- one admission state;
- one terminal retention policy.

For Jobs:

- one `Manager`;
- one occurrence tracking map;
- one retry/recovery ownership model;
- one TaskEngine client;
- one persistence protocol.

### 3.3 Structural pass before semantic pass

First move code into responsibility-based files with behavior unchanged.

Only after the source is understandable and tests stay stable should the session narrow public boundaries or remove compatibility adapters.

This keeps reviewable commits and makes regressions attributable.

## 4. Target Telegram capability model

Exact names may be adjusted to match repository style, but the target shape should be equivalent to the following.

### 4.1 Core command-context capabilities

Suggested small contracts:

- `MessageServicer`
  - send text / send markup;
  - edit text / edit markup;
  - delete;
  - get message;
  - react;
  - pin/unpin;
  - forward;
  - purge only if `MessagesFacade` remains the owner of purge.
- `AdminServicer`
  - ban/unban;
  - kick;
  - mute/unmute;
  - promote/demote;
  - edit default banned rights.
- `MediaServicer`
  - download file;
  - send media.
- `PeerServicer`
  - full user;
  - username resolution;
  - full chat;
  - block/unblock if those remain in `PeerFacade`.
- `ProfileServicer`
  - update profile;
  - upload/delete profile photo;
  - dialogs;
  - contacts.
- `ContextualMessageServicer`
  - contextual message send only.
- `ContextualMediaServicer`
  - contextual media send only.

Callback, inline-query, inline-message-edit, self-inline, and bot-origin tracking should normally be consumer-local interfaces in their own packages/files rather than being forced into command `Context`.

### 4.2 Transitional compatibility

A temporary aggregate may remain:

```go
type TelegramServicer interface {
    MessageServicer
    AdminServicer
    MediaServicer
    PeerServicer
    ProfileServicer
    // only compatibility-only capabilities that still require migration
}
```

Rules for this aggregate:

- mark it compatibility-only;
- do not add new callers;
- add an architecture test/fence preventing new uses outside approved compatibility files;
- delete it after all callers use narrow ports.

### 4.3 Context migration

Do not permanently keep `Context.Svc TelegramServicer`.

Preferred target is explicit capability injection, for example a value container:

```go
type TelegramCapabilities struct {
    Messages MessageServicer
    Admin    AdminServicer
    Media    MediaServicer
    Peers    PeerServicer
    Profile  ProfileServicer
}
```

This is a value container, not another giant interface.

During migration:

1. add capability fields/container;
2. make facades consume their own capability;
3. retain `Svc` only as a temporary fallback;
4. migrate all Context constructors;
5. remove fallback;
6. delete `Svc` and broad `TelegramServicer`.

The production composition may assign the same concrete `*telegram.Service` to several capability fields. That is expected and does not create duplicate transport state.

### 4.4 Test objective

A MessagesFacade test must be able to compile with a fake that implements only the methods MessagesFacade needs.

The same applies to Admin, Media, Peer, callback, and inline tests.

Retire the pattern where every test embeds or implements a 38-method `MockTelegramServicer` just to test one operation.

## 5. Target TaskEngine source layout

This phase is intentionally an intra-package structural refactor.

Suggested layout:

- `engine_config.go`
  - Config defaults;
  - config validation;
  - pool config helpers.
- `engine_state.go`
  - internal request/reply types;
  - taskRecord;
  - Engine struct;
  - constructor-only state initialization.
- `engine_lifecycle.go`
  - Name/Dependencies;
  - Start;
  - runtime-loop lifecycle bookkeeping.
- `engine_control.go`
  - runLoop;
  - handleRequest;
  - request pool;
  - sendInternal/sendControl/sendSubmitControl;
  - submit decision cell.
- `engine_admission.go`
  - admitSubmit;
  - result bounding;
  - admission-side accounting.
- `engine_dispatch.go`
  - tryDispatch;
  - worker spawning/retirement;
  - resource availability/reservation/release;
  - pool reconfiguration.
- `engine_completion.go`
  - worker-start;
  - worker-complete;
  - terminal settlement;
  - terminal retention/eviction.
- `engine_cancel.go`
  - task cancel;
  - scope cancel;
  - snapshot/result application.
- `engine_api.go`
  - Submit/Cancel/CancelScope/Snapshot;
  - configuration entry points;
  - Stats/Health.
- Existing `shutdown.go`, `durability.go`, `delivery.go`, `executor.go`, `accounting.go`, and `diagnostics.go` remain canonical for their current responsibilities unless a move clearly reduces duplication.

Important: moving a function between files in the same package should not alter its receiver, locking, run-loop ownership, or ordering semantics.

## 6. Target Jobs source layout

Suggested layout:

- `manager_state.go`
  - Manager struct;
  - retryItem;
  - trackedOccurrence;
  - constants;
  - constructor.
- `manager_lifecycle.go`
  - Name/Dependencies;
  - Start/Quiesce/Drain/Stop/ForceStop;
  - outbox lifecycle and wake signals;
  - Health.
- `manager_registry.go`
  - RegisterHandler;
  - Register;
  - UpdateDefinition;
  - Definition.
- `manager_occurrence.go`
  - Trigger/TryTrigger;
  - SubmitOccurrence;
  - occurrence lookup;
  - owner/occurrence cancellation;
  - track/untrack.
- `manager_schedule.go`
  - SaveSchedule;
  - policy validation;
  - DisableSchedule;
  - cutover/due queries;
  - ProcessDueSchedules.
- `manager_retry.go`
  - lazy retry worker lifecycle;
  - queue admission;
  - retry loop;
  - retry delay/budgets.
- `manager_attempt.go`
  - attempt summary;
  - prepare lease;
  - watchAttempt;
  - driveAttempt;
  - attempt result state;
  - persistence/finalization helpers.
- `manager_recovery.go`
  - durable coordinator;
  - recovery pass;
  - Recover.
- `manager_diagnostics.go`
  - Diagnostics.

Do not introduce sub-managers with independent locks/state just to make filenames shorter.

## 7. Jobs API boundary target

### 7.1 Plugin-facing boundary

`PluginContext.Jobs() (*jobs.Manager, error)` should not remain the final API.

Follow the existing `TaskClient()` model and expose a scoped, capability-gated client.

Possible consumer-owned contracts:

- job registration client;
- job trigger/occurrence client;
- optional schedule client.

The exact method set must be derived from actual plugin callers before coding.

The adapter should enforce plugin ownership rather than trusting plugins to supply arbitrary `ScopeOwner` / `QuotaOwner` values, analogous to `scopedTaskClient`.

### 7.2 Scheduler capability separation

`CapJobs` and `CapScheduler` should not both expose the entire Manager.

If a plugin only has scheduling capability, it should receive schedule operations only.

### 7.3 Store ports

After Manager source is split, split the current broad store by use-case:

- definition store;
- occurrence store;
- attempt store;
- recovery store;
- schedule store;
- outbox store;
- diagnostics/deadline capability where needed.

Production may continue using one SQLite `ResourceStore` implementing all ports.

Do not create duplicate database stores or duplicate transactions solely for interface purity.

## 8. Implementation phases

### M0 — Audit freeze and regression fences

Status after this document: **documented, implementation pending**.

Tasks:

- record current HEAD and file sizes;
- inventory direct `TelegramServicer`, `Context.Svc`, `*jobs.Manager`, and concrete `*taskengine.Engine` consumers before each migration;
- add architecture tests that prevent new broad-interface usage;
- identify compatibility exit conditions.

Gate:

- no production behavior change;
- caller inventory is explicit enough to migrate without guessing.

### M1 — Split Telegram contracts

Status: **CLOSED**

Implementation commits:

- `c36fc3bcb4ed4f8552a5c36a7694db2a64f247bd` — `refactor(core): split telegram capability contracts`
- `e2c771cb9d2b8968142d80daebb418d630b523ed` — `test(core): prove narrow telegram capability fakes`

Implemented:

- added `MessageServicer`, `AdminServicer`, `MediaServicer`, `PeerServicer`, and `ProfileServicer`;
- added `CommandTelegramServicer` as the command-context target aggregate while keeping the 38-method `TelegramServicer` unchanged for compatibility;
- split contextual transport into independent `ContextualMessageServicer` and `ContextualMediaServicer`;
- added consumer-owned boundaries for dispatcher callback answering, bot-origin tracking, inline-query answering, and presentation bridge transport;
- added compile-time assertions that the existing concrete `telegram.Service` implements every new core capability;
- added compile-time assertions that the compatibility service still covers the new boundaries;
- added focused capability-only test fakes, proving consumers can mock one capability without implementing unrelated Telegram operations;
- added regression tests ensuring command-context capability aggregation does not absorb callback/inline/origin methods and contextual message/media contracts remain independent.

M1 intentionally did **not** change `Context.Svc`, dispatcher service storage, inline Execute signatures, presentation Bridge storage, Assistant compatibility servicers, or runtime wiring. Those caller migrations belong to M2 so this phase remains additive and behavior-neutral.

Tasks:

- add narrow core capability interfaces;
- split contextual send interfaces;
- add consumer-local callback/inline/origin interfaces;
- make `telegram.Service` satisfy the narrow interfaces;
- keep a temporary broad aggregate only for compatibility.

Tests:

- compile-time capability satisfaction;
- tiny fake per facade/consumer;
- existing Telegram service tests unchanged semantically.

Gate:

- no new code needs the 38-method interface.

### M2 — Migrate Context and Telegram callers

Tasks:

- introduce explicit Context capability injection;
- move facades to their narrow dependency;
- migrate dispatcher/assistant/plugin Context construction;
- migrate callback/inline/origin callers to local ports;
- split broad mock into focused fakes;
- remove `Context.Svc` fallback when caller count reaches zero;
- delete broad `TelegramServicer` only after zero callers.

Gate:

- one-operation tests no longer implement unrelated Telegram methods;
- no production caller references broad `TelegramServicer`.

### M3 — Split TaskEngine by responsibility

Tasks:

- move methods/types from `engine.go` into the target files above;
- preserve one Engine and one runLoop;
- do not change exported behavior in the structural commit;
- keep commit(s) mechanically reviewable.

Required regression groups:

- admission/cancel linearization;
- bounded-memory tests;
- worker generation;
- resource reservation;
- durability ack;
- completion delivery;
- drain/shutdown;
- hot/cold TaskEngine benchmarks for gross regressions.

Gate:

- `engine.go` is no longer a 2k-line multi-responsibility file;
- all mutable ownership invariants remain unchanged.

### M4 — Split Jobs Manager by responsibility

Tasks:

- mechanically move code from `manager.go`;
- preserve one Manager;
- preserve retry queue, recovery wake, tracked occurrence map, and persistence semantics;
- keep lazy worker retirement and zero-idle behavior.

Required regression groups:

- manager redesign tests;
- retry/recovery tests;
- durable coordinator tests;
- timing wake tests;
- automatic recovery;
- P5 lifecycle/recovery wake;
- persistence pump integration.

Gate:

- `manager.go` no longer contains lifecycle + schedule + retry + attempt + recovery implementation in one file;
- no new goroutine or queue introduced by the split.

### M5 — Narrow Jobs external/store boundaries

Tasks:

- add plugin-owned narrow job/schedule interfaces;
- add scoped adapter;
- migrate `PluginContext.Jobs()`;
- split Store ports according to the newly separated Manager responsibilities;
- keep compatibility constructor only while callers migrate.

Gate:

- plugins no longer receive `*jobs.Manager`;
- schedule-only callers cannot call unrelated recovery/lifecycle APIs;
- tests can supply only the store ports used by the responsibility under test.

### M6 — Final cleanup and acceptance

Tasks:

- remove compatibility aggregates/adapters with zero callers;
- remove obsolete giant mocks;
- add architecture fences for the final boundaries;
- check source layout and package documentation;
- run local validation requested for the phase.

Suggested local validation, without CI polling:

```text
gofmt -w <all changed Go files>
go test ./internal/core ./internal/telegram
go test ./internal/taskengine
go test ./internal/jobs ./internal/jobs/sqlite
go test ./internal/plugin ./internal/app
go vet ./...
```

A final `go test ./...` and selected `-race` runs are appropriate before closure if the user wants full local acceptance.

## 9. Commit strategy

Keep commits responsibility-scoped. Suggested sequence:

1. `docs(maint): plan interface and subsystem boundary refactor`
2. `refactor(core): split telegram transport capabilities`
3. `refactor(core): migrate context to narrow telegram capabilities`
4. `refactor(taskengine): split coordinator responsibilities`
5. `refactor(jobs): split manager responsibilities`
6. `refactor(plugin): scope jobs capability boundary`
7. `refactor(jobs): split durable store ports`
8. `test(architecture): fence maintainability boundaries`

For every Go-changing commit:

1. make changes;
2. run `gofmt` on every changed Go file;
3. run relevant local tests;
4. inspect diff;
5. commit;
6. push;
7. **do not inspect CI unless the user explicitly asks**.

## 10. Non-goals

Do not use this maintenance work to:

- redesign TaskEngine scheduling/fairness;
- add a second coordinator;
- change queue capacities;
- change retry/backoff semantics;
- change job schema unless a boundary change absolutely requires additive compatibility metadata;
- change Telegram RPC retry/limiter behavior;
- alter command UX;
- rework Assistant interaction protocols;
- merge unrelated resource optimization work;
- rewrite all mocks in one giant commit.

## 11. Risks to watch during implementation

### Interface migration risk

A broad interface can reappear under a new name. Review method counts and caller needs, not names.

### Context construction risk

Some tests and execution surfaces build `core.Context` directly. Keep temporary compatibility only until every construction site is migrated.

### TaskEngine ownership risk

Moving methods must not accidentally move state into helper structs with their own mutexes. The run loop remains the canonical state writer.

### Jobs retry/recovery risk

Retry workers and recovery are coupled through tracked occurrence state, wake signals, attempt leases, and persistence fencing. Split files first; only then consider narrower helper function parameters.

### Plugin capability risk

Replacing `*jobs.Manager` with an interface must not let a plugin forge owner identity. The adapter should stamp owner/scope exactly like `scopedTaskClient`.

### Store transaction risk

Splitting store interfaces must not split atomic SQLite transactions that currently guarantee occurrence/attempt/outbox fencing.

## 12. Definition of done

This maintenance item is closed only when all of the following are true:

- no production consumer requires the 38-method `core.TelegramServicer`;
- command-context facades depend on capability-sized interfaces;
- callback/inline/origin consumers depend on their own minimal ports;
- plugins do not receive `*jobs.Manager` directly;
- `taskengine.Engine` remains one coordinator but its implementation is split by responsibility;
- `jobs.Manager` remains one orchestrator but its implementation is split by responsibility;
- durable store boundaries are narrow enough that responsibility tests do not need a giant store mock;
- no second execution/retry/transport authority was introduced;
- targeted lifecycle, retry, admission, persistence, and resource regressions remain green locally;
- every Go-changing commit was formatted with `gofmt`;
- CI was not inspected unless the user explicitly requested it.

## 13. Recommended next action

Start with **M1 — Telegram capability contracts**.

It has the highest testability payoff, has clear existing facade boundaries, and can be implemented additively before deleting any compatibility API. After M1/M2 are closed, perform the TaskEngine and Jobs file decompositions as mostly mechanical moves, then close the plugin Jobs/store boundary leaks.
