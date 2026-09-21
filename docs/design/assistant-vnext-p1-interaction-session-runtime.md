# Assistant vNext P1 — Interaction Session Runtime

## Status

Implemented as the second foundation phase for the Assistant/Inline rework.

P1 builds bounded interaction state and the next callback token protocol on top of the P0 feature catalog. It intentionally does **not** migrate `/start`, settings, inline feature handlers, menus, PM relay, or any other feature-specific UI.

## Goals

P1 provides one runtime for short-lived interactive state that is:

- bounded by session count and retained bytes;
- TTL-controlled without a permanent background ticker;
- bound to actor/chat/message identity;
- bound to the exact plugin lifecycle generation that created it;
- cancellable with structured context causes;
- safe across plugin disable/re-enable and application shutdown;
- addressable through a compact versioned callback token that fits Telegram's 64-byte callback-data limit.

The runtime stores state only. It does not dispatch feature handlers.

## Runtime model

```text
feature.Registry (P0)
       │
       ├── current feature scope generation
       └── declared action IDs
                │
                ▼
        interaction.Runtime
                │
       ┌────────┼─────────┐
       │        │         │
    Session   Binding    TTL
       │        │         │
       └────────┼─────────┘
                │
                ▼
       callback token a2
```

A session is owned by the `tasks.ScopeIdentity` currently published for its feature. The caller never supplies a generation manually.

## Bounded state

Default limits are conservative and configurable:

| Limit | Default |
| --- | ---: |
| total live sessions | 4096 |
| sessions per plugin generation | 512 |
| sessions per actor | 64 |
| state bytes per session | 64 KiB |
| total retained state bytes | 8 MiB |
| default TTL | 15 minutes |
| maximum TTL | 24 hours |

Capacity behavior is fail-closed. The runtime prunes already-expired sessions first, but it never evicts a live session merely to admit a newer one.

Opaque state is retained as `[]byte`, copied on ingress and egress. This avoids retaining caller-owned backing arrays and avoids an unbounded `any` object graph.

## Idle behavior and expiry

The runtime starts no ticker and no background goroutine.

Expiry is maintained by an indexed min-heap. Every live session owns exactly one heap entry. `Touch` and TTL refresh use `heap.Fix` on that entry rather than appending stale deadlines, so repeated refresh does not grow heap cardinality.

Expired sessions are reclaimed:

- lazily on normal runtime operations;
- by `Stats`;
- explicitly through `PruneExpired`.

This keeps idle CPU/goroutine overhead at zero while bounding expiry metadata to live-session cardinality.

## Session identity and state revision

Every session receives a cryptographically random 128-bit ID encoded as raw base64url. The encoded ID is 22 characters.

A session starts at revision `1`.

`UpdateState` requires an expected revision and performs optimistic revision checking. A successful state transition increments the revision. This gives later UI/action dispatch a deterministic stale-render fence.

`Touch` and `BindTarget` do not advance the state revision.

## Actor/chat/message binding

`Binding` supports these dimensions:

- `ActorID`
- `ChatID`
- `MessageID`
- `InlineMessageID`

At least one dimension must be bound. A stored binding only checks dimensions it actually constrains.

Normal message identity requires a chat ID for a message ID. Inline message identity is mutually exclusive with normal chat/message identity.

A session may initially bind only the actor or chat. After a presentation is actually sent, `BindTarget` can attach the concrete message or inline-message target. Existing target identity cannot be replaced by a conflicting target.

`BindTarget` deliberately does not increment the state revision. This allows callback data to be generated before a send and remain valid after the resulting message ID is attached.

Expiry is rechecked under the mutation lock for target binding and TTL refresh, preventing a just-expired session from being resurrected by a concurrent mutation.

## Plugin-generation binding

P1 extends the read-only feature catalog with:

```go
FeatureScope(featureID string) (tasks.ScopeIdentity, bool)
HasAction(featureID, actionID string) bool
```

Session creation resolves the current feature scope from this catalog. After publishing the session, it resolves the scope again. If the feature disappeared or changed generation during creation, the just-created session is canceled and creation returns `ErrScopeStale`.

This closes the create-vs-disable race without coupling the interaction runtime to plugin internals.

Every subsequent session resolve verifies that the current catalog scope still equals the stored scope. A mismatch removes the session with `ErrScopeStale`.

## Plugin lifecycle integration

The plugin manager owns one interaction runtime alongside the P0 registry.

Feature cleanup order is:

```text
close feature catalog registration
        ↓
cancel sessions for exact old scope generation
        ↓
continue plugin hook/callback/scope teardown
```

