# Assistant Parity P6-G — optional force-sub policy

## Status

P6-G adds an optional subscription-membership gate to the Assistant PM Relay visitor → owner path.

The feature is deliberately scoped to relay admission. It does not redefine the generic Assistant audience registry and it does not reuse PMPermit state.

Default state after migration:

~~~
disabled
failure_mode = fail-closed
revision = 1
~~~

Enabling fail-open behavior requires an explicit owner configuration change.

## Durable configuration

Migration pmrelay.004 creates one durable singleton:

~~~
pm_relay_force_sub_config
├── singleton_id = 1
├── enabled
├── channel_username
├── join_url
├── failure_mode     closed | open
├── revision
└── updated_at
~~~

Configuration updates use revision CAS:

~~~
read revision N
      ↓
write revision N+1
WHERE revision = N
~~~

A stale writer receives ErrForceSubConfigConflict and cannot overwrite a newer policy.

The PM Relay service keeps one process-local config snapshot to avoid a DB read for every visitor message. Successful owner configuration updates update that snapshot immediately. A CAS conflict discards the local snapshot so the next read reloads durable state.

## Channel identity and join guidance

P6-G currently targets a public Telegram channel or supergroup username.

@Required_Channel is normalized to required_channel. When no join URL is supplied, the canonical guidance link is https://t.me/required_channel.

An explicit join URL is accepted only in the Telegram https://t.me/... namespace. The public username is the membership-verification identity; the join URL is presentation data only.

## Owner control surface

P6-G extends the existing canonical owner/private Assistant command namespace:

~~~
/relay forcesub
/relay forcesub status
/relay forcesub set @channel [closed|open] [join_url]
/relay forcesub off
~~~

Omitting the failure mode defaults to closed. Disabling force-sub also resets the stored failure mode to closed. /relay status includes the active force-sub channel and failure mode.

## Telegram membership verification

Production verification uses the Assistant bot MTProto connection and the existing managed RPC executor.

The RPC chain is:

~~~
contacts.resolveUsername(@configured_channel)
        ↓
InputChannel(channel_id, access_hash)
        ↓
resolve visitor InputPeerUser
        ↓
channels.getParticipant(channel, visitor)
~~~

Both Telegram methods are shared read-only RPCs, so they use the same limiter/retry/metrics executor as the rest of Assistant Telegram traffic.

USER_NOT_PARTICIPANT is an authoritative negative membership result, not a verification outage.

Participant interpretation:

~~~
ChannelParticipantLeft               -> non-member
ChannelParticipantBanned(left=true)  -> non-member
ChannelParticipantBanned(left=false) -> member/restricted member
ordinary/self/admin/creator          -> member
~~~

A nil/invalid resolved peer, missing channel access hash, resolver failure, Telegram permission error, exhausted FloodWait, timeout, transport failure, or other RPC failure is a verification error.

## Explicit failure modes

### fail-closed

This is the default.

On verification failure:

~~~
verification error
      ↓
visitor relay denied
      ↓
no relay Telegram forward
      ↓
explicit verification-unavailable guidance
~~~

The error remains observable as ErrForceSubVerify.

### fail-open

This mode must be configured explicitly by the owner.

On verification failure the system logs a warning and may continue visitor relay.

Fail-open applies only to verification failures. A definite USER_NOT_PARTICIPANT result is still denied and receives join guidance even when failure mode is open.

## Bounded membership cache

The verifier keeps no durable per-user membership table.

It uses a process-local bounded O(1) map + LRU list:

~~~
capacity           = 2048 visitors
member TTL         = 10 minutes
non-member TTL     = 1 minute
verification error = never cached
~~~

Positive membership is cached longer to reduce Telegram traffic. Negative membership is deliberately short-lived so a visitor who joins after guidance can retry quickly.

A force-sub config revision change resets cached channel identity, membership cache, and guidance cooldown cache. No full-map cleanup runs on every request.

### Cache consistency window

Force-sub is a bounded-cache policy, not a per-message authoritative membership transaction.

A visitor who leaves the required channel may remain accepted until the positive cache entry expires, at most the member TTL. A non-member who joins is normally recognized after the shorter negative TTL. Owner config changes invalidate cache immediately via revision.

## Bounded verification concurrency

Cache misses are globally bounded:

~~~
maximum concurrent membership verification chains = 32
~~~

Slot acquisition is non-blocking. If all slots are occupied, verification is considered unavailable:

- fail-closed denies relay;
- fail-open allows relay only because the owner explicitly selected that outage behavior.

