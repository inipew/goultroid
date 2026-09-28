# Goultroid Maintainability Boundary Refactor — AI Session Plan

Status: **IN PROGRESS — M1/M2/M3 CLOSED; M4 NEXT**

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

Status: **CLOSED — scoped local acceptance passed; broader unrelated failures remain unattributed**

Implementation commits:

- `570dc4bb996ea60a42b6c9e654ea3aad58c4de34` — `refactor(core): route context through telegram capabilities`
- `f3e141c7d265de37b43f29c58e8d08d6e0093ea3` — `refactor(telegram): narrow dispatcher inline and presentation ports`
- `c23948e691df644620e196a6a3f3574b8199f185` — `refactor(assistant): remove broad telegram fallback servicer`
- `054343d1a15e9a0c1b5539e46359263dce9be11d` — `refactor(core): migrate command contexts off broad telegram service`
- `cd6e96a1e2b8e0e8e466d5d7b13c2e32eeb04581` — `test(architecture): fence telegram capability migration`
- `a062f4d636d0472e3cb6474d88bfa297b2810340` — `fix(m2): route production contexts through capabilities`
- `b671f09fe66fee3cf0ba140b1533c6a107fb4a3a` — `fix(assistant): fail closed for unsupported telegram capabilities`
- `09719a73b45f18ba8f1dd9606d9810af99049176` — `test(m2): reopen and fence capability migration`
- `8d3deb877a7080123928e00d8f743cbc6ecd906f` — `test(core): cover capability backed edit cleanup`
- `2f41b93c6a5c250b80b88061d5d39e48822cb381` — `refactor(m2): narrow module telegram providers`
- `be8a746d99a794c419c41fafcc2dbae77731f21c` — `refactor(m2): narrow service telegram providers`
- `d0fea3bddc8e5622dece5078d40096ed6c6c0963` — `refactor(m2): narrow native presentation transport`
- `7f548c23cab38114962d74302b9eb081443ddf7b` — `refactor(m2): split dispatcher service storage`
- `c19b636a435bcc25b5780a20b562f3f082be2145` — `fix(m2): bind module runtime to dispatcher capabilities`
- `3303b189bb1d369e9c4d82c5101933e6988d68e9` — `refactor(m2): remove command aggregate from production path`
- `ae9bd52a4a3c49ab11568fc2df373b3d4c5bb9db` — `refactor(m2): fence legacy telegram aggregate paths`
- `92b2d17c09bfb5397ef872d9312eddd1afb5463b` — `fix(m2): keep runtime capability providers live`
- `a92aa2a7b050b3435eef79e7d9343e75f098db96` — `fix(m2): restore canonical group rule contracts`
- `1b3c633f5a306d36fac7b2140693edd3f662d86a` — `test(m2): update app fixtures for capability wiring`
- `b91c9cbcaabca08a46a16d7ecba5ca10f1d82e51` — `refactor(m2): make assistant telegram capabilities truthful`

Closed behavior/boundary work:

- `core.Context` now owns `TelegramCapabilities`; production userbot, Assistant, and scheduled-command paths populate that container instead of `Svc`.
- Core message/admin/media/peer/profile facades resolve their own capability and contextual message/media extensions independently.
- `Context.Svc` is no longer typed as the 38-method `TelegramServicer`; it is a deprecated `CommandTelegramServicer` compatibility fallback for direct legacy/test construction only.
- `CommandExecutor.ExecuteExecution` accepts a `TelegramCapabilities` value directly; scheduled execution no longer carries `CommandTelegramServicer`.
- Dispatcher production storage is capability-sized: command state is a `TelegramCapabilities` snapshot and callback/inline/origin/contextual/presentation ports are stored independently. `DispatcherService` remains only as a compatibility constructor/test input.
- Inline engine public execution methods now accept `inline.TelegramAnswerer`.
- Presentation Telegram bridge now stores `BridgeService`; contextual media remains an independent optional capability.
- Assistant inline query service no longer implements unrelated Telegram operations.
- The 38-method `unsupportedTelegramServicer` mega-stub was deleted. Message callback, inline callback, presentation, and audience transports now implement only their consumer contracts.
- Broadcast request delivery was narrowed to `broadcast.Sender` (text + media) so Assistant audience broadcast no longer needs a fake full Telegram service.
- Assistant canonical command Context uses the capability container, including mutation-admission rebinding.
- Architecture tests fence the migrated production surfaces against reintroducing `core.TelegramServicer`, broad `Context.Svc`, or `unsupportedTelegramServicer`.
- Follow-up architecture fence `122b4f8d18006b1882df34838c976e31d7bb9030` prevents production `core.Context` literals from binding deprecated `Svc` and restricts direct `.Svc` reads inside `internal/core` to the compatibility adapter only.

