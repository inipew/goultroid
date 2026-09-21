# Assistant vNext P3 — Interaction execution / presentation orchestration

## Status

P3 introduces the feature-facing orchestration layer above the P0 feature catalog, P1 session runtime, and P2 presentation/action primitives. It deliberately does not migrate `/start`, settings, inline handlers, menus, PM relay, downloader, calculator, or any other feature UI.

## Objective

Feature handlers should no longer coordinate four independent primitives:

```text
Session Runtime + Action Dispatcher + View Compiler + Presentation Port
```

Instead they receive one orchestration context that owns the safe sequence for state and presentation operations.

## Package boundary

The orchestration facade lives in:

```text
internal/interaction/orchestration
```

It is a subpackage rather than part of `internal/interaction` because `internal/presentation` already depends on the P1 interaction package for callback compilation. Keeping orchestration as a child package avoids an import cycle while preserving the interaction domain boundary.

## Engine

An `Engine` is bound to:

- one shared P1 `interaction.Runtime`;
- its matching P2 `interaction.Dispatcher`;
- one `presentation.Compiler` created from that runtime;
- one transport-specific `presentation.Port`.

`New` fails closed if the dispatcher belongs to a different session runtime.

The plugin manager exposes `NewInteractionEngine(port)` so Assistant and the future userbot self-inline bridge can share identical feature-facing semantics while using different transport adapters.

## Initial presentation sequence

`Engine.Begin` owns the full initial-render transaction:

```text
validate transport target
        ↓
derive session binding from actor + target
        ↓
create P1 session
        ↓
compile View → a2 callback tokens
        ↓
Port.Send
        ↓
derive concrete sent target
        ↓
BindTarget(session, revision, target)
```

Any error after session creation cancels the session. If sending succeeds but target binding fails, the visible buttons are therefore inert instead of remaining authorized against a partially initialized session.

The transport target, not feature code, derives the interaction binding. This prevents a caller from validating one chat/message while presenting or editing another target.

## Feature-facing context

Typed action handlers receive `orchestration.Context`, which provides:

- `Context()` — merged execution/session cancellation context;
- `Session()` — defensive session snapshot;
- `State()` — defensive opaque state copy;
- `Target()` — validated presentation target;
- `UpdateState()` — optimistic P1 state transition;
- `Edit()` — compile current revision and edit the validated target;
- `Transition()` — state update followed by rendering the new revision;
- `Answer()` — callback answer scoped to the current callback query;
- `Cancel()` — explicitly terminate the session.

Handlers do not need direct access to the compiler, presentation port, callback token encoder, or target-binding mutation.

## Callback execution

`Engine.Dispatch` accepts raw callback data plus actor, query ID, and the concrete transport target. It derives the P1 binding from that target and passes the request through the P2 dispatcher.

The dispatcher still performs P1 validation before invoking a handler:

1. token protocol/version;
2. TTL/session existence;
3. actor/chat/message binding;
4. current plugin generation;
5. session revision;
6. declared action identity;
7. exact registered handler generation.

Only after those checks does the orchestration wrapper construct the feature-facing context.

## Context cancellation semantics

P2 previously invoked handlers with the session context directly, which discarded caller values/deadlines. P3 merges both lifetimes:

- caller values and deadline remain visible;
- caller cancellation cancels the action handler;
- session expiry/cancel/plugin unload also cancels the handler with the P1 cause;
- no long-lived goroutine is created by the orchestration layer.

## Transition semantics

`Transition(state, ttl, view)` updates P1 state first, advancing the revision, then compiles and edits the new view.

If compile/edit fails after the state transition, buttons from the older visible revision are intentionally stale and cannot mutate the newer state. This is fail-closed. The handler may retry rendering the current revision while its session remains live.

## Lifecycle

P3 tightens plugin cleanup to remove all generation-owned interaction resources:

```text
close P0 feature registration
        ↓
unregister P2 action handlers for exact ScopeIdentity
        ↓
cancel P1 sessions for exact ScopeIdentity
```

`Dispatcher.UnregisterScope` is generation-exact, so delayed cleanup from an old plugin generation cannot remove handlers registered by a replacement generation.

## Transport contract

`presentation.SessionTarget` adds two transport-owned projections:

- `SessionBinding(actorID)` for the partial binding available before an initial send;
- `TargetBinding()` for the concrete target available after send or on callback receipt.

The Telegram bridge implements this for normal message targets and inline targets. Inline transports must provide a stable `BindingID`; the future self-inline bridge can therefore use the same orchestration layer without leaking MTProto types into feature code.

## Non-goals

P3 does not:

- replace the legacy `a1`/`v1` callback paths;
- route existing Assistant callback updates into the new dispatcher;
- migrate existing menu/screens;
- migrate `/start` or settings;
- migrate inline features;
- implement feature-specific handlers;
- change TaskEngine/RPC execution semantics.

Those migrations begin only after the P0–P3 foundation is accepted as stable.
