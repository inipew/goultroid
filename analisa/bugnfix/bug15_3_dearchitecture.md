# Assistant v2.5 — De-Architecture & Functional Unification Plan

**Repository:** `inipew/goultroid`  
**Baseline:** `main` / HEAD `8a823d6`  
**Status:** PLAN / DESIGN — this document does not itself perform the refactor  
**Scope:** Assistant v2.5, command routing, execution surfaces, plugin registration, shared functionality, callbacks, menus, inline handling, lifecycle, observability, and migration safety.

---

## 1. Executive Summary

The current Assistant v2.5 direction solved many transport and interaction problems, but the implementation accumulated a second orchestration stack around the existing core architecture. The most important example is the combination of:

- `core.Command` + `core.Router`;
- `application/command.UnifiedRegistry`;
- `assistant/command.Router`;
- `UnifiedCommandAdapter`;
- duplicated Assistant command implementations;
- a separate capability registry;
- overlapping execution/source models;
- multiple command/execution contexts.

The result is functionally valid in several areas but architecturally heavier than necessary. The Assistant is beginning to behave like a second application rather than a second execution surface.

The target architecture is therefore a **de-architecture**, not another abstraction layer:

> **One functionality, one implementation, multiple execution surfaces.**

The core/plugin layer owns functionality. Userbot and Assistant own transport-specific dispatch. Inline remains a separate transport surface where Telegram semantics genuinely differ. Assistant-specific UI such as `/start` and menus remains in Assistant because it is presentation/navigation, not duplicate business functionality.

The canonical command model remains `core.Command`, including its existing `Surfaces` metadata. The canonical command registry remains `core.Router`. The Assistant must query/execute the same command definitions rather than maintaining a second registry.

The migration must be incremental and reversible. Existing lifecycle, hook cleanup, callback validation, peer resolution, menu session, and transport reliability work must be preserved. The refactor must remove duplication only after the canonical path is proven.

---

## 2. Current Baseline

The baseline used by this plan is HEAD `8a823d6`.

Relevant current code confirms that `core.Router` already owns a command map and canonical command registration. It provides `RegisterBatch`, `Find`, `All`, parsing, alias handling, and atomic registration semantics. `core.Command` already contains command metadata including permission, surface availability, group/private constraints, cooldown, timeout, and handler. This is enough to be the functional command source of truth.  

Current `plugin.Manager` registers plugin commands into `core.Router`, but also registers the same commands into `application/command.UnifiedRegistry`. This creates two registries for the same objects and is a primary source of architectural duplication.

Current `assistant/command.Router` has its own handler map and additionally delegates to `UnifiedCommandAdapter`. It therefore contains two command paths inside one Assistant router: an Assistant-local handler path and a unified-plugin path.

This plan deliberately treats those existing pieces as migration scaffolding rather than the desired final architecture.

---

## 3. Problem Statement

### 3.1 The architectural problem

The problem is not that Assistant has its own dispatcher. A different Telegram transport legitimately needs different parsing, update extraction, callback handling, and reply mechanics.

The problem is that Assistant currently has too much ownership over **what a command does**.

The undesirable dependency graph is effectively:

```text
Plugin
  ├── core.Router
  └── application.command.UnifiedRegistry
                              │
                              ▼
                   UnifiedCommandAdapter
                              │
                              ▼
                   assistant.command.Router
                              │
                     local handlers too
                              │
                              ▼
                        Assistant UI
```

This makes it difficult to answer basic questions:

- Where is `/ping` actually implemented?
- Which registry is authoritative?
- Which permission path is authoritative?
- Which execution context reaches a plugin handler?
- Does a command exist because it is registered in core or because Assistant registered a local handler?
- What happens when the same command is changed in one surface but not another?
- Which metrics describe the actual command execution?

A production architecture should make those answers obvious.

### 3.2 Desired invariant

For a shared command:

```text
             one Command
                 │
          one Handler / logic
                 │
       ┌─────────┴─────────┐
       │                   │
   Userbot              Assistant
       │                   │
 different transport   different transport
       │                   │
       └─────────┬─────────┘
                 ▼
          same functionality
```

The surfaces may differ in:

- update extraction;
- Telegram API interaction;
- message/callback mechanics;
- presentation formatting where necessary;
- availability;
- transport-specific rate limits;
- authorization preflight where the transport provides different actor information.

They must not differ merely because the same feature was implemented twice.

---

# 4. Architecture Principles

## 4.1 One functionality, one implementation

A functional operation should have one implementation whenever its semantics are shared.

Examples:

- `/ping` → one ping use-case/handler.
- `/status` → one status collection/decision path.
- `/alive` → one runtime status source.
- Settings mutations → one settings/domain implementation.
- Shared plugin commands → one `core.Command` definition and one handler.

The Assistant may adapt input/output, but must not reimplement the functional operation.

## 4.2 Transport is not functionality

Assistant-specific code should answer questions such as:

> How did Telegram deliver this interaction, and how should the result be presented back through the Bot API?

It should not answer:

> What does this application feature mean?

That belongs in core/plugin/application services.

## 4.3 Existing abstractions are preferred

Before adding an abstraction, prove that an existing type cannot satisfy the requirement.

The default preference is:

```text
existing core.Command
existing core.Router
existing execution.Source / SurfaceMask
existing shared service
existing assistant interaction
```

not:

```text
new UnifiedCommand
new AssistantCapability
new AssistantExecutionContext
new AssistantRegistry
new CommandAdapterV2
```

## 4.4 Surface-specific behavior must be explicit

