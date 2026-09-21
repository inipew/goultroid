# Assistant vNext P5 — first real feature migration / Assistant shell

## Status

P5 migrates the first production Assistant surface onto the P0-P4 interaction foundation:

- Telegram `/start` remains the transport-specific ingress;
- the owner/private root screen is now the lifecycle-owned `assistant_shell` feature;
- the rendered shell uses P1 sessions, P2 typed actions, P3 orchestration, and P4 a2 callback ingress;
- settings, help, status, and other feature UIs remain on the legacy a1 stack.

## Feature ownership

`assistant_shell` is registered through the normal plugin manager, even though it owns no commands, goroutines, or platform capabilities.

Its P0 contract declares:

- `deep_link:start` — public/private Assistant ingress;
- `screen:home` — owner/private root screen;
- `action:refresh` — owner/private state transition;
- `action:ping` — owner/private callback answer;
- `action:legacy` — owner/private compatibility handoff.

The plugin manager therefore supplies a real `ScopeIdentity` generation. Disable/re-enable removes the old catalog entry, handlers, and sessions before publishing a replacement generation.

## Admission

P5 makes P0 interaction policy executable through `feature.AdmitInteraction`.

Admission evaluates, independently:

1. declared transport surface;
2. private/group chat scope;
3. invocation policy;
4. permission tier.

The shell checks both the `start` deep-link declaration and the owner-only `home` screen before creating a P1 session.

The migration is intentionally conservative:

- owner in a private chat -> a2 shell;
- visitors, groups, or an unavailable/disabled shell -> legacy `/start` compatibility path. P1 capacity/runtime-closed failures remain fail-closed and do not bypass the bounded session runtime.

No visitor behavior is removed during this canary phase.

## Root screen

The a2 home screen is transport-neutral `presentation.View` data.

It exposes only three canary actions:

- **Refresh** — increments bounded opaque session state, calls `Context.Transition`, advances the P1 revision, and edits the same message with newly compiled a2 buttons;
- **Ping** — answers the callback through the P3 presentation port without mutating state;
- **Classic menu** — intentionally hands the same message back to the existing a1 menu, registers the legacy menu instance, then cancels the a2 session.

Settings and the broader navigation tree are deliberately not migrated in P5.

## Stale-button proof

`Refresh` uses `Transition`, which updates state before compiling the replacement view.

Therefore:

```text
revision 1 button
      ↓ Refresh
revision 2 state + markup
      ↓
old revision 1 callback
      ↓
ErrStaleToken
```

An older button cannot execute against the newer shell state.

## Reload-generation proof

The shell action registrations are rebound lazily to the current P0 feature scope.

On plugin disable:

```text
catalog registration removed
      ↓
old action handlers unregistered
      ↓
old sessions canceled
```

On re-enable, the feature receives a new generation. The next owner `/start` binds the three shell handlers to that new scope. Stale registration handles cannot remove the replacement generation.

## Compatibility fallback

The old `/start` implementation is preserved as an explicit handler object rather than deleted.

P5 chooses that handler when the a2 shell is intentionally unavailable or not admitted. The **Classic menu** action also provides a controlled a2 -> a1 handoff for the owner, ensuring settings/help remain reachable without running two active interaction sessions on the same message.

## Scope intentionally deferred

P5 does not migrate:

- settings;
- help browser;
- status screen;
- close/delete behavior;
- public visitor UX;
- inline features;
- PM relay;
- downloader/calculator;
- self-inline render bridge.

Those should move only after this canary has proven the production P0-P4 path under normal lifecycle operations.
