# Assistant Parity P6-F — audience registry integration

## Status

P6-F turns the Assistant audience registry introduced in P6-A into the
cross-entry-point membership authority for the Assistant plane and connects that
membership to the existing bounded Broadcast service.

No second fan-out engine is introduced.

## Entry-point sources

The durable audience row keeps a bitmask of successful Assistant entry points:

```text
AudienceSourceStart
AudienceSourceInline
AudienceSourceDeepLink
AudienceSourceRelay
```

Repeated observations merge bits and update `last_seen_at` without creating
duplicate membership.

### /start

A user is touched with `AudienceSourceStart` only after one of the successful
start paths completes:

- owner a2 shell start;
- public visitor read-only start;
- static recovery response.

A versioned deep-link start does not also claim the plain start source. Its
successful execution records `AudienceSourceDeepLink`.

### inline

Inline membership is touched only inside admitted TaskEngine execution and only
after the shared inline engine returns success.

Preparation failure, TaskEngine rejection, inline execution failure, or answer
failure does not register the user as an inline audience member.

### deep-link

Deep-link membership is touched only after:

```text
token preparation
→ TaskEngine admission
→ provider execution
→ successful delivery
```

Invalid, unauthorized, expired, rejected, or failed deep-links do not enter the
audience registry.

### relay

Relay membership remains part of the durable visitor → owner finalization path.
The relay source is recorded only after the Telegram delivery is durably
committed and the owner↔visitor mapping is persisted.

Unlike the lightweight start/inline/deep-link observation hooks, relay audience
touch remains a durable finalization invariant: failure is surfaced so a later
idempotent relay occurrence can heal the metadata without forwarding twice.

## Touch failure policy

Start, inline, and deep-link observations are intentionally best-effort after
their user-visible action has already succeeded.

A registry write failure is logged but does not turn a successful Assistant
interaction into a false user-facing failure.

The registry itself remains bounded and may lazily prune stale members only on
capacity pressure.

## Stable keyset order

P6-F adds feature migration:

```text
pmrelay.003
```

with:

```text
assistant_audience_membership_order
├── sequence INTEGER PRIMARY KEY AUTOINCREMENT
└── user_id  UNIQUE
```

The sequence is allocated when a user first becomes an active audience member.

Existing-member touches do not change that sequence.

Pruning removes both the active member and its membership-order row in one
transaction. If that user later returns, it receives a new sequence and belongs
only to later snapshots.

This avoids the classic keyset bug where a newly inserted smaller Telegram user
ID could leak into an already-running broadcast.

## Audience snapshot

A broadcast begins with:

```text
SnapshotAudience()
→ MaxSequence
→ Total
```

Iteration then uses:

```text
ListAudienceSnapshot(snapshot, afterSequence, limit)
```

and only returns membership rows whose durable sequence is within the captured
watermark.

New audience members created after the snapshot cannot enter the active
broadcast.

### Retention pruning during a broadcast

Retention pruning is permitted to remove stale members while a long broadcast
is running. P6-F deliberately does not retain stale recipients or maintain an
unbounded membership event history merely to keep sending to users that were
reclaimed.

If a captured member disappears after snapshot creation, the Assistant target
source emits a logical missing target. The shared Broadcast service counts it as
`Failed`.

Therefore:

```text
report.Total == report.Sent + report.Failed
```

remains truthful even when retention reclamation races a broadcast.

## Existing Broadcast service integration

P6-F extends the existing Broadcast service with a narrow `TargetSource`
iterator abstraction.

The same service still owns:

- TaskEngine admission;
- bounded in-flight window;
- background priority;
- per-target ordering;
- Telegram RPC limiter behavior;
- media resource reservations;
- SavedResponse compilation/delivery;
- cancellation;
- coalesced progress;
- final report accounting.

The Assistant does not create another worker pool, goroutine fan-out loop,
scheduler, retry engine, or rate limiter.

The existing maximum accepted ticket window remains:

```text
maxBroadcastInFlight = 64
```

Large Assistant audiences are therefore streamed in bounded pages instead of
materialized into one in-memory target slice.

## Assistant transport authority

`AssistantClient.BroadcastAudience` owns both recipient selection and the
Assistant bot sender.

Callers may provide only the broadcast payload/options. These fields are
rejected when supplied by the caller:

```text
Targets
TargetSource
Sender
```

This prevents bypassing the durable audience snapshot or accidentally sending
through the userbot transport.

Each snapshotted user ID is resolved through the Assistant peer resolver and
sent through the Assistant `ClientInteraction` transport.

## Application wiring

Application composition binds the same `pmrelay.Service` instance as:

```text
PM Relay ingress
AudienceRegistry
```

and binds the existing domain Broadcast service as the Assistant broadcast
engine.

The registry is migrated before application runtime start, so
`pmrelay.003` is present before any Assistant update can touch membership.

## Acceptance gates

P6-F tests freeze the following behavior:

- `/start` touches `AudienceSourceStart`;
- public visitor start also touches the start source;
- successful deep-link touches only `AudienceSourceDeepLink`;
- failed deep-link does not touch audience;
- successful inline execution touches `AudienceSourceInline`;
- failed inline execution does not touch audience;
- committed relay delivery touches `AudienceSourceRelay`;
- all four source bits merge on one durable member;
- new members after snapshot creation do not leak into that snapshot;
- existing-member updates do not change membership order;
- prune removes membership order atomically;
- rejoined users receive a later membership sequence;
- retention-pruned snapshot members are accounted as failed, not silently lost;
- `TargetSource` streams through the same bounded Broadcast implementation;
- large target sources backpressure rather than filling TaskEngine indefinitely;
- caller-supplied Assistant broadcast targets/sender are rejected;
- Assistant audience sends use the Assistant MTProto transport, not the userbot
  Broadcast transport;
- rich SavedResponse/media broadcasts continue using the shared media resource
  budget and cleanup lifecycle.

## Non-goals

P6-F does not add:

- a user-facing broadcast command/UI;
- force-sub;
- audience segmentation/filter expressions;
- a second fan-out engine;
- per-audience custom rate limits;
- a new background worker;
- proactive periodic audience cleanup;
- delivery history per audience member.

Those can be layered later without changing the durable membership identity or
bounded Broadcast execution model.
