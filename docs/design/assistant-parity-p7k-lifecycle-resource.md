# Assistant Parity P7-K — lifecycle and resource hardening

## Status

P7-K is source/design complete for the Assistant group plane P7-A through P7-J.

P7-K does not introduce a new runtime. It hardens the existing canonical group path around:

- bounded cache/state cardinality;
- shared TaskEngine admission and per-chat backpressure;
- bounded queue age and execution lifetime;
- Telegram RPC/FloodWait pressure;
- shutdown/cancellation;
- lazy settings/role state;
- high-cardinality group/topic/service-update behavior;
- idle footprint.

Repository-wide execution evidence and high-cardinality benchmarks remain P7-L work.

## Execution authority

All asynchronous group-plane work continues to use the shared TaskEngine.

The aggregate quota owner for group work is now:

```text
telegram:chat:<chat_id>
```

This owner is shared by:

- canonical Assistant group commands;
- dynamic Assistant SavedResponse group commands;
- P7-I group-rule execution;
- filter continuations and filter delivery;
- GroupService EventBus tasks.

Topic identity remains an ordering concern rather than a quota-cardinality concern:

```text
quota owner:
telegram:chat:<chat_id>

ordering:
chat:<chat_id>
chat:<chat_id>:topic:<topic_id>
```

Therefore thousands of users/topics in one chat cannot each manufacture a separate TaskEngine fairness budget.

Current default TaskEngine admission bounds are:

```text
per owner:
  max waiting: 50
  max active:  10
  max queued payload: 50 MiB

runtime pool defaults:
  interactive:   backlog 128 / queued payload 50 MiB
  general:       backlog 200 / queued payload 100 MiB
  download:      backlog 50  / queued payload 200 MiB
  media-process: backlog 20  / queued payload 200 MiB
  scheduler:     backlog 100 / queued payload 50 MiB
```

Owner counters and ordering locks are compacted/deleted after waiting/active work reaches zero. Historical chat/topic keys are not retained as permanent TaskEngine state.

## Queue age and execution lifetime

Admission capacity alone is not sufficient: accepted interactive work must not execute arbitrarily late.

P7-K now applies bounded queue age:

```text
canonical group command:      10s
dynamic group SavedResponse:  10s
P7-I group rule message:       5s
filter continuation:           <= its execution timeout
GroupService EventBus task:    <= subscriber timeout (5s in current service)
```

Canonical group commands without an explicit command timeout now receive a 30-second TaskEngine execution timeout, matching the normal CommandExecutor default instead of running indefinitely.

This is separate from fresh contextual authorization. A queued mutation is still revalidated at the authorization/RPC boundary; queue expiry additionally prevents stale UX/work from occupying the runtime.

## P7-A/C — contextual identity/authz

Contextual principal/group execution objects are occurrence-scoped. They do not own background workers or cardinality-indexed registries.

Authorization-sensitive operations continue to require fresh role verification at the relevant persistence/RPC boundary.

P7-L re-audit found one delayed persistence edge in P7-I media filters: the admitted command could enqueue a download continuation and the continuation previously persisted the captured filter without another Telegram role check. The continuation now carries only a lightweight contextual authorization guard and executes a fresh role verification immediately before the durable filter replacement. A revoked/failed role aborts persistence and cleans up captured media. This preserves the P7-K lifecycle goal without retaining the full invocation context.

## P7-B — group-role cache and verification pressure

The Telegram group-role resolver is bounded and lazy:

```text
role cache capacity:       4096 entries
fresh verification slots: 32
admin/creator TTL:         30s
member TTL:                2m
left/banned TTL:           30s
```

It owns no goroutine, ticker, cleanup loop, or wait queue.

When all verification slots are in use, new verification fails fast with `ErrGroupRoleSaturated`; it does not create an unbounded waiter. Verification failures are not cached. Successful entries use bounded LRU eviction.

## Peer state

Assistant peer/access-hash cache is bounded:

```text
max entries: 4096
TTL:         30m
```

Capacity pressure reclaims expired entries and otherwise evicts one cache entry without a full-map LRU victim scan on every insertion. The peer cache is an optimization, not an authorization fence.

## P7-F — durable group state

SQLite GroupState is explicitly bounded:

```text
default rows: 10,000
hard max:     50,000
default cleanup batch: 64
hard cleanup batch:    512
value max:             64 KiB
```

The store owns no cache, goroutine, ticker, or periodic polling loop. Expired cleanup is bounded and occurrence-driven.

## P7-H — group event state and lifecycle

Welcome/goodbye state previously retained disabled durable configuration in the hot `chats` map. P7-K changes this contract:

- enabled configuration remains hot for event delivery;
- disabled configuration remains durable only;
- status reads disabled configuration lazily through `StateContext`;
- disabling the last active feature removes that chat from the hot map;
- `Close` clears hot config and feature-interest snapshots.

This keeps resident template memory proportional to enabled features instead of historical configuration churn.

### Transport-aware EventBus subscription

The shared GroupService EventBus subscription now exists only when all of these are true:

```text
service loaded
AND at least one group-event feature enabled
AND Assistant transport attached
AND service not closed
```

`SetTransport(nil)` detaches the subscription. Reattaching transport restores the subscription from durable/interest state without a poller or restart scan.

This prevents an offline Assistant from retaining a useless active GroupService subscriber.

### Bounded service-message payload

A Telegram membership service update can contain a list of users. P7-K caps application-level materialization at:

```text
64 users/event
```

The event separately carries the reported user count so templates can still render `{count}` and “and N more” without retaining every user object.

