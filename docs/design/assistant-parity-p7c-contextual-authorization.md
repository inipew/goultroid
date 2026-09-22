# Assistant Parity P7-C — contextual authorization contract

## Scope

P7-C introduces command metadata for Telegram group-scoped authorization on the Assistant surface. It consumes the authoritative P7-B role resolver but does not enable Telegram group mutation transport; the P7-A mutation fence remains authoritative until P7-G.

Global Goultroid permission and Telegram contextual authorization are independent gates.

```text
InvocationPolicy
      ↓
global Permission (Everyone/Sudo/Owner)
      ↓
contextual Telegram group authorization
      ↓
TaskEngine execution
```

Owner or Sudo identity never manufactures a Telegram group role.

## Command metadata

Commands may declare:

```go
GroupAuthorization: core.GroupAuthorizationRequirement{
    Level: core.GroupAuthorizationAdministrator,
    Rights: core.GroupAdminRights{
        BanUsers: true,
    },
}
```

Supported levels are:

- `GroupAuthorizationMember`
- `GroupAuthorizationAdministrator`
- `GroupAuthorizationCreator`

A member requirement accepts authoritative member, restricted-member, administrator, and creator states. It rejects unknown, left, and banned states.

An administrator requirement accepts administrator or creator. Optional granular rights are an additional conjunctive requirement.

A creator requirement accepts only creator.

Rights metadata is valid only with the administrator level. Registration rejects contextual authorization metadata on commands that are not `GroupOnly`.

## Authorization ordering

For commands without contextual metadata, behavior remains unchanged and no P7-B resolver call is added.

For commands with contextual metadata:

```text
Assistant command
      ↓
InvocationPolicy
      ↓
global Permission
      ↓
TaskEngine dependency check
      ↓
cached-capable P7-B preflight
      ↓
TaskEngine Submit / queue / resource admission
      ↓
WorkSpec.Handler starts
      ↓
fresh P7-B revalidation
      ↓
feature handler
```

The preflight uses `ResolveGroupActor(false)`, so a valid bounded P7-B cache entry may avoid Telegram RPC. A cache miss still performs authoritative verification.

Fresh revalidation uses `ResolveGroupActor(true)` inside the TaskEngine work handler. Therefore a role or right revoked while the command waits for TaskEngine capacity prevents the feature handler from running.

Contextual-authorized commands cannot use the historical no-TaskEngine direct execution fallback, even when they request no explicit resource tokens. Missing TaskEngine configuration fails closed before any role RPC.

## Failure semantics

An authoritative role or rights mismatch returns `core.ErrGroupAuthorizationDenied`, which unwraps to `core.ErrForbidden`.

An unverified role, resolver failure, or Telegram verification failure remains unavailable/transient according to P7-B and is not converted into an authorization denial.

Fresh post-admission authorization errors are preserved as typed errors instead of being flattened through `TaskResult.Failure.Message`.

## Global identity separation

`core.GroupAuthorizationRequirement.Authorize` evaluates only:

- `Verified`
- contextual `Role`
- contextual `Rights`

It deliberately ignores:

- `IsOwner`
- `IsSudo`

This makes combinations explicit. For example, a command with `PermissionOwner` plus `GroupAuthorizationAdministrator` requires the caller to be both the configured Goultroid Owner and an authoritative Telegram administrator/creator in that chat.

## Mutation safety

P7-C does not add managed mutation RPCs and does not alter `assistantServicerAdapter`.

The following remain fenced by `ErrGroupMutationUnavailable`:

- pin/unpin
- ban/unban
- kick
- mute/unmute
- purge
- promote/demote
- default banned-rights mutation

A P7-C regression test drives an authorized `BanUsers` command through cached preflight and fresh revalidation, then confirms execution still terminates at the existing mutation fence.

## Resource and idle impact

P7-C creates no background goroutine, ticker, poller, or cache of its own.

Commands without `GroupAuthorization` perform zero group-role resolution due to P7-C.

Commands with contextual authorization perform at most:

1. one cached-capable preflight resolution before TaskEngine admission; and
2. one forced-fresh resolution after admission, immediately before the command handler.

All Telegram verification concurrency/cache bounds remain owned by P7-B.

## P7-D / later handoff

Later manager feature migration can annotate individual read-only and mutation commands with the exact contextual requirement. P7-G may replace the mutation fence only after managed mutation RPC wiring and bot-right/target revalidation are in place.
