# Assistant Parity P6-H — PM Relay durability, resource, and lifecycle acceptance

## Status

P6-H is the closing acceptance phase for Assistant PM Relay P6-A through P6-G.

It does not add a new relay feature surface. It audits and freezes the
cross-phase durability, bounded-resource, shutdown, migration, and recovery
contracts that must hold before P6 can be considered source/design complete.

## Accepted architecture

The final P6 data plane remains:

```text
Assistant update
    ↓
canonical command / explicit a2 precedence
    ↓
PM Relay fallback
    ↓
visitor block policy
    ↓
optional force-sub gate
    ↓
TaskEngine admission
    ↓
policy/mapping/block/force-sub revalidation
    ↓
durable DeliveryIntent + claim lease
    ↓
final pre-transport revalidation
    ↓
Telegram durable random_id transport
    ↓
CommitDelivery
    ↓
visitor direction only:
  EnsureMapping
  TouchAudience
```

There is still one TaskEngine and one Telegram RPC execution policy. P6 does not
own a second worker pool, retry scheduler, broadcast engine, or background
recovery poller.

## Crash-window matrix

| Failure window | Durable state | Retry/restart behavior | Duplicate Telegram side effect |
| --- | --- | --- | --- |
| before DeliveryIntent | none | source occurrence may retry from prepare/admission | none |
| after intent, before claim | persisted random_id | later occurrence claims same intent | none |
| after claim, before transport | live claim lease | concurrent occurrence fenced; stale lease reclaimable after TTL | none |
| definite transport failure | intent retained, claim normally released | later occurrence reuses same random_id | same logical send identity |
| Telegram success, CommitDelivery failure | intent + random_id + ambiguous live claim | after lease expiry a later occurrence reclaims and sends the same random_id | Telegram sees same logical mutation |
| CommitDelivery success, mapping finalize failure | completed visitor delivery | later occurrence heals mapping from completed target message ID without transport | no second transport |
| mapping success, audience finalize failure | completed delivery + mapping | later occurrence idempotently keeps mapping and heals audience | no second transport |
| owner send success, CommitDelivery failure | owner delivery intent + random_id + claim | later occurrence/restart reuses same random_id | same logical bot-authored send |
| completed delivery duplicated | completed target message ID | finalization only; transport is skipped | no |
| completed delivery past retention | terminally expired | no mapping resurrection | no |

P6-H adds acceptance tests that reconstruct a new `Service` instance around the
same SQLite database. Recovery therefore does not depend on process-local claim,
random-ID, mapping, or audience memory.

### Recovery trigger contract

P6 intentionally does **not** introduce proactive startup replay/scanning of
pending delivery intents. That was already an explicit P6-D non-goal.

Recovery is occurrence-driven:

```text
durable intent survives
        ↓
duplicate/retried source occurrence
        ↓
same delivery key
        ↓
same durable random_id
        ↓
claim/recovery/finalization
```

This means P6 guarantees idempotent, restart-safe recovery when the logical
source is retried/re-observed; it does not guarantee autonomous progress for an
orphaned pending occurrence with no future trigger.

That tradeoff is accepted for P6 because it preserves zero PM Relay polling
cost while keeping ambiguous external side effects duplicate-safe.

## Claim leases and forced cancellation

Delivery claims have a hard maximum lease of:

```text
5 minutes
```

Normal transport/revalidation failures release the matching claim immediately.

A forced shutdown may cancel the TaskEngine context before the best-effort
SQLite `ReleaseDelivery` can execute. P6-H treats the persisted claim lease as
the final crash fence:

```text
task canceled after claim
        ↓
ReleaseDelivery may observe canceled context
        ↓
claim remains durable
        ↓
lease expires
        ↓
restart/retry reclaims intent
        ↓
same durable random_id
```

Acceptance coverage explicitly exercises this path.

## Shutdown ordering

Application shutdown uses one App-owned 30-second hard deadline.

P6-H adds a soft Assistant quiesce boundary.

The relevant sequence is now:

```text
App begins quiesce
    ↓
main dispatcher ingress closes
    ↓
Runtime reverse dependency shutdown
    ↓
Assistant.Quiesce
  shuttingDown = true
  bot MTProto connection stays alive
    ↓
TaskEngine.Quiesce + Drain
  no new relay admission
  already-admitted relay work may still use Assistant transport
    ↓
Assistant.Stop
    ↓
TaskEngine/dependencies stop
    ↓
Runtime complete
    ↓
main Telegram transport cancelled/joined
    ↓
database closes according to dependency graph
```

Assistant declares runtime dependencies on:

```text
dispatcher
settings
taskengine
```

so it is quiesced/stopped before those dependencies.

The soft quiesce is deliberately flag-only. Cancelling the Assistant transport
at quiesce time would break already-admitted PM Relay work during TaskEngine
drain.

## Durable state bounds

PM Relay persistent cardinality is explicitly bounded.

| State | Default cap | Hard cap | Reclamation |
| --- | ---: | ---: | --- |
| relay mappings | 16,384 | 65,536 | expired rows, bounded lazy prune |
| delivery intents | 32,768 | 131,072 | expired rows, bounded lazy prune; active claims protected |
| Assistant audience | 10,000 | 50,000 | stale members, bounded lazy prune |
| visitor blocks | 10,000 | 50,000 | explicit unblock; live rows never evicted |
| force-sub config | 1 singleton | 1 | CAS revision update |

Retention defaults:

```text
mapping:  30 days
delivery: 30 days
audience: 180 days
```

Prune calls are bounded; the service uses small batches and the repository hard
limit is `MaxPruneBatch = 256`.

Capacity exhaustion never authorizes eviction of live mapping, delivery,
block-policy, or active-claim state.

## Force-sub cache and RPC bounds

Force-sub remains an in-memory read-through policy cache, not another durable
per-user registry.

```text
membership cache:          2,048
guidance cooldown cache:   2,048
max concurrent verification RPC paths: 32
positive TTL:              10 minutes
negative TTL:               1 minute
guidance cooldown:          1 minute
verification errors:        not cached
```

Eviction is O(1) through bounded map/list state. A force-sub configuration
revision invalidates channel identity, membership, and guidance cache state
without a hot-path full-map scan.

On a cache miss, verification uses the Assistant managed RPC executor:

```text
contacts.resolveUsername
channels.getParticipant
```

Therefore the global/family/method limiter, retry classification, metrics, and
timeouts remain shared with the rest of Assistant MTProto traffic.

When all 32 verification slots are occupied, a new verifier does not wait for an
unbounded local queue and does not issue another Telegram RPC. The configured
failure mode is applied immediately:

- `closed`: fail closed;
- `open`: allow only because the owner explicitly configured fail-open.

A definite `USER_NOT_PARTICIPANT` result is never converted into fail-open.

## TaskEngine pressure

PM Relay transport work is admitted to the existing interactive TaskEngine
pool.

Per visitor:

```text
quota owner visitor → owner:
  pmrelay:visitor:<visitor-id>

quota owner owner → visitor:
  pmrelay:owner-reply:<visitor-id>

ordering:
  pmrelay:thread:<visitor-id>
```

Thus both directions for one visitor serialize on the same thread ordering key,
while unrelated visitors remain independently schedulable.

Blocked visitors and force-sub non-members do not create a normal relay
WorkSpec. Force-sub guidance, when due, is a separate bounded interactive task
with its own per-visitor quota identity and the same thread ordering key.

Guidance cooldown is claimed inside admitted execution. TaskEngine admission
rejection therefore cannot burn the cooldown.

## Broadcast pressure

P6-F does not create a PM Relay fan-out executor.

Assistant audience broadcast uses the existing Broadcast service and its
`TargetSource` contract:

```text
snapshot watermark
    ↓
bounded keyset page
    ↓
existing Broadcast service
    ↓
max 64 accepted tickets in flight
    ↓
TaskEngine background priority
```

New audience membership cannot leak into a running snapshot. A snapshotted
member reclaimed by retention is counted as failed rather than silently
shrinking the terminal report.

## Idle resource footprint

PM Relay itself has no background goroutine, ticker, periodic cleanup loop,
membership poller, delivery replay scanner, or separate worker pool.

Cleanup/reconciliation is demand-driven:

- delivery/mapping/audience reclamation only on bounded capacity pressure;
- force-sub membership verification only on visitor traffic and cache miss;
- force-sub cache expiry is lazy;
- guidance expiry is lazy;
- delivery recovery is occurrence-driven.

