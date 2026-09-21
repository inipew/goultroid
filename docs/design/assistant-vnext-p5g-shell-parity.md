# Assistant vNext P5-G — shell parity before legacy a1 reclamation

## Status

P5-G closes the remaining normal-path Assistant shell dependencies on the legacy a1 menu before any global reclamation starts.

The parity target is deliberately narrow:

- detailed Help module navigation;
- detailed command navigation;
- command metadata/detail rendering;
- shell Close/delete;
- public/non-owner `/start` without constructing a legacy menu instance.

The a1 router/menu code is **not removed** in P5-G. It remains compatibility infrastructure for already-issued callbacks and foundation-unavailable fallback until a separate reclamation audit proves each reference safe to delete.

## Canonical Help source

All a2 Help screens read the current snapshot from:

```text
core.Router.CommandsForSurface(execution.SourceAssistant)
```

There is no Help registry or cache in the shell. Userbot-only commands are excluded by the canonical surface filter before Help sees them.

The shell groups that snapshot by normalized display module and sorts modules and commands deterministically for presentation.

## Typed Help navigation

P2 callback buttons continue to carry only declared action IDs. P5-G does not encode module names, command names, or command indexes into callback payloads.

Static actions are:

```text
help_prev
help_next
help_open
help_cmd_prev
help_cmd_next
help_cmd_open
help_back
```

The existing fixed-size session state reuses `CategoryIndex` and `SettingIndex` as read-only Help cursors while `Screen == Help`.

This does **not** grant any authority. Each action re-reads the canonical command snapshot and clamps the cursor to the current bounds before rendering.

No state-version bump is needed.

## Navigation

```text
Home
  ↓ Help
Modules
  ├─ Previous / Next
  └─ Open
       ↓
Module commands
  ├─ Previous / Next
  └─ Details
       ↓
Command detail
  ├─ Commands
  ├─ Modules
  └─ Home
```

Every navigation action uses P3 `Transition`, so the visible callback revision advances before the next view is shown. Buttons from the previous screen become stale immediately.

A single P1 session owns the entire flow.

## Command detail parity

The a2 command detail covers the legacy Help metadata:

- command name;
- description;
- usage;
- aliases;
- module/category;
- permission tier;
- group/private/reply constraints;
- cooldown;
- timeout.

It additionally shows the number of declared resource requirements when present.

## Cross-domain state hygiene

Entering Help clears the Settings stable binding fingerprint and schema revision.

Therefore a Help cursor can never retain or resurrect mutation authority from a prior Settings detail/input screen.

Returning to Settings must re-resolve and re-bind the current setting definition as before.

## Close/delete parity

P5-G adds deletion as an **optional** presentation capability:

```text
presentation.Port
      +
optional presentation.Deleter
```

This avoids breaking inline-only and synthetic ports.

The Telegram bridge implements `Deleter` through the existing `core.TelegramServicer.DeleteMessage` boundary. The Assistant v2 servicer delegates that operation to the already-existing `ClientInteraction.Delete` implementation, preserving centralized RPC/error/recovery semantics.

The shell Close action:

1. attempts physical message deletion;
2. acknowledges the callback best-effort;
3. cancels the P1 session only after delete succeeds.

If deletion fails, the session remains live so the user is not left with a visible but ownerless message.

## Public `/start`

Before P5-G, a non-owner failed owner-Home admission and was routed into the legacy `/start` menu.

P5-G instead sends a read-only public welcome directly through the command interaction boundary:

```text
/start
  ↓
start deep-link admission succeeds
  ↓
owner Home admission denied
  ↓
public read-only welcome
```

No P1 session and no legacy menu instance are allocated for this public path.

Legacy `/start` fallback remains only for technical compatibility failures such as an unavailable/stale a2 foundation.

## New shell output no longer depends on Classic menu

Newly rendered a2 Home, Settings, Help, Status, and command-detail navigation do not emit `ActionLegacy` buttons.

`ActionLegacy` remains registered temporarily so already-visible pre-P5-G a2 buttons can still hand off safely during rollout. It is compatibility-only and should be removed in the reclamation phase after the token/session grace window is understood.

## Resource behavior

P5-G adds:

- no goroutine;
- no ticker;
- no polling;
- no Help cache;
- no dynamic callback registry;
- no extra session per Help screen.

Help sorting/allocation occurs only when the user navigates Help and is bounded by the canonical Assistant command catalog size.

## Reclamation boundary after P5-G

Global a1 reclamation can now begin as a separate phase, but it must first inventory:

1. already-issued `a1` callbacks and a2 `ActionLegacy` tokens;
2. emergency legacy `/start` fallback;
3. plugin-specific/custom screens registered into `assistant/menu.Registry` (for example non-shell integrations);
4. any remaining `callback.Router` namespaces outside shell parity;
5. menu instance store consumers and locking assumptions;
6. tests that intentionally exercise backward compatibility.

Removal must be reference-driven, not a directory deletion. P5-G only establishes that the current normal Assistant shell no longer requires a1 for feature parity.
