# Assistant vNext P2 — Presentation / Render Bridge

P2 sits on P0 feature identity and P1 session/token lifecycle. It does not migrate any feature UI.

## Contract

Feature code builds a transport-neutral `presentation.View` containing text and rows of buttons. Action buttons carry only a declared `ActionID`; they cannot supply callback bytes.

`presentation.Compiler` resolves every ActionID through the P1 session runtime and produces an `a2` callback token for the current session revision.

The presentation port is intentionally small:

- `Send(ctx, target, compiledView)`
- `Edit(ctx, target, compiledView)`
- `Answer(ctx, answer)`

Telegram-specific targets and MTProto markup exist only in `internal/presentation/telegram`.

## Typed action dispatch

`interaction.Dispatcher` registers handlers against a feature ID, action ID, and exact plugin `ScopeIdentity`. Dispatch first calls P1 `ResolveCallback`, therefore token version, TTL, actor/chat/message binding, generation, revision, and action declaration are validated before handler execution.

Registration cleanup uses a token so stale cleanup cannot remove a replacement registration. Dispatch also rechecks the current feature generation immediately before invoking the handler.

## Telegram bridge

The Telegram adapter supports:

- sending a normal message and returning its concrete message target;
- editing normal messages;
- editing inline-sent messages;
- answering callback queries.

The adapter is the only P2 layer that imports `gotd/tg`.

## Deferred migration

P2 deliberately does not migrate /start, settings, calculator, downloader, menus, inline handlers, PM relay, or any other feature UI. Existing a1/v1 callback paths remain untouched until feature-by-feature migration.
