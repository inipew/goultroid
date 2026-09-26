# P1-F1-C — Legacy callback consumer / router / bootstrap inventory

Date: 2026-09-26  
Branch: `test-next`  
Audited baseline: `7ba7ac76a28fb079440df6c54dedf189438462d2`

## Scope

P1-F1-C maps every production edge that still consumes the legacy callback subsystem or keeps its `Router` / `StateStore` reachable.

This phase is inventory/freeze only. It does **not** remove callback routing, state, bootstrap wiring, plugin lifecycle hooks, or compatibility fallbacks.

P1-F1-A established the exact direct-import surface. P1-F1-B established zero production feature v1 producers. P1-F1-C follows those edges through composition, Telegram ingress, Assistant ingress, plugin lifecycle, state ownership, and inline-callback coexistence.

## Executive result

The current source has **no identified production feature implementing the legacy `callback.Handler` namespace contract**, but the legacy subsystem is still fully reachable because compatibility infrastructure remains wired in two transport stacks:

```text
userbot MTProto callbacks
  -> native a2 claim
  -> legacy Router fallback

Assistant bot callbacks
  -> Assistant a2 claim
  -> legacy CoreCallbackDispatcher / Router fallback
```

Both message-origin and inline-message-origin callback updates retain those fallbacks.

Therefore:

```text
known legacy feature producers     = 0
known legacy feature handlers      = 0
legacy consumer/fallback paths     > 0
Router construction/wiring         > 0
StateStore construction/wiring     > 0
```

The remaining stack is infrastructure compatibility, not an identified active feature namespace.

This is still **not** permission to delete it in P1-F1-C. P1-F1-D must classify state/protocol/resource ownership file-by-file, and P1-F1-E must close/freeze the complete inventory.

---

## 1. Composition root

### `internal/app/wiring_core.go`

Current construction:

```go
callbackStore := callback.NewStateStore()
callbackRouter := callback.NewRouter(logger, callbackStore)
callbackRouter.SetMetrics(metrics)
callbackRouter.SetLimiter(interLimiter)
callbackRouter.SetTimeout(15 * time.Second)
```

Classification: `PRODUCTION_COMPATIBILITY`.

This is the single application construction point identified for both legacy objects.

Important shared ownership:

- `metrics` is application-wide and must not be reclaimed with the Router.
- `interLimiter` is also used by the Inline engine and must not be reclaimed with the Router.
- TaskEngine remains canonical execution authority and is not callback-owned.

### `internal/app/dependencies.go`

`coreDependencies` retains:

```text
callbackStore  *callback.StateStore
callbackRouter *callback.Router
```

The public composition struct also retains:

```text
Callbacks *callback.Router
```

Classification: `PRODUCTION_COMPATIBILITY`.

These are compile-time blockers to deleting the callback types even if no feature handler exists.

### `internal/app/wiring_telegram.go`

The legacy router enters the native Telegram dispatcher through:

```text
telegram.DispatcherDeps.CallbackRouter = core.callbackRouter
```

Classification: `PRODUCTION_COMPATIBILITY`.

### `internal/app/app.go`

The composition root keeps the legacy subsystem reachable through four separate edges:

```text
pluginManager.SetCallbackRegistrar(coreDeps.callbackRouter)

tgRuntime.assistant.SetCallbackRouter(coreDeps.callbackRouter)

module.TelegramRuntime.CallbackStore = coreDeps.callbackStore

runtime.Register(coreDeps.callbackStore)
```

The final `App` also retains `callbackStore`.

Classification: `PRODUCTION_COMPATIBILITY`.

No production behavior is changed in this phase.

---

## 2. Plugin lifecycle / namespace registration

### Registration authority

`internal/plugin/manager.go` retains:

```go
type callbackRegistrar interface {
    RegisterOwned(string, callback.Handler) (callback.Registration, error)
}
```

During initial registration and re-enable:

```go
if handler, ok := p.(callback.Handler); ok {
    registration, err := callbackRegistry.RegisterOwned(name, handler)
    ...
}
```

The registration lease is stored in `callbackCleanups`.

On:

- staged-registration failure;
- duplicate/rollback;
- disable;
- re-enable failure;
- global shutdown;

the exact callback registration lease is closed through the plugin lifecycle cleanup budget.

Classification: `PRODUCTION_COMPATIBILITY`.

### Current handler population

