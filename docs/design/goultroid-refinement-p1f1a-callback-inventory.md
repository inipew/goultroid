# P1-F1-A — Legacy callback production import / ownership inventory

Date: 2026-09-26  
Branch: `test-next`  
Audited baseline: `a223c4137ba0c873c778e3c0188d32daeb6d5d5f`

## Scope

This document is the authoritative P1-F1-A inventory for the legacy callback subsystem:

`github.com/inipew/goultroid/internal/services/callback`

P1-F1-A is inventory/freeze only. It does **not** migrate or delete production callback behavior.

The audit started from the recursive repository tree rather than GitHub code search. The baseline contains 519 non-test, non-vendor Go files. GitHub code search was not treated as authoritative because it reported incomplete results for this branch. Production plugin/module surfaces, composition roots, Telegram ingress, Assistant bridge, UI helpers, and the callback package itself were then inspected against the legacy API tokens required by the handoff.

## Classification vocabulary

- `PRODUCTION_ACTIVE`: currently owns a live legacy feature/namespace.
- `PRODUCTION_COMPATIBILITY`: production wiring/fallback retained only for legacy compatibility.
- `GENERIC_UTILITY`: reusable helper coupled to the legacy package, but not itself a feature namespace.
- `TEST_ONLY`: non-production only.
- `DEAD`: no production ownership/caller found; deletion still requires the later phase that proves the relevant caller class is zero.
- `FALSE_POSITIVE`: token/name is unrelated to the legacy callback subsystem.

## Direct production imports

| File | Legacy symbol / responsibility | Owner namespace | Classification | Migration needed | Expected replacement / retirement authority | Deletion blocked now |
|---|---|---:|---|---|---|---|
| `internal/app/app.go` | `*callback.StateStore`; passes callback router into plugin manager and Assistant; registers callback store runtime component | none / composition | `PRODUCTION_COMPATIBILITY` | yes | a2 runtime + removal of legacy store/router wiring in P1-F3/F4 | yes |
| `internal/app/dependencies.go` | core dependency fields `*callback.StateStore`, `*callback.Router`; exported dependency surface | none / composition | `PRODUCTION_COMPATIBILITY` | yes | a2-only composition after legacy consumers reach zero | yes |
| `internal/app/wiring_core.go` | constructs `callback.NewStateStore()` and `callback.NewRouter(...)`; limiter/metrics/timeout wiring | none / composition | `PRODUCTION_COMPATIBILITY` | yes | remove construction after F1/F2 prove no feature namespace dependency | yes |
| `internal/assistant/client/servicer.go` | `CoreCallbackDispatcher.Prepare(...)(corecallback.PreparedCallback, error)` | none / Assistant bridge | `PRODUCTION_COMPATIBILITY` | yes | Assistant a2 interaction dispatch; remove legacy dispatcher bridge in P1-F4 | yes |
| `internal/module/module.go` | exposes `callback.StateWriter` in `TelegramRuntime`; `ScopedCallbackStore(owner)` | none / module compatibility | `PRODUCTION_COMPATIBILITY` | yes | a2 session state; remove legacy writer surface in P1-F3 | yes |
| `internal/plugin/manager.go` | callback registrar; runtime assertion `p.(callback.Handler)`; registration cleanup on lifecycle/reload | dynamic legacy plugin namespace, currently none found | `PRODUCTION_COMPATIBILITY` | yes | interaction driver registration / normal plugin lifecycle; remove assertion path in P1-F4 | yes |
| `internal/telegram/dispatcher.go` | stores `*callback.Router` and accepts it in dispatcher config | none / Telegram ingress | `PRODUCTION_COMPATIBILITY` | yes | native a2 callback ingress only, then explicit unknown-callback policy | yes |
| `internal/telegram/dispatcher_accessors.go` | `SetCallbackRouter` / `getCallbackRouter` | none / Telegram ingress | `PRODUCTION_COMPATIBILITY` | yes | remove legacy router field/accessors in P1-F4 | yes |
| `internal/ui/toast.go` | toast helpers accept `*callback.CallbackContext` | none / UI helper | `GENERIC_UTILITY` | maybe: move or delete according to caller audit | presentation/a2 response helper if still useful; otherwise delete | yes, pending F1-D/P2-D caller proof |

The direct import surface is frozen by `internal/architecture/legacy_callback_imports_p1f1a_test.go`. The allowlist is exact and must only shrink.

## Production ownership without a direct import

