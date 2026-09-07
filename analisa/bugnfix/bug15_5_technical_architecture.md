# BUG15.5 — GoUltroid Boundary Hardening Technical Specification

**Repository:** `inipew/goultroid`  
**Baseline:** `main` after BUG15.3 de-architecture planning  
**Status:** TECHNICAL SPECIFICATION / IMPLEMENTATION PLAN  
**Primary invariant:** **one functionality, one implementation, multiple execution surfaces**

---

# 1. Objective

This document converts the architecture analysis into an implementation-grade specification for the next hardening cycle.

The goal is not to introduce another framework. The goal is to make the existing GoUltroid architecture simpler while making Telegram identity, interaction lifecycle, authorization, concurrency, persistence, and shutdown deterministic.

The implementation must preserve working behavior and migrate incrementally.

---

# 2. Target Architecture

```text
                              GoUltroid
                                  │
                               Plugins
                                  │
                                  ▼
                           core.Command
                                  │
                           core.Router
                                  │
                   ┌──────────────┴──────────────┐
                   │                             │
                   ▼                             ▼
            Userbot dispatcher             Assistant dispatcher
                   │                             │
                   │                             ├── /start / UI
                   │                             │
                   └──────────────┬──────────────┘
                                  ▼
                         canonical execution
                                  │
                         shared functionality
                                  │
                       application/plugin service
```

Assistant remains a transport surface. It owns Telegram Bot API mechanics, presentation, callback transport, and menu session state. It does not own duplicate feature implementations.

---

# 3. Non-Negotiable Invariants

## N1 — Single command authority

`core.Router` is the only authoritative command registry.

## N2 — Single shared implementation

A shared feature has one functional implementation. Assistant wrappers are permitted only when they adapt transport or presentation.

## N3 — Peer-aware message identity

A message is identified by peer + message ID.

## N4 — Cache miss is not resolution failure

A peer cache miss must be able to trigger an authoritative Telegram fetch when the operation permits it.

## N5 — Authorization before side effect

No privileged handler executes before authorization succeeds.

## N6 — Stale interaction cannot mutate state

Expired/invalidated menu callbacks must be rejected before business mutation.

## N7 — Duplicate delivery is safe

Repeated Telegram updates must not accidentally repeat harmful side effects.

## N8 — Shutdown is a barrier

After shutdown begins, new work is rejected and in-flight work is drained before dependencies are destroyed.

## N9 — Persistent and runtime state are distinct

Ephemeral UI/cache/transport state is not treated as durable configuration.

## N10 — One owner per semantic concern

If two packages both appear to own the same policy, the implementation is incomplete.

---

# 4. Phase 0 — Baseline and Safety Gate

Before code changes:

1. Record current HEAD.
2. Run full unit tests.
3. Run `go test ./...`.
4. Run static analysis available in the repository.
5. Record existing failures separately from introduced failures.
6. Search all usages of the duplicate Assistant command architecture.

Required search targets:

```text
UnifiedRegistry
UnifiedCommandAdapter
assistant/command.Router
capabilityRegistry
ExecutionSource
SurfaceMask
Context
MessageTarget
GetMessage
MenuInstance
callbackStore
ownerID
ErrCallbackAlreadyAnswered
metrics
OnInlineBotCallbackQuery
```

Do not delete a symbol until all references are classified.

---

# 5. Phase 1 — Canonical Command Path

## 5.1 `core.Router`

Keep:

```text
RegisterBatch
Find
All
Parse
alias handling
atomic collision validation
```

Do not introduce another registry.

## 5.2 Assistant dispatch

Replace the conceptual flow:

```text
Assistant Router
 → local handler
 → UnifiedRegistry
 → Adapter
```

with:

```text
Assistant update
 → parse
 → core.Router.Find
 → IsAvailableOn(SourceAssistant)
 → authorization/context checks
 → Command.Handler
```

## 5.3 Assistant-only commands

Keep `/start` as an explicit Assistant controller.