Regression audit / reopened findings:

- fixed core edit delivery that still read `Context.Svc`, restoring capability-backed `Edit`, `EditOrReply`, and semantic responses;
- fixed delayed response deletion so `AutoDeleteDelay` resolves the message capability rather than gating on `Svc`;
- migrated native downloader child Contexts and plugin command paths away from direct `ctx.Svc` reads;
- migrated broadcast/profile/clone/quote/userlog and self-inline cleanup paths to narrow capability accessors;
- removed production Assistant embedding of `MockTelegramServicer`; follow-up capability refinement now removes unsupported Assistant methods from its type entirely and binds only supported narrow ports;
- Assistant message deletion now propagates transport errors instead of discarding them;
- full plugin production sweep after the fixes found no remaining direct `ctx.Svc` reads.

Compatibility intentionally retained:

- the legacy `core.TelegramServicer` type still exists for older composition/test factories;
- `core.Context.Svc` remains as a deprecated, narrower command-only fallback, but direct production callers must not read or bind it;
- compatibility assertions/adapters may mention the legacy aggregate, but migrated production execution surfaces may not depend on it.

Scoped local acceptance evidence:

- application build passes at HEAD `1b3c633f5...`;
- targeted M1/M2 architecture tests pass;
- targeted core, Telegram, Assistant command, and app tests pass;
- related-package `go vet` passes;
- previously confirmed `Context.Svc`, module-provider, self-inline, group-rule-contract, and app-fixture regressions were fixed;
- broader related package tests still contain failures, but the current investigation found no evidence attributing those failures to M1/M2.

The scoped evidence above closes M2. It must not be restated as a claim that the entire repository or every related package test is green. Do not inspect or poll CI unless explicitly requested.

Production boundary cleanup completed:

- module runtime exposes narrow message/admin/media/contextual/origin providers instead of a legacy Telegram aggregate;
- PMPermit, UserLog, AFK, Blacklist, Filters, broadcast, and native interaction production wiring consume narrow ports/adapters;
- dispatcher production state stores capability-sized ports; the wide `DispatcherService` path is compatibility-only;
- `Client.Service() core.TelegramServicer` is deprecated compatibility-only and is fenced out of application production composition;
- scheduler and `CommandExecutor.ExecuteExecution` consume `TelegramCapabilities` directly;
- production application wiring is fenced from `Client.Service()`, `Dispatcher.Service()`, and `Dispatcher.CommandService()`.

Deferred compatibility cleanup, not a production M2 dependency:

- remove `Context.Svc`, `CommandTelegramServicer`, `DispatcherService`, and `core.TelegramServicer` only after their remaining compatibility/test callers are migrated;
- continue capability granularity cleanup only where new consumers justify it; Assistant command transport now advertises supported message-action, admin, media-send, full-chat, and contextual capabilities without pretending to support reactions, forwarding, media download, or broad peer/profile APIs.

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

Status: **CLOSED — structural split, H1–H5 hardening, and scoped local acceptance passed**

Implementation commits:

- `2fc271d7ce6d035ac1e8ecfc60860b89093384b0` — `refactor(taskengine): split config and state declarations`
- `11431fdb96aa25fab3f96b3741c689110c74369d` — `refactor(taskengine): split coordinator responsibilities`
- `4449a1ded52ac0c280d4b9db6877f1e21f0235de` — `test(maint): fence taskengine m3 ownership`
- `f3aba7842be22b76fbe011b0438b3fbe6ebdc102` — `fix(taskengine): harden lifecycle shutdown boundary`
- `d41718f2319d4fc49a719a043093030ae68d4851` — `fix(taskengine): decouple shutdown enqueue gate`
- `37ede0c88f5f1de21cb32431c5e468543caa2f72` — `fix(taskengine): harden live pool reconfiguration`
- `bee42d731d6866993649c2ed722e172bf83c78c7` — `test(taskengine): tighten live pool hardening coverage`
- `cf8f22c1a059c4e6d2fccce7581b296562d81c67` — `fix(admission): refresh active owner fairness weights`
- `3444b202b7499a5254fcfd8b946e5d4bf6a1e6db` — `fix(taskengine): fence owner limit control updates`
- `71c7a0d8178e994b0c1ae8288c901f91af84665d` — `test(taskengine): cover owner limit control fencing`
- `0729340338e3ffe0e34aa096ca22154da746ac18` — `style(taskengine): restore h3 gofmt`
- `7d1f17abba7733c17eda1683b0a99cde1af0de09` — `style(taskengine): finish h3 gofmt`
- `76add6fac9b464c95d2c22877333c9996acdff59` — `fix(taskengine): bound retained work spec memory`
- `f85625fd0973fe0d8df5155189755801134b67f1` — `fix(taskengine): harden durability forced cleanup`
- `b84be03d2d1653bc5b1958fb0e293247f42057bd` — `fix(taskengine): release detached terminal charges`
- `2bcc9742e4e60ef0fb9a3ae8e67b9428705d44d8` — `test(taskengine): cover terminal occurrence detachment`
- `1b26bc1ed72122d8a4a44c7b48d99bcab606dd60` — `refactor(taskengine): clarify runtime observability fences`
- `70b4bc6c75bcb032b9e76462e06c56400448346c` — `test(maint): strengthen taskengine ownership fences`
- `a1550beb5f085ef995acb737ae5932a2f3bbb8c1` — `test(taskengine): fence worker start identity`
- `77b913b8c9be468a3a34a614271fa7175cc84a22` — `refactor(taskengine): drop inert permit worker id`
- `afcb5097911b3ba1ad90176da36290367371f3a6` — `fix(taskengine): retain attempt id memory charge`
- `b3d2fcf5e32f7a2e00538f7968bfe8495d602ad1` — `style(taskengine): restore h4 attempt accounting gofmt`

Implemented structure:

- `engine.go` now owns lifecycle/control coordination and the single canonical `runLoop`;
- `engine_config.go` owns configuration defaults, validation, and construction;
- `engine_state.go` owns the sole `Engine` struct plus coordinator request/state types;
- `engine_admission.go` owns submit admission, owner-limit control, and submit linearization;
- `engine_dispatch.go` owns queue expiry, worker/resource dispatch, pool configuration, and dispatch-time worker hooks;
- `engine_completion.go` owns physical completion, cancellation settlement, terminal retention, and drain detection;
- `engine_api.go` owns public stats/health/cancel/snapshot query surfaces;
- existing `durability.go`, `delivery.go`, `accounting.go`, `executor.go`, `shutdown.go`, and `diagnostics.go` remain specialized files;
- no second Engine, coordinator, queue, registry, or execution authority was introduced.

The structural audit at `4449a1ded52ac0c280d4b9db6877f1e21f0235de` compared the pre-split `engine.go` with the split implementation: 71 functions/methods and the moved type declarations were preserved, with no function-body change found after whitespace normalization. This supports a behavior-neutral mechanical split; it does **not** establish that the pre-existing TaskEngine behavior is correct. The architecture fence requires exactly one `type Engine struct` and one `(*Engine).runLoop` in their intended files, but does not prove that no differently named coordinator, registry, or execution authority can be introduced. It also checks file existence rather than ownership of each responsibility.

Current-code audit findings to resolve before M3 closure (these were preserved by the mechanical split; no causal attribution to the split):

