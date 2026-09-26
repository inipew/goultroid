# P1-F1-D — Legacy callback state / protocol / resource ownership inventory

Date: 2026-09-26  
Branch: `test-next`  
Audited baseline: `d2b1e2a2661ce21b390a7046e320848a230d9928`

## Scope

P1-F1-D classifies every production file under:

`internal/services/callback/`

by ownership, deletion/move destination, lifecycle/resource cost, and the phase in which it can safely disappear.

P1-F1-A established the direct importer set. P1-F1-B established zero production feature v1 producers. P1-F1-C established zero identified production legacy feature handlers while mapping the still-live native and Assistant compatibility consumers.

P1-F1-D does **not** delete or migrate production callback behavior.

## Executive result

The legacy callback package contains eight production Go files:

```text
lifecycle.go
middleware.go
router.go
scope.go
scoped_writer.go
state.go
store.go
types.go
```

There is no production file in this package that must be moved wholesale to another authority.

Every production responsibility is one of:

1. legacy v1 protocol/state ownership;
2. legacy Router/Handler compatibility;
3. helper behavior already owned canonically by a2/TaskEngine/presentation elsewhere;
4. an empty wiring/index file.

The package therefore has a complete deletion path. The only required caution is **staging**: `router.go` currently depends directly on v1 parsing and StateStore semantics, while the roadmap retires state/protocol in P1-F3 and Router/wiring in P1-F4.

The safe interpretation is:

```text
P1-F3
  -> remove StateStore/module writer surfaces
  -> remove v1 producer/parser/state protocol
  -> narrow any temporarily retained Router compatibility shell so it no longer
     depends on v1 state/protocol

P1-F4
  -> remove the remaining Router/Handler compatibility shell
  -> remove plugin-manager registration and transport/bootstrap fallbacks
```

Do not attempt to delete parser/state files while leaving the current Router implementation unchanged.

---

## File-by-file ownership and deletion plan

| File | Current ownership | Classification | Canonical replacement / destination | Planned action |
|---|---|---|---|---|
| `store.go` | bounded opaque callback StateStore, defensive clone, TTL, single-use claim, retained-byte accounting | `LEGACY_STATE` | `internal/interaction.Runtime` session state | delete in P1-F3 |
| `scope.go` | user/chat/message/namespace/plugin-generation scope attached to opaque callback state | `LEGACY_STATE` | a2 `interaction.Binding`, session `ScopeIdentity`, target binding, revision | delete in P1-F3 |
| `scoped_writer.go` | plugin-generation-stamped callback state writer | `LEGACY_STATE` | a2 session creation/update under feature scope | delete in P1-F3 |
| `lifecycle.go` | runtime.Component adapter and explicit StateStore pruning | `LEGACY_STATE_LIFECYCLE` | a2 runtime lifecycle; no new callback component | delete in P1-F3 |
| `state.go` | comment-only wiring index for state subsystem | `DEAD_INDEX` | none | delete with state subsystem in P1-F3 |
| `types.go` | mixed v1 protocol, legacy errors/actions, Handler contracts, CallbackContext Telegram helpers | `MIXED_LEGACY` | a2 token/session/orchestration/presentation authorities already exist | shrink in P1-F3; delete remainder in P1-F4 |
| `router.go` | namespace registry, v1/noop admission, state resolution, lifecycle generation validation, ACK/error routing, handler dispatch | `LEGACY_ROUTER` | a2 native/Assistant interaction ingress plus explicit unknown-callback policy | remove state/v1 dependency in P1-F3 if Router shell must remain; delete in P1-F4 |
| `middleware.go` | Handler panic recovery and fallback timeout middleware | `LEGACY_HANDLER_EXECUTION` | TaskEngine already owns panic recovery and execution timeout for production callback tasks | delete with Handler/Router in P1-F4 |

No generic utility in these eight files warrants preserving the callback package.

---

## 1. `store.go` — legacy state authority

### Responsibilities

`StateWriter` exposes:

```text
Store
StoreWithScope
```

`StateStore` owns:

- opaque 8-byte random IDs encoded as 16 hex characters;
- arbitrary callback state cloning;
- per-entry retained-byte estimation;
- TTL;
- single-use claim;
- bounded cardinality;
- bounded total retained bytes;
- defensive copies;
- opportunistic expiry/eviction.

Current hard limits:

```text
maxStateStoreEntries = 5,000
maxStateStoreBytes   = 8 MiB
maxStateItemBytes    = 64 KiB
default TTL          = 15 minutes
```

When capacity is exceeded, expired entries are swept first and then the oldest expiry is evicted until the state fits.

### Ownership conclusion

This is not generic cache infrastructure. Its keys, scope, replay rules, and state semantics exist specifically for the v1 callback protocol.

