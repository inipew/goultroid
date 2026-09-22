# Assistant Parity P7-H — event-driven group features

## Scope

P7-H adds Assistant group service-event automation for:

- welcome messages when users join;
- goodbye messages when users leave;
- canonical Telegram service-message extraction;
- chat-scoped enable/disable state;
- interest-aware ingress and dynamic EventBus subscription.

It does **not** add:

- a per-group worker;
- a per-group goroutine;
- a polling loop;
- a second event dispatcher;
- a second TaskEngine;
- ordinary group-message fan-out into the feature.

P7-H reuses the existing P7-F durable group-state boundary, shared EventBus, and shared TaskEngine.

## Durable control plane

The canonical namespace is:

```text
assistant_group_events
```

with chat-local keys:

```text
welcome
goodbye
```

Each state row remains scoped by:

```text
chat_id + namespace + key
```

and uses P7-F revision/CAS semantics.

The control commands are:

```text
/welcome [status|on [template]|off|set <template>|reset]
/goodbye [status|on [template]|off|set <template>|reset]
/farewell ...
```

They are Assistant-only, group-only commands with contextual Telegram administrator authorization.

A write is authorized again by the P7-F persistence boundary immediately before CAS persistence. Global settings are not used as a fallback.

## Template contract

Welcome/goodbye templates support:

```text
{user}
{user_id}
{chat}
{count}
```

Template bytes are bounded to 2 KiB.

Rendered user lists are bounded to eight explicit users. Additional users are represented as an aggregate suffix such as:

```text
and N more
```

HTML-sensitive user/chat display values are escaped before rendering.

## Telegram service-event ingress

P7-H recognizes:

```text
MessageActionChatAddUser
MessageActionChatJoinedByLink
MessageActionChatJoinedByRequest
MessageActionChatDeleteUser
```

and maps them to the canonical core kinds:

```text
member.joined
member.left
```

Both Telegram ingress classes are covered:

- basic-group `UpdateNewMessage`;
- supergroup `UpdateNewChannelMessage`.

Broadcast channels fail closed when entity metadata identifies them as non-megagroup channels.

The Assistant bot itself is removed from joined/left user sets.

Duplicate user IDs inside one service message are collapsed only after the chat-interest gate has passed.

## Cold-path feature-interest routing

The normal inactive path is intentionally:

```text
Telegram MessageService
        ↓
cheap chat/service-kind classification
        ↓
lock-free Interested(chatID, kind)
        ↓
false
        ↓
return
```

Before an interest hit, P7-H does not:

- allocate `GroupServiceEvent`;
- allocate the user-dedup map;
- allocate the rendered-user slice;
- create an InputPeer;
- touch the global entity cache;
- resolve an access hash;
- publish to EventBus;
- enter TaskEngine;
- send Telegram RPC.

Interest is held by two lock-free `core.ChatFeatureSnapshot` instances:

- welcome interest;
- goodbye interest.

Read-side lookup is O(1) and lock-free.

Control-plane `SetActive` uses bounded copy-on-write. It runs only on configuration transitions, not for each incoming group update.

## Lazy supergroup peer recovery

Telegram does not guarantee every service update carries the full channel entity/access hash.

P7-H therefore separates:

```text
chat ID + service kind
        ↓
interest check
        ↓
peer resolution only on hit
```

For an interested supergroup:

1. use the entity snapshot directly when it contains a megagroup access hash;
2. otherwise recover the peer lazily through the canonical bounded Assistant peer resolver/cache.

An inactive supergroup with missing entity metadata never touches the resolver.

This prevents enabled chats from silently losing welcome/goodbye events while preserving the cold path.

## Dynamic shared subscription

P7-H owns exactly one EventBus subscription site:

```text
EventTypeGroupService
owner = assistant:groupevents
```

It is not a per-chat subscription.

The shared subscription exists only while at least one chat has welcome or goodbye enabled:

```text
activeChats == 0
    → no P7-H EventBus subscriber

activeChats > 0
    → one shared subscriber
```

Enable ordering is:

```text
persist
→ attach shared subscriber if needed
→ expose chat interest
```

Disable ordering is:

```text
persist
→ hide chat interest
→ remove shared subscriber if this was the final active chat
```

This prevents an ingress update from observing active interest without a corresponding delivery path.

## EventBus and TaskEngine execution

P7-H does not call TaskEngine directly.

The application already binds:

```text
coreDeps.eventBus.SetTasks(coreDeps.taskEngine)
```

so delivery uses:

```text
P7-H Publish
    ↓
shared EventBus
    ↓
chat ordering key
    ↓
shared TaskEngine
    ↓
P7-H handler
    ↓
managed Assistant SendMessage
```

The feature therefore inherits existing bounded EventBus queues, TaskEngine admission, ordering, shutdown, retry/FloodWait transport policy, and observability.

There is no feature-owned execution pool.

## Ordering

`GroupServiceEvent` is an `OrderedEvent` with:

```text
chat:<chat_id>
```

so service events for one chat retain chronological execution ordering while unrelated chats are not serialized together by the feature.

## Restart/preload semantics

P7-H preloads its namespace once at application startup using the bounded P7-F namespace reader.

The preload is bounded by the group-state store's own entry limit and never starts a poller.

From persisted rows it reconstructs:

- durable welcome/goodbye config;
- revision values;
- active-chat count;
- welcome interest snapshot;
- goodbye interest snapshot;
- whether the one shared EventBus subscriber is needed.

Disabled durable rows remain visible to status commands so revision/template state is not replaced by a synthetic default.

## Lifecycle/shutdown

The group-event service owns no permanent goroutine.

`Close()` is terminal and idempotent.

Close atomically/fail-closed:

- marks the service closed;
- makes interest return false;
- releases the shared EventBus subscription;
- clears the transport;
- marks preload state inactive.

The `closed / loaded / ready / subscription` transition is serialized under the service mutex.

A configure operation whose persistence completed just before Close cannot resurrect the subscription when its in-memory apply arrives after Close.

Likewise, transport rebinding and a second Load after terminal Close fail closed.

Assistant client start/stop only installs/removes the transport used by the currently live MTProto session. The P7-H service itself remains application-owned.

## Bounded resource model

P7-H adds no:

- permanent goroutine;
- ticker;
- timer;
- polling loop;
- per-group subscriber;
- per-group worker;
- unbounded queue;
- global scan on service-message ingress.

Bounded values include:

| Resource | Bound |
|---|---:|
| durable P7-F rows | inherited store bound |
| startup preload | inherited store max, hard ceiling 50,000 |
| template | 2 KiB |
| explicitly rendered users | 8 |
| EventBus subscriptions owned by P7-H | 0 or 1 |
| purge / moderation resources | not part of P7-H |

The in-memory chat map is derived only from the bounded namespace preload and subsequent bounded P7-F writes.

## Acceptance coverage

P7-H acceptance tests cover:

- basic-group join ingress;
- supergroup join ingress;
- goodbye/delete-user ingress;
- joined-by-link/request ingress;
- duplicate user collapse;
- Assistant self exclusion;
- broadcast-channel rejection;
- inactive chat stops before publish;
- inactive service message bypasses global entity cache;
- interest gate precedes peer resolution and allocations;
- interested supergroup peer recovery when entity metadata is absent;
- inactive supergroup does not invoke resolver;
- one shared subscription across multiple enabled features/chats;
- final disable removes the shared subscription;
- disabled durable revision/template remains readable;
- restart preload restores interest/subscription;
- bounded rendered welcome output;
- oversized template rejected before persistence/interest;
- EventBus delivery reuses the application TaskEngine;
- no feature-owned execution engine/goroutine/timer;
- ordinary group text does not enter generic free-form interaction before P7-J;
- terminal Close cannot resurrect subscription or transport.

## Handoff

P7-H closes the event-driven welcome/goodbye foundation.

It intentionally leaves later group-plane work separate:

- **P7-I** — anti-spam / filters / warnings / manager-rules foundation;
- **P7-J** — media/reply/thread/forum-topic semantics;
- **P7-K** — whole group-plane lifecycle/resource hardening;
- **P7-L** — final group-plane acceptance and irrelevant-message benchmark.
