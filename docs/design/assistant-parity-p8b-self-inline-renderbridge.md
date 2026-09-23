# Assistant Parity P8-B — production self-inline / RenderBridge

## Status

**P8-B is CLOSED for implementation/source acceptance.**

P8-B adds one production userbot → own Assistant self-inline rendering capability without creating a new interaction runtime or callback protocol.

Baseline:

`8cb0d9cadfd6599dadcaf92677a15e9fecc77094`

## Problem

Goultroid already had:

- FeatureSpec-owned inline handlers;
- bounded Inline vNext execution;
- a2 typed actions;
- P1 interaction sessions;
- Assistant inline callback ingress;
- transport-neutral presentation primitives.

What was still missing was the Ultroid-style capability:

```text
userbot feature
    ↓
query own Assistant inline surface
    ↓
select one result
    ↓
insert it into the current chat
    ↓
continue using typed callbacks
```

Feature code must not implement that by reaching into a raw Telegram client and manually calling `messages.getInlineBotResults` / `messages.sendInlineBotResult`.

## Architecture

The new boundary is:

```text
feature
  ↓
selfinline.Renderer
  ↓
RenderBridge
  ↓
selfinline.Transport
  ↓
telegram.Service
  ├─ shared Resolver
  └─ shared RPCExecutor / limiter
        ↓
messages.getInlineBotResults
messages.sendInlineBotResult
```

The bridge is intentionally stateless.

It owns no:

- result cache;
- session map;
- goroutine;
- ticker;
- per-user worker;
- retry loop;
- callback registry.

## Feature-facing contract

`internal/presentation/selfinline/render.go` defines:

- `Renderer`;
- `Request`;
- `Result`;
- narrow `Transport`.

A render request carries:

- destination peer;
- inline query;
- optional offset;
- stable result ID or bounded result index;
- reply/topic coordinates;
- silent/hide-via options.

Validation is fail-closed:

- peer required;
- query 1..4096 bytes;
- offset max 512 bytes;
- result index limited to Telegram's first 50 results;
- running Assistant username required;
- returned query ID/result ID must be usable.

Exactly one result is selected.

## Assistant identity

The App exposes:

`SelfInlineRenderer() selfinline.Renderer`

The renderer receives an Assistant username provider rather than copying the username at construction time.

Therefore:

```text
Assistant restart/relogin
    ↓
username may change
    ↓
next Render reads Assistant.Username() again
```

There is no stale bot identity cache in feature code.

## Telegram transport

`internal/telegram/selfinline.go` implements the production transport.

### Query

`QueryInlineBot`:

1. resolves the current Assistant username through the existing Telegram resolver;
2. refreshes its access hash through existing peer storage;
3. refreshes the destination peer through the normal service peer wrapper;
4. executes `messages.getInlineBotResults` via `execReadOnlyPeerVal`.

### Send

`SendInlineBotResult`:

1. accepts a cryptographically generated non-zero `random_id`;
2. preserves explicit reply/topic coordinates;
3. executes `messages.sendInlineBotResult` through `execNonIdempotentPeerVal`.

The bridge therefore does not create another retry or FloodWait authority.

## Random ID

Every render uses `crypto/rand` for the Telegram `random_id`.

No process-local counter or history map is retained.

## Topic/thread behavior

If a caller supplies:

- `ReplyToID`, that reply is preserved;
- `TopicID`, it is preserved as top-message context when appropriate;
- a topic with no explicit reply falls back to the topic root as the reply target.

This keeps self-inline insertion inside the originating forum topic instead of silently dropping to General.

## a2 callback continuity

Inline vNext already creates interactive sessions before Telegram assigns a concrete inline message ID:

```text
Binding{ActorID: userID}
```

P1 already implements first-callback target claiming:

`TestResolveCallbackClaimsFirstConcreteInlineTarget`

Therefore P8-B deliberately does **not** introduce an inline-message/session map.

The first valid callback binds the session to the concrete inline message. Later callbacks from a copied/different inline message fail the existing binding check.

## Bounds

P8-B adds no historical cardinality.

Per render:

```text
query bytes   <= 4096
offset bytes  <= 512
selectable results <= 50
selected results   = 1
retained bridge state after call = 0
```

P1 session limits remain the only interaction-state limits.

## Acceptance coverage

New tests:

- `TestRenderQueriesCurrentAssistantAndSendsSelectedResult`
- `TestRenderCanSelectStableResultID`
- `TestRenderFailsClosedBeforeTelegramSend`
- `TestRenderUsesTopicRootAsReplyWhenNoExplicitReply`
- `TestP8BSelfInlineUsesSingleManagedTransport`
- `TestP8BSelfInlineBridgeOwnsNoWorkersOrGlobalResultCache`

The architecture fence also locks the dependency on:

- actor-only Inline vNext session creation;
- existing first concrete inline-target claim.

## Non-goals

P8-B does not:

- implement calculator;
- add a search provider;
- add YouTube/download UI;
- create an inline result cache;
- change a2 token format;
- change Inline vNext dispatch;
- add a second TaskEngine;
- expose raw MTProto to features;
- inspect CI.

## Next phase

Proceed to **P8-C — calculator callback-heavy canary**.

P8-C should consume this RenderBridge rather than implementing its own self-inline query/click path.