A command that is unavailable on Assistant should be unavailable because its `Surfaces` metadata says so, not because it happens to be absent from an Assistant-specific registry.

`core.Command.IsAvailableOn(execution.SourceAssistant)` is the semantic source of availability.

## 4.5 UI state is not domain state

`MenuInstance`, screens, buttons, callback payloads, and rendering are Assistant UI state.

They may identify an action, but they should not become an alternate business-command registry.

## 4.6 Lifecycle remains deterministic

De-architecture must not weaken the existing lifecycle guarantees. Plugin shutdown order, hook detachment, idempotent shutdown, and resource ownership remain load-bearing behavior.

The goal is fewer owners, not less lifecycle safety.

## 4.7 Do not design for hypothetical future requirements

Inline may eventually need a shared action model, but that does not justify creating a generic abstraction before the real requirement exists.

Introduce a new abstraction only when:

1. two or more real implementations currently require it;
2. the semantics are actually identical;
3. the abstraction reduces complexity rather than moving it;
4. ownership becomes clearer afterward.

---

# 5. Target Architecture

## 5.1 High-level target

```text
                         GoUltroid
                            │
                         Plugins
                            │
                            ▼
                      core.Command
                            │
                    Surface availability
                            │
          ┌─────────────────┴─────────────────┐
          │                                   │
          ▼                                   ▼
  Userbot dispatcher                   Assistant dispatcher
          │                                   │
          │                                   ├── Assistant-only /start
          │                                   │
          │                                   └── shared commands
          │
          └───────────────┬───────────────────┘
                          ▼
                    same Handler
                          │
                          ▼
                  shared service/domain
```

The Assistant dispatcher is therefore a **transport adapter/dispatcher**, not a second command system.

## 5.2 Component ownership

| Component | Owns | Must not own |
|---|---|---|
| `core.Command` | Command definition + metadata + handler | Telegram Bot API mechanics |
| `core.Router` | Canonical command registration/lookup | Assistant UI state |
| Plugin manager | Plugin lifecycle + registration | Duplicate command registries |
| Userbot dispatcher | Userbot update extraction + dispatch | Duplicate feature logic |
| Assistant dispatcher | Bot update extraction + dispatch | Duplicate feature logic |
| Assistant interaction | Send/edit/delete/answer | Domain decisions |
| Assistant callback | Decode/validate/resolve/dispatch | Business implementation |
| Assistant menu | Screens/session/buttons | Domain implementation |
| Assistant presentation | Bot-specific rendering | Command semantics |
| Shared application services | Functional use-cases | Transport-specific update parsing |
| Inline transport | Inline-specific Telegram semantics | Assistant command registry |

---

# 6. Canonical Command Flow

## 6.1 Userbot

```text
Telegram update
      │
      ▼
Userbot dispatcher
      │
      ▼
parse command
      │
      ▼
core.Router.Find
      │
      ▼
core.Command
      │
      ▼
availability / permission / context checks
      │
      ▼
Command.Handler
      │
      ▼
shared functionality
```

## 6.2 Assistant

```text
Telegram Bot update
      │
      ▼
assistant client/update dispatcher
      │
      ▼
Assistant command parser
      │
      ▼
core.Router.Find
      │
      ▼
core.Command
      │
      ▼
availability / permission / context checks
      │
      ▼
Command.Handler
      │
      ▼
shared functionality
      │
      ▼
assistant interaction / presentation
```

The only important difference is how the execution context is constructed.

## 6.3 Assistant-only `/start`

`/start` is allowed to remain Assistant-owned because it is primarily a Bot UX entry point.

```text
/start
  │
  ▼
Assistant dispatcher
  │
  ▼
Start controller
  │
  ├── build menu
  ├── create MenuInstance
  └── render dashboard
```

It must not become a model for every other command.

---

# 7. Canonical Command Model

`core.Command` should remain the canonical representation.

The current model already contains:

```go
Name
Aliases
Description
Usage
Category
Permission
Surfaces
GroupOnly
PrivateOnly
ReplyOnly
Cooldown
Timeout
Handler
```

This metadata is sufficient for command discovery and surface filtering.

## 7.1 Surface availability

Use:

```go
cmd.IsAvailableOn(execution.SourceAssistant)
```

for Assistant availability.

The Assistant must not infer availability from:

- local handler registration;
- presence in another registry;
- adapter configuration;
- menu membership.

## 7.2 Registry invariant

There must be exactly one authoritative command registry per application runtime:

```text
core.Router
```

Aliases must resolve through the same canonical command object.

No secondary registry should contain a second authoritative copy of the command set.

---

# 8. De-Architecture of `assistant/command`

## 8.1 `assistant/command/router.go`

### Current role

It currently owns:

- Assistant-local handlers;
- unified registry reference;
- unified adapter;
- owner configuration;
- Assistant command parsing;
- dispatch fallback.

### Target role

The file should either be deleted or reduced to a very thin Assistant dispatcher.

It should:

1. parse `/command` syntax;
2. normalize optional `@botname` suffix;
3. obtain the canonical `core.Command` from `core.Router`;
4. verify surface availability;
5. build the canonical execution context;
6. invoke the canonical handler;
7. return transport-level errors.

It must not maintain:

```go
handlers map[string]Handler
unifiedRegistry CommandSource
adapter *UnifiedCommandAdapter
```

## 8.2 Local handler map

Delete the general-purpose local handler map.

If Assistant-only handlers remain, use explicit controllers/components rather than treating them as ordinary plugin commands.

