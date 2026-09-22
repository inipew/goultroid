# Assistant Parity P7-I — anti-spam, filters, and manager rules

## Scope

P7-I closes the Assistant group rule foundation for:

- chat-scoped blacklist interception;
- chat-scoped automated filters;
- durable warning commands and threshold enforcement;
- rule-interest routing;
- per-chat compiled/indexed matching;
- bounded caches and persistence;
- mutation revision/invalidation;
- Owner/Sudo and Telegram administrator bypass semantics.

P7-I reuses the existing canonical blacklist, filters, admin, moderation, P7-B role resolver, P7-G mutation transport, and shared TaskEngine. It does not create a second rule registry or execution engine.

## Assistant ingress

Ordinary Assistant group text follows:

```text
incoming Telegram message
        ↓
private/slash/broadcast classification
        ↓
Owner/Sudo automation bypass
        ↓
GroupRules.Interested(chat_id)
        ↓
inactive
   → return immediately

active
   ↓
cache Telegram entities
   ↓
shared TaskEngine admission
   ↓
peer resolution inside admitted task
   ↓
authoritative chat-kind classification
   ↓
canonical MessageEnvelope
   ↓
shared P7-I coordinator
```

For an inactive group, the interest gate occurs before:

- global entity cache population;
- peer/access-hash resolution;
- managed channel metadata lookup;
- MessageEnvelope allocation;
- TaskEngine admission;
- Telegram role verification;
- rule compilation/database lookup;
- filter response or blacklist action.

For `PeerChannel`, known entity metadata rejects broadcast channels immediately. If
Telegram omits channel metadata, classification is deferred until after interest
and TaskEngine admission. The admitted path resolves the peer and uses managed
`channels.getChannels` to prove `Megagroup=true`; durable rule interest alone
never reinterprets an unknown channel as a supergroup.

The admitted rule task uses:

```text
QuotaOwner  = telegram:chat:<chat_id>
Pool        = interactive
Class       = interactive
OrderingKey = chat:<chat_id>
timeout     = 5 seconds
```

No P7-I-owned worker pool or TaskEngine exists.

## Canonical rule ownership

The Assistant coordinator does not maintain its own blacklist/filter definitions.

It receives the already-registered canonical plugin instances through the `grouprules.Source` contract:

```text
blacklist plugin ─┐
                  ├─ shared Assistant rule coordinator
filters plugin ───┘
```

Therefore userbot and Assistant rule-management commands mutate the same durable rule rows and invalidate the same compiled per-chat state.

Plugin enable/disable lifecycle is respected before a source contributes chat interest or actions.

## Rule precedence

For an ordinary member message:

```text
blacklist match
    ↓ yes
delete / intercept
    ↓
filters suppressed

blacklist no-match
    ↓
filter match
    ↓
deliver saved response
```

Blacklist is a decision/interception rule and therefore outranks filter automation.

Rule application always re-matches current compiled state after contextual bypass verification so a rule mutation racing the verification RPC cannot execute stale rule data.

## Owner and Sudo bypass

Goultroid Owner/Sudo bypass happens at Assistant ingress before chat-rule
interest, entity-cache population, TaskEngine admission, rule matching, or any
Telegram role RPC. The coordinator repeats the check as defense in depth:

```text
sender is Owner/Sudo
        ↓
automation bypass
        ↓
0 rule-interest lookup
0 entity-cache work
0 TaskEngine admission
0 peer/channel resolution
0 matcher work
0 Telegram role verification
0 automatic delete/reply
```

The Assistant client privileged checker explicitly checks:

1. configured Owner ID;
2. every current Sudo ID.

This bypass is an automation policy only.

It does **not** convert Owner/Sudo into Telegram administrator or satisfy contextual mutation rights.

## Telegram administrator bypass

For non-Owner/Sudo senders:

```text
per-chat compiled matcher
        ↓
no match
   → done, no role RPC

match
   ↓
fresh P7-B role verification
   ↓
administrator / creator
   → bypass automation

ordinary member
   → apply matching rule
```

Role verification is fresh only after an actual rule match.