P1-F1-A found no production plugin/module implementing the legacy callback ownership contract, and P1-F1-B found no production feature v1 producer.

The application-owned SavedResponse callback feature is explicitly **a2**, not legacy:

```text
internal/assistant/savedresponsecallback.Feature
 -> feature.Spec interaction
 -> Assistant a2 driver
 -> orchestration.Engine
```

Therefore the dynamic `p.(callback.Handler)` path has no identified current built-in participant.

This is source-level ownership proof, not a runtime claim about arbitrary future/external code.

### `HandlerWithOptions`

`callback.HandlerWithOptions` is consumed internally by the legacy callback package to configure ACK behavior. No external production implementation was identified.

It remains part of the legacy subsystem file-level reclamation work for P1-F1-D/F4.

---

## 3. Native/userbot Telegram callback path

### Wiring

`internal/telegram/dispatcher.go` retains:

```text
callbackRouter     *callback.Router
nativeInteractions NativeInteractionDispatcher
```

and `DispatcherDeps.CallbackRouter`.

`internal/telegram/dispatcher_accessors.go` retains:

```text
SetCallbackRouter
getCallbackRouter
```

### Message-origin callback

Current path in `internal/telegram/dispatcher_callback.go`:

```text
UpdateBotCallbackQuery
 -> transport ingress/idempotency
 -> publish canonical callback EventBus event when subscribed
 -> dispatchNativeInteraction
      -> a2 owns a2:* and fails closed if a2 runtime unavailable
      -> non-a2 returns unhandled
 -> getCallbackRouter
 -> Router.Prepare
 -> TaskEngine interactive admission
 -> PreparedCallback.Dispatch
```

### Inline-message callback

`UpdateInlineBotCallbackQuery` follows the same ownership order:

```text
inline callback update
 -> transport ingress/idempotency
 -> callback EventBus publication
 -> native a2 claim
 -> legacy Router fallback
 -> TaskEngine
 -> legacy dispatch
```

Classification: `PRODUCTION_COMPATIBILITY`, but this is an executable production fallback and therefore a hard Router-deletion blocker until F4 replaces it with an explicit unknown-callback policy.

### What is not legacy-owned

The following parts occur before or around legacy dispatch but remain valid after Router deletion:

- transport callback idempotency;
- EventBus callback publication;
- TaskEngine;
- native a2 adapter;
- Telegram service / RPCExecutor;
- plugin scope resolver where still needed by other surfaces.

Do not delete them as part of legacy Router reclamation.

---

## 4. Assistant callback compatibility path

### Type bridge

`internal/assistant/client/servicer.go` defines:

```go
type CoreCallbackDispatcher interface {
    Prepare(...)(corecallback.PreparedCallback, error)
}
```

This interface exists specifically to admit the legacy Router into Assistant without exposing the concrete Router type throughout Assistant code.

### Wiring

`internal/assistant/app.go` keeps `SetCallbackRouter` in the Assistant `Client` contract and forwards it.

`internal/assistant/client/client.go` retains:

```text
callbackDispatcher CoreCallbackDispatcher
SetCallbackRouter(...)
```

At Assistant startup, that dispatcher is copied into `UpdateHandlerDeps.CallbackDispatcher`.

### Assistant message-origin callback

Current path in `internal/assistant/client/updates.go`:

```text
UpdateBotCallbackQuery
 -> shutdown/entity/target handling
 -> if data is a2:
      interactionIngress.tryMessage
      -> orchestration a2
      -> TaskEngine
 -> else:
      build core.CallbackQueryEvent
      -> dispatchCoreCallback
      -> legacy CoreCallbackDispatcher.Prepare
      -> TaskEngine
      -> PreparedCallback.Dispatch
```

### Assistant inline-message callback

Current path:

```text
UpdateInlineBotCallbackQuery
 -> if data is a2:
      interactionIngress.tryInline
 -> else:
      build inline CallbackQueryEvent
      -> dispatchCoreCallback
      -> legacy Router Prepare/Dispatch
```

Classification: `PRODUCTION_COMPATIBILITY`.

### Assistant dedupe is shared, not legacy-owned

`callbackQueryDeduper` is used by both:

- the a2 branch before `InteractionIngress`;
- the legacy `dispatchCoreCallback` branch.

It must **not** be deleted merely because the legacy Router is deleted.

The same principle applies to Assistant TaskEngine admission and ordering keys.

---

## 5. Inline coexistence

Three distinct concepts must remain separate:

### Inline queries

```text
UpdateBotInlineQuery
 -> services/inline Engine
 -> TaskEngine
```

This is independent of the legacy callback Router.

### a2 inline-message callbacks

```text
UpdateInlineBotCallbackQuery with a2:*
 -> a2 interaction ingress
```

Canonical and retained.

### legacy inline-message callbacks

```text
UpdateInlineBotCallbackQuery with non-a2 data
 -> legacy Router fallback
```

Compatibility only.

Router removal must therefore remove only the third branch. It must not remove Inline query handling or a2 inline callbacks.

---

## 6. Legacy Router internals still reachable

`internal/services/callback/router.go` currently owns:

```text
RegisterOwned
Prepare
ParseCallbackData
handler registration identity
plugin-generation scope resolution
rate limiting
StateStore claim/scope validation
HandlerWithOptions ACK policy
CallbackContext construction
middleware/timeout/recover
handler dispatch
fallback callback answers
metrics
```

The Router also special-cases the unversioned control payload:

```text
noop
```

before parsing v1.

This is not a feature namespace, but it is part of legacy transport compatibility and must be accounted for before Router deletion.

`internal/ui.NoopData` also defines raw `noop` callback bytes. Generic UI builders can place those bytes into transport-ready legacy buttons.

P1-F1-B's conclusion remains valid—there are zero identified **v1 feature namespace producers**—but P1-F1-C records `noop` as a separate protocol-control residual.

F1-D/F4 must decide the correct replacement behavior for stale/unknown/no-op non-a2 callback data rather than silently relying on the legacy Router.

---

## 7. StateStore reachability

### Internal Router ownership

The Router holds:

```text
stateStore *StateStore
```

and `PreparedCallback.Dispatch` reaches `resolveState`, which claims and validates:

- actor/user;
- namespace;
- plugin generation;
- chat;
- message;
- single-use semantics;
- TTL.

This is the direct runtime dependency keeping StateStore semantics attached to legacy dispatch.

### Application/module ownership

Outside the Router, StateStore remains reachable through:

```text
internal/app/wiring_core.go
 -> callback.NewStateStore

internal/app/app.go
 -> runtime.Register(callbackStore)
 -> module.TelegramRuntime.CallbackStore
 -> App.callbackStore

internal/module/module.go
 -> TelegramRuntime.CallbackStore callback.StateWriter
 -> Runtime.ScopedCallbackStore(owner)
 -> callback.NewScopedStateWriter(...)
```

### Current writer population

No current production module was found calling `Runtime.ScopedCallbackStore`.

P1-F1-B also found no v1 feature producer.

Therefore no production feature StateStore writer is currently identified.

Classification of the remaining module/state API: `PRODUCTION_COMPATIBILITY`.

StateStore deletion is nevertheless blocked until P1-F1-D closes the file-by-file state/protocol ownership audit and P1-F3 executes the planned state/protocol reclamation.

---

## 8. Exact Router deletion blockers

The following production surfaces must be removed or adapted before `callback.Router` can disappear:

| Area | Files | Blocking edge |
|---|---|---|
| construction | `internal/app/wiring_core.go` | `callback.NewRouter` |
| dependency graph | `internal/app/dependencies.go` | typed router fields |
| native wiring | `internal/app/wiring_telegram.go`, `internal/telegram/dispatcher.go` | `DispatcherDeps.CallbackRouter` |
| native accessors | `internal/telegram/dispatcher_accessors.go` | setter/getter + stored router |
| native ingress | `internal/telegram/dispatcher_callback.go` | non-a2 message + inline fallback |
| plugin registration | `internal/plugin/manager.go` | registrar, `callback.Handler` assertions, cleanup leases |
| app plugin wiring | `internal/app/app.go` | `SetCallbackRegistrar` |
| Assistant API | `internal/assistant/app.go` | `SetCallbackRouter` contract |
| Assistant concrete client | `internal/assistant/client/client.go` | stored dispatcher + setter/startup dependency |
| Assistant type bridge | `internal/assistant/client/servicer.go` | `CoreCallbackDispatcher` / `PreparedCallback` |
| Assistant ingress | `internal/assistant/client/updates.go` | non-a2 message + inline fallback |
| Assistant executor bridge | `internal/assistant/client/callback_dispatch.go` | legacy Prepare -> TaskEngine -> Dispatch |

These are compatibility blockers, not evidence of an active feature namespace.

---

