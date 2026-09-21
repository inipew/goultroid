# Assistant vNext P5-B — read-only root destinations

## Status

P5-B extends the production `assistant_shell` canary beyond the root Home screen by migrating the two lowest-risk read-only destinations to a2:

- **Status**
- **Help overview**

Settings, free-form input, persisted mutations, detailed help pages, and other feature workflows remain on the legacy a1 path.

## Navigation model

The owner/private Assistant shell now follows one bounded P1 session across multiple screens:

```text
Home
 ├─ Help
 │    └─ Home
 └─ Status
      ├─ Refresh
      └─ Home
```

Every navigation uses `orchestration.Context.Transition`. Even when opaque state bytes do not change, the P1 revision advances before the new View is compiled. Buttons from the previous screen are therefore stale immediately after navigation.

This intentionally proves multi-screen lifecycle and stale-button behavior before adding mutable settings state.

## P0 contract additions

`assistant_shell` now declares read-only screen metadata for:

- `screen:status`
- `screen:help`

and typed actions for:

- `action:status`
- `action:help`
- `action:home`
- `action:status_refresh`

All remain owner-only, Assistant-only, and private-chat-only.

Action admission is re-evaluated at callback time, and the destination screen policy is also re-evaluated before a transition. A live session therefore cannot bypass a later owner/policy change simply because its token was minted earlier.

## Status

The a2 Status View keeps the existing read-only semantics:

- Assistant username
- operational status
- uptime
- transport engine
- callback availability

`status_refresh` updates the same bounded 8-byte shell state counter and re-renders the same message. It does not create a new session, timer, goroutine, or cache entry.

## Help overview

The a2 Help root reads directly from:

```text
core.Router.CommandsForSurface(execution.SourceAssistant)
```

There is no parallel help registry.

The View derives a deterministic, sorted module summary from the current canonical Assistant command set. Userbot-only commands are not included. The module list is capped to a small display bound so a growing plugin catalog cannot make the root Help message unbounded.

Detailed module pagination and command-detail pages intentionally remain in the legacy Classic menu for now.

## State and revision behavior

The shell continues to retain only an 8-byte refresh counter. Screen identity is presentation state, not a second retained state machine.

Navigation performs:

```text
current session revision N
        ↓
Transition(existing state, new View)
        ↓
revision N+1
        ↓
old buttons -> ErrStaleToken
```

Status refresh performs the same sequence with an incremented refresh counter.

## Compatibility

The **Classic menu** handoff remains available from Home and Help. It edits the same Telegram message into a1 markup and cancels the a2 session before legacy menu ownership begins.

P5-B does not remove or alter the existing a1 Help/Status routes. They remain valid compatibility surfaces for legacy menu instances and non-canary flows.

## Tests

P5-B adds coverage proving:

- Status and Help are declared owner/private a2 screens;
- Help summary ordering is deterministic;
- Help reads only Assistant-surface canonical commands;
- Home -> Status -> Status refresh -> Home -> Help -> Home remains one P1 session;
- navigation increments revision and makes old buttons stale;
- action admission reacts to owner changes during a live session;
- plugin generation reload behavior from P5 remains intact.

## Deferred work

The next migration should be Settings, but only after the read-only navigation canary remains stable. Settings adds materially different risk:

- persistent mutations;
- validation errors;
- free-form input sessions;
- optimistic state changes and cancellation;
- recovery from transport/persistence failure.

Those concerns should not be mixed into P5-B.