Do not place `/start` into a generalized local-command map merely to make the architecture appear uniform.

## 5.4 Help

Implementation:

```go
for _, cmd := range router.All() {
    if !cmd.IsAvailableOn(execution.SourceAssistant) {
        continue
    }
    // apply visibility policy
}
```

Rendering belongs to Assistant presentation.

---

# 6. Phase 2 — Delete Duplicate Command Infrastructure

## 6.1 `application/command.UnifiedRegistry`

Migration steps:

1. enumerate consumers;
2. migrate lookups to `core.Router`;
3. migrate surface filtering to `Command.IsAvailableOn`;
4. migrate help/discovery;
5. remove plugin manager registration;
6. delete registry package;
7. compile/test/search again.

## 6.2 `UnifiedCommandAdapter`

Delete after no direct registry consumer remains.

Acceptance condition:

```text
zero production references
zero test references except migration tests that are then deleted
```

## 6.3 Assistant local handler registry

Remove generic local command registration.

Only explicitly Assistant-specific controllers may remain.

---

# 7. Phase 3 — Execution Context Consolidation

## 7.1 Canonical model

Use one execution source:

```go
execution.SourceUserbot
execution.SourceAssistant
execution.SourceInline
```

and one surface representation.

## 7.2 Context requirements

The shared execution context should expose, directly or through stable interfaces:

```text
Actor
Source
Peer
MessageTarget
Command
Arguments
Authorization
CorrelationID
Context/cancellation
```

Transport-only fields such as Bot API callback objects should remain outside the shared business context unless a real shared handler requires them.

## 7.3 Migration rule

Do not rewrite every handler at once.

Use this order:

```text
identify duplicate field
 → choose canonical field
 → migrate consumers
 → delete duplicate field/type
 → compile/test
```

---

# 8. Phase 4 — Peer Resolver Hardening

## 8.1 Required API contract

Conceptual interface:

```go
Resolve(ctx context.Context, ref PeerRef) (Peer, error)
```

The exact repository types should be preserved where possible.

## 8.2 Resolution algorithm

```text
Resolve(ref)
   │
   ├─ valid cache hit → return
   │
   ├─ cache miss/stale
   │       ↓
   │   fetch from Telegram
   │       ↓
   │   normalize entity
   │       ↓
   │   update cache
   │       ↓
   │   return
   │
   └─ permanent failure → typed error
```

## 8.3 Retry constraint

Retry at most once at the semantic fetch layer unless the Telegram retry policy explicitly permits more.

Do not implement:

```text
invalidate → resolve cache → fail
```

because that is not a real recovery path.

## 8.4 Cache invariants

Cache entries must have enough information to construct the required `InputPeer`.

A cache record that contains only an ID must not be treated as a complete peer when an access hash is required.

---

# 9. Phase 5 — Message Target Contract

## 9.1 Canonical identity

Conceptually:

```go
type MessageTarget struct {
    Peer PeerRef
    ID   int
}
```

The actual repository representation may differ, but the semantics must be equivalent.

## 9.2 Rules

- no operation may silently interpret a message ID without peer context;
- channel messages use channel-aware APIs;
- user/group messages use appropriate message APIs;
- scheduled messages are explicit if supported;
- unsupported targets return typed errors.

## 9.3 `GetMessage`

Required flow:

```text
MessageTarget
  ↓
Resolve peer
  ↓
select API by peer/message class
  ↓
fetch
  ↓
verify returned peer + message ID
  ↓
return message
```

Do not blindly call a generic message fetch with only the numeric ID.

---

# 10. Phase 6 — Callback State Machine

## 10.1 State model

```text
CREATED
  ↓
RENDERED
  ↓
RECEIVED
  ↓
VALIDATED
  ↓
AUTHORIZED
  ↓
EXECUTING
  ↓
EXECUTED
  ↓
ANSWERED
  ↓
OBSERVED
```

Terminal failure states:

```text
INVALID
UNAUTHORIZED
STALE
DUPLICATE
TARGET_NOT_FOUND
EXECUTION_FAILED
ANSWER_FAILED
CANCELLED
```

