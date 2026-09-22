# Assistant Parity P7-G — moderation/action execution

## Scope

P7-G opens real Assistant group mutations only after the P7-A→P7-F manager foundations are in place.

The phase does **not** introduce a moderation scheduler, worker pool, dispatcher, or second execution engine. Canonical Assistant commands still enter the shared `core.Router` and execute through the existing TaskEngine.

The managed mutation service owns only Telegram-specific authorization, hierarchy protection, desired-state semantics, and managed RPC execution.

## Execution path

```text
Assistant group command
        ↓
canonical core.Router
        ↓
P7-C cached contextual preflight
        ↓
shared TaskEngine admission
        ↓
task-local mutation admission capability
        ↓
P7-C fresh command authorization
        ↓
plugin/core moderation facade
        ↓
typed GroupMutationRequest
        ↓
managed P7-G mutation executor
        ↓
fresh bot-self rights
        ↓
fresh actor rights
        ↓
fresh target role/hierarchy (targeted operations)
        ↓
physical managed Telegram mutation RPC
```

The base Assistant Telegram servicer is mutation-disabled. A task-local clone is marked admitted only inside `WorkSpec.Handler`.

Therefore a direct/non-TaskEngine command path cannot perform a group mutation even if a mutation executor is configured.

## Canonical operation rights

`core.GroupMutationRequirement` is the single operation-to-right mapping used by command metadata and the managed executor.

| Operation | Required Telegram right |
|---|---|
| ban | `BanUsers` |
| unban | `BanUsers` |
| kick | `BanUsers` |
| mute | `BanUsers` |
| unmute | `BanUsers` |
| default permissions | `BanUsers` |
| pin | `PinMessages` |
| unpin | `PinMessages` |
| purge/delete messages | `DeleteMessages` |
| promote | `AddAdmins` |
| demote | `AddAdmins` |

The same requirement is checked for both:

- the human actor;
- the Assistant bot itself.

Global Goultroid Owner/Sudo identity does not substitute for Telegram rights.

## Bot-self verification

For supergroups, the bot verifies its own role through managed `channels.getParticipant` with `InputPeerSelf`.

For basic groups, bot-self verification uses the P7-B full-chat participant resolver.

A missing/unverifiable bot role is treated as verification unavailability, not as an authoritative actor denial.

## Target hierarchy and protection

Participant-targeting operations resolve the target through the canonical peer resolver and reject:

- the acting user targeting themselves;
- the Assistant bot targeting itself;
- Telegram group creators;
- administrator targets for destructive member actions;
- non-editable supergroup administrators;
- other hierarchy combinations Telegram would predictably reject.

For editable administrator targets, promote/demote is allowed only when the actor satisfies the creator/hierarchy rule.

The final target role is resolved **inside the same fresh RPC authorization fence** after bot and actor verification. No cached target role is used to authorize the physical mutation.

```text
bot fresh
   ↓
actor fresh
   ↓
target fresh
   ↓
hierarchy / desired-state decision
   ↓
RPC
```

This closes the window where a member could become an administrator between an earlier target lookup and the mutation.

## Promotion delegation

Promotion is treated as capability delegation.

A promoted supergroup administrator receives only rights present in both:

```text
actor rights ∩ Assistant bot rights
```

The delegated subset includes:

- change info;
- delete messages;
- ban users;
- invite users;
- pin messages;
- add admins;
- manage topics.

Channel-only `PostMessages` and `EditMessages` are not granted on the supergroup manager plane.

This prevents a caller with only `AddAdmins` from using the bot to grant unrelated privileges that neither side actually holds.

Basic-group promotion uses Telegram's legacy `messages.editChatAdmin` semantics and remains subject to the P7-B basic-group authority model.

## Physical RPC revalidation

Single-RPC operations perform the final actor/bot/target gate immediately before their managed RPC.

Multi-RPC operations revalidate again before every physical mutation:

### Kick

Supergroup kick is a desired-state two-step operation:

1. temporarily ban;
2. clear the temporary ban so the target remains removed rather than permanently banned.

Both physical `channels.editBanned` calls receive independent fresh actor/bot/target authorization.

### Purge

Purge:

1. performs a fresh authorization before scanning;
2. enumerates at most 1,000 candidate messages;
3. deletes in chunks of at most 100;
4. performs a fresh actor+bot authorization before every delete chunk.

Topic-aware purge uses `messages.getReplies`; non-topic purge uses `messages.getHistory`.

No delete chunk bypasses the fresh gate.

## Managed Telegram RPC boundary

P7-G physical mutations use the existing Assistant managed RPC executor.