Example:

```text
assistant/
  start.go
```

rather than:

```text
assistant/command/commands.go
assistant/command/router.go
assistant/command/start.go
```

## 8.3 `UnifiedCommandAdapter`

Delete after canonical direct invocation is proven.

The adapter currently exists because the architecture has two command registries. Once Assistant directly queries `core.Router`, the adapter has no semantic job.

---

# 9. Removal of `application/command.UnifiedRegistry`

## 9.1 Why it is redundant

The current system registers plugin commands into both:

```text
core.Router
application.command.UnifiedRegistry
```

The second registry does not provide a genuinely different source of functionality. It provides another lookup structure around the same `core.Command` objects.

That adds:

- synchronization requirements;
- duplicate registration paths;
- duplicate lifecycle assumptions;
- more test cases;
- more failure modes;
- unclear ownership.

## 9.2 Migration

Before deletion:

1. Identify every call to `UnifiedRegistry`.
2. Replace Assistant command lookup with `core.Router.Find`.
3. Replace surface listing with `core.Router.All()` plus `IsAvailableOn` filtering.
4. Remove `PluginManager.SetUnifiedCommandRegistry`.
5. Remove `RegisterBatch` duplication.
6. Remove the package.
7. Run repository-wide compile/test/search checks.

## 9.3 Help command

Help should use the canonical command list:

```text
router.All()
    │
    ▼
filter IsAvailableOn(SourceAssistant)
    │
    ▼
filter presentation/permission policy as required
    │
    ▼
Assistant renderer
```

This gives one source of truth without another registry.

---

# 10. Plugin Manager Simplification

The current plugin manager has valuable lifecycle behavior and should not be rewritten wholesale.

### Preserve

- plugin duplicate checks;
- shutdown guard;
- command validation;
- initialization outside mutex;
- atomic command registration;
- compensating cleanup on failure;
- hook cleanup;
- metadata storage;
- reverse-order shutdown;
- hook detachment before plugin shutdown.

### Remove

```go
capabilityRegistry
unifiedCommandRegistry
```

unless a concrete non-command consumer still requires them after repository-wide usage analysis.

The normal registration path becomes conceptually:

```go
cmds := p.Commands()
validate(cmds)
initialize(p)
router.RegisterBatch(cmds)
registerHooks(p)
registerMetadata(p)
```

The manager remains a lifecycle component, not an application-wide registry coordinator.

---

# 11. Capability Registry Decision

The capability layer should not be removed blindly.

First classify all current capability consumers:

### Keep if

A capability represents something materially different from command discovery, for example:

- a non-command plugin capability;
- runtime feature negotiation;
- programmatic capability lookup by another subsystem;
- a stable API contract unrelated to Telegram commands.

### Remove if

It exists primarily to answer:

> Which commands are available on Assistant/Userbot?

That question is already answered by `core.Command.Surfaces`.

### Decision rule

If all capability registrations are command aliases/discovery metadata, delete the capability registry from the Assistant v2.5 path.

If real non-command capabilities exist, keep the registry but remove its coupling to Assistant command routing.

---

# 12. Execution Model Simplification

The repository currently has overlapping execution/source concepts. This is a major source of accidental complexity.

## 12.1 Target

There should be one canonical:

```go
execution.Source
execution.SurfaceMask
```

and one canonical execution context used by shared command handlers.

## 12.2 Assistant context

The Assistant-specific context should be a transport adapter only if the core handler genuinely requires Bot-specific information.

Do not create a second business context merely because the Assistant parser has different inputs.

Conceptually:

```text
Transport data
    │
    ▼
canonical ExecutionContext
    │
    ├── source = Assistant
    ├── actor
    ├── peer
    ├── command
    ├── args
    └── interaction capabilities when required
```

The exact final shape must follow the existing core context and current handler signatures. The migration must avoid introducing another parallel context hierarchy.

## 12.3 Duplicate source model

Search for all definitions/usages of:

```text
Source
SurfaceMask
ExecutionSource
```

Select one canonical model, migrate imports, then delete the duplicate.

Do not maintain compatibility aliases indefinitely unless external packages actually require them.

---

# 13. Shared Command Migration

Migration order matters because each duplicate removal should leave a working canonical path.

## 13.1 `/ping`

Current Assistant ping already uses the shared application ping use-case, which is a useful intermediate state.

Target:

```text
core.Command("ping")
        │
        ▼
shared ping implementation
        │
        ├── Userbot presentation
        └── Assistant presentation
```

Delete `assistant/command/ping.go` after the canonical command is verified on Assistant.

The latency calculation must not be duplicated.

## 13.2 `/alive`

Current Assistant implementation uses shared status collection but still owns Assistant-local command registration.

Target:

```text
core.Command("alive")
        │
        ▼
shared status/runtime source
        │
        ▼
Assistant presentation adapter if needed
```

The renderer may remain surface-specific. The underlying status facts must not be duplicated.

## 13.3 `/status`

Apply the same rule as `/alive`.

Avoid maintaining:

- Assistant status state;
- Userbot status state;
- duplicate status collection;
- duplicate permission semantics.

## 13.4 `/help`

Help is mostly discovery/presentation.

It should read from `core.Router.All()` and filter using `Surfaces`.

No unified registry is necessary.

## 13.5 Settings

Settings callbacks may remain Assistant-specific as transport/UI handlers, but the actual settings mutation must be shared.

```text
button callback
      │
      ▼
menu/action resolution
      │
      ▼
shared settings operation
      │
      ▼
render updated screen
```