## 10.2 Processing order

The callback pipeline must be:

```text
decode
 ↓
validate syntax
 ↓
resolve menu instance
 ↓
validate ownership
 ↓
validate expiration/state
 ↓
resolve target
 ↓
authorize
 ↓
execute
 ↓
answer callback
 ↓
render/update UI
 ↓
metrics
```

Never move business execution before validation/authorization.

## 10.3 Duplicate callback

Use a stable callback identity where practical:

```text
MenuInstanceID + callback/action identity
```

If the action is already completed, return a deterministic duplicate result rather than executing again.

---

# 11. Phase 7 — Menu Session Enforcement

## 11.1 Required `MenuInstance`

Conceptual fields:

```text
ID
OwnerID
RootMessage
CurrentScreen
CreatedAt
ExpiresAt
State
```

## 11.2 Registration

Creating a menu must register it in the active session store.

Rendering alone is not registration.

## 11.3 Callback enforcement

Every callback must resolve its instance and check:

```text
exists?
owner matches?
expired?
invalidated?
current state accepts action?
```

Failure must occur before the action executes.

## 11.4 Expiration

Expiration should be monotonic/time-safe where practical.

The callback path must not depend on a periodic cleanup goroutine for correctness. Cleanup is optimization; expiration validation is correctness.

---

# 12. Phase 8 — Authorization Policy

## 12.1 Canonical policy inputs

```text
actor ID
owner/sudo state
chat membership
admin/creator state
private/group/channel
command permission
reply-only constraints
```

## 12.2 Fail closed

If a protected mode requires configured owner identity and it is unavailable, reject privileged execution.

Do not use zero-value IDs as an accidental authorization bypass.

## 12.3 Surface consistency

Userbot and Assistant may acquire actor/chat information differently, but the semantic policy must be the same.

---

# 13. Phase 9 — Concurrency Model

## 13.1 Required serialization boundaries

Start with the smallest safe scope:

```text
per menu instance
```

for UI transitions.

For mutable persistent settings, serialize according to the settings store's transaction/concurrency guarantees.

For independent read-only commands, allow concurrent execution.

## 13.2 Message edit ordering

Two transitions targeting the same root message must not produce out-of-order screen states.

Recommended:

```text
MenuInstance lock
   ↓
validate current state
   ↓
apply state transition
   ↓
edit Telegram message
   ↓
commit observable state
```

The exact transaction order should account for Telegram API failure. Do not mark a UI transition permanently successful if the external edit failed unless the state model deliberately records “desired state” vs “rendered state”.

## 13.3 Shutdown race

New updates after shutdown begins should be rejected before entering mutable application state.

---

# 14. Phase 10 — Idempotency

## 14.1 Classification

Classify operations as:

```text
pure/read-only
state mutation
external side effect
irreversible/destructive
```

Idempotency is mandatory for operations where duplicate delivery can cause harmful repeated effects.

## 14.2 Execution record

Where durable idempotency is needed:

```text
operation key
status
started/finished time
result/error
```

Avoid a global idempotency system unless actual requirements justify it. Per-feature idempotency is often simpler.

---

# 15. Phase 11 — Error and Retry Policy

## 15.1 Internal error classes

Use typed/sentinel errors where appropriate:

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

## 15.2 Retry matrix

| Error | Retry | Notes |
|---|---|---|
| network temporary | yes | bounded backoff |
| timeout | usually | bounded |
| flood wait | yes | honor server delay |
| Telegram server temporary error | yes | bounded |
| invalid peer | no | resolve/fix identity |
| invalid message | no | semantic failure |
| permission denied | no | policy failure |
| stale callback | no | UI session expired |
| unauthorized | no | policy/config |
| malformed input | no | user error |
| cancelled | no | propagate cancellation |

Transport code should map these into user-visible responses and metrics.

---

# 16. Phase 12 — Lifecycle and Shutdown

## 16.1 Ownership rule

Every long-lived goroutine must have:

