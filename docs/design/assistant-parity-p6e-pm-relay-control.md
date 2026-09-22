# Assistant Parity P6-E — PM Relay owner control plane

## Status

P6-E adds the canonical owner-only control plane for Assistant PM Relay and a
durable visitor block policy enforced by both delivery directions.

The control plane is registered through the shared `core.Router` / feature
catalog. It does not add a transport-local command registry.

## Canonical commands

P6-E exposes these Assistant-only, owner-only, private-only commands:

```text
/relay
/relay status
/relay ban [user_id] [reason]
/relay unban [user_id]
/relay blocked [after_user_id]
/who
```

`/who` is reply-only and resolves the visitor from the durable PM Relay mapping
for the owner message being replied to.

The generic canonical `ban` and `unban` command names already belong to the
Admin plugin for group moderation. PM Relay therefore deliberately uses
`/relay ban` and `/relay unban` instead of creating a second global command
with conflicting semantics.

## Reply identity

Before P6-E, the Assistant command adapter passed command text and sender
identity into canonical `core.Context`, but not Telegram message/reply
coordinates.

P6-E adds a backward-compatible `DispatchMessage` path so production Assistant
updates also provide:

```text
message_id
reply_to_message_id
```

The legacy `Dispatch` API remains available for embedding/tests.

This keeps reply-aware commands canonical rather than forcing `/who` into a
special Assistant-only dispatcher.

## Durable block policy

Migration `pmrelay.002` adds:

```text
pm_relay_visitor_blocks
  visitor_user_id PRIMARY KEY
  blocked_at
  reason
```

The blocklist is intentionally separate from PMPermit. PMPermit protects the
user account's incoming PM plane; PM Relay blocks Assistant visitors and uses a
different principal and delivery path.

The repository is bounded. Existing entries may be updated while capacity is
full, but inserting a new visitor at capacity fails closed with
`ErrBlockCapacity`. Live blocks are never evicted automatically.

## Visitor ingress policy

A blocked visitor is rejected during `PrepareVisitor` before TaskEngine
admission:

```text
private visitor update
        ↓
PrepareVisitor
        ↓
GetVisitorBlock
        ↓
blocked
        ↓
no TaskEngine work
no Telegram forward
```

A block is PM Relay policy only. It does not globally disable unrelated
Assistant commands or explicit a2 interactions for that account.

Repository uncertainty fails closed.

## Owner reply policy

For owner → visitor delivery, the mapping and block policy are both
authoritative.

```text
owner reply
   ↓
mapping lookup
   ↓
block check
   ↓
TaskEngine admission
   ↓
mapping + block revalidation
   ↓
delivery intent + claim
   ↓
mapping + block revalidation AGAIN
   ↓
Telegram send/copy
```

A blocked mapped visitor therefore cannot receive an owner reply.

## Post-claim revalidation

Both directions already had a policy revalidation immediately before Telegram
transport. P6-E extends that revalidation with a durable block lookup.

This closes the race:

```text
Prepare
  ↓
TaskEngine queue
  ↓
delivery claim
  ↓
owner executes /relay ban
  ↓
RevalidatePrepared
  ↓
ErrVisitorBlocked
  ↓
release claim
  ↓
NO Telegram side effect
```

The same fence applies to:

- visitor → owner forwarding;
- owner → visitor bot-authored text/media copy.

## /relay status

`/relay` and `/relay status` report bounded repository/runtime state:

- runtime enabled/disabled state;
- known Assistant audience count;
- durable blocked visitor count;
- stored reply mapping count;
- stored durable delivery intent count.

This is a read-only status surface; P6-E does not introduce a persistent
enable/disable setting. Application composition still determines whether PM
Relay is enabled for the Assistant runtime.

## /who

`/who` must reply to a live relayed owner message.

It reports:

- visitor Telegram user ID;
- original visitor message ID;
- owner relay message ID;
- current relay policy: allowed/blocked;
- block timestamp/reason when blocked;
- audience first/last seen timestamps when available.

No profile RPC is required. The durable relay mapping is the identity
authority.

## Ban/unban targeting

`/relay ban` and `/relay unban` support two targeting modes:

1. explicit positive Telegram user ID;
2. omitted user ID while replying to a live relayed owner message.

For ban, remaining arguments are stored as an optional durable reason.

Examples:

```text
/reply-to-relayed-message
/relay ban spam

/relay ban 123456789 repeated abuse
/relay unban 123456789
```

## Blocked list

`/relay blocked` returns a bounded page of at most 5 durable blocks sorted by
visitor user ID. Reasons are previewed at a bounded length; `/who` remains the
surface for the full stored reason. This keeps the rendered Telegram response
well below the message-size ceiling even for heavily escaped HTML input.

Pagination is keyset-based:

```text
/relay blocked <after_user_id>
```

No background scan/ticker is added.

## Acceptance gates

P6-E tests freeze these invariants:

- block rows survive repository reconstruction/restart;
- block capacity fails closed without evicting live blocks;
- repeated block updates remain possible at capacity;
- unblock is idempotent;
- `pmrelay.002` is idempotent and schema-verified;
- blocked visitors are rejected before visitor TaskEngine admission;
- owner replies to blocked mapped visitors fail closed;
- visitor → owner delivery rechecks block policy after claim;
- owner → visitor delivery rechecks block policy after claim;
- late block releases the active delivery claim and prevents Telegram transport;
- `/relay` and `/who` are canonical owner/self/private Assistant commands;
- `/who` is reply-only;
- Assistant canonical command routing preserves reply identity;
- reply-based ban resolves through the durable relay mapping;
- `/who` surfaces current blocked policy;
- unban removes the durable block;
- status and blocked-list surfaces are bounded.

## Explicit non-goals

P6-E does not implement:

- force-sub policy;
- proactive audience broadcast;
- public/non-owner relay management;
- a second PMPermit-backed blocklist;
- persistent relay on/off settings;
- background block cleanup;
- automatic block expiry;
- semantic media types not already supported by P6-D.

Force-sub and audience integration can build on this control-plane boundary in
later P6 phases without changing delivery identity or TaskEngine admission.


## Later audience integration

P6-F expands `assistant_audience_members` from relay-observed visitors into the
shared Assistant audience registry for successful `/start`, inline, deep-link,
and relay entry points.

The P6-E visitor block remains deliberately scoped to the PM Relay data plane.
A relay-blocked visitor is therefore not implicitly removed from the generic
Assistant audience and is not a global Assistant ban.
