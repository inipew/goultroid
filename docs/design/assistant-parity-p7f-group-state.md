# Assistant Parity P7-F — group-scoped persistent state

## Scope

P7-F adds a durable manager-state domain that is explicitly scoped by Telegram chat and cannot fall back to global/user settings.

The durable identity is:

```text
(chat_id, namespace, key)
```

This foundation does not enable P7-G moderation mutations and does not create a new command surface.

## Why this is not generic Settings

The existing Settings service supports effective scope fallback across chat, user, and global values. That behavior is useful for configuration inheritance, but it is unsafe as the persistence authority for group-manager state because a missing chat row could silently inherit a global value.

P7-F therefore uses a dedicated `assistant_group_state` table and a dedicated `core.GroupStateStore` contract.

There is no:

- global scope;
- user scope;
- effective setting lookup;
- namespace fallback;
- implicit inheritance.

A read for chat A can never return chat B or global state.

## Schema

```text
assistant_group_state
  chat_id
  namespace
  key
  value
  revision
  updated_by
  updated_at
  expires_at
```

Primary key:

```text
(chat_id, namespace, key)
```

Revision starts at 1.

An expiry index supports bounded occurrence-driven reclamation.

## Optimistic concurrency

P7-F uses exact revision CAS.

Create:

```text
expected_revision = 0
missing coordinate
        ↓
insert revision 1
```

If a live row already exists, create returns a conflict.

Update:

```text
expected_revision = N
stored revision = N
        ↓
write value
revision = N + 1
```

A stale revision returns a typed conflict and does not mutate the row.

Delete also requires the exact current non-zero revision.

There is no last-write-wins overwrite API.

## Contextual authorization at the persistence boundary

Group-wide state writes require an administrator or creator contextual requirement.

The write path is:

```text
P7-C command preflight
        ↓
TaskEngine admission
        ↓
P7-C fresh command authorization
        ↓
feature handler
        ↓
Context.CompareAndSwapGroupState / DeleteGroupState
        ↓
fresh P7-B role lookup again
        ↓
P7-C requirement authorization
        ↓
opaque GroupStateWriteGrant
        ↓
repository CAS/delete
```

The second fresh lookup is deliberate: feature work may occur between task admission and the actual persistence boundary.

If the actor loses administrator authority after the command starts but before persistence, no state write occurs.

## Opaque write grant

`GroupStateWriteGrant` has private fields in `internal/core`.

Only `core.Context` creates a valid grant after fresh contextual authorization. The persistence interface requires that grant for CAS/delete, and the SQLite store verifies that the grant matches both:

- exact chat ID;
- exact actor ID.

Code that obtains or constructs a repository directly cannot perform an authorized write with the zero value or a mismatched grant.

Global `Owner` / `Sudo` flags do not manufacture the grant; Telegram contextual role still has to satisfy the declared requirement.

## Write authority

P7-F rejects group-wide persistence when the supplied write requirement is only:

- none;
- member.

Valid write requirements are:

- administrator, optionally with granular Telegram rights;
- creator.

Global Owner/Sudo may remain an additional command permission, but never replaces contextual group authority.

## Bounds

Default/hard bounds:

| Resource | Default | Hard |
|---|---:|---:|
| persistent rows | 50,000 | 200,000 |
| value bytes per row | 64 KiB | 64 KiB |
| lazy cleanup batch | 64 | 512 |
| namespace bytes | 64 | 64 |
| key bytes | 128 | 128 |

Live rows are never evicted to make room.

When a create reaches capacity:

1. reclaim at most the configured cleanup batch of expired rows;
2. recount physical rows;
3. insert only if capacity is available;
4. otherwise fail with a resource-limit error.

Create-at-capacity is serialized per production store instance so concurrent create operations cannot race the local capacity decision.

## Expiry and cleanup lifecycle

Expiry is optional. Durable state may have no expiry.

Expired rows:

- are invisible to `Get`;
- cannot be updated as a live revision;
- may be reclaimed when their exact coordinate is reused;
- are lazily reclaimed in a bounded batch when create pressure reaches capacity;
- may be explicitly pruned with a bounded `PruneExpired` call.

P7-F creates no:

- goroutine;
- ticker;
- polling loop;
- cleanup worker;
- in-memory state cache.

Physical row count remains bounded by the configured capacity; cleanup work is occurrence-driven.

## Assistant wiring

The store is constructed by the domain-service composition root and injected:

```text
SQLite groupstate store
        ↓
AssistantApp
        ↓
command.Router
        ↓
core.AttachGroupStateStore
        ↓
private Context state capability
```

The store is not a public field on `core.Context`. Feature handlers use typed Context methods.

The schema migration is registered through the existing feature-migration runner as:

```text
assistant_group_state.001
```

The store may be constructed before migrations, but no Assistant execution starts until application startup after builtin migrations complete.

## API

Read:

```go
record, err := ctx.GetGroupState(namespace, key)
```

Write:

```go
record, err := ctx.CompareAndSwapGroupState(
    requirement,
    namespace,
    key,
    expectedRevision,
    value,
    ttl,
)
```

Delete:

```go
err := ctx.DeleteGroupState(
    requirement,
    namespace,
    key,
    expectedRevision,
)
```

All coordinates are normalized and derived from the current group context; callers do not supply arbitrary chat IDs through these Context methods.

## Acceptance coverage

P7-F tests cover:

- builtin migration registration;
- restart durability;
- explicit chat isolation;
- create-only revision 1;
- exact-revision update;
- stale CAS conflict;
- exact-revision delete;
- expired rows invisible to reads;
- lazy bounded expiry reclamation;
- live state never evicted at capacity;
- hard capacity failure;
- bounded explicit pruning;
- max value/limit validation;
- direct repository write rejected without opaque grant;
- fresh authorization immediately before persistence;
- Owner/Sudo not bypassing Telegram admin role;
- member-level write requirements rejected;
- role revoked between TaskEngine admission and persistence prevents the write;
- no Settings dependency;
- no background goroutine/ticker/poller.

## P7-G boundary

P7-F stores manager state only. It does not alter the Assistant moderation adapter.

Ban/kick/mute/pin/delete/promote and related Telegram mutation transport remain fenced by P7-G requirements.