1. **Lifecycle and control after root cancellation — M3-H1 IMPLEMENTED.** `applyStopFinalize()` now performs a final `checkDrained()`, so root cancellation settles `drainDone` even when no record transition can trigger `onTaskSettled`. Control producers now publish through one lifecycle-gated enqueue path. Coordinator exit takes the exclusive side of that gate, unpublishes the inbox, then drains queued pooled envelopes, preventing a producer from racing a send into an already-unconsumed inbox. The gate is a dedicated `RWMutex`, so blocked control sends do not hold `Engine.mu` and therefore do not extend unrelated lifecycle-handle lock acquisition. `Health` now uses the bounded shared control path and directly resolves root-stop semantics; `SetOwnerLimits` first moved onto that bounded path in H1; M3-H3 now completes its applied/not-applied result semantics with an explicit decision fence and context-aware variant. Focused regression source covers parent cancellation followed by `Drain`/`Stop` and repeated post-stop control calls. The scoped M3 acceptance gate passed after H1–H5 and the AttemptID correction.
2. **Live pool bounds and validation — M3-H2 IMPLEMENTED.** Startup and live pool normalization now share the same effective semantics: a configured concurrency of zero uses the historical effective default of four, and `MinConcurrency=0, ZeroIdle=false` means fixed-size at the effective concurrency while `ZeroIdle=true` remains minimum zero. Startup validation rejects a minimum above the effective concurrency and rejects negative `MaxTerminalRetained` before startup can proceed. Live configuration rejects negative idle timeout, backlog limit, and payload budget before mutating admission/runtime state; zero backlog/payload values retain the existing startup meaning instead of introducing a new semantic. Shrinking a pool now retires already-idle workers above the new maximum immediately, while busy excess workers are allowed to finish and are retired as each becomes idle before replacement dispatch. This makes a busy 8-to-2 shrink converge to at most two replacement executions without cancelling the original eight. Focused regression source covers the busy 8-to-2 case with persistent backlog, fixed-size live minimum parity, effective startup bounds, fail-closed Start on invalid config, and negative live admission limits. The scoped M3 acceptance gate passed after H1–H5 and the AttemptID correction.
3. **Owner fairness and control admission — M3-H3 IMPLEMENTED.** `admission.Controller.SetOwnerLimits` now normalizes limits once, refreshes every cached DRR quantum for an already-active owner across pools/classes, and resets deficit accumulated under the old weight so a lowered or raised weight takes effect on the next scheduling turn instead of leaking stale credit. TaskEngine owner-limit mutation now uses a dedicated decision cell around the existing single coordinator: cancellation/root-stop can win only before the coordinator claims the request, in which case the mutation is fenced out; once the coordinator claims it, the caller waits for acknowledgement so a returned `nil` means applied rather than an ambiguous timeout. `SetOwnerLimits` now returns that result while preserving its two-argument call shape for ordinary existing call statements, and `SetOwnerLimitsContext` lets callers supply their own bound; calls without a deadline still inherit the engine decision timeout instead of the former bespoke two-second wrapper. Focused regression source covers active-owner quantum/deficit refresh, successful public application, cancelled public updates that do not change admission state, and a pre-cancelled coordinator decision fence. No second scheduler, queue, or execution authority was introduced. The scoped M3 acceptance gate passed after H1–H5 and the AttemptID correction.
4. **Retention and accounting — M3-H4 IMPLEMENTED, with post-H5 defect correction.** Admission now charges engine-owned variable WorkSpec metadata (`ID`, scope owner, quota owner, pool/class, ordering key, handler ref, occurrence IDs, resource names, and copied resource backing) in addition to the validated input payload and fixed record overhead before ownership is copied. Engine-owned string payloads/metadata and retained result strings are cloned to exact-size backing storage so a short substring cannot pin a much larger caller allocation; failure code/message/detail are bounded consistently. `settleTerminal` preserves only diagnostic identity plus the bounded `TaskResult`, copies `AttemptID` out of `Job` when the task never reached the executor, and detaches `Input`, `Job`, resources, ordering/handler metadata, execution callbacks, cancel context, permit, and pending durability result. Detached-memory accounting now deliberately releases only the Job header plus `JobID`/`OccurrenceID`; the existing admission charge for `AttemptID` remains on the terminal record because the same exact-size string remains reachable through `TaskResult.AttemptID`. That charge is released only when the terminal record itself is evicted, matching other retained result ownership. This corrects the H4 under-reporting defect found after H5 without imposing an arbitrary `AttemptID` length cap not present in the domain contract. Focused regression source now covers metadata-only retained-budget rejection, defensive ownership while active, terminal graph detachment/charge release, queued cancellation preserving occurrence identity, a large retained `AttemptID` remaining charged after Job detachment, and a second large AttemptID being rejected by `MaxRetainedBytes` while the first terminal result is retained.
5. **Durability and forced cleanup — M3-H4 IMPLEMENTED.** The direct durability fallback now closes only over the already-snapshotted commit function/result plus `taskID`/`commitSeq` and routes completion through the shared acknowledgement helper; it no longer dereferences the `taskRecord` from the durability lane. Direct commit panic is recovered inside that fallback and converted into an explicit failure acknowledgement, producing `RecoveryRequired` rather than being silently swallowed by the lane and leaving `CommitPending` unresolved. Forced in-flight finalization now releases coordinator-owned resource reservations before dropping admission ownership; because the record is transitioned terminal before any late worker completion is accepted, the late completion path cannot release those resources a second time. Focused regression source covers direct-fallback panic resolution and resource accounting after forced stop.
6. **Observability and structural fences — M3-H5 IMPLEMENTED.** Public `RuntimeStats` now exposes the coordinator-owned `Accepting` and `Quiesced` lifecycle state already captured by the internal stats snapshot, with regression coverage for running → quiesced transitions. The inert pool-generation counter, per-record/per-permit generation copies, dead `opWorkerIdle` opcode, and an additional write-only `permit.workerID` field were removed instead of being presented as active safety mechanisms. Started/completed transitions remain fenced by the exact coordinator-owned permit identity plus monotonically increasing dispatch epoch, while physical worker reincarnation remains fenced independently by the fresh-mailbox-per-spawn design; focused tests now exercise both foreign-permit and stale-epoch rejection for Started and Completed. Architecture coverage now requires the control inbox to have exactly one production send site (`engine.go:enqueueRequest`) and prevents the principal run-loop-owned mutable maps (`registry`, `idleSlots`, `workerRunning`, `resourceUsed`, `commitWaiters`, `cancelledScopes`, `terminalOrder`) from being duplicated onto another TaskEngine struct. The existing single `Engine`/single `runLoop` and responsibility-file fences remain. Further file movement is deliberately deferred because it would not improve the ownership invariant enough to justify extra churn.