```text
owner
cancel source
wait mechanism
resource dependencies
shutdown behavior
```

## 16.2 Shutdown sequence

Recommended high-level sequence:

```text
1. mark application shutting down
2. stop accepting new updates
3. stop scheduler producers
4. stop EventBus new delivery
5. drain/cancel in-flight work according to policy
6. detach plugin hooks
7. shutdown plugins reverse-order
8. close Assistant/Userbot transport resources
9. close shared services
10. close DB/storage last
```

The exact order must follow the actual dependency graph. The critical invariant is that consumers stop before their dependencies are destroyed.

## 16.3 Idempotent shutdown

Calling shutdown twice must not panic, double-close resources, or execute restoration logic twice.

---

# 17. Phase 13 — EventBus / Hooks

## 17.1 Required contract

Document:

```text
registration
unregistration
ordering
parallelism
error propagation
cancellation
in-flight waiting
shutdown behavior
```

## 17.2 Safe plugin shutdown

Required order:

```text
stop new events
 ↓
unregister/detach hooks
 ↓
wait for in-flight hook execution
 ↓
plugin shutdown
```

If the current EventBus cannot guarantee this, add the smallest synchronization needed rather than another generic event framework.

---

# 18. Phase 14 — Settings and Persistence

Create an ownership table for every setting.

Example:

| Setting | Canonical owner | Persistent? | Runtime cache? |
|---|---|---:|---:|
| owner ID | config/settings | yes | optional |
| sudo users | config/settings | yes | optional |
| Assistant UI state | menu runtime | no | yes |
| peer cache | peer resolver | no | yes |
| plugin config | plugin/settings | yes | optional |
| callback instance | menu runtime | no | yes |

The Assistant must mutate shared settings through the canonical service rather than maintaining a second configuration model.

---

# 19. Phase 15 — Observability

## 19.1 Correlation

Every command/callback execution should have a non-zero correlation identity generated at the transport boundary if one is not supplied.

## 19.2 Metrics

At minimum track:

```text
assistant_updates_total
assistant_commands_total
assistant_command_success_total
assistant_command_error_total
assistant_callback_total
assistant_callback_stale_total
assistant_callback_duplicate_total
assistant_callback_unauthorized_total
peer_resolution_success_total
peer_resolution_failure_total
telegram_api_retry_total
telegram_api_error_total
```

Use repository naming conventions rather than blindly adding these exact metric names.

## 19.3 Logging

Logs should contain, where safe:

```text
correlation ID
source
command/action
actor identifier (redacted according to policy)
peer class/identifier where appropriate
error class
retry decision
```

Never log bot tokens or sensitive credentials.

---

# 20. Phase 16 — Inline Audit

Inline must be audited separately for:

- update registration;
- callback registration;
- source selection;
- rate-limit bucket;
- authorization;
- correlation ID;
- lifecycle cancellation.

If inline currently uses a shared service, preserve it. Do not create a second functionality implementation merely to fit Assistant architecture.

---

# 21. File-Level Migration Map

## Core

### `internal/core/command.go`

**Keep:** canonical command metadata and handler.  
**Verify:** surface semantics and permission metadata.  
**Do not:** add Assistant-specific routing logic.

### `internal/core/router.go`

**Keep:** canonical registry, aliases, atomic registration.  
**Do not:** add UI/menu state.

## Plugin

### `internal/plugin/manager.go`

**Keep:** lifecycle and transactional registration.  
**Remove:** duplicate command registry/capability coupling when proven unused.

## Assistant command

### `internal/assistant/command/router.go`

**Target:** thin transport dispatcher or deletion if update routing can live directly in Assistant client/dispatcher.

### `internal/assistant/command/ping.go`

**Target:** remove duplicate functionality; call canonical ping implementation.

### `internal/assistant/command/alive.go`

**Target:** remove duplicate functionality.

### `internal/assistant/command/status.go`

**Target:** remove duplicate functionality.

### `internal/assistant/command/help.go`