Closing the catalog first prevents new sessions from being admitted for a feature generation being disabled.

Re-enable publishes a new P0 scope generation. New sessions therefore belong to the new generation; old cleanup cannot remove them because `CancelScope` is generation-exact.

Global plugin-manager shutdown first executes all feature cleanups, then closes the interaction runtime. `Close` is terminal: all remaining session contexts are canceled and new admissions return `ErrClosed`.

## Cancellation semantics

Every session owns a child `context.Context` created with `context.WithCancelCause` under the runtime root context.

Relevant causes include:

- `ErrExpired` — TTL elapsed;
- `ErrCanceled` — explicit session cancellation;
- `ErrScopeStale` — owning plugin generation disappeared or changed;
- `ErrClosed` — interaction runtime shut down.

Consumers can use `context.Cause(sessionContext)` rather than guessing why an interaction stopped.

No goroutine waits on these contexts inside the runtime.

## Callback protocol v2

P1 introduces protocol version `a2`:

```text
a2:<feature>:<action>:<session-id>.<revision-base36>
```

Example shape:

```text
a2:calculator:digit:q3v...rQ.4
```

Properties:

- strict protocol-version parsing;
- feature and action identifiers are validated;
- 128-bit random session ID;
- state revision encoded in base36;
- no mutable state embedded in callback data;
- hard maximum of 64 bytes;
- legacy/malformed payloads are rejected by the v2 parser.

`CallbackData` only emits a token for an action declared in the current P0 feature spec.

`ResolveCallback` validates, in order:

1. token structure/version;
2. session existence and TTL;
3. actor/chat/message binding;
4. current plugin generation;
5. token feature matches session feature;
6. token revision matches current session revision;
7. action remains declared by the current feature generation.

An old button from a prior state revision fails with `ErrStaleToken` rather than executing against newer state.

## Compatibility boundary

The existing `internal/services/callback` service remains unchanged in P1. Existing v1/a1 callback consumers continue to use it.

The new `internal/interaction` package owns only the v2 session/token foundation. Feature migration to `a2` happens in later phases, one interaction surface at a time.

## Concurrency and lock ordering

The runtime never calls the feature catalog while holding the interaction mutex. Catalog checks happen before or after the session lock as needed.

This is intentional because plugin cleanup takes the catalog registration lock before calling `CancelScope`. Avoiding the reverse lock order prevents catalog/runtime deadlocks during disable or reload.

State mutation uses optimistic revision checks under the runtime mutex. Session removal updates all secondary indexes and expiry heap entries before canceling the session context.

## Diagnostics

`Stats` exposes only bounded-retention counters, not session contents:

- live sessions;
- retained state bytes;
- expired count;
- canceled count;
- stale-generation count;
- capacity-rejected count.

It also lazily prunes expired sessions before returning the snapshot.

## What P1 intentionally does not do

P1 does not:

- dispatch interaction actions to feature handlers;
- replace the existing Assistant callback router;
- migrate `/start` or deep-link routing;
- migrate Assistant settings/menu screens;
- migrate current inline handlers;
- define a presentation `View` model;
- implement the userbot self-inline/render bridge;
- implement PM relay;
- add periodic cleanup goroutines;
- change TaskEngine, Telegram RPC, or canonical command execution semantics.

## P1 acceptance criteria

P1 is complete when:

- session state is bounded globally, per generation, per actor, and by retained bytes;
- live sessions are never evicted to satisfy capacity;
- TTL expiry cancels session context with an explicit cause;
- expiry metadata stays O(live sessions) across repeated TTL refreshes;
- actor/chat/message/inline-message bindings are enforced;
- concrete target binding cannot silently retarget an existing session;
- state updates use optimistic revision checking;
- sessions are tied to P0 plugin scope generations;
- a disable/create race cannot leave a zombie session;
- plugin disable cancels exactly the old generation's sessions;
- re-enable creates sessions under the new generation;
- manager shutdown terminally closes interaction admission;
- callback `a2` data remains within Telegram's 64-byte limit;
- old callback revisions are rejected;
- existing callback v1/a1 behavior remains untouched;
- no feature-specific UI is migrated in this phase.

## Next phase

P2 should build the **Presentation / Render Bridge** and typed action-dispatch boundary on top of P0 + P1: transport-neutral views, buttons that reference declared action IDs, send/edit/answer operations, and Assistant/userbot-inline rendering without exposing raw Telegram transport mechanics to features.

Feature-specific `/start`, settings, calculator, downloader, and other UI migrations should still wait until that presentation/dispatch foundation exists.
