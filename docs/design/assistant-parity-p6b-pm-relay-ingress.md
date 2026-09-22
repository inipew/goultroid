# Assistant Parity P6-B — explicit PM Relay ingress and admission boundary

## Status

P6-B attaches PM Relay classification to the Assistant update path without
attaching Telegram relay delivery.

At the P6-B milestone the production PM Relay service was constructed and wired
but intentionally left disabled. P6-C now supersedes that dormant state by
activating the service when an Assistant runtime exists and installing the
durable visitor delivery plane.

There is no new goroutine, ticker, polling loop, Telegram dispatcher, callback
router, or plugin registry.

## Separation of responsibility

P6-B deliberately splits the path into two layers:

```text
Assistant client
    |
    | Telegram update classification
    v
client.RelayIngress
    |
    | Prepare (read-only)
    | TaskEngine admission
    v
pmrelay.Service.RevalidatePrepared
    |
    | revalidate policy/mapping
    v
(no transport action in P6-B)
```

`pmrelay.Service` has no Telegram transport dependency.

`client.RelayIngress` has no repository knowledge. Its only execution authority
is the shared `tasks.Client`.

## Assistant new-message precedence

The explicit precedence is:

```text
shutdown barrier
    ↓
slash command/control plane
    ├─ /cancel → a2 input cancellation first
    └─ all other slash commands → command router
    ↓
owner reply to durable relay mapping
    ↓
generic a2 AwaitInput / TakeInput
    ↓
visitor PM relay fallback
```

Unknown slash commands stop at the command boundary and are never reclassified
as relay traffic.

Owner mapped replies outrank generic input so an owner replying to a relayed
visitor cannot accidentally submit that reply as a pending Settings/MyXL input.

Generic a2 input outranks visitor fallback so a visitor interacting with an
explicit input session does not simultaneously create PM relay work. An input
classification error also fails closed and never falls through into PM Relay;
only a clean "not handled" result may continue to the visitor fallback.

PM Relay classification is otherwise independent from a2 presentation and peer
resolution. If the interaction presentation/resolver path is unavailable, a
private non-command message may still reach RelayIngress because relay admission
uses only the transport-neutral message identity at this phase.

Only private `PeerUser` messages may reach either PM relay classification path.

## Prepare contract

The transport-neutral message identity is:

```text
sender_id
chat_id
message_id
reply_to_message_id
```

Visitor prepare is accepted only when:

- relay policy is enabled;
- owner ID is configured;
- sender is not the owner;
- `chat_id == sender_id`, preserving private-chat semantics.

Owner-reply prepare is accepted only when:

- relay policy is enabled;
- sender/chat are the configured owner;
- the message is a reply;
- the replied-to owner message has a durable, unexpired P6-A mapping.

A repository failure while classifying a possible owner relay reply fails closed:
the message is claimed by the relay boundary with an error instead of falling
through into generic AwaitInput.

## Admission authority

Each prepared relay becomes one TaskEngine `WorkSpec`:

```text
Scope:            service:pmrelay / generation 1
QuotaOwner:
  visitor→owner:  pmrelay:visitor:<visitor-id>
  owner→visitor:  pmrelay:owner-reply:<visitor-id>
Pool:             interactive
Class:            interactive
OrderingKey:      pmrelay:thread:<visitor-id>
ExecutionTimeout: 15s
Resources:        none in P6-B
```

Task IDs are unique per admission:

```text
asst:relay:<direction>:<source-chat>:<source-message>:<sequence>
```

TaskEngine terminal IDs are retained for a bounded period, so they must not own
PM Relay idempotency. A failed logical delivery must be retryable immediately;
P6-A `pm_relay_deliveries` remains the authoritative source-message
idempotency/claim boundary.

Visitor and owner-reply work use separate quota lanes so an inbound flood cannot
consume the owner's reply admission budget. Both directions still use the same
visitor ordering key, preserving conversation order without globally serializing
independent visitors.

A TaskEngine submission failure never calls `RevalidatePrepared`.

## Revalidation fence

Prepare captures the current relay policy revision. `RevalidatePrepared` rejects
work when:

- relay was disabled after prepare;
- relay was disabled and re-enabled, changing policy revision;
- prepared coordinates are inconsistent;
- an owner reply mapping disappeared;
- the mapping expired;
- the durable mapping no longer matches the prepared mapping identity.

Therefore queued work does not gain authority merely because it was valid at
ingress time.

P6-B does not persist a delivery intent and does not touch Assistant audience.
Those operations belong to P6-C because they must occur inside admitted,
revalidated execution.

## Shutdown

The existing Assistant shutdown barrier runs before PM Relay preparation.
Quiescing Assistant therefore creates no new prepared relay work.

Existing admitted work is owned by TaskEngine and follows the global TaskEngine
shutdown/cancellation semantics.

## P6-B production wiring

P6-B established this composition boundary:

```text
pmrelay.SQLiteRepository
        ↓
pmrelay.Service(enabled=false)
        ↓
Assistant.SetRelayIngress
        ↓
client.RelayIngress
```

The service itself still defaults to disabled when constructed directly.
P6-C activates it only when the application has an Assistant runtime and
attaches durable visitor-to-owner delivery after the P6-B revalidation boundary.

## Acceptance gates

Regression tests freeze:

- TaskEngine rejection cannot execute prepared relay work;
- relay work carries service scope, direction-aware per-thread quota,
  interactive class and per-thread ordering;
- visitor flood cannot consume the owner-reply quota lane;
- both relay directions share the same visitor ordering key;
- repeated source admission gets a fresh TaskEngine ID while retaining the same
  thread ordering key;
- policy revision changes stale queued prepared work;
- owner reply mapping is re-read at execution;
- missing/pruned mapping fails closed;
- mapped owner reply outranks generic input;
- generic input outranks visitor fallback;
- input classification errors fail closed instead of becoming relay traffic;
- visitor fallback does not depend on a2 presentation or peer resolution;
- slash and unknown-slash commands never enter relay;
- `/cancel` remains a2 input control;
- shutdown rejects relay before prepare.

## Explicit non-goals

P6-B does not implement:

- Telegram forward/copy/send;
- delivery-intent creation or claim;
- Assistant audience updates;
- media handling;
- visitor block/ban policy;
- force-sub;
- relay settings/control UI;
- startup delivery recovery.

Those belong to P6-C and later phases.
