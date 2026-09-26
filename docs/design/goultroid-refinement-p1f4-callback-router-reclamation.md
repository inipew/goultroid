# P1-F4 — Legacy Router + bootstrap/plugin wiring reclamation

Date: 2026-09-26
Branch: `test-next`
Baseline before implementation: `58da6185bdc8dc9d06650993546653a39d7d30ca`

## Scope

P1-F4 removes the temporary legacy callback Router/Handler compatibility shell left by P1-F3.

P1-F5 is not part of this change. This phase performs the production reclamation and installs explicit non-a2 callback handling at ingress; P1-F5 will perform final repo-wide behavior/resource acceptance.

## Removed legacy authority

Removed from production:

- `internal/services/callback`
- `callback.Router`
- `callback.Handler` / `HandlerWithOptions`
- `CallbackContext` / `PreparedCallback` / `Registration`
- legacy callback middleware
- plugin-manager callback registrar/cleanup leases
- app callback Router construction
- Dispatcher callbackRouter field/deps/accessors
- Assistant `CoreCallbackDispatcher` and `dispatchCoreCallback`

The legacy callback package is deleted rather than moved.

## Native/userbot ingress after P1-F4

```text
Telegram callback
 -> transport idempotency / EventBus
 -> native a2 ownership
 -> non-a2:
      raw noop -> silent ACK
      otherwise -> expired ACK
```

Unknown callbacks no longer enter TaskEngine, plugin scope resolution, shared interaction limiter, or namespace routing.

## Assistant ingress after P1-F4

```text
Assistant callback
 -> a2?
      yes -> bounded callback dedupe -> InteractionIngress
      no  -> bounded callback dedupe -> explicit noop/expired ACK
```

The Assistant callback deduper remains because it also protects canonical a2.

## Plugin lifecycle

`plugin.Manager` no longer imports callback, asserts `callback.Handler`, stores callback registrations, or runs callback rollback/disable/re-enable/shutdown cleanup.

Feature registration, message hooks, generation scopes, TaskEngine cancellation, command cleanup, and plugin shutdown remain unchanged.

## Shared infrastructure preserved

P1-F4 keeps a2 runtime, native/Assistant interaction ingress, TaskEngine, callback idempotency/dedupe, EventBus, Inline engine, shared interaction limiter, RPCExecutor, plugin generation scopes, and presentation transport.

## UI compatibility cleanup

`internal/ui/toast.go` no longer depends on `CallbackContext` or callback-only errors. Generic core/a2 user-error presentation remains.

## Resource effect

Reclaimed: Router handler map/lock, registration leases, plugin callback cleanup map, callback middleware/timeout path, callback Router wiring, and legacy prepared-dispatch TaskEngine path.

The deleted callback stack owned no background goroutine/ticker, so idle goroutine savings are zero. Shared limiter and TaskEngine remain.

## Verification

No CI was inspected.

There is no executable repository checkout in the container, so full build/test and an actual `gofmt` executable pass are not claimed. New/rewritten Go text is kept gofmt-compatible; P1-F5 must include executable verification when a checkout is available.

## Closure

P1-F4: **CLOSED** after commit.

Next, only after explicit confirmation:

**P1-F5 — repo-wide legacy callback final acceptance.**