Scoped local M3 acceptance at `f954369ba89828dfe7d4f99946b93bb105287898` (28 September 2026):

- `go test -race ./internal/taskengine ./internal/admission -count=1 -timeout=180s` — passed; this runs the admission/cancel, bounded-memory, worker-generation, resource, durability, delivery, and shutdown regressions in those packages.
- `go test ./internal/architecture -run '^TestM3TaskEngine' -count=1` — passed.
- `go vet ./internal/taskengine ./internal/admission` — passed.
- `go test ./internal/taskengine -run '^$' -bench 'BenchmarkB0_IdleOverhead|BenchmarkB1_TinyEphemeralTask|BenchmarkTaskEngineFirstTaskAfterIdle' -benchtime=1000x -count=2 -timeout=180s` — passed at current HEAD and at the pre-hardening M3 structural baseline `4449a1d` in an isolated `/tmp` checkout, on the same machine. Current versus baseline samples: idle snapshot 2.63–2.71 µs versus 2.50–2.57 µs; tiny task 8.36–10.38 µs versus 9.21–10.59 µs; cold wake 55.82–58.03 µs versus 58.34–59.53 µs; warm wake 12.14–13.79 µs versus 11.57–17.34 µs. No gross latency regression appeared in these samples. Tiny-task and wake allocations rose by about four per operation, consistent with the new defensive ownership copies; this is a measured cost, not a latency gate failure. These short local samples are not a controlled long-run performance study.
- The broader architecture suite has unrelated P7K/P8E/callback-file failures; those were not used to infer M3 correctness. CI was not inspected.

Next-session execution contract:

- audited source baseline after the H4 AttemptID accounting correction: `b3d2fcf5e32f7a2e00538f7968bfe8495d602ad1` on `test-next`; refresh HEAD and re-read the touched TaskEngine files before running acceptance because later commits may have advanced the branch;
- **do not redo the mechanical M3 split** and do not create a second Engine, coordinator, queue, registry, worker authority, or execution path;
- **M3-H1 through M3-H5 and the scoped local acceptance gate are complete; M3 is CLOSED.** M4 may begin from the refreshed HEAD without repeating the M3 split or hardening;
- treat the six audit findings as hypotheses to reconfirm against current code before patching; several are pre-existing behavior defects, not regressions caused by the structural split;
- keep each hardening commit narrow and behavior-focused, add a focused regression test with the fix, and run `gofmt` before every Go-changing commit;
- preserve the user's standing rule: **do not inspect or poll CI unless explicitly requested**;
- preserve the completed M3 ownership and regression fences while working on M4;
- when a finding is disproved or already fixed by newer HEAD, update this section instead of implementing an obsolete patch.

Hardening sequence:

- **M3-H1 — lifecycle/shutdown — IMPLEMENTED:** settle `drainDone` on every root-cancellation/finalization path; reject or resolve post-stop control requests promptly; cover parent cancellation followed by graceful `Stop` and repeated post-stop API calls. Implemented in `f3aba784...` with the enqueue-gate refinement in `d41718f2...`. Focused regression and the scoped M3 local acceptance gate passed.
- **M3-H2 — live pool/admission — IMPLEMENTED:** enforce the new maximum for replacement dispatch after shrink; align startup/live minimum semantics; validate effective startup bounds and live backlog/payload limits; cover a busy 8-to-2 shrink with persistent backlog. Implemented in `37ede0c8...` with focused coverage tightened in `bee42d73...`. `gofmt` was run on the changed Go sources before the final H2 code/test commit; the scoped M3 local acceptance gate later passed.
- **M3-H3 — fairness/control — IMPLEMENTED:** active-owner DRR quantum is refreshed across current pools/classes and stale deficit is cleared on weight changes; owner-limit control now has a decision fence so cancellation-before-claim means not applied, while claim-before-cancellation produces an acknowledgement-backed result. `SetOwnerLimits` returns `error`, and `SetOwnerLimitsContext` provides caller-controlled cancellation/deadlines while no-deadline calls remain bounded by the engine decision timeout. Implemented in `cf8f22c1...`, `3444b202...`, and `71c7a0d8...`; formatting follow-ups are `07293403...` and `7d1f17ab...`. The scoped M3 local acceptance gate later passed.
- **M3-H4 — memory/durability — IMPLEMENTED, with AttemptID accounting correction:** terminal records detach input/job/resource and execution-lifecycle graphs while preserving bounded ticket results/diagnostic identity; retained WorkSpec metadata and exact-size string ownership are accounted; detached charges are released at terminal transition except the `AttemptID` bytes that remain reachable through `TaskResult`, whose admission charge stays with the terminal record until eviction; direct durability fallback no longer captures the record and turns commit panic into an explicit failure acknowledgement; forced cancellation releases resource reservations with late completion fenced against double-release. Core H4 landed in `76add6fa...`, `f85625fd...`, and `b84be03d...`, queued-cancel occurrence coverage in `2bcc9742...`, and the post-H5 AttemptID under-reporting correction in `afcb5097...` with formatting follow-up `b3d2fcf5...`. The scoped M3 local acceptance gate later passed.
- **M3-H5 — observability/fences — IMPLEMENTED:** `RuntimeStats` exposes `Accepting/Quiesced`; inert pool/permit generation state, dead `opWorkerIdle`, and write-only permit worker ID were removed; exact permit identity + dispatch epoch remain the explicit Started/Completed fences; architecture tests require one production control-inbox send site and keep principal mutable execution maps owned only by `Engine`. Implemented in `1b26bc1e...`, `70b4bc6c...`, `a1550beb...`, and `77b913b8...`. `gofmt` was applied to the new/changed H5 test and permit fragments before their Go-changing commits. The scoped M3 local acceptance suite subsequently passed at `f954369b`.

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
- all mutable ownership invariants remain unchanged;
- M3-H1 through M3-H5 and the scoped local TaskEngine architecture/regression/benchmark gate passed at `f954369b`; M3 is CLOSED;
- do not inspect or poll CI unless explicitly requested.

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