Mutation wrappers use the idempotent-mutation lane for desired-state operations:

- `channels.editBanned`
- `channels.editAdmin`
- `channels.deleteMessages`
- `messages.deleteChatUser`
- `messages.editChatAdmin`
- `messages.updatePinnedMessage`
- `messages.editChatDefaultBannedRights`
- `messages.deleteMessages`

Read-side verification/enumeration remains in the managed read-only lane.

The admin plugin never calls raw Telegram RPC methods.

## Desired-state and idempotency semantics

P7-G treats already-reached states as success where Telegram exposes them authoritatively.

Examples:

- `CHAT_NOT_MODIFIED` → success;
- `RIGHTS_NOT_MODIFIED` → success;
- unban/unmute/kick/demote + `USER_NOT_PARTICIPANT` → success;
- basic-group ban/kick + `USER_NOT_PARTICIPANT` → success, because removal is the desired state;
- a fresh target snapshot already in the desired terminal state → no physical mutation, but actor/bot authorization is still verified.

Supergroup ban does **not** treat a merely-left user as equivalent to banned, because a left user may still be able to rejoin.

## Error semantics

Authoritative operation-right failure maps to:

```text
core.ErrGroupMutationDenied
    → core.ErrForbidden
```

Protected hierarchy maps to:

```text
core.ErrGroupMutationTargetProtected
    → core.ErrForbidden
```

Verification failure remains unavailable and is not rewritten as permission denial.

Known Telegram errors are normalized:

- admin/right rejection → mutation denied;
- creator/admin target rejection → protected target;
- invalid peer/user/chat coordinates → invalid arguments;
- already-applied desired state → success.

Admin feature handlers render user-safe messages and do not expose access hashes, raw resolver internals, or Telegram transport secrets.

## Supported chat semantics

### Supergroups

Full P7-G managed mutation semantics are available subject to actor/bot rights and target hierarchy.

### Basic groups

Telegram's legacy basic-group RPC model is used where meaningful.

Examples:

- ban/kick → `messages.deleteChatUser`;
- promote/demote → `messages.editChatAdmin`;
- pin/default permissions → Telegram messages APIs.

Operations that require supergroup restriction state, such as granular mute/unmute/unban, fail closed as unsupported rather than emulating unsafe state.

### Broadcast channels/private chats

The Assistant manager plane rejects them before mutation execution.

## Command policy split

Existing userbot policy remains unchanged.

For Assistant moderation commands:

```text
global Userbot permission: Sudo
Assistant global permission: Everyone
Assistant contextual requirement: exact Telegram admin right
```

This means a Telegram group administrator may use the Assistant manager surface without becoming Goultroid Sudo, while userbot command semantics remain unchanged.

Warnings/anti-spam state workflows are **not** implicitly opened by P7-G; those belong to P7-I.

## Resource and lifecycle behavior

P7-G adds no:

- goroutine;
- ticker;
- polling loop;
- worker pool;
- local retry engine;
- second TaskEngine;
- persistent mutation queue.

RPC retry/backoff/FloodWait behavior remains owned by the existing managed Assistant RPC executor.

Role verification remains bounded by P7-B.

Purge enumeration is capped at 1,000 messages with 100-message delete chunks.

## Acceptance coverage

P7-G tests cover:

- exact action→right metadata;
- Userbot/Assistant permission separation;
- actor right failure;
- bot-self right failure;
- target creator/admin hierarchy protection;
- target peer resolution;
- final target revalidation ordering;
- desired-state no-op with authorization still checked;
- `CHAT_NOT_MODIFIED` idempotent success;
- basic-group removal idempotency;
- two-physical-RPC kick revalidation;
- bounded/topic-aware purge and per-delete revalidation;
- basic-group managed mutation path;
- verification failure distinct from permission denial;
- Telegram protected-target error mapping;
- promotion rights limited to actor∩bot;
- real admin command execution through shared TaskEngine;
- direct mutation rejected without TaskEngine admission capability;
- mutation adapter delegates only to the typed P7-G port;
- all physical mutation wrappers use the existing managed idempotent RPC executor;
- no second execution engine/background lifecycle.

## Handoff

P7-G closes the privileged moderation/action execution foundation.

It intentionally does not absorb later P7 concerns:

- P7-H: event-driven welcome/goodbye/service-message features;
- P7-I: anti-spam/filter/warning rule engine;
- P7-J: broader reply/media/forum-topic context semantics;
- P7-K: whole group-plane resource/lifecycle hardening;
- P7-L: final group-plane acceptance matrix and irrelevant-message benchmark.
