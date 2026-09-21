# Assistant vNext P4 — a2 ingress cutover

P4 wires the P0–P3 interaction foundation into the live Assistant update transport without migrating any real feature UI.

## Protocol discrimination

Assistant callback updates now discriminate protocols before invoking the legacy parser:

- payloads beginning with `a2:` are owned exclusively by the P3 interaction engine;
- all other payloads continue through the existing Assistant `a1/v1` callback router unchanged.

Malformed or stale `a2` payloads never fall back into legacy routing.

## Runtime wiring

The plugin manager remains the lifecycle owner of the shared P1 session runtime and P2 action dispatcher. The Assistant receives those two foundation pointers through `SetInteractionFoundation`.

Each Assistant start/restart creates a fresh transport-facing P3 engine over:

```text
plugin-owned Runtime + Dispatcher
        +
Assistant presentation adapter
```

Restarting the bot transport therefore does not reset feature/session lifecycle ownership.

## Assistant presentation adapter

The P3 Telegram presentation bridge is backed by the existing Assistant `ClientInteraction`, preserving the current managed RPC executor and interaction implementation for:

- send with markup;
- normal message edit;
- inline message edit;
- callback answer.

The ingress tracks successful P3 answers per in-flight query. If a feature handler does not answer, the ingress sends an empty acknowledgement. On dispatch failure it sends a bounded user-facing expiry/retry message. Answer tracking is cleared immediately after dispatch and is not a long-lived callback cache.

## Message and inline callbacks

Normal callbacks derive a P3 Telegram message target from the resolved input peer, chat ID, and message ID.

Inline callbacks derive a stable in-process binding ID from Telegram's inline message identifier and use the same P3 dispatcher.

Both paths retain the existing Assistant rate-limit and shutdown gates before protocol dispatch.

## Compatibility

The legacy Assistant callback router, native menus, core callback fallback, and a1/v1 parsing remain unchanged for non-a2 payloads.

P4 does not migrate /start, settings, menus, downloader, calculator, PM relay, or existing inline handlers.

## Synthetic proof surface

P4 includes a synthetic integration test surface that registers a P0 action, creates a P1 session through P3 Begin, renders a P2 typed button, and feeds the generated a2 bytes through the Assistant ingress. The action is invoked end-to-end without the legacy parser, while a legacy payload is explicitly reported as not handled by the a2 ingress.
