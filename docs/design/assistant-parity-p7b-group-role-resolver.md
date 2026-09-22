# Assistant Parity P7-B — Telegram group role resolver

## Scope

P7-B turns the P7-A contextual-principal slot into an authoritative, bounded Telegram role resolver. It does not decide which commands are allowed; that policy belongs to P7-C.

## Resolution lanes

### Supergroup

Supergroups use the managed `channels.getParticipant` RPC.

`USER_NOT_PARTICIPANT` is an authoritative result:

```text
role = left
verified = true
error = nil
```

It is not treated as a Telegram verification failure.

### Basic group

Telegram basic groups do not use `channels.getParticipant`. P7-B uses the same managed Assistant RPC executor with `messages.getFullChat` and reads `ChatParticipants`.

A complete participant list that does not contain the actor is authoritative `left`. A forbidden or incomplete participant state that cannot identify the requested actor is a verification failure and fails closed.

## Role and rights mapping

P7-B maps Telegram participant state into:

- member
- restricted
- administrator
- creator
- banned
- left

Supergroup administrator and creator rights map into the transport-neutral P7-A subset:

- change info
- delete messages
- ban users
- invite users
- pin messages
- add admins
- manage topics

Basic-group role results are authoritative, but the resolver does not manufacture granular admin rights that Telegram did not provide.

## Cache and pressure bounds

The role cache is keyed by `(chat_id, user_id, chat_kind)`. Expiration is lazy on lookup and capacity eviction is LRU.

| Resource | Bound |
|---|---:|
| Cached observations | 4,096 |
| Simultaneous Telegram verifications | 32 |
| Administrator / creator TTL | 30 seconds |
| Member / restricted TTL | 2 minutes |
| Left / banned TTL | 30 seconds |

When all verification slots are occupied, another verification fails immediately with a resource-limit error. It does not create an unbounded waiter queue and does not issue another Telegram RPC.

Verification failures are never cached. Successful authoritative membership and non-membership results are cacheable.

## Fresh revalidation

The core contract exposes:

- `ResolveGroupRole` for bounded cached preflight;
- `ResolveGroupRoleFresh` to bypass cached authorization state.

`core.Context.ResolveGroupActor(fresh)` merges only the global Goultroid Owner/Sudo identity into the verified contextual Telegram principal. A Telegram administrator never becomes Goultroid Sudo merely because of group role.

This lets P7-C use cached observations for cheap preflight/read-only decisions and fresh resolution after TaskEngine admission before privileged mutations.

## Lifecycle and idle behavior

P7-B creates no:

- goroutine;
- ticker;
- polling loop;
- background cache cleanup;
- startup role scan.

Resolution is occurrence-driven only. An architecture regression test fences the resolver source against background goroutines and periodic timers.

## P7-C handoff

P7-C should add contextual authorization requirements such as:

- member;
- administrator;
- creator;
- administrator with `BanUsers`;
- administrator with `DeleteMessages`;
- administrator with `PinMessages`;
- administrator with `AddAdmins` or `ManageTopics`.

Privileged mutations must use a fresh P7-B observation after TaskEngine admission. The P7-A Assistant mutation safety fence remains in place until the P7-G transport/mutation stage.