If Telegram role verification is unavailable or cannot prove the sender is an ordinary member, P7-I fails safe by suppressing automatic moderation rather than risking deletion/action against an administrator.

## Per-chat interest

Blacklist and filters each use `core.ChatFeatureSnapshot`.

Persistent active-chat IDs are loaded once during plugin initialization.

Read-side interest is O(1) and lock-free.

Repository active-chat cardinality is hard-bounded:

| Domain | Maximum active chats |
|---|---:|
| blacklist | 50,000 |
| filters | 50,000 |

A chat without rules therefore stays outside the Assistant group-rule hot path.

## Per-chat revision generations

P7-I no longer uses one global rule revision per plugin.

Each plugin keeps:

```text
chatRevision[chat_id] = generation
```

A process-wide atomic sequence is used only to mint unique generation numbers.

Consequences:

- mutation in chat A invalidates only chat A;
- warm compiled cache for chat B remains valid;
- delete/re-add receives a new generation and cannot revive stale compiled state;
- inactive chats remove their revision entry;
- revision state does not grow with historical inactive-chat churn.

This prevents cross-chat cache invalidation amplification.

## Blacklist indexed matcher

Blacklist previously evaluated every compiled rule independently for every matching message.

P7-I now compiles each chat's blacklist into one failure-linked trie/Aho-Corasick-style matcher.

The matcher:

- lowercases the message once;
- scans message bytes once;
- carries terminal keyword lengths through failure links;
- validates Unicode-aware word boundaries;
- supports phrases;
- preserves underscore as a word character;
- does not iterate over all configured rules per message.

Blacklist bounds:

| Resource | Bound |
|---|---:|
| rules per chat | 512 |
| bytes per rule | 256 |
| compiled chat cache | 500 |

## Filter indexed matcher

Filters use the existing compiled `keywordMatcher`, also failure-linked and chat-local.

Filter bounds:

| Resource | Bound |
|---|---:|
| filters per chat | 512 |
| keyword bytes | 256 |
| compiled chat cache | 500 |
| cooldown entries | 1,000 |
| compiled cache TTL | 10 minutes |

Saved-response validation remains part of filter compilation.

Filter media/text delivery continues to use the existing SavedResponse delivery foundation and shared TaskEngine continuations where required.

## Bounded compiled-cache access

Compiled blacklist/filter caches are capped at 500 chats each.

Cache hits no longer acquire the global cache write lock merely to update LRU metadata.

Each compiled entry stores an atomic `lastUsed` sequence, updated by:

```text
entry.lastUsed.Store(cacheClock.Add(1))
```

Eviction scanning happens only when inserting a compiled cache entry, never on every incoming message.

Filters retain their TTL through immutable per-entry `cachedAt`.

Thus hot cache hits use:

- one read lock for map lookup;
- atomic usage update;
- no global write lock;
- no global rule scan.

## Repository cardinality

Durable blacklist and filter repositories enforce:

- at most 512 rules per chat;
- at most 50,000 active chats.

Control-plane mutations are serialized per repository instance so concurrent `COUNT → INSERT` operations cannot overshoot those caps.

These mutexes are not part of ordinary message matching.

## Warning foundation

P7-I opens the canonical admin commands on the Assistant surface:

```text
/warn
/warns
/resetwarns
```

The underlying warning records remain scoped by:

```text
chat_id + user_id
```

Assistant warning threshold action uses the caller's admitted Assistant Telegram servicer, so enforcement cannot accidentally escape through the userbot transport.

Before warning persistence, Assistant additionally protects:

- Goultroid Owner;
- Goultroid Sudo users;
- Telegram administrators;
- Telegram group creator.

Telegram target role is freshly verified twice for Assistant warnings:

1. handler preflight, for early user-safe rejection;
2. again inside the same-target warning stripe immediately before `AddWarning`.

The second check closes the race where a member is promoted to administrator after
the command preflight but before durable warning persistence.

## Warning resource bounds

Warning state is explicitly bounded:

| Resource | Bound |
|---|---:|
| default threshold | 3 |
| hard threshold / active rows per target | 16 |
| reason size | 1,024 bytes |
| global active warning rows | 50,000 |
| in-process lock stripes | 64 |