Canonical a2 state already provides bounded session ownership:

```text
DefaultMaxSessions         = 4096
DefaultMaxSessionsPerScope = 512
DefaultMaxSessionsPerActor = 64
DefaultMaxStateBytes       = 64 KiB
DefaultMaxTotalStateBytes  = 8 MiB
DefaultTTL                 = 15 minutes
DefaultMaxTTL              = 24 hours
```

Therefore **do not move StateStore**. Delete it in P1-F3 after the module/state caller freeze is closed.

---

## 2. `scope.go` — legacy callback state scope

`StateScope` carries:

```text
UserID
ChatID
MessageID
Namespace
SingleUse
OwnerScope
ExpiresAt
```

and private `stateEntry` / `stateItem` bind data to that scope.

These responsibilities have a2 equivalents spread across the correct authorities:

- actor/target binding: `interaction.Binding`;
- feature/plugin generation: `tasks.ScopeIdentity`;
- target identity: interaction/presentation target binding;
- freshness/replay: session revision + callback token revision;
- expiry: interaction session TTL;
- state: interaction session state.

There is no reason to move `StateScope` to a generic package.

**Action: delete in P1-F3.**

---

## 3. `scoped_writer.go` — generation-stamped legacy writer

`NewScopedStateWriter` wraps `StateWriter` and resolves the current plugin generation before storing callback state.

This was a sound legacy safety property, but it is protocol-specific duplication now that a2 session ownership already carries feature scope/generation.

P1-F1-C found no production module calling `Runtime.ScopedCallbackStore`.

**Action: delete `ScopedCallbackStore`, `CallbackStore`, `ScopeResolver`, and `scopedStateWriter` together in P1-F3.**

Do not replace them with a second wrapper around the a2 runtime.

---

## 4. `lifecycle.go` — StateStore runtime adapter

StateStore implements `runtime.Component` through:

```text
Name
Dependencies
Health
Start
Stop
Prune
```

Important resource fact:

- `Start` creates **no goroutine**;
- `Stop` has nothing to join;
- there is **no ticker**;
- expiry is opportunistic or explicitly pruned.

Therefore removing StateStore produces **no idle-goroutine or idle-wakeup reduction** by itself.

Its lifecycle component exists only because StateStore is still wired into the application runtime.

**Action: delete in P1-F3; do not create a replacement component.**

The a2 interaction runtime has its own lifecycle and bounded-session reclamation semantics.

---

## 5. `state.go` — dead wiring index

This file contains no types or executable logic. It only documents where the state implementation lives.

Classification: `DEAD_INDEX`.

**Action: delete with the state subsystem in P1-F3.**

---

## 6. `types.go` — mixed file requiring staged reclamation

This file mixes several unrelated legacy responsibilities.

### v1 protocol

```text
CallbackVersion1
MaxCallbackDataLen
Action*
EncodeCallbackData
EncodeCallbackDataChecked
ParseCallbackData
validateCallbackField
isHexID
isValidOpaqueID
```

Classification: `LEGACY_PROTOCOL`.

Canonical replacement:

```text
interaction.CallbackVersion = "a2"
interaction.EncodeCallbackToken
interaction.ParseCallbackToken
interaction.OwnsCallbackData
```

**Action: remove in P1-F3.**

### state-specific errors/policy

```text
ErrStateExpired
ErrStateNotFound
ErrStateConsumed
ErrStateScopeStale
HandlerWithStatePolicy
RequiresState option
```

Classification: `LEGACY_STATE_CONTRACT`.

Canonical replacement is a2 interaction/session errors and admission.

**Action: remove in P1-F3.**

### Handler contracts

```text
Handler
HandlerWithOptions
CallbackHandlerOptions
```

Classification: `LEGACY_ROUTER_CONTRACT`.

These are required only while plugin-manager legacy registration and Router remain.

**Action: retain only as long as required by the temporary P1-F3 compatibility shell; delete in P1-F4.**

### `CallbackContext`

It owns legacy handler convenience operations:

- callback answer;
- normal/inline edit;
- markup-only edit;
- delete;
- get originating message;
- progress/success/error convenience;
- button removal;
- answer success/error.

Classification: `LEGACY_HANDLER_CONTEXT`.

Do **not** move it.

The useful behavior already has canonical authorities:

- callback answer: `orchestration.Context.Answer` -> `presentation.Port.Answer`;
- edit: `orchestration.Context.Edit` -> presentation compiler/port;
- delete: `orchestration.Context.Delete` -> `presentation.Deleter`;
- normal/inline transport behavior: `presentation/telegram.Bridge`;
- state/target context: a2 orchestration/session;
- error presentation: `ui.PresentUserError` and a2 errors.

`GetMessage` is not a generic presentation responsibility and has no active legacy feature caller to preserve.

