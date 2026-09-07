# BUG15.4 — GoUltroid Architecture Complexity & Risk Analysis

**Repository:** `inipew/goultroid`  
**Baseline:** current `main` after Assistant v2.5 de-architecture planning  
**Status:** ANALYSIS / READ-ONLY  
**Scope:** whole Assistant/Userbot execution architecture, peer/message identity, callbacks, menus, concurrency, authorization, persistence, lifecycle, EventBus, scheduler, plugin lifecycle, observability, and parity.

> **Purpose:** identify the remaining sources of complexity and failure risk beyond the already documented Assistant v2.5 de-architecture problem.

---

## 1. Executive Summary

The largest remaining risk is no longer simply “Assistant has too many command abstractions”. The deeper issue is **boundary ownership**: several concerns can still be interpreted or implemented by more than one subsystem.

The architectural target should therefore be evaluated with one rule:

> **Every important semantic concern has exactly one owner.**

The current highest-risk boundaries are:

| Priority | Area | Risk | Main consequence |
|---|---|---|---|
| P0 | Peer / Entity Resolution | Very High | edit/delete/reply/download/admin/menu operations fail or target the wrong entity |
| P0 | Message Identity / Target | Very High | message operations become ambiguous across chats/channels |
| P0 | Callback lifecycle | Very High | stale, duplicate, unauthorized, or concurrent clicks execute incorrectly |
| P0 | Concurrency / ordering | Very High | race conditions, double execution, inconsistent menus |
| P0 | Lifecycle / shutdown | Very High | goroutines continue after dependencies are closed |
| P1 | Execution context | High | duplicated context semantics and inconsistent authorization/source |
| P1 | Authorization | High | security boundary differs between surfaces |
| P1 | Error taxonomy / retry | High | wrong retries, poor UX, hidden failures |
| P1 | Persistence vs runtime state | High | stale state survives incorrectly or required state disappears |
| P1 | Plugin/EventBus semantics | High | partial registration and shutdown races |
| P1 | Settings/config ownership | High | conflicting sources of truth |
| P1 | Idempotency | High | Telegram redelivery causes repeated side effects |
| P2 | Help/discovery | Medium | command parity and visibility drift |
| P2 | Alias collisions | Medium | command identity ambiguity |
| P2 | Metrics/correlation | Medium | failures become difficult to trace |

The recommended sequence is:

```text
De-architecture
      ↓
Peer + Message identity
      ↓
Callback + Menu lifecycle
      ↓
Concurrency + Idempotency
      ↓
Authorization + Error/Retry policy
      ↓
Plugin + EventBus + Lifecycle
      ↓
Settings/Persistence ownership
      ↓
Parity validation
```

---

# 2. Architectural Invariant

The final system should satisfy:

```text
                 ONE FUNCTIONAL SEMANTICS
                         │
             ┌───────────┼───────────┐
             ▼           ▼           ▼
          Userbot     Assistant     Inline
          transport   transport    transport
             │           │           │
             └───────────┼───────────┘
                         ▼
                  shared command /
                  service semantics
```

Transport differences are allowed. Semantic duplication is not.

A second invariant applies to infrastructure:

```text
one owner per concern
one source of truth per state
one lifecycle owner per resource
one authorization policy
one error taxonomy
one retry policy
```

---

# 3. P0 — Peer / Entity Resolution

## 3.1 Why this is the most dangerous boundary

Telegram operations frequently require more than a numeric ID. Depending on the entity, the operation can require a complete `InputPeer`, an `access_hash`, channel-specific identity, or an entity fetched from Telegram.

A cached numeric ID is therefore not equivalent to a usable Telegram peer.

The failure pattern is particularly dangerous because it can be silent:

```text
message has chat ID
        ↓
resolver finds no complete peer
        ↓
operation cannot construct correct InputPeer
        ↓
retry uses the same incomplete cache
        ↓
operation fails again
```

## 3.2 Required resolution contract

The resolver must implement:

```text
Resolve(identifier)
   │
   ├── cache hit + valid entity → return
   │
   ├── cache miss/stale
   │          ↓
   │     Telegram entity fetch
   │          ↓
   │     normalize + cache
   │          ↓
   │     return
   │
   └── fetch failed → typed error
```

After invalidation, retrying the cache alone is not a real retry.

## 3.3 Required semantics

The resolver must distinguish:

- user;
- bot;
- basic group;
- supergroup/channel;
- input peer;
- full entity;
- access hash unavailable;
- entity deleted/not found;
- cache stale;
- Telegram API failure.

## 3.4 Operations depending on this boundary

Audit all of:

- get message;
- reply;
- edit;
- delete;
- pin/unpin;
- forward;
- media download;
- ban/unban;
- admin operations;
- callback target resolution;
- menu target messages.