The menu should not contain the settings domain logic.

---

# 14. Callback Architecture

Callback handling is a transport problem and should remain inside Assistant.

The target lifecycle is:

```text
Telegram callback
       │
       ▼
Decode
       │
       ▼
Validate
       │
       ▼
Resolve target / actor / session
       │
       ▼
Authorize
       │
       ▼
Execute shared action
       │
       ▼
Answer callback exactly once
       │
       ▼
Observe metrics/logging
```

## 14.1 Message callback

`MessageTarget` remains valid for message-based callbacks.

The remaining load-bearing fixes are:

- `GetMessage` must respect `MessageTarget.Peer` rather than defaulting to a generic message lookup when peer information is required;
- `ReResolve` must support cache miss → Telegram entity fetch → cache refresh → one retry;
- target fields should not remain exported mutable state unless required by a stable API contract.

## 14.2 Inline callback

Inline callbacks are not message callbacks.

They require their own target representation and update registration because Telegram supplies different identifiers and context.

Do not force inline data into `MessageTarget`.

The correct separation is:

```text
MessageTarget
InlineTarget
```

with a shared lower-level action execution path only where the semantics are genuinely common.

## 14.3 Inline metrics

Inline execution must participate in the same observability model:

```text
source = inline
```

rather than pretending to be an Assistant message callback.

---

# 15. Peer Resolution

Peer resolution is a correctness boundary, not a convenience helper.

## 15.1 Required behavior

```text
resolve(peer)
   │
   ├── cache hit → return
   │
   └── cache miss
          │
          ▼
      entity fetch
          │
          ▼
      update cache
          │
          ▼
        retry once
          │
          ├── success → return
          └── failure → typed error
```

## 15.2 Why this matters to de-architecture

The Assistant dispatcher should not contain peer-fetch logic. That would simply move complexity from one layer to another.

Peer correctness belongs to `assistant/peer` and is consumed by callbacks/interaction.

## 15.3 Message lookup

A message target with an explicit peer must use that peer when invoking Telegram APIs. Ignoring peer context is unsafe for channels, supergroups, and other peer-specific message addressing.

---

# 16. Menu Session Enforcement

`MenuInstance` is useful and should remain.

However, it must become an enforced invariant rather than a passive data structure.

## 16.1 Target behavior

```text
button callback
      │
      ▼
resolve MenuInstance
      │
      ├── active → continue
      └── expired
             │
             ▼
       SESSION_EXPIRED
             │
             ▼
      do not edit screen
```

## 16.2 Required operations

The menu subsystem should have a minimal registry/session store capable of:

```text
Register(instance)
Get(instanceID)
Invalidate(instanceID)
```

No additional abstraction is required beyond the actual session lifecycle.

## 16.3 Security invariant

A callback generated for an old menu must never be allowed to mutate a newer menu merely because its action name is still valid.

Session validation must happen before mutation.

---

# 17. Authorization

Owner/sudo checks must remain centralized.

A particularly important safety rule is:

```text
ownerID == 0
```

must not silently mean “allow everyone” for privileged Assistant operations.

The safe policy is fail-closed for owner-only functionality when owner identity is not configured.

The exact configuration fallback may depend on the application's startup contract, but an uninitialized owner identity must never accidentally become unrestricted authorization.

Authorization should happen before invoking the shared handler when the command metadata requires it.

---

# 18. Rate Limiting

Rate limiting remains surface-aware transport policy.

Command and callback buckets can remain distinct where the abuse model differs.

Inline must not be forgotten:

```text
Allow(..., "inline")
```

must participate in inline callback processing if inline traffic is independently rate-limited.

Do not create a generalized policy framework solely for this. Existing buckets are sufficient unless actual duplication appears.

---

# 19. Observability and Correlation

The de-architecture is not complete if execution becomes simpler but invisible.

## 19.1 Required correlation invariant

Every Assistant interaction should have a non-zero correlation ID before entering the main execution path.

Conceptually:

```text
update
  │
  ▼
correlation ID
  │
  ├── log
  ├── metrics
  ├── peer resolution
  ├── command execution
  ├── callback answer
  └── error
```

## 19.2 Required metrics

At minimum:

- command attempts;
- command success;
- command errors;
- command latency;
- callback attempts;
- callback success/errors;
- callback latency;
- peer resolution success/failure/cache miss;
- message edit success/failure;
- message delete success/failure;
- inline attempts/success/errors/latency.

Metrics should be emitted by the actual execution path, not merely declared in a metrics package.

## 19.3 Duplicate callback answer

`ErrCallbackAlreadyAnswered` must be semantically reachable if duplicate answering is treated as a controlled lifecycle condition.

A declared error that can never be returned is not a useful invariant.

---

# 20. Lifecycle and Shutdown

De-architecture must preserve the current lifecycle ordering.

Target ownership remains:

```text
App
 │
 ▼
Dispatcher / transports
 │
 ▼
Scheduler / runtime services
 │
 ▼
Plugins
 │
 ▼
EventBus / shared resources
 │
 ▼
DB / persistence
```

For Assistant specifically:

```text
Start
  ├── configure dependencies
  ├── connect Bot client
  ├── register update handlers
  └── start receiving updates

Shutdown
  ├── stop accepting new updates
  ├── detach callbacks/hooks
  ├── stop Assistant worker paths
  ├── wait for in-flight operations according to existing policy
  └── close Bot client/resources
```

Do not let command-registry cleanup become a new lifecycle concern. Removing `UnifiedRegistry` should simplify startup/shutdown, not add another phase.