These files participate in legacy ownership through types/interfaces defined elsewhere and therefore matter even though they do not directly import the package:

| File | Symbol / edge | Classification | Meaning |
|---|---|---|---|
| `internal/app/wiring_telegram.go` | passes `core.callbackRouter` as `CallbackRouter` | `PRODUCTION_COMPATIBILITY` | keeps the legacy router reachable from Telegram runtime |
| `internal/assistant/client/client.go` | `SetCallbackRouter(CoreCallbackDispatcher)` | `PRODUCTION_COMPATIBILITY` | retains the legacy core callback bridge behind an interface |
| `internal/telegram/dispatcher_callback.go` | native a2 claim first, then `getCallbackRouter().Prepare(...)`, then TaskEngine dispatch | `PRODUCTION_COMPATIBILITY` | this is the native/userbot legacy fallback path; do not delete before F1-C/F2 proof |

The native Telegram callback path is therefore currently:

```text
Telegram callback update
 -> native a2 dispatch attempt
 -> legacy Router fallback
 -> PreparedCallback
 -> TaskEngine
 -> legacy handler (if registered)
```

P1-F1-C remains responsible for the full consumer/bootstrap dependency graph and deletion blockers.

## Feature / namespace ownership result

### Known-zero domains reconfirmed

- `plugins/settings`: no production legacy callback import, `callback.Handler`, `SetStateStore`, `RequiresCallbackState`, or `HandleCallback` surface found.
- `plugins/myxl`: no production legacy callback import, `callback.Handler`, `SetStateStore`, `RequiresCallbackState`, or `HandleCallback` surface found.

### Plugin inventory

All production `plugins/*/module.go` files were inspected for legacy callback ownership hooks including `ScopedCallbackStore`, `SetStateStore`, `RequiresCallbackState`, `HandleCallback`, and callback namespace ownership. No current plugin module was found wiring a legacy callback store or handler.

Plugin implementation files sampled across every plugin domain likewise showed no direct import of `internal/services/callback`. In particular, no current plugin implementation was found satisfying the legacy `callback.Handler` surface by an explicit legacy dependency.

**P1-F1-A conclusion:** no `PRODUCTION_ACTIVE` feature namespace is identified at the import/ownership layer. The remaining legacy dependency is infrastructure compatibility plus a generic UI helper. This does **not** yet prove that the legacy protocol has zero producers or that all router/state consumers are removable; those are intentionally deferred to P1-F1-B/C/D.

## Callback subsystem itself

`internal/services/callback/` remains the legacy subsystem authority and is not counted as an external import caller. Current responsibilities observed here include:

- `Router` and owned registrations;
- `Handler` / `HandlerWithOptions`;
- `PreparedCallback` admission/dispatch;
- v1 protocol parsing/encoding;
- `StateStore`, `StateReader`, `StateWriter`, scoped writer;
- callback scope validation;
- rate limiting / metrics / rejection feedback;
- passive lifecycle integration.

Detailed file-by-file deletion/move classification belongs to P1-F1-D. No callback package file is deleted or rewritten in P1-F1-A.

## State/resource observation relevant to later phases

The current legacy `StateStore` is bounded at 5,000 entries and 8 MiB retained state. Its runtime `Start` is intentionally passive and expiration is opportunistic; `Stop` is a no-op, so the store does not own a cleanup goroutine. These facts are recorded only to prevent later reclamation work from assuming a permanent callback cleanup worker exists.

## Freeze contract

The P1-F1-A architecture fence enforces:

1. every production Go file importing `internal/services/callback` must be one of the exact files listed above;
2. every allowlisted importer must still exist as an importer, forcing intentional allowlist shrink when a dependency is removed;
3. a new production import fails the architecture test immediately;
4. Settings/MyXL remain outside the allowlist.

This freeze is intentionally limited to direct package imports. P1-F1-E will strengthen the final freeze with the producer and namespace allowlists after F1-B/C/D establish them.

## What P1-F1-A does not claim

P1-F1-A does **not** claim:

- zero legacy v1 payload producers;
- zero legacy parser/encoder callers;
- zero router/bootstrap consumers;
- zero state/protocol callers;
- safe deletion of `StateStore` or `Router`;
- safe removal of Assistant/native fallback wiring.

Those are the explicit subjects of P1-F1-B through P1-F1-D.

## Closure

P1-F1-A is complete when this inventory and the import freeze fence are committed.

Next phase, only after explicit user confirmation:

**P1-F1-B — legacy producer inventory.**