One resolver defect can therefore create many apparently unrelated bugs.

---

# 4. P0 — Message Identity and `MessageTarget`

## 4.1 Message ID is not globally unique

A Telegram message must be identified with its peer context.

Conceptually:

```text
MessageIdentity = (Peer, MessageID)
```

not:

```text
MessageIdentity = MessageID
```

A `MessageTarget` carrying `ChatID`, `ChatInstance`, or other fields must have explicit semantics. Exported mutable fields should not imply that any combination is valid.

## 4.2 `GetMessage` risk

A generic `GetMessage(id)` implementation that falls back to `MessagesGetMessages` without the target peer is semantically unsafe for non-user/channel cases.

The target resolution algorithm must be:

```text
MessageTarget
    ↓
resolve peer
    ↓
choose Telegram API according to peer/message type
    ↓
fetch message
    ↓
verify identity
```

## 4.3 Scheduled/album/inline cases

The target model must explicitly define whether it supports:

- ordinary messages;
- channel messages;
- scheduled messages;
- album/grouped media;
- inline-originated results;
- callback-originated messages;
- deleted messages.

Unsupported cases must fail explicitly rather than being interpreted as ordinary messages.

---

# 5. P0 — Callback Lifecycle

Callbacks are state machines, not simple function calls.

Recommended lifecycle:

```text
Created
  ↓
Rendered
  ↓
Clicked
  ↓
Decoded
  ↓
Validated
  ↓
Authorized
  ↓
Executed
  ↓
Answered
  ↓
Observed
```

Failure states include:

```text
Invalid
Unauthorized
Stale
Expired
Duplicate
TargetMissing
ExecutionFailed
AnswerFailed
Cancelled
```

## 5.1 Required rules

1. Validate callback payload before side effects.
2. Validate menu/session ownership before execution.
3. Validate authorization before execution.
4. Detect stale menu instances.
5. Make duplicate clicks harmless.
6. Answer the Telegram callback exactly once where applicable.
7. Record correlation information for every callback.
8. Do not execute a callback after shutdown has begun.

## 5.2 `ErrCallbackAlreadyAnswered`

If the code defines an `AlreadyAnswered` condition but no real path returns it, the lifecycle model is incomplete. Either implement the state transition or remove the dead abstraction.

---

# 6. P0 — Menu Session Consistency

`MenuInstance` should represent a real session boundary.

Minimum conceptual state:

```text
ID
Owner
RootMessage
CurrentScreen
CreatedAt
ExpiresAt
State
```

Recommended state transitions:

```text
Created → Active → Expired
             ↓
          Invalidated
```

A callback must carry an opaque instance identifier and resolve the current session from that identifier.

The system must reject:

- callback from another user;
- callback from an expired menu;
- callback from an invalidated menu;
- callback for a deleted root message;
- callback against an already replaced screen.

The UI may display “expired” or silently ignore stale buttons, but the internal semantics must be deterministic.

---

# 7. P0 — Concurrency and Ordering

Assistant interactions are naturally concurrent. Telegram can deliver multiple updates quickly, and Go handlers run concurrently unless explicitly serialized.

Potential races include:

```text
Settings click A ─┐
Settings click B ─┼→ both edit same message
Back click      ──┘

Close + callback

Shutdown + incoming update

Command + callback changing same state
```

## 7.1 Required decisions

The implementation must explicitly decide:

- whether callbacks for one menu are serialized;
- whether state mutation is per-user serialized;
- whether edits to the same Telegram message are serialized;
- whether shared command handlers may run concurrently;
- whether destructive commands require idempotency keys;
- what happens to in-flight operations during shutdown.

Do not add global locking as a shortcut. Prefer the smallest serialization boundary that matches the state being protected.

---

# 8. P1 — Execution Context

The system risks accumulating:

```text
core.Context
execution.Context
assistant.Context
callback.Context
interaction.Context
```

The problem is not the number of structs itself. The problem is duplicated semantic fields and unclear ownership.

The canonical execution context should contain only information needed by shared functionality:

```text
Actor
Source
Peer
Message / target
Command
Arguments
Authorization result/policy access
Correlation ID
Cancellation
```

Transport-specific Bot API objects should remain transport-owned where possible.

---

# 9. P1 — Authorization

Authorization must be a policy, not a collection of surface-specific checks.

Potential dimensions:

```text
Actor identity
Owner
Sudo
Chat membership
Admin
Creator
Private/group/channel
Reply-only
Command permission
```

The policy should be evaluated once before a shared handler executes.

## 9.1 Fail-closed rule

If owner configuration is required for a protected Assistant surface, missing/invalid owner configuration must not silently become “everyone allowed”.

A special `ownerID == 0 → allow` path is therefore a security risk unless the surrounding configuration contract explicitly guarantees that zero means unrestricted mode.