**Target:** render `core.Router.All()` filtered by Assistant surface.

### `internal/assistant/command/start.go`

**Keep:** Assistant-specific entry/dashboard behavior.

### `internal/assistant/command/commands.go`

**Target:** delete or merge into explicit start controller if still needed.

### Assistant adapters

**Target:** delete after direct canonical dispatch is verified.

## Application command

### `internal/application/command/registry.go`

**Target:** delete after all consumers migrate.

## Execution

### `internal/execution/source.go`

**Keep:** canonical source/surface model.

### duplicate execution/core models

**Target:** migrate and delete duplicate semantic types.

## Assistant peer

### `internal/assistant/peer/*`

**Keep:** resolver/cache/error boundary.  
**Harden:** cache miss → Telegram fetch → cache refresh → bounded retry.

## Assistant interaction

### `internal/assistant/interaction/*`

**Keep:** send/edit/delete/answer and target resolution.  
**Harden:** message identity and peer-aware fetch.

## Assistant callback

### `internal/assistant/callback/*`

**Keep:** transport lifecycle.  
**Harden:** validation → session → auth → execution → answer.

## Assistant menu

### `internal/assistant/menu/*`

**Keep:** presentation/session.  
**Harden:** registration, ownership, expiration, state transitions, concurrency.

## Assistant client

### `internal/assistant/client/*`

**Keep:** Telegram Bot connection/update lifecycle.  
**Harden:** shutdown barrier, rate limits, metrics, correlation.

---

# 22. Test Strategy

## 22.1 Unit tests

### Peer

- cache hit;
- cache miss + successful fetch;
- stale cache + refresh;
- fetch not found;
- access hash missing;
- retry exactly once;
- retry failure classification.

### Message

- user peer + message;
- group peer + message;
- channel peer + message;
- wrong peer rejected;
- missing target rejected.

### Callback

- valid;
- malformed;
- unauthorized;
- wrong owner;
- expired;
- invalidated;
- duplicate;
- target missing;
- handler failure;
- answer failure.

### Menu

- register;
- get;
- invalidate;
- expiration;
- owner enforcement;
- concurrent Back/Settings;
- close while callback arrives.

### Authorization

- owner;
- sudo;
- admin;
- unauthorized;
- missing owner configuration;
- surface consistency.

### Lifecycle

- start/stop;
- double stop;
- update during shutdown;
- EventBus unregister while callback in flight;
- plugin shutdown after hooks detach.

---

# 23. Integration Matrix

The minimum Assistant flow must cover:

```text
/start
 → Settings
 → Back
 → Help
 → Ping
 → Status
 → Close
```

Then failure paths:

```text
old button
unauthorized user
expired menu
invalid callback
duplicate click
concurrent click
message deleted
peer cache miss
Telegram transient failure
shutdown during callback
```

The Userbot equivalent should execute the same shared functionality where the command is available on both surfaces.

---

# 24. Parity Matrix

For every shared feature record:

| Feature | Userbot | Assistant | Same implementation? | Transport difference only? |
|---|---|---|---|---|
| ping | ✓ | ✓ | must be yes | yes |
| status | ✓ | ✓ | must be yes | yes |
| help | ✓ | ✓ | metadata same | presentation differs |
| settings mutation | ✓/as applicable | ✓ | must be yes | UI differs |
| plugin command | ✓ | according to surface | must be yes | yes |
| start dashboard | n/a | ✓ | Assistant-only | yes |

Any row marked “different implementation” requires explicit justification.

---

# 25. Definition of Done

The cycle is complete only when:

## Architecture

- [ ] `core.Router` is the sole command registry.
- [ ] `UnifiedRegistry` is deleted or proven to have a genuinely different non-command responsibility.
- [ ] `UnifiedCommandAdapter` is deleted.
- [ ] generic Assistant local command registry is deleted.
- [ ] duplicate execution/source models are removed.

## Telegram identity