**Action: delete with Handler/Router in P1-F4.**

### Legacy error taxonomy

`ErrInvalidCallbackData`, `ErrHandlerNotFound`, `ErrUnauthorized`, and state errors are still referenced by `internal/ui/toast.go::PresentUserError`.

That mapping is a compile-time blocker to deleting the error declarations.

When v1/state errors disappear, remove their legacy branches from `PresentUserError`; the a2 error cases already exist there.

Do not move the old callback errors merely to keep the mapping compiling.

### `truncateUTF8Bytes`

This helper is private and only supports legacy `CallbackContext` answer/edit byte budgets.

Classification: `LEGACY_PRIVATE_HELPER`.

**Action: delete with CallbackContext. No move.**

---

## 7. `router.go` — remaining compatibility execution engine

Router currently owns:

- handler registration map and registration generation ID;
- v1 parsing;
- raw `noop` special case;
- per-user interaction limiter call;
- plugin scope resolution/revalidation;
- StateStore claim and scope validation;
- prepared-dispatch identity;
- Handler execution;
- callback ACK/rejection ownership;
- callback metrics.

### Handler map

`handlers map[string]registration` has no explicit package-local max cardinality. Its practical population is bounded by plugin registration.

F1-A/C found no current production Handler participant, so the built-in handler population is expected to remain zero.

Do not infer this map is safe to retain indefinitely solely because it is currently empty; it is part of a dead dynamic registration surface.

### v1/state coupling

Current Router cannot survive literal deletion of `ParseCallbackData` or `StateStore` without adaptation.

Therefore P1-F3 must atomically remove the Router's v1/state dependency if the Router shell is retained until P1-F4.

A safe temporary P1-F3 shell may only own an explicit compatibility response for non-a2 residuals such as raw `noop` / unknown stale buttons. It must not preserve namespace registration or recreate state elsewhere.

P1-F4 then deletes that shell and replaces transport fallback with the final explicit unknown-callback behavior.

### Rate limiter ownership

Router does **not** construct or own a rate limiter.

`wiring_core.go` injects the shared `interLimiter`, also used by the Inline engine.

Router currently creates keys of the form:

```text
DimensionOperation / "callback:<user-id>"
```

The shared limiter:

- has a global maximum of 4096 buckets;
- lazily starts one cleanup worker only when buckets exist and the limiter lifecycle is started;
- retires the worker when no buckets remain.

Removing Router only stops creation/use of legacy callback buckets. It does **not** authorize removal of the limiter or its lifecycle worker because other interaction/inline traffic uses the same limiter.

### ACK/error ownership

Router currently answers:

- raw noop;
- invalid payload;
- missing handler;
- stale registration;
- rate limit;
- state expiry/scope/replay failures;
- final success/error fallback ACK.

This behavior cannot simply vanish. P1-F4 must leave a deterministic policy for non-a2/stale callback data so Telegram loading spinners are not left hanging.

The policy belongs at transport/interaction ingress, not in a retained legacy Router.

**Action: state/v1 narrowing in P1-F3; full deletion in P1-F4.**

---

## 8. `middleware.go` — legacy handler execution wrapper

This file owns:

```text
chain
recoverMiddleware
timeoutMiddleware
handlerFunc
```

No generic move is required.

Production native and Assistant legacy dispatch already run `PreparedCallback.Dispatch` inside TaskEngine with an explicit `ExecutionTimeout: 15s`.

TaskEngine itself:

- recovers panics from task handlers;
- applies execution timeout through a context deadline.

Therefore the callback-local middleware is a legacy Handler safety net, not the canonical cross-feature execution authority.

**Action: delete with Handler/Router in P1-F4.**

Do not transplant this middleware into a2.

---

## Test-only files

The callback package also has three test files:

| Test file | Coverage | Reclamation |
|---|---|---|
| `store_hardening_test.go` | atomic single-use, cloning, bounds, retained bytes, passive expiry | delete with StateStore in P1-F3 |
| `router_test.go` | protocol, Router, state, CallbackContext, middleware, lifecycle/reload semantics | shrink if necessary during P1-F3; delete remaining Router-specific tests in P1-F4 |
| `contract_test.go` | legacy callback scenario/metric contract | delete with Router in P1-F4 |

Test helpers in these files are package-local. No production utility must be moved to preserve them.

Do not mechanically port obsolete legacy contract tests to a2 when equivalent a2/TaskEngine/presentation tests already cover the canonical authority. Only preserve a behavior test if the final non-a2 fallback policy still needs it.

---

## Resource ownership audit

### StateStore

Hard maximum retained payload accounting:

```text
8 MiB total
5,000 entries
64 KiB per entry
```

Expected reclamation after P1-F3:

- one map + lock + accounting object removed;
- up to 8 MiB of **legacy-only bounded state capacity** disappears;
- opaque-state clone/eviction/prune CPU paths disappear.

Because F1-B/C found no current production writers, this is **potential capacity**, not a claim that the running process currently uses 8 MiB.

### Router

Expected reclamation after P1-F4:

- handler registration map and lock removed;
- registration leases and `callbackCleanups` lifecycle branch removed;
- v1 parse/validation and state lookup path removed;
- callback-local middleware allocations/deadline checks removed;
- invalid non-a2 callbacks no longer enter legacy namespace/rate-limit/state machinery.

Current built-in handler population is zero, so handler-map RSS savings should be small.

### Rate limiter

No dedicated callback limiter exists.

Expected change:

- legacy callback-specific keys stop being created in shared `interLimiter`;
- no claim is made that the limiter's 4096-bucket capacity disappears;
- no callback-specific limiter goroutine exists.

### Goroutines / idle wakeups

Callback package production code owns:

```text
0 background goroutines
0 tickers
0 periodic workers
```

StateStore is passive.

Therefore expected steady-state idle goroutine savings from deleting the package are:

```text
0
```

The shared rate limiter worker is outside callback ownership and must remain as required by its other consumers.

### Shutdown

StateStore `Stop` is a no-op.

Router owns no explicit shutdown component.

Plugin callback registration leases are detached through `plugin.Manager`, but with zero current callback Handler participants those cleanup entries should be empty for built-ins.

Removing the legacy registration branch simplifies shutdown/disable/reload bookkeeping but does not remove a dedicated callback worker join.

---

## Source footprint

At the audited baseline the eight production files total approximately 49 KiB of Go source and the three callback-specific test files approximately 42 KiB.

This is maintenance/dependency footprint, not a runtime RSS estimate.

---

## Safe staged deletion order

The current roadmap remains valid if interpreted as the following atomic sequence.

### P1-F3 — state + protocol reclamation

1. remove `module.TelegramRuntime.CallbackStore` and `Runtime.ScopedCallbackStore`;
2. remove StateStore construction, application field, runtime registration;
3. remove `store.go`, `scope.go`, `scoped_writer.go`, `lifecycle.go`, `state.go`;
4. remove v1 encoder/parser/action/state protocol pieces from `types.go`;
5. remove corresponding legacy error mappings from `ui.PresentUserError`;
6. narrow any temporarily retained Router so it no longer depends on StateStore or v1 parsing;
7. preserve an explicit ACK policy for any temporarily recognized raw control residual such as `noop`.

Do not invent a new state store or protocol compatibility layer.

### P1-F4 — Router/Handler/wiring reclamation

1. remove plugin-manager `callback.Handler` assertion/registration/cleanup branch;
2. remove Assistant `CoreCallbackDispatcher` and `SetCallbackRouter`;
3. remove native Dispatcher callback Router field/deps/accessors;
4. remove both native and Assistant legacy fallback dispatch;
5. place final unknown/non-a2 callback ACK behavior at the appropriate transport ingress;
6. delete remaining `router.go`, `middleware.go`, and legacy portions of `types.go`;
7. delete remaining callback-specific Router/contract tests;
8. remove the callback package entirely when no imports remain.

Shared TaskEngine, interaction runtime, presentation bridge, idempotency/dedupe, metrics, EventBus, Inline engine, and shared limiter stay intact.

---

## P1-F1-D freeze fence

`internal/architecture/legacy_callback_ownership_p1f1d_test.go` freezes the resource/ownership baseline by enforcing:

1. the callback subsystem has exactly the eight production files inventoried above;
2. a new production file under the legacy package fails the architecture test;
3. the package must not acquire a background goroutine;
4. the package must not acquire ticker/`AfterFunc`-owned lifecycle work;
5. the package must not construct a private `ratelimit.Limiter`.

The fence is intentionally stale-sensitive: as files are removed in P1-F3/F4 the inventory must shrink deliberately.

---

## F1-D conclusion

Every production file under `internal/services/callback/` now has a deletion/move decision.

There is no generic callback-package implementation that must be preserved by moving it elsewhere.

The only nontrivial sequencing constraint is:

`Router -> ParseCallbackData + StateStore`

so P1-F3 must narrow that dependency atomically rather than deleting state/protocol from underneath the existing Router.

Resource expectation is correspondingly conservative:

- up to 8 MiB of legacy-only state capacity becomes reclaimable;
- callback registration/state/protocol maps and locks disappear;
- legacy fallback CPU work disappears;
- no dedicated callback idle goroutine/ticker is reclaimed because none exists;
- shared limiter/TaskEngine/metrics/a2 resources remain.

No speculative rewrite is proposed.

Next phase, only after explicit user confirmation:

**P1-F1-E — inventory closure + freeze fence.**