This prevents a high-cardinality visitor burst from turning force-sub into unbounded concurrent Telegram RPC work. There is no verifier goroutine, ticker, worker pool, or background scanner.

## Relay admission order

Visitor path:

~~~
private visitor message
        ↓
PM Relay classification
        ↓
relay enabled?
        ↓
durable visitor block policy
        ↓
force-sub membership check
        ↓
allowed?
    ├── no  → optional guidance work only
    └── yes → submit normal relay WorkSpec
~~~

A definite non-member therefore creates zero normal relay WorkSpecs, zero DeliveryIntent rows, zero relay mappings, zero relay audience touches, and zero visitor → owner Telegram forwards.

## Guidance execution

Guidance uses the Assistant bot and contains a clear membership-required or verification-unavailable message, configured channel username, Telegram URL button, and instruction to resend after joining.

Guidance WorkSpec:

~~~
scope        service:pmrelay
quota owner  pmrelay:guidance:<visitor>
pool         interactive
priority     interactive
ordering     pmrelay:thread:<visitor>
timeout      10s
~~~

Guidance has an additional bounded cooldown:

~~~
capacity = 2048 visitors
cooldown = 1 minute per visitor/config revision
~~~

Before an admitted guidance task sends anything, it re-runs the force-sub gate. If the owner disabled force-sub, changed channel/revision, or the visitor is now allowed, the stale guidance is suppressed. The cooldown is then claimed inside admitted execution, not before TaskEngine submission. Rejected guidance work therefore does not suppress a later valid attempt. While cooldown is active, later denied updates skip creating another guidance WorkSpec.

No background cleanup is needed; expiry is lazy and bounded eviction is O(1).

## Revalidation fences

P6-G checks force-sub three times on an allowed visitor path:

~~~
1. before normal relay TaskEngine submission
2. inside admitted relay handler
3. through guarded visitor transport immediately before ForwardVisitor
~~~

The final transport check runs after durable delivery intent/claim work in ExecuteVisitor.

If denied at that final fence:

~~~
delivery claim exists
       ↓
force-sub transport check denies
       ↓
NO messages.forwardMessages
       ↓
ExecuteVisitor releases claim
       ↓
DeliveryIntent remains retryable
~~~

Repeated checks still obey the bounded membership-cache TTL.

## Separation from audience registry

Force-sub does not read, remove, or mutate assistant_audience_members or assistant_audience_membership_order.

A non-member may still be a valid generic Assistant audience member because they used /start, inline, or deep-link. Audience membership is not proof of channel membership. Only a successful relay adds AudienceSourceRelay.

## Separation from PMPermit

P6-G does not use PMPermit settings, warning counts, approvals, block state, or user-account PM protection decisions.

PMPermit protects the user account PM plane. Force-sub protects the Assistant PM Relay visitor → owner plane. They have different principals, transport paths, persistence, and policy meaning.

## Owner → visitor replies

P6-G gates visitor → owner relay ingress. It does not prevent the owner from replying to an already-created durable mapping. Reverse delivery remains governed by relay enabled state, mapping validity, and the P6-E durable visitor block policy.

## Acceptance gates

P6-G tests freeze:

- pmrelay.004 schema and disabled/fail-closed singleton default;
- durable config reconstruction;
- CAS revisions and stale-writer protection;
- config cache reload after CAS conflict;
- username/default join URL normalization;
- unsafe config rejection;
- canonical owner controls with default fail-closed and explicit fail-open;
- managed membership RPCs through shared Assistant RPC execution;
- member/non-member TTL behavior;
- revision invalidation of channel/member/guidance caches;
- nil username-resolution safety;
- definite USER_NOT_PARTICIPANT handling;
- fail-open versus fail-closed verification-error behavior;
- bounded 2048 membership cache;
- bounded 32 concurrent verification chains;
- bounded/revision-aware one-minute guidance cooldown;
- guidance policy is revalidated again before the join message is sent;
- no invalid guidance when durable policy read fails;
- non-members skip normal relay admission;
- admitted relay work rechecks force-sub;
- post-claim/pre-forward denial performs zero Telegram forward and releases the delivery claim.

## Non-goals

P6-G does not add global Assistant account bans, audience filtering/opt-out, PMPermit integration, persistent per-user membership rows, background membership polling, join-request approval automation, automatic joining, multiple simultaneous required channels, force-sub on owner → visitor replies, or force-sub on /start/inline/deep-link/unrelated Assistant commands.

Any later expansion should build on the explicit ForceSubPolicy boundary rather than overload audience or PMPermit semantics.