The renderer still materializes only eight display labels.

## P7-I — anti-spam/filter/warning state

Current hard bounds remain:

```text
blacklist:
  rules/chat:          512
  active chats:        50,000
  compiled chat cache: 500

filters:
  rules/chat:          512
  active chats:        50,000
  compiled chat cache: 500
  cooldown entries:    1,000
  rule lock stripes:   64

warnings:
  threshold hard max: 16
  reason bytes:       1,024
  global rows:        50,000
  lock stripes:       64
```

Ordinary messages still stop at feature-interest routing before expensive role verification/rule compilation when no rule domain is active.

P7-K also fixes a production admission bug in filter continuations: their WorkSpec previously had a topic ordering key but no `QuotaOwner`. They now use the canonical chat quota owner and a bounded queue deadline.

## P7-J — topic/cardinality behavior

Forum topic IDs are not retained in a permanent registry.

Topic identity appears only in transient TaskEngine ordering keys:

```text
chat:<chat_id>:topic:<topic_id>
```

Ordering locks are deleted at task terminal state.

Text/media topic delivery remains fail-closed. P7-K additionally closes fallback paths in filter and group-rule adapters: if the transport cannot preserve a non-zero `TopicID`, delivery returns `ErrUnavailable` instead of falling back to a plain send.

## EventBus pressure

EventBus is globally bounded and starts no dispatch worker at construction/startup.

Current bounds:

```text
normal queue:             1024
priority queues:           256 each
ordered partitions:          4
ordered partition queue:    256
max general workers:          8
idle retirement:            30s
```

Workers start on accepted work and retire after idle time.

GroupService events implement `QuotaOwnedEvent`, so EventBus->TaskEngine delivery uses `telegram:chat:<chat_id>` rather than the global subscriber owner `assistant:groupevents`.

GroupService tasks also receive a bounded queue deadline.

## Telegram RPC / FloodWait

Assistant Telegram API calls remain routed through the application-owned shared `RPCExecutor`.

The production hierarchical RPC limiter is bounded:

```text
max buckets:   4096
max penalties: 4096
max penalty:   24h
```

Capacity saturation fails closed. It does not drop depleted buckets or active FloodWait penalties merely to admit a new identity. Penalty overflow is promoted to an account-wide cooldown.

For Assistant non-idempotent mutations, inline FloodWait is capped at 5 seconds and attempts are limited to one. Shared RPC timeout/MaxElapsed logic bounds other waits as well.

Role verification, group queries, group mutations, reply lookup, interaction sends, and group-event sends all use managed Assistant API/interaction paths backed by the same executor in production.

## Cancellation and shutdown

Assistant ingress checks the shared shutting-down flag before accepting new callback/inline/message work.

Shutdown ownership remains:

```text
quiesce ingress
    ↓
single App shutdown deadline
    ↓
runtime / TaskEngine / EventBus drain-stop
    ↓
transport cancellation
```

The App owns one deadline across teardown phases.

Assistant client exit clears:

- GroupEvent transport;
- GroupRule transport;
- GroupRule role resolver.

With the P7-K transport-aware GroupEvent lifecycle, detaching transport also removes the GroupService EventBus subscription.

Queued group work is additionally bounded by queue deadlines and execution timeouts, so shutdown is not dependent on permanent chat/topic workers.

## Idle footprint

P7-K introduces no:

- per-group goroutine;
- per-topic goroutine;
- per-user worker;
- periodic per-chat poll;
- cleanup ticker for role/group-state/group-event state;
- second TaskEngine;
- second Telegram RPC executor.

The group plane's long-lived state is bounded maps/snapshots plus existing shared global runtimes.

## High-cardinality behavior

For many historical groups/users/topics:

- role cache stops at 4096;
- peer cache stops at 4096;
- durable GroupState stops at configured row capacity;
- compiled blacklist/filter caches stop at 500 chats/domain;
- filter cooldown stops at 1000;
- warnings stop at 50,000 rows;
- Telegram limiter stops at 4096 buckets/penalties;
- TaskEngine backlog is bounded per runtime pool (interactive 128, general 200, download 50, media-process 20, scheduler 100);
- per-chat waiting work stops at 50;
- topic ordering locks disappear when tasks finish;
- service-message user materialization stops at 64;
- disabled group-event templates are not retained in hot memory.

Normal irrelevant group traffic does not create chat/topic workers or cache entries merely because a new chat/topic ID was observed.

## Source fences

P7-K architecture/regression coverage now checks:

- role/peer/cache cardinality;
- durable GroupState limits;
- P7-I rule/warning bounds;
- per-chat TaskEngine quota ownership;
- topic ordering plus queue deadlines;
- default group execution timeout;
- EventBus per-chat GroupService quota;
- filter continuation quota ownership;
- filter/group-rule topic fail-closed fallback;
- transport-aware GroupEvent subscription;
- bounded service-event user payload;
- TaskEngine owner/ordering-state compaction;
- shared RPC executor + bounded limiter;
- single-deadline shutdown;
- absence of per-chat workers/tickers in group-plane state services.

## P7-K / P7-L boundary

P7-K closes lifecycle/resource design and source hardening.

P7-L remains responsible for repository-wide acceptance evidence:

- full behavior matrix;
- build/test/race evidence;
- high-cardinality group/user/topic tests;
- idle goroutine/RSS observations;
- hot-path benchmarks for irrelevant group messages;
- admission/backpressure stress;
- topic isolation under concurrency;
- verification outage and FloodWait scenarios.

No benchmark result is claimed by this document.