---

# 21. Detailed File-Level Migration Map

| File / Area | Action | Target |
|---|---|---|
| `internal/core/command.go` | KEEP | Canonical command model |
| `internal/core/router.go` | KEEP | Canonical command registry/lookup |
| `internal/assistant/command/router.go` | SIMPLIFY → DELETE | Thin transport dispatcher or remove if existing dispatcher can directly use core router |
| `internal/assistant/command/ping.go` | MIGRATE → DELETE | Use canonical command/shared ping |
| `internal/assistant/command/alive.go` | MIGRATE → DELETE | Use canonical status functionality |
| `internal/assistant/command/status.go` | MIGRATE → DELETE | Use canonical status functionality |
| `internal/assistant/command/help.go` | MIGRATE → DELETE | Read from `core.Router.All()` |
| `internal/assistant/command/start.go` | KEEP / MOVE | Assistant-only dashboard/navigation |
| `internal/assistant/command/commands.go` | MERGE / DELETE | Keep only Assistant-specific UI wiring |
| `internal/assistant/command/*adapter*` | DELETE | No second command registry |
| `internal/application/command/registry.go` | DELETE | Redundant registry |
| `internal/application/command/*` | DELETE after callers migrate | No duplicate command orchestration |
| `internal/application/capability/*` | REVIEW | Delete if only command discovery; otherwise isolate |
| `internal/execution/source.go` | KEEP / CANONICALIZE | One source/surface model |
| duplicate execution/source definitions | DELETE | One execution model |
| `internal/execution/context.go` | SIMPLIFY | One canonical execution context |
| `internal/plugin/manager.go` | SIMPLIFY | Keep lifecycle, remove duplicate registrations |
| `internal/assistant/client/*` | KEEP | Bot transport/lifecycle |
| `internal/assistant/interaction/*` | KEEP | Telegram interaction primitives |
| `internal/assistant/peer/*` | KEEP + FIX | Correct cache miss/entity fetch/retry |
| `internal/assistant/callback/*` | KEEP + FIX | Callback transport lifecycle |
| `internal/assistant/menu/*` | KEEP + FIX | Session-aware UI state |
| `internal/assistant/presentation/*` | KEEP | Bot-specific rendering |
| `services/inline.Engine` | REVIEW | Keep only as inline transport until inline-specific code is properly separated |
| Assistant update registration | KEEP + FIX | Register inline callback update separately |
| metrics implementation | KEEP + WIRE | Actual emission from Assistant paths |

---

# 22. Migration Strategy

The refactor must be performed in small, compile-safe phases.

## Phase 0 — Freeze architecture

**Goal:** Stop adding abstractions.

Tasks:

- [ ] Mark `UnifiedRegistry` as migration-only.
- [ ] Mark `UnifiedCommandAdapter` as migration-only.
- [ ] Do not add new Assistant capability/registry/context abstractions.
- [ ] Record this document as the target architecture.

Exit criteria:

- No new duplicate command infrastructure is introduced.

## Phase 1 — Fix transport correctness first

**Goal:** Ensure Assistant transport is correct before changing command ownership.

Tasks:

- [ ] Fix `MessageTarget.Peer` handling in `GetMessage`.
- [ ] Implement cache miss → entity fetch → refresh → retry in `ReResolve`.
- [ ] Enforce `MenuInstance` registration/lookup/expiration.
- [ ] Add `InlineTarget`.
- [ ] Register `OnInlineBotCallbackQuery`.
- [ ] Implement inline Decode → Validate → Resolve → Authorize → Execute → Answer → Observe.
- [ ] Make owner configuration fail-closed.
- [ ] Ensure inline rate-limit bucket is used.

Exit criteria:

- Message callbacks work with correct peer context.
- Cache invalidation no longer causes permanent resolution failure.
- Old menu buttons cannot mutate expired screens.
- Inline callbacks are a first-class transport path.

## Phase 2 — Direct Assistant access to core commands

**Goal:** Remove the need for the unified adapter.

Tasks:

- [ ] Make Assistant dispatcher resolve commands through `core.Router.Find`.
- [ ] Filter by `Command.IsAvailableOn(SourceAssistant)`.
- [ ] Build canonical execution context.
- [ ] Invoke the existing `Command.Handler` directly.
- [ ] Preserve Assistant interaction object through context only where required.
- [ ] Add tests proving the exact same `core.Command` is reached by Userbot and Assistant.

Exit criteria:

- Shared Assistant commands no longer require `UnifiedCommandAdapter`.
- `UnifiedRegistry` can be disconnected without behavior change.

## Phase 3 — Migrate command implementations

### Ping

- [ ] Identify canonical ping command.
- [ ] Remove Assistant-specific implementation.
- [ ] Verify Assistant output.
- [ ] Verify Userbot output.
- [ ] Keep presentation differences only where necessary.

### Alive

- [ ] Identify canonical status/runtime command.
- [ ] Remove Assistant-local business logic.
- [ ] Preserve Bot-specific card renderer.

### Status

- [ ] Unify status data collection.
- [ ] Remove duplicate status implementation.
- [ ] Verify permissions and surface availability.

### Help

- [ ] Generate command list from `core.Router.All()`.
- [ ] Filter by Assistant surface.
- [ ] Delete unified registry dependency.

Exit criteria:

- `/ping`, `/alive`, `/status`, `/help` have one canonical command definition/implementation path.

## Phase 4 — Remove duplicate registry

Tasks:

- [ ] Remove `PluginManager.unifiedCommandRegistry`.
- [ ] Remove `SetUnifiedCommandRegistry`.
- [ ] Remove duplicate `RegisterBatch`.
- [ ] Remove Assistant `CommandSource` dependency.
- [ ] Remove `UnifiedCommandAdapter`.
- [ ] Remove `internal/application/command` if no callers remain.

Exit criteria:

```text
Plugin → core.Router
Assistant → core.Router
Userbot → core.Router
```

No second command registry exists.

## Phase 5 — Simplify execution model

Tasks:

- [ ] Search all `Source`/`SurfaceMask`/execution-source definitions.
- [ ] Select one canonical type.
- [ ] Migrate duplicate imports.
- [ ] Simplify Assistant context.
- [ ] Remove redundant context types.
- [ ] Run full compile/test suite.

Exit criteria:

- One execution-source model.
- One canonical command execution context.

## Phase 6 — Settings/menu cleanup

Tasks:

- [ ] Keep menu/session/navigation in Assistant.
- [ ] Move business mutations to shared settings service where not already shared.
- [ ] Remove Assistant-specific duplicate settings implementation.
- [ ] Make callback action map point to shared operations.
- [ ] Verify session ownership before every mutation.

Exit criteria:

- Menu code is UI-only.
- Settings behavior is shared.

## Phase 7 — Observability completion

Tasks:

- [ ] Ensure correlation ID is always non-zero.
- [ ] Emit Assistant command metrics.
- [ ] Emit callback metrics.
- [ ] Emit peer-resolution metrics.
- [ ] Emit edit/delete metrics.
- [ ] Emit inline metrics.
- [ ] Make duplicate callback-answer condition observable.

Exit criteria:

- Every Assistant execution path is traceable from update to completion/error.

## Phase 8 — Delete migration scaffolding

Tasks:

- [ ] Delete obsolete Assistant command files.
- [ ] Delete `UnifiedCommandAdapter`.
- [ ] Delete `UnifiedRegistry`.
- [ ] Delete unused capability layer.
- [ ] Delete duplicate execution models.
- [ ] Delete legacy callback path.
- [ ] Remove compatibility aliases such as legacy `NewBotClient` if no longer needed.

Exit criteria:

- Repository contains one command source and no dead migration architecture.

## Phase 9 — Full parity and regression audit

Tasks:

- [ ] Run command parity matrix.
- [ ] Run callback matrix.
- [ ] Run menu session matrix.
- [ ] Run peer-resolution matrix.
- [ ] Run lifecycle/shutdown matrix.
- [ ] Run inline matrix.
- [ ] Compare Assistant/Userbot functionality against Ultroid requirements.

Exit criteria:

- No known Assistant-vs-Userbot functional duplication.
- No load-bearing v2.5 transport blockers remain.

---

# 23. Task List — Concrete Engineering Checklist

## A. Discovery

- [ ] Search all `UnifiedRegistry` references.
- [ ] Search all `UnifiedCommandAdapter` references.
- [ ] Search all `assistant/command` registrations.
- [ ] Search all `core.Router` command registrations.
- [ ] Search all `SurfaceMask` definitions.
- [ ] Search all `execution.Source` definitions.
- [ ] Search all Assistant-local `/ping`, `/alive`, `/status`, `/help` handlers.
- [ ] Search all settings mutations.
- [ ] Search all callback answer calls.
- [ ] Search all correlation ID generation.
- [ ] Search all metric declarations and emissions.

## B. Peer

- [ ] `MessageTarget.Peer` is honored by `GetMessage`.
- [ ] Channel message lookup uses channel-aware API.
- [ ] Non-channel peer lookup uses the correct peer-specific path.
- [ ] `ReResolve` performs real Telegram fetch on cache miss.
- [ ] Cache is refreshed after successful entity fetch.
- [ ] Retry count is bounded to one refresh attempt.
- [ ] Failure returns a typed resolution error.
- [ ] No silent fallback to an unrelated peer.

## C. Menu

- [ ] Every rendered screen creates/registers a `MenuInstance`.
- [ ] Callback payload contains sufficient session identity.
- [ ] Callback resolves session before mutation.
- [ ] Expired instance returns `SESSION_EXPIRED`.
- [ ] Invalid session never edits a message.
- [ ] Session invalidation is idempotent.
- [ ] Close invalidates the correct session.
- [ ] Old callbacks cannot mutate a new screen.

## D. Inline

- [ ] Add `InlineTarget`.
- [ ] Register inline callback update handler.
- [ ] Validate inline payload.
- [ ] Resolve actor/context.
- [ ] Authorize action.
- [ ] Execute shared operation.
- [ ] Answer callback exactly once.
- [ ] Record inline metrics.
- [ ] Enforce inline rate limit.

## E. Commands

- [ ] Assistant command lookup uses `core.Router`.
- [ ] Surface filtering uses `core.Command.IsAvailableOn`.
- [ ] Assistant does not maintain a general local command registry.
- [ ] `/start` remains explicitly Assistant-only.
- [ ] `/ping` has one canonical implementation.
- [ ] `/alive` has one canonical implementation.
- [ ] `/status` has one canonical implementation.
- [ ] `/help` reads canonical command metadata.

## F. Plugin manager

- [ ] Remove duplicate unified command registration.
- [ ] Preserve transactional registration.
- [ ] Preserve reverse shutdown.
- [ ] Preserve hook cleanup.
- [ ] Preserve initialization outside manager mutex.
- [ ] Preserve compensating cleanup on registration failure.

## G. Execution

