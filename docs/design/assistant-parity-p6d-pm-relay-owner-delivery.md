# Assistant Parity P6-D — owner → visitor durable reply delivery

## Status

P6-D adds the durable reverse PM Relay data plane for **plain-text owner replies**.

A reply is eligible only when the owner replies to a still-valid durable
`RelayMapping` created by P6-C. The mapping remains the authorization authority
throughout execution.

The delivery path is:

```text
owner private reply
        ↓
reply_to owner message
        ↓
GetMapping(owner_chat_id, reply_to_message_id)
        ↓
PrepareOwnerReply
        ↓
TaskEngine admission
        ↓
RevalidatePrepared(mapping)
        ↓
EnsureDelivery(owner_to_visitor)
        ↓
ClaimDelivery
        ↓
RevalidatePrepared(mapping) again
        ↓
reload owner source message from Telegram
        ↓
messages.sendMessage(
    visitor,
    source.Message,
    source.Entities,
    random_id = durable random_id
)
        ↓
CommitDelivery(visitor_message_id)
```

The reverse path never calls `messages.forwardMessages`.

## Why send/copy instead of forward

The owner is the control-plane principal. Their Telegram identity must not leak
to a visitor through a forwarded-message header.

P6-D therefore reloads the source owner message and copies only:

- plain message text;
- Telegram formatting entities.

It deliberately does **not** copy:

- forward metadata;
- owner author metadata;
- owner reply header;
- media;
- callback markup.

The resulting visitor-side message is authored by the Assistant bot.

## Durable identity

The logical reverse delivery key is:

```text
(owner_to_visitor, owner_chat_id, owner_source_message_id)
```

The durable row stores the visitor as `target_chat_id`.

As in P6-C, `random_id` is generated and persisted before Telegram transport.
If a later execution sees an existing delivery, the stored random ID is
authoritative.

## Mapping as authorization

`PrepareOwnerReply` records the exact mapping identity that authorized the
reply:

```text
(owner_chat_id, owner_relay_message_id)
        ↕
(visitor_user_id, visitor_source_message_id)
```

`RevalidatePrepared` checks the mapping again immediately before execution.

P6-D adds a second check after the delivery claim and immediately before any
Telegram side effect.

If the mapping has expired, been pruned, or no longer matches the prepared
identity:

```text
claim
  ↓
mapping revalidation fails
  ↓
release claim
  ↓
NO Telegram send
```

This prevents queued/stale owner replies from reaching the wrong visitor.

## Source payload durability

P6-D does not persist owner message content in PM Relay tables.

The durable delivery stores only source identity:

```text
source_chat_id
source_message_id
```

At execution/recovery time the Assistant reloads the owner source message from
Telegram. This means a retry after restart can reconstruct the text from the
authoritative Telegram message rather than depending on an in-memory update
payload.

## Telegram idempotency

P6-D uses a caller-owned durable `random_id` with
`messages.sendMessage`.

Production exposes a separate durable send path through the Assistant managed
RPC adapter. Only this pre-persisted-random-ID path enters the idempotent
mutation lane; ordinary Assistant sends keep their previous behavior.

Failure semantics:

### Telegram failure

```text
intent persisted
claim acquired
sendMessage fails
        ↓
release claim
retain random_id
```

A later admitted retry reuses the same random ID.

### Telegram success, CommitDelivery failure

```text
sendMessage succeeds
        ↓
durable completion write fails / process dies
        ↓
claim remains
        ↓
lease expires
        ↓
same delivery is reclaimed
        ↓
same source message is reloaded
        ↓
same random_id is sent
```

Telegram sees the recovery as the same logical send.

### Already completed

A duplicate owner source message does not invoke Telegram again.

## Audience and mapping semantics

Owner → visitor replies do not create a new relay mapping.

They also do not touch `assistant_audience_members`: an owner sending a reply is
not evidence that the visitor is newly active.

The original P6-C mapping remains the conversation authority until its own
retention deadline.

## Admission and ordering

P6-D reuses the P6-B admission contract:

- scope: `service:pmrelay`;
- pool: `interactive`;
- priority: interactive;
- quota owner: `pmrelay:owner-reply:<visitor-id>`;
- ordering key: `pmrelay:thread:<visitor-id>`.

Both relay directions therefore serialize within one visitor conversation while
remaining independent across visitors.

No delivery intent or Telegram request is created if TaskEngine rejects
admission.

## Text-only gate

P6-D is intentionally text-only.

The transport accepts a source message only when:

```text
source.Media == nil
AND trim(source.Message) != ""
```

A mapped media reply is still claimed by relay precedence, but execution returns
`ErrUnsupportedDelivery` without calling `messages.sendMessage` or
`messages.forwardMessages`.

This fail-closed behavior prevents media replies from falling into an unrelated
AwaitInput session while the media transport is not implemented.

## Acceptance gates

Tests freeze these invariants:

- successful owner reply creates one completed
  `owner_to_visitor` `DeliveryIntent`;
- target is the visitor authorized by the durable mapping;
- transport receives the persisted non-zero random ID;
- duplicate completed owner delivery does not send again;
- owner transport failure releases the claim;
- retry reuses the original random ID;
- ambiguous Telegram-success/local-commit-failure recovery uses the same random
  ID;
- mapping is revalidated again after claim;
- mapping expiry after claim prevents Telegram transport and releases the lease;
- owner delivery creates no extra relay mapping;
- owner delivery does not update Assistant audience activity;
- TaskEngine admission rejection creates no delivery or transport side effect;
- source text/entities are copied into `messages.sendMessage`;
- owner reverse delivery never invokes `messages.forwardMessages`;
- media source messages produce no send/forward side effect.

## Explicit non-goals

P6-D does not implement:

- media/photo/document/sticker/audio/video relay;
- media download/re-upload;
- album grouping;
- visitor block/ban controls;
- force-sub;
- relay control UI;
- broadcast fan-out;
- proactive startup replay/scanning of pending delivery intents.

The next transport expansion should extend this same durable
`owner_to_visitor` contract for media rather than creating a parallel relay
pipeline.
