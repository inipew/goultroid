# Assistant Parity P6-C — visitor → owner durable delivery plane

## Status

P6-C activates the first real PM Relay data plane for the Assistant.

The phase is intentionally limited to **private plain-text visitor messages** and
only the visitor → owner direction. Media and owner → visitor delivery remain
outside this phase.

The execution path is:

```text
Assistant private text update
        ↓
P6-B precedence / PrepareVisitor
        ↓
TaskEngine admission
        ↓
RevalidatePrepared
        ↓
ensure durable DeliveryIntent
        ↓
claim delivery lease
        ↓
RevalidatePrepared again
        ↓
messages.forwardMessages(random_id = durable random_id)
        ↓
CommitDelivery(target_message_id)
        ↓
EnsureMapping(owner message ↔ visitor source)
        ↓
TouchAudience(source=relay)
```

No Telegram side effect is authorized before TaskEngine admission.

## Transport semantics

P6-C uses Telegram `messages.forwardMessages` for the text canary rather than
copying message text into a new bot-authored message.

Reasons:

- the owner sees the original visitor identity;
- the relay does not need to persist message text;
- the method accepts an explicit `random_id`;
- restart/retry can repeat one logical forward with the same durable
  `random_id`.

The Assistant transport adapter resolves both source visitor and owner peers
inside admitted execution. Relay classification remains independent of peer
resolution.

The low-level forward primitive requires a caller-owned non-zero
`random_id`. Production routes this RPC through the idempotent mutation lane
because the request ID has already been durably persisted.

## Durable delivery state machine

The logical delivery identity remains the P6-A key:

```text
(visitor_to_owner, source_chat_id, source_message_id)
```

A new delivery generates a random ID before transport and persists:

```text
target_chat_id
random_id
created_at
updated_at
expires_at
```

before the Telegram RPC.

An existing delivery is authoritative. A retry-generated candidate random ID
never replaces the stored value.

Execution then acquires the durable claim lease:

```text
claim_id
claim_expires_at
attempts
```

Only the claimant may perform the forward and commit its target message ID.

## Two revalidation fences

P6-C revalidates mutable relay policy twice:

1. at admitted handler entry, before durable delivery preparation;
2. after the durable claim and immediately before Telegram transport.

The second fence closes the race where relay is disabled while the task is
performing repository work.

If the second revalidation fails, the matching claim is released and Telegram
is not called.

## Failure windows

### Transport failure

```text
intent persisted
claim acquired
Telegram forward fails
        ↓
release claim
retain same random_id
```

A later admitted retry reuses the same intent/random ID.

No mapping or audience member is created.

### Telegram success, local commit failure

```text
Telegram forward succeeds
        ↓
CommitDelivery fails / process dies
        ↓
claim remains durable
        ↓
claim lease expires
        ↓
recovery claims same intent
        ↓
forward retries with same random_id
```

Telegram therefore sees the recovery attempt as the same logical forward.

The ambiguous claim is deliberately not released after a successful transport
whose durable completion could not be written.

### Delivery already committed

A duplicate admitted source does not call Telegram again. It uses the completed
delivery to heal post-delivery metadata if necessary.

### Post-delivery metadata failure

Mapping and audience writes occur only after `CommitDelivery` succeeds.

If either write fails, the delivery stays completed. A later duplicate
occurrence does not forward again; it retries the idempotent finalization.

## Relay mapping

After successful delivery commit, P6-C creates:

```text
(owner_chat_id, owner_message_id)
        ↕
(visitor_user_id, visitor_message_id)
```

using the returned Telegram target message ID.

Mapping lifetime is anchored to the original delivery timestamp. Recovery does
not extend it.

This mapping is the authority needed by P6-D owner reply delivery.

## Assistant audience

Only a successfully committed visitor delivery may touch the generic Assistant
audience with:

```text
AudienceSourceRelay
```

The member's `last_seen_at` is anchored to the original delivery timestamp.
Repeated recovery/finalization does not make an old visitor appear newly
active.

## Retention and cleanup

Capacity recovery is lazy and bounded; P6-C adds no ticker or polling worker.

On typed capacity exhaustion only:

- expired delivery rows may be pruned in a bounded batch and creation retried;
- expired mappings may be pruned in a bounded batch and finalization retried;
- stale audience rows older than the default audience retention may be pruned
  in a bounded batch and touch retried.

Live rows are never evicted to make room.

An already completed delivery that has passed its delivery-retention deadline is
terminally expired. Recovery does not resurrect an already-expired mapping.

## Production activation

The PM Relay service remains disabled by default when constructed directly.

Application composition enables it only when the Assistant bot runtime exists.
The Assistant client then installs:

```text
pmrelay.Service
        ↓
RelayIngress
        ↓
telegramRelayVisitorTransport
        ↓
ClientInteraction.ForwardMessageWithRandomID
        ↓
managed messages.forwardMessages RPC
```

## P6-C payload gate

Only incoming private messages satisfying:

```text
PeerUser
AND media == nil
AND non-empty text
AND non-command
AND not claimed by a2 input
```

reach the P6-C visitor delivery fallback.

Media/caption messages are not forwarded by this phase.

At the P6-C milestone, mapped owner replies remained higher precedence than
generic AwaitInput but terminated with `ErrUnsupportedDelivery`. P6-D now
supersedes that terminal boundary for plain-text replies with the durable
owner → visitor bot-authored delivery plane. Media remains fail-closed until
the transport is extended explicitly.

## Acceptance gates

Tests freeze these invariants:

- admission rejection creates no delivery, mapping, audience, or transport call;
- admitted visitor execution creates one durable delivery;
- transport receives the persisted non-zero random ID;
- successful delivery commits the target message ID before mapping/audience;
- duplicate completed delivery does not forward twice;
- transport failure releases the claim and creates no mapping/audience;
- retry after transport failure reuses the original random ID;
- crash-like commit failure leaves an ambiguous durable claim;
- recovery after lease expiry reuses the same random ID;
- mapping and audience are repaired after successful recovery;
- disable after claim but before RPC prevents transport and releases the claim;
- recovery does not advance audience activity time;
- expired completed delivery cannot resurrect reply mapping;
- P6-C visitor fallback rejects media;
- mapped owner replies fail closed until P6-D instead of becoming successful no-ops.

## Explicit non-goals

P6-C does not implement:

- visitor → owner media relay;
- local visitor block/ban policy;
- force-sub checks;
- relay settings/control UI;
- audience broadcast fan-out;
- proactive startup scan/replay of pending delivery intents.

Those remain later P6 phases.
