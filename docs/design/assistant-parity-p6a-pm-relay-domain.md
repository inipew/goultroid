# Assistant Parity P6-A — PM Relay domain and durable repository

## Status

P6-A defines the transport-neutral durable state required by the Assistant PM relay. It intentionally does not attach any Telegram update handler, Assistant ingress dependency, command, callback, or runtime worker.

The phase creates three independent persistence domains:

```text
pm_relay_mappings
pm_relay_deliveries
assistant_audience_members
```

Keeping these domains separate prevents reply-routing retention, Telegram delivery idempotency, and audience/broadcast discovery from acquiring accidental shared lifecycle rules.

## Relay mapping

A mapping binds one owner-visible relay message to the visitor message that produced it:

```text
(owner_chat_id, owner_message_id)
        <->
(visitor_user_id, visitor_message_id)
```

Both sides are unique durable identities. Re-registering the exact identity is idempotent; attempting to reuse either side for a different peer message fails with `ErrMappingConflict`.

Mappings have an explicit bounded lifetime. The default target retention is 30 days and the domain rejects retention beyond 90 days. Expired rows are removed only through bounded explicit pruning; capacity pressure never evicts a live mapping.

## Delivery intent and idempotency

A logical Telegram send is identified by:

```text
(direction, source_chat_id, source_message_id)
```

where direction is either:

```text
visitor_to_owner
owner_to_visitor
```

The durable intent stores the destination chat and Telegram `random_id` before transport execution. Once a source identity exists, that stored `random_id` is authoritative and is returned on retry even if a caller generated a new candidate value. The same source cannot be retargeted to another destination.

This is the persistence prerequisite for the later P6 ingress/delivery phases to recover this failure window safely:

```text
intent persisted
    ↓
Telegram RPC succeeds
    ↓
process dies before local completion is persisted
```

A retry reloads the same intent and therefore the same Telegram `random_id` instead of creating a new logical send.

## Claim lease

Delivery execution uses a bounded durable claim:

```text
claim_id
claim_expires_at
attempts
```

Only an uncompleted, unexpired intent without an active claim may be claimed. A stale claim can be replaced after its lease expires. Successful commit records the target message ID and completion timestamp and clears the claim.

An already persisted successful commit is idempotent when replayed with the same target message ID. A different target result fails closed as a delivery conflict.

Failed execution releases its matching claim and stores a bounded diagnostic string. Delivery cleanup never removes an expired row while its execution claim is still live.

## Assistant audience boundary

Audience persistence is deliberately not part of PMPermit and is not named as a relay-only table. It represents users observed through Assistant surfaces and can accumulate source bits for:

- public `/start`;
- PM relay;
- inline usage;
- deep-link usage.

P6-A only defines persistence. Later phases decide which successful Assistant interactions call `TouchAudience`.

Touching an existing member is always permitted even when the table is at capacity. Adding a new member at capacity fails with `ErrAudienceCapacity`; the repository does not evict another member automatically.

Audience cleanup is an explicit bounded operation based on `last_seen_at`. The intended default retention is 180 days with a maximum policy horizon of 365 days.

## Capacity invariants

Default retained-row budgets are:

```text
mappings     16,384
deliveries   32,768
audience     10,000
```

Repository limits are configurable downward for tests and future application policy, with hard upper bounds to prevent accidental unbounded configuration.

Capacity behavior is fail-closed:

```text
full + existing identity -> read/update existing durable row
full + new identity      -> typed capacity error
```

There is no LRU eviction and no `DELETE oldest` fallback in create/touch paths.

Cleanup APIs are bounded to at most 256 rows per call. P6-B/P6-C will own when cleanup is invoked so storage reclamation cannot silently add polling or background goroutines.

## Migration ownership

The schema is owned by feature migration:

```text
pmrelay.001
```

It is registered in the application built-in migration set but creates no runtime PM Relay service. This keeps P6-A safe to deploy before Telegram ingress is implemented.

## Explicit non-goals

P6-A does not implement:

- Assistant `OnNewMessage` routing;
- visitor admission or block policy;
- PMPermit integration;
- Telegram forwarding/copying;
- owner reply dispatch;
- force-sub checks;
- relay control commands or a2 UI;
- broadcast fan-out;
- cleanup ticker or polling worker.

Those are later P6 phases and must consume this repository through an admitted/revalidated execution path rather than bypass it.
