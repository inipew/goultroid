# Assistant Parity P7-A — Group execution model / contextual principal

## Scope

P7-A establishes the execution boundary for Assistant group/manager features. It does **not** resolve Telegram participant roles yet; that is P7-B.

The contract is:

```text
canonical core.Router
        +
SourceAssistant
        +
authoritative chat kind
        +
global Owner/Sudo identity
        +
chat-scoped role slot (unknown until P7-B)
```

## Chat classification

Telegram `PeerChannel` is ambiguous: it can represent a supergroup or a broadcast channel. Assistant ingress therefore carries entity-derived `core.Chat` metadata into command execution instead of inferring group semantics from `InputPeerChannel` alone.

Manager-plane `GroupOnly` accepts only:

- basic group
- supergroup

Broadcast channels fail closed until a dedicated channel capability exists.

If channel entity metadata is unavailable, the peer remains classified as a broadcast channel. It is never upgraded to a supergroup by guesswork.

## Contextual principal

P7-A introduces `core.GroupExecutionContext` and keeps global and contextual authority separate.

Global authority remains:

- Owner
- Sudo
- Everyone

Telegram chat-scoped role is modeled separately:

- unknown
- member
- restricted
- administrator
- creator
- banned
- left

P7-A deliberately leaves role/rights as `unknown` / unverified. P7-B will resolve and cache them using managed Telegram RPC.

## Forum topic identity

Assistant command ingress now preserves forum `TopicID` in the canonical `core.Message`, so later manager operations can derive ordering and state keys that do not cross topic boundaries.

## Mutation safety fence

Before P7-A, `assistantServicerAdapter` embedded `core.MockTelegramServicer`. Unsupported group mutations such as ban/mute/promote could therefore return nil even though no Telegram mutation occurred.

P7-A overrides group mutation methods to return `ErrGroupMutationUnavailable`, which unwraps to `core.ErrUnavailable`.

This is intentional. P7-G will replace the fence only after all of the following exist:

1. contextual actor authorization;
2. bot-rights verification;
3. post-TaskEngine revalidation;
4. managed Telegram mutation RPC.

Until then Assistant manager mutations fail closed rather than reporting false success.

## Compatibility

- No second command registry is introduced.
- Existing private Assistant flows remain on the canonical router.
- Userbot `GroupOnly` compatibility semantics are unchanged.
- PM Relay remains private-message-only.
- No background goroutine, ticker, or polling loop is added.

## P7-B handoff

P7-B should implement the `GroupRoleResolver` using managed `channels.getParticipant`, bounded lazy caching, bounded concurrent verification, explicit authoritative-not-member vs verification-failure semantics, and no periodic poller.