## 9. Exact StateStore deletion blockers

| Area | Files | Blocking edge |
|---|---|---|
| construction | `internal/app/wiring_core.go` | `callback.NewStateStore` |
| application ownership | `internal/app/dependencies.go`, `internal/app/app.go` | concrete field + runtime registration |
| module capability | `internal/module/module.go` | `CallbackStore`, `ScopedCallbackStore` |
| module composition | `internal/app/app.go` | injects store into `module.TelegramRuntime` |
| Router runtime | `internal/services/callback/router.go` | state claim / scope validation / handler dispatch |
| state implementation | `internal/services/callback/store.go`, `scope.go`, `scoped_writer.go`, `lifecycle.go` | legacy state authority |

No production feature writer is currently identified.

---

## 10. Resource/lifecycle ownership that must survive reclamation

Do **not** conflate these shared mechanisms with the legacy callback stack:

| Mechanism | Why it survives |
|---|---|
| `TaskEngine` | canonical execution/admission for a2 and other work |
| `interLimiter` | shared with Inline engine |
| metrics collector | application-wide |
| native callback idempotency | transport-level protection also precedes a2 |
| Assistant `callbackQueryDeduper` | used by a2 and legacy branches |
| plugin generation scopes | used by feature/a2 lifecycle generally |
| EventBus callback events | generic update/event surface |
| Telegram RPCExecutor | canonical Telegram RPC authority |
| Inline engine | independent query runtime |

Legacy StateStore itself is passive: no cleanup goroutine/ticker is owned by it.

---

## 11. Dependency graph

### Native/userbot

```text
buildCore
 -> StateStore
 -> Router
 -> plugin.Manager callback registrar (no current Handler participant)
 -> Dispatcher.CallbackRouter

Telegram UpdateBotCallbackQuery / UpdateInlineBotCallbackQuery
 -> transport dedupe / canonical event
 -> native a2 claim
 -> [non-a2 only] Router.Prepare
      -> v1 parser OR raw noop
      -> handler lookup
      -> plugin generation scope
 -> TaskEngine
 -> PreparedCallback.Dispatch
      -> StateStore claim if opaque state exists
      -> Handler
```

### Assistant

```text
buildCore.Router
 -> Assistant.SetCallbackRouter
 -> AssistantClient.callbackDispatcher
 -> UpdateHandlerDeps.CallbackDispatcher

Assistant callback update
 -> a2 prefix?
      yes -> InteractionIngress -> a2
      no  -> dispatchCoreCallback
              -> Router.Prepare
              -> TaskEngine
              -> PreparedCallback.Dispatch
```

---

## 12. P1-F1-C freeze fence

`internal/architecture/legacy_callback_consumers_p1f1c_test.go` freezes the current high-risk compatibility topology.

It rejects new production files that introduce the following consumer edges outside their exact path allowlists:

```text
callback.NewRouter(
callback.NewStateStore(
SetCallbackRegistrar(
p.(callback.Handler)
SetCallbackRouter(
getCallbackRouter()
dispatchCoreCallback(
ScopedCallbackStore(
```

The allowlists are intentionally stale-sensitive: when an edge is removed, the test requires the corresponding allowlist entry to shrink.

This is not the final F1 freeze; P1-F1-E will combine importer, producer, consumer, state/protocol, and namespace evidence.

---

## 13. P1-F2 implication

P1-F1-C found **no hidden production feature namespace handler**.

Together with F1-B:

```text
feature v1 producers = 0
feature legacy handlers = 0
```

So there is still no concrete namespace candidate for P1-F2.

Do not formally declare P1-F2 empty yet. P1-F1-D must first classify every legacy state/protocol/resource file and verify that no useful generic responsibility is hiding inside the callback package.

If F1-D confirms compatibility-only ownership, F1-E can make the authoritative decision that F2 contains no namespace migration work.

---

## Closure

P1-F1-C conclusion:

- exact native and Assistant legacy consumer paths are mapped;
- both message and inline-message legacy fallbacks are still executable;
- no current production `callback.Handler` feature participant is identified;
- exact Router deletion blockers are known;
- exact StateStore deletion blockers are known;
- Inline engine/a2/shared resource ownership is separated from legacy ownership;
- raw `noop` is recorded as a non-namespace legacy control residual;
- no production callback behavior was changed.

Next phase, only after explicit user confirmation:

**P1-F1-D — state/protocol/resource ownership inventory.**