- [ ] peer cache miss can fetch authoritative entity.
- [ ] access hash handling is correct.
- [ ] `MessageTarget` is peer-aware.
- [ ] `GetMessage` does not rely on message ID alone.

## Interaction

- [ ] callback state machine is explicit.
- [ ] `MenuInstance` is registered and enforced.
- [ ] owner/expiry/state checks occur before execution.
- [ ] duplicate callbacks are safe.

## Security

- [ ] protected configuration fails closed.
- [ ] authorization semantics are shared.

## Reliability

- [ ] retry classification is explicit.
- [ ] error taxonomy is stable.
- [ ] correlation IDs are generated.
- [ ] metrics cover success and failure paths.

## Concurrency/lifecycle

- [ ] menu transitions have deterministic ordering.
- [ ] shutdown rejects new work.
- [ ] in-flight work is drained/cancelled intentionally.
- [ ] EventBus/plugin shutdown is race-safe.

## Parity

- [ ] Assistant shared commands invoke the same functionality as Userbot.
- [ ] Help uses canonical command metadata.
- [ ] Assistant-specific UI remains limited to transport/presentation concerns.

---

# 26. Recommended Commit Sequence

Use small, reversible commits:

```text
1. test: establish Assistant architecture regression coverage
2. refactor: route Assistant commands through core.Router
3. refactor: remove UnifiedCommandAdapter
4. refactor: remove UnifiedRegistry
5. refactor: consolidate execution source/context
6. fix: harden peer resolution refresh
7. fix: make MessageTarget peer-aware
8. fix: enforce MenuInstance lifecycle
9. fix: harden callback authorization/staleness
10. fix: define Assistant concurrency boundaries
11. fix: harden shutdown/EventBus lifecycle
12. fix: unify error/retry/observability policy
13. test: complete Assistant/Userbot parity matrix
```

Do not combine all changes into one giant refactor commit. Each commit should compile and have a clear rollback boundary.

---

# 27. Review Gates

### Gate A — Architecture

No duplicate command registry or adapter remains.

### Gate B — Identity

Peer/message operations work after cache invalidation and across entity classes.

### Gate C — Interaction

Stale/duplicate/foreign callbacks cannot execute actions.

### Gate D — Concurrency

Concurrent menu transitions are deterministic.

### Gate E — Lifecycle

Shutdown is race-safe and waits for required in-flight work.

### Gate F — Parity

Shared commands have one implementation and equivalent semantics across surfaces.

### Gate G — Regression

Full test suite and integration matrix pass.

---

# 28. Anti-Rework Rules

1. Do not create another command registry.
2. Do not create another generic execution context.
3. Do not create `AssistantX` versions of shared functionality.
4. Do not make callback handlers the business layer.
5. Do not make menu state persistent unless explicitly required.
6. Do not solve cache miss by retrying the same cache lookup.
7. Do not use global locks where per-resource serialization is sufficient.
8. Do not make shutdown “safe” merely by cancelling a context; wait for owned goroutines.
9. Do not add abstractions for hypothetical inline/future requirements.
10. Do not optimize before semantic correctness is established.
11. Do not remove existing lifecycle guarantees while simplifying registries.
12. Do not treat numeric Telegram IDs as complete peer identity.

---

# 29. Final Technical Direction

The desired architecture is deliberately boring:

```text
core.Command
core.Router
execution.Source
shared handler/service
        │
        ├──────── Userbot transport
        │
        ├──────── Assistant transport
        │
        └──────── Inline transport where applicable
```

Around that core:

```text
Assistant
 ├── client
 ├── interaction
 ├── peer
 ├── callback
 ├── menu
 └── presentation
```

Those Assistant packages solve **Telegram Bot transport and UX problems**. They must not become a second application domain.

The highest-value engineering work is therefore:

```text
simplify ownership
      ↓
correct peer/message identity
      ↓
deterministic interaction state
      ↓
correct concurrency
      ↓
consistent authorization
      ↓
deterministic lifecycle
      ↓
parity verification
```

Only after these boundaries are stable should additional Ultroid feature parity be expanded aggressively.
