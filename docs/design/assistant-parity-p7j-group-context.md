# Assistant Parity P7-J — group media, reply, and topic context

## Scope

P7-J closes the Assistant group-context semantics required before lifecycle/resource hardening:

- canonical reply-target extraction;
- media input on Assistant group commands;
- explicit mention and reply-to-Assistant context;
- linked-chat reply rejection;
- forum topic/thread identity;
- topic-scoped TaskEngine ordering;
- topic-preserving text/media responses;
- fail-closed delivery when thread context cannot be preserved.

P7-J extends the existing canonical `core.Context`, Assistant command router, shared TaskEngine, interaction transport, and P7-I group-rule path. It does not add a second dispatcher, execution engine, per-topic worker, poller, or conversation runtime.

## Canonical message context

The Assistant update boundary preserves transport-derived coordinates in `command.MessageContext` and then projects them into the normal `core.Context`:

```text
Telegram message
    ↓
authoritative chat kind
message id
reply message id
reply peer
forum top/topic id
media metadata
album/grouped id
entities
Assistant self identity
mentions
    ↓
canonical core.Context / core.Message
```

The canonical message therefore carries the information feature handlers need without re-parsing Telegram updates independently.

## Reply target extraction

User-targeting commands use one precedence rule:

```text
explicit numeric user id / @username
        ↓
reply target
        ↓
bare username compatibility (only when there is no reply)
```

An explicit target always overrides reply context.

When reply targeting is used, `ResolveTargetUser()` goes through `GetReply()`. It does not trust only the integer `ReplyToID`.

If Telegram returns a replied message that does not identify a user sender, target resolution fails closed instead of falling through to a command argument that may have a different meaning, such as a warning reason or admin title.

## Linked-chat reply fence

Telegram can represent replies with `ReplyToPeerID`, including linked discussion/channel contexts.

P7-J records that peer as a canonical `PeerRef`.

Before issuing the message lookup, `GetReply()` compares a non-empty reply peer against the command peer. A reply targeting another chat is rejected with `ErrInvalidArgs`.

Therefore a manager command cannot use a linked-channel reply as authorization to resolve or mutate a target in the current group.

## Forum topic fence

The canonical topic ID is the Telegram forum top-message ID. For topic roots, the root message itself is normalized as the topic.

After loading a replied message, `GetReply()` verifies that command and reply belong to the same topic.

```text
command topic A
    ↓
reply target
    ├─ topic A → allowed
    ├─ topic B → ErrInvalidArgs
    └─ ambiguous topic metadata → fail closed when either side is topic-scoped
```

This fence is reused by reply-based target extraction and destructive operations such as purge.

## Media input

Assistant group command ingress now carries Telegram media into the canonical message through `core.ExtractMediaFromTG`.

This supports both:

- media attached directly to a slash-command message/caption;
- media on the replied message through the existing lazy `GetReply()` path.

Album identity is retained through `GroupedID`.

Features continue to consume media through the existing `core.MediaFacade`; P7-J does not introduce an Assistant-specific media API.

## Mentioned / replied Assistant semantics

The update boundary records the Assistant bot's authenticated self identity and parsed Telegram mention entities.

Canonical helpers are:

```go
ctx.MentionedSelf()
ctx.RepliedToSelf()
ctx.AddressedToSelf()
```

`AddressedToSelf()` short-circuits when the message explicitly mentions the Assistant. Otherwise it lazily resolves the replied message and uses the same linked-chat/topic fences as any other reply lookup.

This is contextual information, not a second invocation protocol. A plain mention or reply does not synthesize a command registry or bypass the canonical slash-command/router policy. Features that need conversational addressing can consume the canonical helper explicitly.

## Topic-scoped ordering

Group-plane work uses one ordering namespace:

```text
non-topic group:
chat:<chat_id>

forum topic:
chat:<chat_id>:topic:<topic_id>
```

`core.GroupOrderingKey(chatID, topicID)` is reused by:

- canonical Assistant group commands;
- P7-I group-rule execution;
- filter continuations/delivery;
- dynamic Assistant SavedResponse commands.

Consequences:

- work in one topic remains ordered with related work in that topic;
- topic A does not serialize unrelated topic B work;
- group work cannot accidentally reuse a private/user correlation key as its ordering authority.

No per-topic goroutine or permanent worker exists. Topic isolation is expressed only through TaskEngine ordering keys.

## Response thread preservation

Core responses derive a `MessageSendContext` from the triggering message:

```text
ReplyToID = source message id
TopicID   = source forum topic id
```

The production Assistant interaction sends that context through Telegram's reply/top-message fields for both text and media.

P7-J also closes a critical fallback case: when `TopicID > 0`, a transport that cannot preserve contextual thread delivery is rejected with `ErrUnavailable`.

This fail-closed behavior exists at:

- core text response fallback;
- core media response fallback;
- Assistant TelegramServicer adapter;
- dynamic SavedResponse text/media delivery.

A topic-scoped task therefore fails instead of silently posting its result into the main chat or another topic.

## Group rules and filters

P7-I ordinary-message envelopes now retain reply peer, topic, mention, media summary, and Assistant identity metadata without introducing extra Telegram RPC on the normal rule hot path.

Filter responses are anchored to the matched source message and topic. Filter continuations use the same group/topic ordering key.

The blacklist/filter matcher remains chat-scoped and indexed; P7-J adds context but does not reintroduce global rule scans.

## Resource and lifecycle properties

P7-J adds no:

- topic registry;
- permanent per-topic worker;
- per-topic goroutine;
- ticker;
- polling loop;
- secondary TaskEngine;
- secondary Telegram RPC path.

Reply fetching remains lazy and invocation-scoped. Existing reply memoization prevents duplicate lookups inside one command context while transient lookup errors remain retryable.

Topic cardinality therefore does not create long-lived in-process state.

## Acceptance coverage

P7-J regression/architecture coverage includes:

- Assistant ingress carries media, album, entities, self identity, mentions, reply peer, and topic;
- explicit user target overrides reply target;
- reply target is used before payload-style arguments;
- unresolved reply user fails closed;
- linked reply peer is rejected before Telegram lookup;
- cross-topic reply is rejected;
- topic root normalization;
- reply-to-Assistant uses canonical reply fences;
- mention-based addressing does not require a reply RPC;
- canonical handler receives media/self context;
- group command TaskEngine ordering differs by topic;
- group-rule ordering is topic-scoped;
- filter delivery and continuations preserve topic ordering;
- contextual text response carries source message + topic;
- topic text fallback fails closed without contextual transport;
- topic media fallback fails closed without contextual transport;
- architecture fences retain canonical context/ordering and prohibit topic polling workers.

## P7-J / later-phase boundary

P7-J closes group media/reply/context semantics.

It intentionally leaves:

- **P7-K** — whole group-plane lifecycle/resource/FloodWait/backpressure/shutdown hardening;
- **P7-L** — final group-plane behavior/resource/architecture acceptance and benchmarks.

P7-J is therefore a context-correctness phase, not a new runtime subsystem.
