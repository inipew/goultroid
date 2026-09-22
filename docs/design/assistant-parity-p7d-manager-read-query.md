# Assistant Parity P7-D — manager read/query surface

## Scope

P7-D proves the P7-A/P7-B/P7-C contracts on a real Assistant group feature without opening moderation mutations.

The canonical canary is the existing `info.chatinfo` command, exposed on both Userbot and Assistant surfaces. Assistant execution uses the canonical `core.Router`; no manager command registry or dispatcher is introduced.

## Routing contract

Assistant group commands use Telegram slash-command routing:

- `/chatinfo`
- `/groupinfo`
- `/cinfo`
- `/chatinfo@AuthenticatedBotUsername`

Commands explicitly targeted at another bot are ignored before interaction input, PM Relay, contextual role lookup, TaskEngine admission, or manager query RPC.

A suffixed command fails closed until the authenticated Assistant username is known.

Ordinary non-command group messages do not enter the canonical command path.

## Manager canary metadata

`chatinfo` keeps its historical Userbot invocation semantics while adding Assistant manager semantics:

```text
Userbot:
  InvocationSelfOrSudo

Assistant:
  InvocationAnyone
  GroupOnly
  GroupAuthorizationAdministrator
```

Global Goultroid Owner/Sudo permission is not substituted for Telegram group role.

Creator satisfies the administrator requirement through the P7-C role hierarchy.

## Execution path

```text
Assistant group command
        ↓
canonical command lookup
        ↓
bot-target validation
        ↓
P7-C cached-capable contextual preflight
        ↓
TaskEngine admission
        ↓
P7-C fresh Telegram role revalidation
        ↓
read-only manager query boundary
        ↓
managed Telegram read RPC
        ↓
user-safe response
```

For a member/non-admin denial, no task is submitted and no manager query is issued.

A role downgrade while queued remains protected by P7-C fresh revalidation.

## Read-only query boundary

The Assistant command adapter exposes exactly one manager query capability:

```go
type GroupQueryReader interface {
    GetFullChat(context.Context, tg.InputPeerClass) (*tg.MessagesChatFull, error)
}
```

The transport implementation supports only:

- basic group: managed `messages.getFullChat`
- supergroup: managed `channels.getFullChannel`

Missing supergroup access hashes are recovered through the existing bounded peer resolver.

The query path contains no moderation mutation method.

## Response surface

`/chatinfo` renders useful read-only state such as:

- canonical title/id/type
- caller's verified Telegram group role
- forum Topic ID when present
- member/admin/banned/kicked counts when Telegram returns them
- slow mode
- description

Assistant query failures are rendered through `core.UserMessage`; raw Telegram/internal details are not included in the response.

Help rendering also exposes `Group Authorization` metadata so manager command requirements are visible from the canonical feature catalog.

## Failure boundaries

P7-D fails closed for:

- broadcast channels
- private chats for `GroupOnly` manager commands
- wrong-bot `@username` targeting
- contextual role verification failure
- non-admin/member callers
- unavailable read-query transport
- peer/access-hash resolution failure

Read/query failures do not relax contextual authorization.

## Mutation fence

P7-D does not enable moderation actions.

The Assistant adapter continues to return `ErrGroupMutationUnavailable` for:

- pin/unpin
- ban/unban
- kick
- mute/unmute
- purge
- promote/demote
- default banned-rights mutation

An architecture regression test verifies both the read-only query interface and the P7-A mutation fence.

## Resource/lifecycle impact

P7-D adds no worker, poller, ticker, or persistent cache.

The only new Telegram work is occurrence-driven:

1. P7-B role verification when required by P7-C;
2. one manager read RPC after successful TaskEngine admission and fresh authorization.

No read RPC happens for wrong-bot targeting, invalid chat scope, or cached authorization denial.

## Acceptance coverage

P7-D source tests cover:

- administrator canary success
- creator canary success
- member denial before TaskEngine/query
- cached preflight before admission
- fresh verification after admission
- `/command@botusername` targeting
- wrong-bot command ignored with zero manager work
- broadcast channel fail-closed with zero role/query RPC
- supergroup/basic-group read RPC selection
- access-hash recovery
- unsupported peer failure
- user-safe read-query failure rendering
- help metadata visibility
- Userbot invocation compatibility
- mutation fence remains closed

## Handoff

With P7-D closed, the manager plane has a proven routing and read-only canary. The next phase should build group-scoped persistent manager state (P7-F in the original roadmap) or another explicitly read-only manager capability before P7-G moderation transport is opened.
