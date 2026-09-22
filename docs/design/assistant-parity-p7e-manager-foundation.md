# Assistant Parity P7-E — manager/admin command foundation

## Scope

P7-E closes the Assistant group-manager command foundation on top of the P7-D routing plane.

P7-D owns command ingress and dispatch semantics:

- slash-command routing in groups;
- `/command@botusername` targeting;
- canonical `core.Router`;
- no accidental ordinary-message dispatch;
- no second command dispatcher.

P7-E owns the manager authorization/UX contract:

- a real administrator/creator surface;
- one read-only canary before moderation mutations;
- contextual authorization before execution;
- safe UX for permission denial, stale role, verification failure, and unsupported chat;
- mutation transport remains fenced until P7-G.

Some P7-E implementation landed historically while the work was still labelled P7-D. This document separates the ownership boundary explicitly.

## Canonical read-only canary

The P7-E canary is the canonical `info.chatinfo` command:

```text
/chatinfo
/groupinfo
/cinfo
```

On the Assistant surface it declares:

```text
InvocationAnyone
GroupOnly
GroupAuthorizationAdministrator
```

Administrator and creator are accepted according to the P7-C hierarchy.

The same plugin command remains available to the Userbot surface with its existing invocation policy. P7-E does not create an Assistant-only duplicate command.

## Manager execution contract

```text
Assistant group command
        ↓
P7-D canonical routing
        ↓
global Invocation / Permission
        ↓
P7-C cached-capable contextual preflight
        ↓
TaskEngine admission
        ↓
P7-C fresh role revalidation
        ↓
read-only handler
        ↓
managed P7-D group query
```

A member/non-admin caller is rejected before TaskEngine admission.

A caller who was administrator at preflight but loses the role while waiting for execution is rejected by the fresh check before the feature handler or read query executes.

## Failure and response UX

P7-E distinguishes authoritative authorization denial from inability to verify authorization.

### Permission denied

An authoritative member/non-admin observation returns:

```text
ErrGroupAuthorizationDenied
        ↓
ErrForbidden
        ↓
"You are not authorized..."
```

The task/query path is not entered for preflight denial.

### Stale role

If cached preflight says administrator but fresh post-admission verification says member:

```text
TaskEngine admitted
        ↓
fresh role = member
        ↓
ErrGroupAuthorizationDenied
        ↓
safe authorization-denied response
        ↓
feature handler NOT called
```

This is an authorization result, not a Telegram availability error.

### Verification failure

If Telegram role verification cannot complete, P7-E does not claim the actor is non-admin.

```text
verification unavailable
        ↓
ErrUnavailable / bounded resource error
        ↓
temporary-unavailable UX
```

Raw Telegram details, access hashes, tokens, or resolver internals are not rendered to the user.

The distinction is preserved both:

- before TaskEngine admission;
- during fresh post-admission verification.

### Unsupported chat

The manager plane supports basic groups and supergroups only.

Private chats and broadcast channels fail closed as `ErrGroupOnly` before contextual role RPC, TaskEngine admission, or group query execution.

P7-E now renders the canonical user-safe group-only message for this case rather than returning an error without feedback.

## Read-only boundary

The P7-E canary may call read/query operations such as `GetFullChat`.

An architecture regression test parses `plugins/info/info.go` and fails if `handleChatInfo` begins calling manager mutation methods.

The Assistant mutation adapter remains fenced for:

- pin / unpin;
- ban / unban;
- kick;
- mute / unmute;
- purge;
- promote / demote;
- default banned-rights mutation.

These methods continue to return `ErrGroupMutationUnavailable`.

## Owner/Sudo separation

P7-E inherits the P7-C invariant:

```text
Goultroid Owner/Sudo != Telegram group administrator
```

Global identity may be an additional command permission, but it cannot satisfy contextual group authorization.

## Acceptance matrix

P7-E is considered closed when the following remain true:

| Case | Admission | Fresh check | Query/handler | User response |
|---|---|---|---|---|
| administrator | yes | pass | executes | normal result |
| creator | yes | pass | executes | normal result |
| member | no | n/a | no | authorization denied |
| admin becomes member while queued | yes | deny | no | authorization denied |
| preflight verification unavailable | no | n/a | no | temporarily unavailable |
| fresh verification unavailable | yes | unavailable | no | temporarily unavailable |
| private chat | no | n/a | no | groups-only |
| broadcast channel | no | n/a | no | groups-only |
| wrong `@botusername` target | no | n/a | no | ignored |

Existing P7-D tests cover administrator/creator success, member denial, bot targeting, and read-query safety. P7-E-specific tests cover stale-role UX, verification-failure distinction, and unsupported-chat feedback.

## Resource/lifecycle impact

P7-E adds no cache, worker, poller, ticker, or persistence subsystem.

It reuses:

- P7-B bounded role verification/cache;
- P7-C TaskEngine authorization ordering;
- P7-D managed read query;
- P7-F state only for later manager features that explicitly need durable chat-local state.

## P7-G handoff

P7-E does **not** open Telegram moderation mutations.

P7-G may replace the mutation fence only when it implements all of the following as one managed execution contract:

1. operation-specific actor right requirement;
2. fresh actor authorization;
3. bot/self participant and rights verification;
4. target participant/hierarchy/protection checks;
5. TaskEngine/resource admission;
6. fresh revalidation immediately before the Telegram mutation RPC;
7. managed RPC execution;
8. typed Telegram result/error semantics;
9. no second moderation execution engine.

Until those invariants are present, `ErrGroupMutationUnavailable` remains the correct behavior.
