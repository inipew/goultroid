# Assistant vNext P5-F — Settings parity and cutover cleanup

## Status

P5-F completes the Settings ownership cutover from the legacy a1 Assistant menu to the P0-P5 a2 interaction stack.

At this point a2 Settings has parity for the user-facing workflows that previously required the legacy Settings menu:

- browse categories and effective values;
- open setting details;
- bool and enum mutation;
- integer and duration stepping;
- user-scope reset;
- bounded free-form string input;
- cancel and TTL expiry;
- sensitive-value masking;
- stale schema / registry reorder protection;
- persistence no-op reporting;
- persistence and post-commit presentation recovery.

Because the a2 flow now owns the complete Settings mutation surface, P5-F removes the remaining dual-write path.

## Write ownership

After P5-F, **only a2 Settings may create new Settings writes** from the Assistant UI.

The authoritative path is:

```text
/start a2 shell
  → Settings
  → bound namespace:key + schema revision
  → revision-fenced mutation/input
  → SetRegisteredResult / ResetRegisteredResult
```

Legacy a1 Settings callbacks remain parseable only for compatibility with messages rendered before the cutover.

## Legacy compatibility policy

The old callback namespace is intentionally not removed abruptly.

Stale callbacks still behave as follows:

| Legacy callback | P5-F behavior |
| --- | --- |
| `a1:settings:home` | read-only compatibility browser |
| `a1:settings:category` | read-only category browser |
| `a1:settings:info` | read-only setting detail |
| `a1:settings:set` | cutover notice, **no persistence** |
| `a1:settings:reset` | cutover notice, **no persistence** |
| `a1:assistant:settings` | cutover screen telling the owner to reopen `/start` |

This lets already-rendered messages fail forward without retaining the weaker legacy write contract.

## No new legacy Settings workflows

The Classic root no longer generates an a1 Settings button.

The old Assistant Settings screen no longer generates an `a1:settings:home` dashboard button.

New legacy setting-detail renders no longer include Change, Decrease, Increase, or Reset buttons.

Therefore no new a1 Settings mutation/input workflow can be created after the cutover.

## Legacy pending-input removal

The old Settings implementation retained:

```text
map[userID]pendingSettingInput
```

with its own TTL and target state.

P5-E moved input ownership into the bounded P1 runtime, so P5-F removes that Settings-specific pending map and all associated code:

- legacy `beginStringInput`;
- legacy pending TTL;
- legacy pending-setting target retention;
- direct `Service.Set` from ordinary text ingress;
- SettingsService dependency from `UpdateHandlerDeps`.

The generic Assistant `TextHandler` extension point remains intact for unrelated features.

## Transport ingress after cutover

Ordinary Assistant text now follows:

```text
Telegram message
  ↓
a2 TakeInput(actor, chat)
  ├─ claimed → P5-E Settings input
  └─ not claimed
       ↓
generic legacy TextHandlers
       ↓
normal command router
```

There is no second Settings input consumer after the a2 claim check.

## Settings UI cleanup

The a2 Settings root no longer exposes **Classic menu** as a Settings fallback.

Classic menu remains reachable from the broader Assistant shell for still-unmigrated surfaces such as detailed Help, but Settings itself is now an a2-owned destination.

## Safety properties preserved

P5-F does not weaken the P5-D/E invariants:

- cursor index is never write authority;
- stable `namespace:key` fingerprint is required;
- bound schema revision must still match;
- session revision is consumed before persistence;
- input is actor/chat bound;
- input TTL is bounded;
- plugin reload/session shutdown clears claims;
- sensitive result values remain redacted;
- persistence success is never rolled back because a Telegram edit failed.

## Resource impact

P5-F removes one legacy retained map and one duplicate input state machine.

It adds:

- no goroutine;
- no timer;
- no polling;
- no new cache;
- no new persistence path.

## Deferred compatibility removal

The a1 Settings **read-only** handlers are deliberately retained for one compatibility phase so old inline keyboards do not immediately become invalid.

A later global Assistant migration/removal phase may delete the entire a1 menu stack once Help and the remaining legacy destinations have also moved to a2. P5-F does not remove shared a1 infrastructure prematurely.