`GetWarnings` reads at most the hard threshold rows.

The moderation service serializes same-target count/add/enforce/reset through one of 64 fixed mutex stripes.

This prevents concurrent warnings/reset for the same target from:

- racing the warning count;
- resetting midway through threshold enforcement;
- overshooting the threshold rows;
- invoking duplicate threshold punishment.

The SQLite repository additionally caps total active warning rows at 50,000.
`chat_id` and `user_id` must both be positive at the service and repository
boundaries, preventing unreachable rows from consuming that capacity.

No dynamic lock map is created.

The SQLite warning repository repeats reason/row bounds as defense in depth and serializes its own control-plane mutations.

If threshold enforcement fails, rows stay capped at the threshold and are reused by later retries instead of growing indefinitely.

After successful punitive enforcement, active warnings are reset.

## Mutation and stale-state semantics

Rule manager mutations are linearized per chat with one of 64 fixed striped
`RWMutex` fences per plugin:

```text
manager mutation (write fence)
        ↓
persist durable mutation
        ↓
advance only this chat's generation
        ↓
drop only this chat's compiled cache
        ↓
publish chat interest
```

Matching uses the corresponding read fence. Compiled state is installed only
after the captured generation still matches the current chat generation.
Interest is published only after that final generation-validated commit, so a
stale compiler cannot incorrectly mark a newly active chat inactive.

Final Assistant application also stays inside the same per-chat read fence:

- blacklist re-match + destructive message deletion are fenced together;
- filter re-match + response delivery are fenced together.

Therefore a manager cannot commit rule removal between the final re-match and
the side effect that was authorized by that rule.

If the generation changes during compilation or contextual role verification:

- stale compiled state is not installed;
- apply re-matches against current rules;
- result revision reflects the latest generation.

## No global rule scan

P7-I specifically avoids both forms of global scan:

1. inactive chat ingress never walks all chats/rules;
2. active chat matching only uses that chat's compiled matcher.

The only bounded cache-wide scan is eviction of at most 500 compiled chat entries on a cache insertion.

It is not executed per message.

## Acceptance coverage

P7-I tests cover:

- inactive group stops before entity cache, resolver, and TaskEngine;
- interested chat submits one ordered shared TaskEngine occurrence;
- peer resolution occurs inside admitted work;
- slash commands/private/broadcast traffic do not enter the rule plane;
- Owner/Sudo bypass before interest, entity cache, TaskEngine, matcher, and role RPC;
- administrator/creator bypass only after a match and fresh role verification;
- verification failure fails safe;
- blacklist precedence over filters;
- disabled plugins do not contribute interest/actions;
- authoritative managed channel classification after interest/admission;
- unknown broadcast remains fail-closed even when durable rules exist;
- stale rule generation re-match;
- generation-safe interest publication;
- per-chat mutation/match linearization;
- blacklist delete and filter delivery fenced with final re-match;
- per-chat revision isolation across mutations;
- another chat's compiled filter cache remains warm;
- indexed blacklist word/phrase boundary behavior;
- blacklist matching at maximum rule cardinality;
- blacklist/filter repository cardinality bounds;
- compiled rule payload bounds;
- filter cooldown hard cap;
- warning target protection before persistence;
- target promotion between preflight and persistence is rejected with zero warning rows;
- warning enforcement stays on caller transport;
- failed threshold retry does not grow rows;
- oversized warning reason/threshold rejected before persistence;
- concurrent same-target warning has one threshold enforcement;
- reset cannot interleave with same-target threshold enforcement;
- global warning rows are capped at 50,000;
- invalid warning chat/user coordinates are rejected;
- direct warning repository callers cannot exceed hard row/reason limits.

## P7-I / later-phase boundary

P7-I closes the manager-rule foundation only.

It intentionally leaves:

- **P7-J** — reply/media/thread/topic context semantics;
- **P7-K** — whole group-plane lifecycle/resource/FloodWait/shutdown hardening;
- **P7-L** — final parity acceptance matrix and benchmarks.

P7-I introduces no permanent per-chat worker, poller, or second execution engine.