- [ ] One `Source` model.
- [ ] One `SurfaceMask` model.
- [ ] One canonical execution context.
- [ ] No Assistant-specific duplicate business context.
- [ ] Permission semantics are shared.

## H. Observability

- [ ] Non-zero correlation ID guaranteed.
- [ ] Command attempt/success/error/latency emitted.
- [ ] Callback attempt/success/error/latency emitted.
- [ ] Peer resolution metrics emitted.
- [ ] Edit/delete metrics emitted.
- [ ] Inline metrics emitted.
- [ ] Duplicate answer error reachable.

## I. Cleanup

- [ ] Delete `assistant/command/router.go` or reduce to minimal transport dispatcher.
- [ ] Delete duplicate command files after migration.
- [ ] Delete `application/command`.
- [ ] Delete adapter.
- [ ] Delete unused capability infrastructure.
- [ ] Delete duplicate execution model.
- [ ] Delete legacy callback route.
- [ ] Remove stale aliases.

---

# 24. Testing Strategy

The refactor is only complete when tests prove **functional identity**, not merely successful compilation.

## 24.1 Command parity

For every shared command:

```text
Userbot → core.Command X → Handler H
Assistant → core.Command X → Handler H
```

The test should verify that both surfaces resolve the same canonical command definition or equivalent canonical handler semantics.

## 24.2 Surface filtering

Test:

- Userbot-only command rejected on Assistant.
- Assistant-enabled command accepted.
- Inline-only command rejected by Assistant message dispatcher.
- Surface mask zero preserves documented backward compatibility.

## 24.3 Permission matrix

Test at least:

| Actor | Everyone | Sudo | Owner |
|---|---:|---:|---:|
| Normal user | allow | deny | deny |
| Sudo | allow | allow | deny |
| Owner | allow | allow | allow |
| Owner ID unconfigured | allow | fail-closed | fail-closed |

Exact semantics should follow the existing application policy.

## 24.4 Callback matrix

```text
valid
invalid payload
expired session
wrong actor
wrong peer
missing peer
cache hit
cache miss
entity fetch success
entity fetch failure
already answered
concurrent duplicate callback
shutdown during callback
```

## 24.5 Integration flow

The full Assistant flow should include:

```text
/start
  → Settings
  → Back
  → Help
  → Ping
  → Status
  → Close
  → old button
  → unauthorized actor
  → stale callback
  → duplicate callback
  → concurrent callback
  → inline callback
```

## 24.6 Lifecycle tests

Verify:

- startup is idempotent;
- shutdown is idempotent;
- update handlers stop accepting work during shutdown;
- in-flight callbacks obey existing shutdown policy;
- plugin hooks are detached before plugin teardown;
- command registry remains valid until consumers stop using it.

---

# 25. Anti-Regression Rules

The following rules are mandatory during implementation.

### Rule 1 — No parallel command source

Do not introduce another registry to make migration easier.

### Rule 2 — No duplicate feature implementation

If a shared command already exists, adapt it rather than creating `assistant/<feature>.go`.

### Rule 3 — No speculative generic abstraction

Do not create a generic `Action`, `Capability`, `ExecutionSurface`, or `DispatcherBus` unless current code proves it is necessary.

### Rule 4 — Transport may adapt, not redefine

Assistant can transform:

```text
Telegram Bot update → canonical execution input
```

but cannot redefine what the command means.

### Rule 5 — Preserve good transport boundaries

The de-architecture must not collapse:

- peer resolver;
- interaction layer;
- callback lifecycle;
- menu session state;
- Assistant lifecycle.

These are legitimate Assistant transport concerns.

### Rule 6 — Delete after proof

A duplicate component is removed only after:

1. all callers migrate;
2. tests cover the canonical path;
3. repository-wide search confirms no required references remain;
4. compile/test passes.

### Rule 7 — No compatibility forever

Temporary compatibility aliases are acceptable during migration but must have an explicit deletion task.

---

# 26. Before / After

## Before

```text
                         Plugin
                           │
                 ┌─────────┴─────────┐
                 ▼                   ▼
            core.Router       UnifiedRegistry
                 │                   │
                 │             UnifiedAdapter
                 │                   │
                 └───────┬───────────┘
                         ▼
                Assistant Router
                  │           │
                  ▼           ▼
              local cmd    plugin cmd
```

Problems:

- two command registries;
- two command dispatch concepts;
- adapter indirection;
- duplicate command implementations;
- unclear ownership.

## After

```text
                         Plugin
                           │
                           ▼
                     core.Command
                           │
                     core.Router
                           │
              ┌────────────┴────────────┐
              ▼                         ▼
       Userbot dispatcher       Assistant dispatcher
              │                         │
              └────────────┬────────────┘
                           ▼
                      same Handler
                           │
                           ▼
                  shared functionality

Assistant-only:

Assistant dispatcher → /start → Menu → UI state

Transport-only:

Assistant → callback / peer / interaction / Bot API
Inline → inline-specific transport
```

The after architecture is intentionally less clever. That is the point.

---

# 27. Complexity Reduction Targets

The refactor should produce measurable structural simplification.

Target reductions:

- one command registry instead of two;
- one shared command definition instead of Assistant/Userbot duplicates;
- one execution-source model instead of overlapping models;
- one canonical execution context;
- no unified command adapter;
- fewer Assistant command files;
- fewer plugin registration calls;
- fewer synchronization points;
- fewer lifecycle owners.

Do not optimize for a specific line count. Optimize for **fewer semantic owners**.

