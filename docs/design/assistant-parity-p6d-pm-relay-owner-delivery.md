# Assistant Parity P6-D — owner → visitor durable reply delivery

## Status

P6-D adds the durable reverse PM Relay data plane for owner replies. The phase
first establishes plain-text delivery, then extends the same durable transport
to reusable Telegram photo/document media references.

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
plain text:
  messages.sendMessage(
      visitor,
      source.Message,
      source.Entities,
      random_id = durable random_id
  )

photo/document media:
  messages.sendMedia(
      visitor,
      reusable Telegram media reference,
      source.Message caption,
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
- callback markup.

For photo/document-class media, only the Telegram media reference and caption
are reused. This includes document-backed file/sticker/audio/video/voice
variants without downloading or re-uploading bytes.

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

Production exposes separate durable `sendMessage` and `sendMedia` paths
through the Assistant managed RPC adapter. Only these pre-persisted-random-ID
paths enter the idempotent mutation lane; ordinary Assistant sends keep their
previous behavior.

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

## Payload gate

The reverse transport supports:

- plain text;
- Telegram `MessageMediaPhoto`;
- Telegram `MessageMediaDocument`.

Photo/document media is copied server-side using `InputMediaPhoto` or
`InputMediaDocument` built from the freshly reloaded source message. The
existing file reference is therefore refreshed by the source lookup before each
attempt, and no local media bytes are retained.

Other semantic media such as polls, contacts, locations, games, invoices, and
unsupported constructors remain fail-closed with `ErrUnsupportedDelivery`.
They are never coerced into a lossy text/media representation.

Mapped unsupported-media replies still outrank AwaitInput, preventing them from
being consumed as unrelated workflow input.

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
- photo references are copied through `messages.sendMedia`;
- document references are copied through `messages.sendMedia`;
- media copy performs no upload/download;
- owner reverse delivery never invokes `messages.forwardMessages`;
- unsupported semantic media produces no send/forward side effect.

## Explicit non-goals

P6-D does not implement:

- visitor → owner media relay;
- semantic media copy for polls/contacts/locations/games/invoices;
- media download/re-upload fallback;
- album grouping;
- visitor block/ban controls;
- force-sub;
- relay control UI;
- broadcast fan-out;
- proactive startup replay/scanning of pending delivery intents.

Any later semantic-media expansion should extend this same durable
`owner_to_visitor` contract rather than creating a parallel relay pipeline.