P6-H adds an architecture regression test that rejects `go` statements and
`time.NewTicker`, `time.Tick`, or `time.AfterFunc` in the PM Relay service
and force-sub/relay-ingress implementation surfaces.

This gate is intentionally structural rather than a flaky goroutine-count test.

## Migration acceptance

PM Relay schema history is append-only:

| Migration | Purpose |
| --- | --- |
| `pmrelay.001` | mappings, delivery intents, Assistant audience |
| `pmrelay.002` | durable visitor blocks |
| `pmrelay.003` | stable audience membership sequence |
| `pmrelay.004` | durable force-sub singleton policy |

Acceptance covers:

- fresh database migration;
- repeated migration execution/idempotency;
- schema invariant verification;
- P6-A-only database upgraded through P6-G;
- existing mapping and delivery preservation;
- existing audience preservation;
- P6-F membership-order backfill;
- P6-G default force-sub singleton creation;
- all four feature migration records retained.

Migration checksums remain immutable history.

## P6-H acceptance matrix

| Area | Acceptance condition | Source gate |
| --- | --- | --- |
| visitor durable identity | retry/restart reuses persisted random_id | P6-C + P6-H restart tests |
| owner durable identity | retry/restart reuses persisted random_id | P6-D + P6-H restart tests |
| ambiguous send crash | live lease prevents concurrent resend; stale lease reclaim works | P6-H restart tests |
| post-commit crash | mapping/audience heal without Telegram resend | P6-H finalize-recovery tests |
| forced cancellation | stale claim remains recoverable after lease TTL | P6-H shutdown-lease test |
| pre-transport policy | relay/block/force-sub revalidated after claim | P6-E/P6-G relay tests |
| admission rejection | no delivery/mapping/transport side effect | RelayIngress acceptance tests |
| lifecycle ingress | Assistant quiesces before TaskEngine drain | P6-H lifecycle tests |
| drain transport | Assistant connection remains alive during soft quiesce | P6-H client quiesce test |
| DB cardinality | mappings/deliveries/audience/blocks bounded, live rows not evicted | repository capacity tests |
| force-sub cache | 2,048 bounded O(1) cache + revision invalidation | force-sub cache tests |
| verification concurrency | maximum 32 active verification paths | force-sub saturation test |
| guidance pressure | bounded cooldown and TaskEngine admission | force-sub guidance tests |
| broadcast pressure | one existing bounded Broadcast engine, 64 in-flight | P6-F/Broadcast tests |
| migration upgrade | P6-A data survives upgrade through `.004` | P6-H migration test |
| idle footprint | no PM Relay goroutine/ticker/poller | P6-H architecture test |
| block/audience separation | block policy does not mutate audience authority | P6-E/P6-F tests |
| PMPermit separation | no PMPermit persistence or policy dependency | architecture/design boundary |

## Accepted limitations

The following are explicit constraints, not hidden guarantees:

1. Pending delivery recovery is trigger-driven, not proactively replayed on
   startup.
2. Force-sub positive membership may be stale for up to the configured
   10-minute cache TTL; P6-G is a bounded-cache policy, not a per-message
   membership transaction.
3. A forced shutdown can leave an active delivery claim until its 5-minute
   lease expires.
4. Unsupported semantic media remains fail-closed rather than being coerced
   into a lossy relay representation.
5. Relay enablement itself remains runtime composition state; durable force-sub,
   blocks, mappings, delivery intents, and audience state survive restart.

These constraints are deliberate P6 scope choices and must remain visible to
later phases.

## Closure criteria

P6 can be considered source/design complete when all of these remain true:

- P6-A durable identities and capacities are preserved;
- P6-B admission precedence is unchanged;
- P6-C visitor delivery remains durable and idempotent;
- P6-D owner replies remain bot-authored and identity-safe;
- P6-E block/control policy revalidates immediately before transport;
- P6-F audience is cross-entry-point and broadcasts through the shared bounded
  service;
- P6-G force-sub remains optional, explicit, bounded, and isolated;
- P6-H restart, shutdown, migration, pressure, and idle-footprint gates remain
  in place.

No additional runtime subsystem is required to close P6.
