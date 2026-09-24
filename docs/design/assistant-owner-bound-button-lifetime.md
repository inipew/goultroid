# Goultroid — Owner-Bound Interactive Button Lifetime

Status: **implemented**

Branch: `test-next`

Purpose: keep safe interactive buttons usable for the actor who opened the interaction while ensuring copied/shared buttons cannot be executed by another user.

## Security and lifecycle model

Modern a2 sessions are bound to:

```text
feature generation
actor ID
chat/message or inline target
session revision
```

A callback must pass all of those checks before feature code runs.

Therefore another Telegram user cannot operate a button created by someone else's interaction. A copied button on another target also fails binding.

## Lifetime classes

Long-lived does not mean immortal.

The interaction runtime remains bounded and keeps its canonical maximum TTL of 24 hours. Safe navigation surfaces use a **sliding 24-hour idle lease**: every valid owner interaction renews the deadline without changing the session revision.

```text
authorized click
    ↓
actor + target + generation + revision validation
    ↓
Touch(24h)
    ↓
same revision / same state
    ↓
feature action
```

An unauthorized click never reaches `Touch` or the feature handler.

### Long-lived / sliding

- Assistant shell: Home, Status, Help, Settings navigation and setting detail.
- MyXL navigation/read-only screens.
- MyXL legacy quota refresh callback state.
- Calculator interactive state.
- Downloader selection/format workflow before task execution.

### Intentionally short-lived

- Settings free-form input: 2 minutes.
- MyXL free-form input/wizards: 2 minutes.
- MyXL purchase confirmation: 5 minutes.
- MyXL destructive account-delete confirmation: 5 minutes.
- Pending QRIS domain lifetime: 5 minutes.
- Existing single-use transaction callback state remains single-use and short-lived.

These states are not resurrected after expiry.

## MyXL screen lifetime class

MyXL a2 state carries a small `Sustain` flag.

```text
navigation screen
    Sustain=true
    TTL=24h
    valid owner click => Touch(24h)

input screen
    Sustain=false
    TTL=2m

purchase/delete confirmation
    Sustain=false
    TTL=5m
```

The flag is screen-owned rather than inferred from the clicked action. This prevents a Cancel/navigation action on a short-lived confirmation screen from accidentally extending stale confirmation authority if the next render fails.

## Why expired transaction buttons are not revived

After an a2 session is removed, its session-bound slot-to-intent state no longer exists. Reconstructing an old action would require retaining tombstones or introducing a second callback protocol.

More importantly, purchase tokens, confirmation data, and destructive intent must not be revived after their safety window.

Therefore the policy is:

```text
safe navigation/read-only
    => sliding bounded lifetime

input / financial / destructive confirmation
    => explicit short lifetime

hard-expired session
    => reopen interaction
```

## Legacy MyXL refresh

The older MyXL quota refresh path still uses the canonical callback `StateStore`.

Its state is scoped to:

```text
UserID
ChatID
MessageID when bound
Namespace=myxl
```

The read-only refresh TTL is now 24 hours. Each successful refresh replaces the markup with newly scoped state, giving it a sliding practical lifetime.

Purchase confirmation state is unchanged at 5 minutes and `SingleUse=true`.

The callback router validates scope before consuming single-use state, so an unauthorized user cannot burn the owner's purchase token.

## User-facing unauthorized behavior

a2 `ErrBindingMismatch` now answers the callback with an alert:

```text
This button can only be used by the user who opened it on the original message.
```

The failure occurs before TaskEngine feature execution/handler dispatch.

## Resource invariants

No persistence table, worker, ticker, poller, or recovery map was added.

```text
new worker        = 0
new goroutine     = 0
new ticker        = 0
new global map    = 0
new callback proto= 0
new session bytes = MyXL Sustain bool only
```

Runtime bounds remain authoritative:

- maximum sessions;
- per-feature/scope capacity;
- per-actor capacity;
- per-session state bytes;
- total retained state bytes;
- maximum TTL.

## Regression coverage

Coverage added/retained includes:

- wrong actor rejected before a2 handler;
- wrong target rejected before handler;
- copied inline target rejected;
- `Context.Touch` extends expiry without changing revision;
- MyXL feature policy remains owner/private/self-only;
- MyXL long-lived navigation TTL = 24h;
- MyXL input TTL = 2m;
- MyXL confirmation TTL = 5m;
- pending QRIS TTL = 5m;
- legacy MyXL refresh wrong user/chat returns unauthorized;
- unauthorized single-use callback does not consume the owner's state.

## Relevant commits

```text
6c40c895e9524957875a3c562ce0592d3b50676a
fix(assistant): sustain owner-bound MyXL buttons safely

480dbd5940faef63dec0fd7d709fc9725673f00e
fix(myxl): keep sensitive screens on short leases

7e475e7d9920d847595b03f345f385ff994fc5b4
feat(assistant): sustain safe owner-bound interaction buttons
```
