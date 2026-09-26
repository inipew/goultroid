# P1-F5 — Repo-wide legacy callback final acceptance

Date: 2026-09-27  
Branch: `test-next`  
Baseline entering acceptance: `1d7660b650de0e41272d2a68e400c4151b1915a8`  
Final code acceptance baseline before documentation: `985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e`

## Scope

P1-F5 is the final acceptance phase for P1-F. It does not introduce a new interaction architecture. Its job is to prove that P1-F3/P1-F4 removed the legacy callback authority completely while preserving canonical a2 behavior, lifecycle invalidation, bounded resources, and explicit unknown callback handling.

## Audit finding repaired in P1-F5

The audit found one real production regression from P1-F4: `RegisterHooks()` still registered `OnInlineBotCallbackQuery`, but the method itself had been accidentally removed while the legacy Router branch was deleted.

P1-F5 restored the method with only the canonical path:

```text
native inline callback
 -> ingress admission
 -> callback idempotency
 -> EventBus publication
 -> native a2 ownership/dispatch
 -> non-a2:
      noop    -> silent ACK
      unknown -> expired-interaction ACK
```

No Router, Handler, legacy StateStore, legacy TaskEngine callback execution, plugin-scope legacy resolution, or callback ordering helper was restored.

The audit also found Router-era tests that still expected `getCallbackRouter()` or "Interaction service unavailable." fallback. Those fences were rewritten to assert the current a2 -> explicit unknown/noop policy instead.

Implementation chain:

```text
009aec2ace7986f29797df06a625bb05990ef893  test(callback): close p1-f5 acceptance gaps
985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e  fix(callback): repair p1-f5 acceptance patch markers
```

## Final production authority result

The new `internal/architecture/legacy_callback_p1f5_test.go` freezes these conditions:

- no production import of `internal/services/callback`;
- no legacy Router/Handler/CallbackContext/StateStore selectors;
- no callback registrar/router bootstrap setters/getters;
- no legacy callback state capability;
- no legacy callback encoder/parser/version authority;
- no Assistant `CoreCallbackDispatcher` / `dispatchCoreCallback` fallback;
- deleted callback package directory remains absent;
- Settings and MyXL remain zero-legacy;
- `interaction.Runtime` owns no background goroutine/ticker/AfterFunc worker.

The existing P1-F1 producer fence remains the exact raw `v1:` callback-protocol guard, avoiding an unsafe blanket ban on unrelated future versioned strings.

`HandleCallback` is not banned as a bare repo-wide identifier because the canonical native a2 adapter legitimately exposes `HandleCallback`; the final fence targets legacy ownership/protocol symbols instead.

## Behavior acceptance matrix

| Surface / invariant | Final evidence |
|---|---|
| Native Settings | Existing P1-D native tests assert a2 callback data; text-only mode allocates zero a2 sessions |
| Assistant Settings | Existing Assistant Settings tests assert a2 tokens, stale replay rejection, and wrong-actor rejection |
| Native MyXL quota | Existing P1-E1 test asserts a2, wrong actor/target fail-closed, revision advance, stale rejection, generation reload invalidation |
| Native MyXL purchase | Existing P1-E3 test asserts a2 confirm/cancel, session termination, stale old-confirm rejection, fresh-price authority |
| Assistant MyXL | Existing Assistant tests assert session-bound a2 actions and reject the legacy a1 envelope |
| Native unknown callback | Message + inline tests assert unknown => expired ACK; raw `noop` => silent ACK |
| Assistant unknown callback | Message + inline tests assert unknown => expired ACK; raw `noop` => silent ACK |
| Plugin disable/reload | `TestManagerInteractionRuntimeFollowsPluginLifecycle` invalidates old-generation sessions/actions and creates a new generation |
| Shutdown | Plugin/runtime lifecycle tests reject post-shutdown work and prevent prepared actions crossing shutdown |
| Session settling | Runtime close acceptance asserts `Sessions == 0`, `Inputs == 0`, and `StateBytes == 0` |

## Resource/lifecycle closure

Legacy callback-owned resources are gone:

```text
StateStore allocation            0
Router namespace map/lock        0
callback registration lease      0
legacy callback middleware       0
legacy callback TaskEngine path  0
callback ordering helper         removed
callback-owned worker/ticker     0
```

Canonical resources remain intentionally:

- `interaction.Runtime` for bounded a2 sessions;
- TaskEngine for admitted execution;
- callback idempotency/dedupe at transport ingress;
- EventBus;
- shared interaction limiter;
- RPCExecutor;
- plugin generation scopes.

`interaction.Runtime` remains lazy/no-idle-worker by construction, and its close path clears retained sessions, input claims, expiry heap, scope/actor indexes, and state bytes.

No claim is made that the overall process has fewer goroutines than a historical build: P1-F4/P1-F5 removed no callback background worker because the deleted legacy callback shell did not own one.

## Verification constraints

No CI was inspected.

A direct container checkout was attempted but GitHub DNS resolution was unavailable in the executable container. Therefore this session does **not** claim that `gofmt`, `go build`, `go test`, `go vet`, race tests, or benchmarks executed.

The acceptance evidence in this phase is current GitHub source inspection plus the regression/architecture gates committed above. The next session must continue to follow the rule that executable commands are only claimed when they actually run.

## Closure

**P1-F5: CLOSED.**  
**P1-F: globally CLOSED.**

Next phase, only after explicit user confirmation:

**P2-A — plugin hook registration simplification.**

Do not start P2-B until P2-A is independently completed and accepted.