A 20-line abstraction that removes a real duplicated concept is good. A 100-line abstraction that merely forwards between registries is not.

---

# 28. Definition of Done

## Functional

- [ ] Assistant and Userbot execute the same shared command implementation.
- [ ] `/ping`, `/alive`, `/status` no longer have duplicate implementations.
- [ ] `/help` derives from canonical command metadata.
- [ ] Settings mutations use shared functionality.
- [ ] Surface availability is controlled by command metadata.

## Assistant transport

- [ ] Peer resolution is correct and refreshable.
- [ ] Message target uses its peer.
- [ ] Menu sessions are enforced.
- [ ] Expired callbacks cannot mutate screens.
- [ ] Inline callbacks are separate and complete.
- [ ] Rate limits cover commands, message callbacks, and inline.
- [ ] Lifecycle remains deterministic.

## Architecture

- [ ] `core.Router` is the only command registry.
- [ ] `core.Command` is the only canonical command definition.
- [ ] No `UnifiedCommandAdapter`.
- [ ] No `application/command.UnifiedRegistry`.
- [ ] No duplicate execution-source model.
- [ ] No unnecessary capability layer.
- [ ] Assistant command package is no longer a second application layer.

## Observability

- [ ] Correlation IDs are always non-zero.
- [ ] Command/callback/inline metrics are emitted.
- [ ] Peer resolution metrics are emitted.
- [ ] Edit/delete metrics are emitted.
- [ ] Duplicate callback answer has reachable semantics.

## Testing

- [ ] Unit tests pass.
- [ ] Integration tests pass.
- [ ] Command parity matrix passes.
- [ ] Callback matrix passes.
- [ ] Inline matrix passes.
- [ ] Menu stale-session matrix passes.
- [ ] Shutdown/race tests pass.
- [ ] Repository-wide search shows no unintended legacy architecture.

---

# 29. Recommended Commit Sequence

Use small commits so each migration step is reviewable and reversible.

```text
1. fix(assistant): make message target peer-aware
2. fix(assistant): refresh peer entities on cache miss
3. fix(assistant): enforce menu instance sessions
4. feat(assistant): separate inline callback target and lifecycle
5. refactor(assistant): dispatch shared commands through core router
6. refactor(plugin): remove duplicate unified command registration
7. refactor(assistant): migrate ping/alive/status/help to canonical commands
8. refactor(execution): consolidate source and execution context
9. refactor(assistant): remove unified command adapter and registry
10. refactor(assistant): remove legacy callback path
11. test(assistant): add parity and lifecycle matrix
12. docs: finalize Assistant v2.5 architecture audit
```

Avoid mixing large deletion commits with behavioral changes when possible.

---

# 30. Review Gates

Every phase should answer the following before proceeding.

### Gate A — Is there still one command source?

If no, stop and simplify before continuing.

### Gate B — Can Assistant invoke a plugin command without an Assistant-specific duplicate?

If no, the migration is incomplete.

### Gate C — Can the same command execute on both surfaces with the same business semantics?

If no, identify whether the difference is legitimate transport behavior or accidental duplication.

### Gate D — Does callback handling remain transport-only?

If callback code starts implementing business rules, stop.

### Gate E — Are menu sessions enforced before mutation?

If no, callback migration is not complete.

### Gate F — Are peer misses recoverable?

If no, interaction correctness is not complete.

### Gate G — Are metrics emitted by the actual execution path?

If no, observability is incomplete.

### Gate H — Did complexity go down?

If a phase adds more registries, adapters, contexts, or interfaces than it removes, re-evaluate before proceeding.

---

# 31. Final Architectural Contract

The final Assistant architecture should be explainable in a few sentences:

> GoUltroid has one canonical command model and one canonical command registry. Plugins register commands once into `core.Router`. Userbot and Assistant are separate Telegram execution surfaces that parse their own transport events and then invoke the same canonical command definitions. Assistant-specific code owns Bot API interaction, callbacks, peer resolution, menu/session state, and presentation. Assistant-only UX such as `/start` remains local. Shared functionality is never duplicated merely because it is reachable from another Telegram surface.

If the architecture cannot be explained this way after the migration, the de-architecture is not finished.

---

# 32. Expected End State

```text
                                  GoUltroid
                                     │
                                  Plugins
                                     │
                                     ▼
                              ┌─────────────┐
                              │ core.Command│
                              └──────┬──────┘
                                     │
                              ┌──────▼──────┐
                              │ core.Router  │
                              └──────┬──────┘
                                     │
                    ┌────────────────┴────────────────┐
                    │                                 │
                    ▼                                 ▼
             Userbot surface                   Assistant surface
                    │                                 │
              parse/update                      parse/update
                    │                                 │
                    └──────────────┬──────────────────┘
                                   ▼
                              same handler
                                   │
                                   ▼
                         shared functionality

                 Assistant transport-only subsystems

        ┌──────────┬───────────┬───────────┬───────────┐
        │          │           │           │           │
        ▼          ▼           ▼           ▼           ▼
      Client   Interaction   Callback     Peer       Menu/UI
        │          │           │           │           │
        └──────────┴───────────┴───────────┴───────────┘
                                   │
                                   ▼
                              Bot API

Inline remains a separate Telegram transport where its update and
identifier semantics differ, while reusing shared functionality only
at the point where the semantics are genuinely identical.
```

**The goal is not to make Assistant smaller by deleting useful boundaries. The goal is to remove semantic duplication and make ownership obvious.**

That is the definition of the Assistant v2.5 de-architecture.