---

# 10. P1 — Error Taxonomy

Transport code should not infer semantics from arbitrary error strings.

Recommended internal classes:

```text
InvalidInput
Unauthorized
Forbidden
NotFound
StaleInteraction
PeerResolutionFailed
TelegramAPIError
AlreadyAnswered
Timeout
Cancelled
Duplicate
Internal
```

The transport maps these to:

```text
user response
log level
metric
retry/no-retry
```

This keeps business logic independent of Telegram-specific presentation.

---

# 11. P1 — Telegram Retry Policy

Retryability must be classified.

### Retryable candidates

- transient network failure;
- connection reset;
- temporary Telegram server failure;
- selected timeout classes;
- flood wait, using Telegram-provided delay.

### Non-retryable candidates

- invalid peer;
- invalid message ID;
- permission denied;
- entity not found;
- malformed request;
- unauthorized account/bot;
- permanently stale callback.

Never retry a semantic error as if it were a network error.

---

# 12. P0 — Lifecycle and Shutdown

The desired dependency order is:

```text
App
 ↓
Assistant/Userbot clients
 ↓
Dispatchers
 ↓
Scheduler/EventBus
 ↓
Plugin Manager
 ↓
Shared services
 ↓
DB / persistence
```

Shutdown must prevent new work from entering a subsystem before its dependencies are closed.

For every goroutine, identify:

```text
who starts it?
who cancels it?
who waits for it?
what dependencies may it access?
what happens if shutdown begins while it is blocked?
```

A context cancellation without a wait is not deterministic shutdown.

---

# 13. P1 — EventBus and Hook Semantics

The EventBus must define:

- ordering;
- synchronous vs asynchronous delivery;
- handler isolation;
- error propagation;
- cancellation;
- unregister semantics;
- waiting for in-flight callbacks;
- behavior during plugin shutdown.

Recommended invariant:

```text
unregister
   ↓
stop new delivery
   ↓
wait for in-flight handlers
   ↓
shutdown plugin resources
```

Do not shut down plugin state while an EventBus callback can still access it.

---

# 14. P1 — Plugin Lifecycle

The existing transactional registration behavior is valuable and should be preserved.

Required properties:

```text
validate
  ↓
initialize
  ↓
register commands atomically
  ↓
register hooks
  ↓
publish metadata
```

If a later phase fails:

```text
cleanup everything already registered
```

No partially visible plugin should remain.

Shutdown should remain reverse-order and detach hooks before plugin teardown.

---

# 15. P1 — Persistence vs Runtime State

Separate persistent state from ephemeral runtime state.

### Persistent

- owner/sudo configuration;
- plugin configuration;
- user settings intended to survive restart;
- durable feature state.

### Runtime

- peer cache;
- menu instances;
- callback state;
- rate-limit buckets;
- in-flight operations;
- correlation state;
- transport connections.

A restart should invalidate runtime state unless there is a deliberate durable-session requirement.

---

# 16. P1 — Settings and Configuration Ownership

Define one source of truth for each setting.

Potential sources currently include:

```text
config file
DB
environment
runtime memory
Telegram settings UI
Assistant settings UI
plugin-local settings
```

For every setting document:

```text
canonical storage
load path
mutation path
cache/runtime representation
validation
persistence timing
restart behavior
```

The Assistant UI must call the same settings service used elsewhere. It must not create a second settings model merely to render menus.

---

# 17. P1 — Idempotency

Telegram delivery should be treated as potentially duplicated.

Separate these concepts:

```text
received
validated
processed
side effect executed
response sent
```

A response failure does not necessarily mean the side effect failed.

For destructive or externally visible operations, use an idempotency boundary where practical:

```text
operation identity
     ↓
check previous execution
     ↓
execute once
     ↓
record result
```

Do not make every command artificially idempotent. Apply this to operations where duplicate execution is harmful.

---

# 18. P2 — Command Aliases and Identity

`core.Router` already provides important collision validation. Continue to enforce:

- command name uniqueness;
- alias uniqueness;
- canonical identity;
- normalization rules;
- deterministic case behavior;
- surface filtering without changing command identity.

An alias must never create a second functional command.

---

# 19. P2 — Help and Discovery

Help should derive from the canonical router:

```text
Router.All()
  ↓
SurfaceAssistant filter
  ↓
permission visibility
  ↓
presentation
```

No static Assistant command list should become authoritative.

---

# 20. Observability

Correlation IDs must exist for meaningful execution paths.

Recommended chain:

```text
update
  ↓
correlation ID
  ↓
command/callback
  ↓
peer resolution
  ↓
handler
  ↓
Telegram API
```

Metrics should distinguish:

- received;
- rejected;
- unauthorized;
- stale;
- peer resolution failure;
- handler success/failure;
- Telegram API failure;
- retry;
- duplicate.

A correlation ID of zero or absent should be treated as missing instrumentation, not a valid execution identity.

---

# 21. Inline Surface

Inline is not automatically the same as Assistant callbacks.

The existing inline engine should be audited independently for:

- update registration;
- callback registration;
- rate limiting;
- authorization;
- correlation;
- command/action reuse;
- lifecycle shutdown.

The important rule is:

> Share semantics only where Telegram semantics are genuinely shared.

Do not create a generic “universal interaction framework” solely to make inline look architecturally symmetrical.

---

# 22. Boundary Ownership Matrix

The following ownership should become explicit:

| Concern | Canonical owner |
|---|---|
| command definition | `core.Command` |
| command lookup | `core.Router` |
| plugin lifecycle | plugin manager |
| command surface availability | `core.Command.Surfaces` |
| execution source | `execution.Source` |
| shared functionality | plugin/application service |
| Telegram Bot transport | Assistant client |
| send/edit/delete | Assistant interaction |
| callback transport lifecycle | Assistant callback layer |
| menu UI/session | Assistant menu |
| peer resolution | Assistant peer resolver / shared resolver boundary |
| message identity | MessageTarget / canonical message identity |
| authorization semantics | one policy layer |
| settings semantics | shared settings owner |
| persistence | DB/storage owner |
| retry classification | one transport/API policy |
| lifecycle cancellation | owning component |
| metrics | observability layer |

If a concern appears under two owners, investigate it before adding more abstractions.

---

# 23. Complexity Smells

Treat these as architectural alarms:

- `Adapter` whose only job is to translate one internal registry to another;
- `Registry` that duplicates an existing registry;
- `Context` that only wraps another context;
- `Capability` that merely lists commands;
- `AssistantX` implementation whose only difference is output formatting;
- callback handler containing business logic;
- menu package containing domain mutation rules;
- transport code deciding plugin semantics;
- two independent authorization implementations;
- cache retry without a source-of-truth fetch;
- shutdown implemented only as `cancel()` without waiting;
- persistent state held only in runtime memory;
- runtime state accidentally persisted;
- generic abstraction introduced for a future feature that does not exist.

---

# 24. Recommended Audit Sequence

## Phase A — Ownership inventory

Search every major concern and assign one owner.

## Phase B — Peer/message correctness

Fix entity fetching, `InputPeer`, message target identity, and retry semantics.

## Phase C — Callback/menu state machine

Make stale/duplicate/unauthorized interactions deterministic.

## Phase D — Concurrency/idempotency

Define serialization boundaries and duplicate delivery behavior.

## Phase E — Authorization/error/retry

Unify policies.

## Phase F — Lifecycle/EventBus/plugin

Prove shutdown and registration semantics under races.

## Phase G — Persistence/config

Remove competing sources of truth.

## Phase H — Parity

Compare behavior against the intended Ultroid feature surface only after infrastructure boundaries are stable.

---

# 25. Production-Grade Exit Criteria

The architecture should not be considered production-grade until all of the following are true:

### Architecture

- one canonical command registry;
- one functional implementation per shared feature;
- no redundant Assistant command registry;
- no unnecessary adapters;
- one canonical execution source/context model.

### Telegram correctness

- peer resolution can recover from cache miss/stale state;
- message identity always includes peer context;
- channel/user/group operations use correct peer semantics;
- callback lifecycle is deterministic.

### Security

- owner configuration fails closed where required;
- authorization is consistent across surfaces;
- stale/foreign callbacks cannot execute privileged actions.

### Concurrency

- menu interactions have defined ordering;
- duplicate delivery is safe;
- shutdown waits for in-flight work;
- shared state has explicit synchronization boundaries.

### Reliability

- Telegram retry classification is explicit;
- errors have typed semantics;
- correlation and metrics identify failures.

### Lifecycle

- plugins cannot receive new events after shutdown starts;
- hooks are detached before plugin teardown;
- DB is not closed while consumers are still active.

### Parity

- Assistant and Userbot execute the same shared functionality;
- help derives from the same command source;
- Assistant-specific UI remains only where genuinely transport-specific.

---

# 26. Final Conclusion

The project is not suffering from a lack of abstractions. It is suffering from **too many possible owners for the same semantic concern**.

The next engineering goal should therefore not be “make the architecture more generic”. It should be:

> **Reduce semantic ownership until every important behavior has one obvious source of truth.**

The most important remaining work is not cosmetic. Peer resolution, message identity, callback lifecycle, concurrency, authorization, and shutdown are load-bearing boundaries. If these are correct, the simplified Assistant architecture can become predictable and maintainable. If they are not correct, further feature parity work will multiply hidden bugs.
