# Goultroid — Userbot / Assistant / Inline / UI Refinement AI Session Handoff

Date: 2026-09-27
Branch: `test-next`
Current audited implementation baseline after P3-B completion-drain refinement: `440c9ba977ecaa95da4cf9ce232305915f2a3936` — `refactor(taskengine): make completion drain event-driven`
Purpose: finish **P3-A benchmark measurements** before any P3-C optimization. P3-B is CLOSED; P3-A remains OPEN until real benchmark numbers are captured.

Authority rule: **always refresh current HEAD and current source first. Source/tests win over this handoff if the branch has moved.**


## 2026-09-27 P3-A isolated diagnostic measurement update

P3-A is still **OPEN**, but source-isolated diagnostics have now been captured after the benchmark harness work.

Environment: Linux amd64, Intel Xeon Platinum 8573C, 5 logical CPUs, Go 1.23.2, three runs per case.

Directional findings:

- generic limiter hot path stayed roughly flat from ~262 ns/op at one bucket to ~300 ns/op at 4096 buckets;
- saturated generic-limiter fail-closed lookup was ~250 ns/op;
- an intentionally forced 4096-bucket cleanup sweep was ~110 µs;
- Inline cache hit stayed ~195–215 ns/op through the 500-entry production cap;
- Inline cache next-expiry scan across 500 entries was ~7.2 µs with zero allocation;
- saturated Inline cache churn measured ~15 µs/insert for a 501 working set and ~21 µs/insert for a 4096 working set;
- exact Inline resolution stayed ~136–160 ns/op through 4096 handlers;
- custom matcher resolution grew from ~163 ns/op at one matcher to ~16.5 µs/op at 4096 matchers.

These are not full-repository acceptance numbers: cache/registry measurements used minimal local stubs for unrelated dependency types, and the complete repository cannot be built in this environment. They narrow P3-C candidates but do not authorize optimization.

Provisional direction:

```text
generic limiter hot path     -> no optimization evidence
inline exact lookup          -> no optimization evidence
inline cache hit             -> no optimization evidence
inline cache saturated churn -> confirm with real-checkout workload
inline custom matcher scan   -> confirm against production cardinality
```

See `docs/design/goultroid-refinement-p3a-high-cardinality-benchmark.md`.

**P3-A remains OPEN and P3-C remains BLOCKED until real-checkout measurements are captured.**


## 2026-09-27 P3-B closure update

P3-B is **CLOSED** at `440c9ba977ecaa95da4cf9ce232305915f2a3936` (`refactor(taskengine): make completion drain event-driven`).

Fresh source audit confirmed the historical issue still existed specifically in `internal/taskengine/delivery.go`: `completionDelivery.drain()` polled `pending`, `active`, and queue length with a 1 ms `time.NewTicker`. The main TaskEngine admitted-task drain was already event-driven through `drainDone`; only completion callback settlement still polled.

The completion delivery lane now uses a generation-scoped broadcast drain channel:

```text
first callback enqueue in a drained generation
  -> markBusy()
  -> fresh open drainCh

pending/active callback transition to zero
  -> signalDrained()
  -> close(drainCh)
  -> broadcast to every concurrent drain waiter

Drain(ctx)
  -> wait on drainCh OR ctx.Done()
```

Properties preserved:

- no permanent drain worker or ticker;
- lazy completion workers remain unchanged;
- callback queue ordering/concurrency semantics remain unchanged;
- multiple concurrent drain waiters receive the same close broadcast;
- caller context/deadline remains the hard upper bound;
- graceful `Stop(ctx)` still uses the existing global shutdown context;
- forced stop behavior and callback reservation/backpressure semantics remain unchanged.

Regression coverage:

- `internal/taskengine/delivery_drain_test.go` verifies active callbacks block drain, concurrent drain waiters all wake, and deadline cancellation wins;
- `internal/architecture/taskengine_completion_drain_p3b_test.go` rejects `time.NewTicker`, `time.Sleep`, and `time.After` inside `completionDelivery.drain()` and requires the event-driven drain signal boundary.

All changed Go files were run through local `gofmt` before commit. CI was not inspected. A complete executable checkout/module cache is still unavailable in this environment, so this closure does **not** claim `go test`, `go test -race`, `go vet`, or full repository build execution.

**P3-B is CLOSED. P3-A measurements remain OPEN, therefore P3-C is still blocked on measurement evidence.**


## 2026-09-27 P3-A high-cardinality benchmark harness update

P3-A measurement infrastructure is **IMPLEMENTED but NOT YET CLOSED** at `aeae302d125bf1e5ac39df9671c0ea6b026bd10e` (`bench(refinement): add p3-a high-cardinality probes`).

This slice intentionally adds benchmark coverage only; it does **not** optimize or modify production behavior.

High-cardinality benchmark matrix now includes:

- Inline registry exact/custom resolution at 1 / 16 / 64 / 256 / 1024 / 4096 handlers.
- Inline cache hit/fill at 1 / 16 / 64 / 256 / 500 entries.
- Inline cache saturated churn with working sets 501 and 4096 against the production hard cap of 500.
- Inline cache next-expiry scan at 500 retained entries.
- Generic rate limiter hot/insert paths at 1 / 16 / 64 / 256 / 4096 buckets.
- Generic rate limiter saturated fail-closed lookup at 4096 buckets without a capacity sweep.
- Generic rate limiter forced capacity sweep at the 4096-bucket ceiling.

Existing Telegram hierarchical RPC limiter benchmarks already cover cardinality up to the production bucket ceiling, so P3-A does not duplicate that suite.

All changed Go benchmark files were run through local `gofmt` before commit. CI was not inspected.

The current execution environment still cannot resolve github.com and has no complete checkout/module cache, so this session cannot honestly record benchmark numbers from the real repository. **Do not interpret the existence of the benchmark harness as performance acceptance.** P3-A stays open until a real checkout runs the suite and records `ns/op`, `B/op`, and `allocs/op`.

See `docs/design/goultroid-refinement-p3a-high-cardinality-benchmark.md`.

**Current NEXT remains P3-A measurement execution and result capture. Do not start P3-C optimization from historical assumptions.**


## 2026-09-27 P2-D closure update

P2-D is **CLOSED** at implementation baseline `6261169c07de9c26f60304b0136c14a1e2a85b30` (`refactor(ui): reclaim dead legacy helpers`).

Fresh current-source audit classified `internal/ui` into production/pure helpers versus callback-era compatibility. The reclamation removed zero-production-call generalized builders and aliases while preserving the UI values that current Settings/MyXL and ordinary userbot surfaces still consume.

Reclaimed:

- `internal/ui/actions.go`
- `internal/ui/confirmation.go`
- `internal/ui/navigator.go`
- `internal/ui/wizard.go`
- callback/menu builders such as toggle/stepper/selectors/navigation/action rows
- presentation-role-to-legacy-button adapters
- stale tests whose only purpose was exercising those reclaimed helpers

Preserved intentionally:

- `Card` and HTML/value formatters
- `Screen`
- `Button` / `ButtonRow` / `Markup` transport-neutral values still used by MyXL presentation
- `PaginateSlice`, used by Settings
- `PresentUserError`
- `internal/ui/render` as a thin adapter that delegates keyboard serialization to canonical `internal/presentation/telegram.EncodeMarkup`

`internal/architecture/ui_helper_reclamation_p2d_test.go` now fences the reclaimed symbol/file set and protects the remaining pure/production helpers plus the single Telegram serializer delegation.

No second callback/runtime/serializer authority was introduced. Presentation remains the common label vocabulary owner.

CI was not inspected. A complete executable checkout is still unavailable from the container, so this session does not claim repository-wide `gofmt`, `go build`, `go vet`, or `go test` execution.

**P2-A/B/C/D are now CLOSED. Current NEXT = P3-A — benchmark current post-refinement source before any optimization.**

## 2026-09-27 P2-B closure update

P2-B is **CLOSED**. The branch already contained a substantial P2-B implementation when this continuation began, so the phase was first re-audited against current HEAD rather than replayed from the roadmap. Existing work had already centralized locale binding, resolved userbot locale per invocation from the canonical `ui:locale` setting hierarchy, added EN/ID common Help/Settings/Downloader vocabulary, reused the same vocabulary in Assistant surfaces, removed manual Assistant locale branches, and added initial authority fences.

This continuation closed the remaining common-userbot gaps in Settings CLI, Admin/moderation, Media, and Profile. Those surfaces now resolve user-visible common UX through the invocation-local `core.Context` translator or the existing explicit locale helper. No localization dependency was introduced into `internal/presentation`, and metrics remain localization-independent.

Canonical authority at closure:

```text
locale setting authority     settings.Service / ui:locale
locale normalization         localization.CanonicalLocale
per-invocation resolution    localization.ResolveLocale
per-invocation translator    localization.Bind
built-in fallback            English
built-in locales             en / id
presentation                 localization-agnostic
```

The userbot catalog has **136 English keys and 136 Indonesian keys with zero parity mismatch**. Representative Settings/Admin/Media/Profile translations have explicit regression coverage. Standalone plugin tests now bind the canonical English Localizer so their fixtures match production command wiring instead of falling back to raw translation keys.

Pre-existing P2-B chain discovered at phase refresh:

`50b42ea080` -> `3c8288b58c` -> `370dd50585` -> `be3c0f85b9` -> `c9b3f6ddba` -> `8b5bcdab36` -> `86c67b7ec3` -> `4044243dce` -> `1fa5f46c4d` -> `b0f7721b74` -> `40ee5eb6b0` -> `83fe7597d7` -> `28fac5ceae`.

Continuation/closure chain:

`27bd75d5c9` Settings CLI -> `f290a5f703` Admin -> `e9f9a0051d` Media -> `3484891f4e` Profile -> `005944d883` production-like localized test fixtures -> `0fd0d08314` final architecture/parity fences.

No CI was inspected. A complete executable checkout was still unavailable from the container, so this continuation does **not** claim `gofmt`, `go build`, `go test`, `go vet`, race, or benchmark execution. The Go source is syntactically inspected through current GitHub source/diffs and regression fences, but test struct literals touched by the connector have not been locally reformatted with `gofmt`.

See `docs/design/goultroid-refinement-p2b-localization.md`.

**P2-C is CLOSED. Current NEXT = P2-D — remove dead legacy UI helpers, only after explicit user confirmation.**

## 2026-09-27 P2-A closure update

P2-A is **CLOSED**. The plugin message-hook registration boundary has been compressed from one base registrar plus nine capability interfaces into one explicit shared `core.MessageHookRegistration` contract and one `HookRegistrar.RegisterMessageHook(...)` method. `plugin.Manager` no longer capability-probes the registrar; raw/canonical handler shape, generation scope, structural routing, and optional state gate are registration data.

The Telegram Dispatcher implements that single contract and still funnels registrations into the existing indexed `addMessageHandler` authority. No second registry, router, worker, or hot-path fan-out was introduced. Existing convenience registration methods remain compatibility APIs but are no longer capability contracts consumed by `plugin.Manager`.

Canonical hooks remain preferred and continue to require `telegram.read` when the capability gate is configured. Privileged raw hooks remain supported behind `telegram.raw`. Unrouted raw compatibility hooks explicitly carry `LegacyRouting`, preserving historical scoped priority/lane behavior; routed/stateful raw hooks and all canonical hooks use explicit routing.

Current production hook inventory is canonical: AFK and UserLog use canonical routed hooks; Blacklist, Filters, and PMPermit use canonical routed hooks with dynamic state gates. No active production raw hook implementation was found during the P2-A inventory.

Implementation/acceptance chain:
`ec55296b8971ed3c3cebe697dfa849e932fb69b3` -> `bb7de208c409a9a05ce5bdad6c724e654a54012d` -> `cb95dfd1a032ce8102d2e1b7d46afc0afef17361` -> `0b3dac3b45e8b1117657c9fafd8a96a03ffe4fa7`.

Architecture fences now require a one-method `HookRegistrar`, prohibit the retired registrar capability interfaces and registrar type assertions, and AST-check the shared registration field set. Dispatcher tests preserve explicit routing/state/scope, cleanup, canonical/raw exclusivity, and legacy raw lane compatibility.

No CI was inspected. The executable container still cannot resolve GitHub, so no full checkout was available and this session does not claim `gofmt`, `go build`, `go test`, `go vet`, race, or benchmark execution. Source-level acceptance and committed regression/architecture fences are the evidence recorded here. See `docs/design/goultroid-refinement-p2a-plugin-hook-registration.md`.

**NEXT = P2-B — localization, only after explicit user confirmation.**

## 2026-09-27 P1-F5 closure update

P1-F5 is **CLOSED**. Final repo-wide acceptance found one real P1-F4 regression plus stale Router-era tests: native `OnInlineBotCallbackQuery` had been accidentally removed while the Router path was reclaimed, while old tests still expected `getCallbackRouter()` / "Interaction service unavailable." compatibility behavior. P1-F5 restored native inline ingress using only canonical a2 ownership + explicit unknown/noop ACK policy, removed the dead callback ordering helper, rewrote stale fences, added a repo-wide zero-legacy architecture gate, extended Assistant noop acceptance, and made runtime shutdown assert `Sessions/Inputs/StateBytes == 0`.

Final code acceptance state before this documentation commit: `985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e`. Implementation chain: `009aec2ace7986f29797df06a625bb05990ef893` followed by `985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e`.

Production legacy callback authority is frozen at zero: no callback package, Router/Handler/StateStore, legacy v1 encoder/parser, bootstrap/plugin wiring, native legacy fallback, or Assistant legacy fallback. Raw `noop` receives a silent ACK; other non-a2 data receives the expired-interaction ACK. Settings and MyXL remain a2-only. Plugin generation invalidation and shutdown/session settling remain owned by `interaction.Runtime` + `plugin.Manager`.

No CI was inspected. A complete executable checkout could not be obtained because direct GitHub DNS access from the container failed, so no `gofmt`, `go build`, or `go test` command is claimed. Source-level inspection plus the committed regression/architecture gates are the acceptance evidence. See `docs/design/goultroid-refinement-p1f5-callback-final-acceptance.md`.

**P1-F is globally CLOSED. At P1-F5 closure the next step was P2-A; P2-A is now also CLOSED. Current NEXT is P2-B.**

## 2026-09-26 P1-F4 closure update

P1-F4 is **CLOSED**: the legacy callback Router/Handler/CallbackContext/middleware package and all app/plugin/native/Assistant bootstrap wiring are removed. Native and Assistant ingress now own explicit non-a2 policy directly: raw `noop` receives a silent ACK; every other unknown/non-a2 callback receives the expired-interaction ACK. a2, callback idempotency/dedupe, EventBus, TaskEngine, Inline, shared limiter, RPCExecutor, and plugin generation scopes remain canonical. Next executable phase, after explicit user confirmation, is **P1-F5 — repo-wide legacy callback final acceptance**. See `docs/design/goultroid-refinement-p1f4-callback-router-reclamation.md`.

## 2026-09-26 P1-F3 closure update

P1-F3 is **CLOSED**: legacy StateStore/module callback-state capability and the v1 encoder/parser/protocol are removed. The temporary Router/Handler shell remains only for P1-F4 ownership; raw `noop` still clears the spinner, while every other non-a2 callback is rejected as expired before TaskEngine/legacy-handler execution. No callback StateStore replacement was introduced. Next executable phase, after explicit user confirmation, is **P1-F4 — remove legacy Router + bootstrap/plugin wiring**. See `docs/design/goultroid-refinement-p1f3-callback-state-protocol-reclamation.md`.

## 2026-09-26 P1-F1 closure update

P1-F1-A/B/C/D/E are **CLOSED**. The authoritative F1-E matrix found zero production feature v1 producers, zero production legacy `callback.Handler` feature implementations, and zero identified production feature `StateStore` writers. Settings and MyXL remain zero-legacy. The exact P1-F2 namespace worklist is **EMPTY**; do not invent an F2 namespace. Remaining legacy ownership is infrastructure compatibility only. Next executable phase, after explicit user confirmation, is **P1-F3 — remove legacy StateStore + legacy v1 protocol**. See `docs/design/goultroid-refinement-p1f1e-callback-inventory-closure.md`.

---

# 0. Executive state

The broad redesign is already complete. The remaining program is refinement and reclamation:

- keep one authority per concern;
- remove compatibility layers only after zero-production-caller proof;
- preserve a2 as the single interaction/session runtime;
- preserve TaskEngine as the execution/resource authority;
- preserve RPCExecutor as Telegram retry/FloodWait authority;
- preserve zero-idle / bounded-state behavior;
- benchmark before optimization;
- do not recreate deleted legacy stacks under a new name.

## Completed refinement chain

```text
1f6aaae50833  P0-A  Settings zero-state text rendering
189aefe2ca96  P0-B  typed safe user-facing error boundary
20b5532c5429  P0-C  commit-safe EditOrReply contract
b7892e13a703  P0-D  Inline exact-handler fast path + benchmark fence
27b1f0fe7dd2  P1-A  canonical presentation vocabulary

5c998be0814c  P1-B  canonical Telegram keyboard serialization
892f10dca14e  P1-C  native/userbot a2 interaction adapter
0d47ae3efd44  P1-D  native Settings migrated to a2

bc09289b83da  P1-E1 native MyXL quota refresh -> a2
886d0ecb83f9  P1-E2 hardened purchase confirmation state
b29ea43d0e2c  P1-E3 native buy confirm/cancel -> a2
f72f3e4d36a3  P1-E4 remove legacy MyXL callback stack
122991c611c1  P1-E acceptance closure: Assistant purchase TaskEngine/revision parity

1d00110be8e6  Settings final legacy callback transport retirement + Assistant Settings a2
41148cd5d1e2  P1-F4 legacy Router/bootstrap reclamation
009aec2ace79  P1-F5 acceptance fixes + final fences
985b2ea5de7f  P1-F5 acceptance patch marker repair

ec55296b8971  P2-A unified message-hook registration
bb7de208c409  P2-A patch marker repair
cb95dfd1a032  P2-A architecture fence hardening
0b3dac3b45e8  P2-A format-stable contract fence
```

Earlier Assistant-optional redesign remains closed and must not be reopened without fresh regression evidence.

---

# 1. Current architectural truth

Canonical authorities:

```text
Command routing                 core.Router
Execution/admission             TaskEngine + execution semantics
Telegram retry/FloodWait        telegram.RPCExecutor
Plugin lifecycle                plugin.Manager + generation scope
Interaction/session state       interaction.Runtime (a2)
Interaction orchestration       interaction/orchestration.Engine
Native a2 transport             internal/interaction/native.Adapter
Assistant a2 transport          internal/assistant/interaction driver
Inline lifecycle                services/inline vNext
Presentation model              internal/presentation
Telegram rich presentation      presentation/telegram Bridge
Settings domain state           internal/settings service
Downloader/provider selection   download.Registry
Retained media ownership        shared media/storage infrastructure
```

Do not create a second authority for any of these.

---

# 2. Binding engineering rules

1. Work on `test-next`.
2. Refresh exact HEAD before every subphase.
3. Read current source/tests before trusting this document.
4. Run `gofmt` before every Go commit.
5. **Do not inspect or wait for CI unless the user explicitly asks.**
6. No second TaskEngine.
7. No second Telegram RPC/retry/FloodWait executor.
8. No second interaction/session runtime.
9. No second inline registry.
10. No second downloader/provider registry.
11. No permanent per-feature worker/ticker/poller.
12. Every cache/state/queue/retained resource must be bounded.
13. Prefer event-driven/lazy workers and zero-idle behavior.
14. Generation-scoped work must fail closed after disable/reload.
15. Revalidate fresh authority immediately before mutations.
16. Never auto-send an alternate Telegram message after ambiguous send/edit commit.
17. Ordinary user-facing errors must not expose raw provider/db/fs/process/RPC causes.
18. Do not use compatibility code as a reason to preserve a whole dead subsystem.
19. Delete only after zero-production-caller proof.
20. **Execution cadence for P1-F:** one subphase at a time. Finish it, summarize, stop, and wait for explicit user confirmation before the next subphase.

Verification honesty:

- Current AI environment previously could not obtain a complete executable checkout because GitHub DNS was unavailable in the container.
- Source-level diff/fence verification was performed.
- Do **not** claim `go test`, `go vet`, race, build, or benchmark commands ran unless they actually run in the current session.
- Do not check CI unless explicitly requested.

---

# 3. P1-B — CLOSED

Commit:

`5c998be0814ca25fab0a4b44c059756185e0cdb5` — `refactor(ui): centralize Telegram keyboard serialization`

Result:

- `internal/presentation/telegram/markup.go` is the canonical generic Telegram keyboard encoder.
- Bridge and legacy UI renderer delegate to the same implementation.
- Callback bytes are copied/owned safely.
- URL and switch-inline semantics preserved.
- No a2 compilation of already-transport-ready legacy payloads.

Do not revisit unless a current regression proves duplication has returned.

---

# 4. P1-C — CLOSED

Commit:

`892f10dca14eb3d5f47e90ec47d26eef167a5403` — `feat(interaction): add native userbot a2 adapter`

Result:

- native/userbot a2 works without Assistant;
- reuses shared `interaction.Runtime`, dispatcher, orchestration engine, TaskEngine;
- native callback ingress checks a2 before the explicit unknown/noop ACK policy;
- malformed `a2:` is owned/fail-closed;
- actor/chat/message/revision/generation validation;
- plugin lifecycle bind/rebind through `nativeinteraction.FeatureDriver`;
- bounded callback answer tracking;
- no feature ticker/worker.

This is the native interaction foundation for Settings/MyXL and any future migration.

---

# 5. P1-D — CLOSED, including final Settings legacy retirement

Initial native migration:

`0d47ae3efd4447b15e0a5040083ac5733e8be410` — `feat(settings): migrate native dashboard to a2`

Final legacy retirement:

`1d00110be8e664032e728902fb563dec93abe2d9` — `refactor(settings): retire legacy callback transport`

Current truth:

- native Settings is a2;
- Assistant Settings is also a2;
- shared Settings domain/mutation authority remains `internal/settings`;
- Settings production code no longer imports `internal/services/callback`;
- no Settings `StateStore`, `ScopedCallbackStore`, `HandleCallback`, `CallbackOptions`, `EncodeCallbackData`, `ParseCallbackData`, or `v1:settings`;
- interactive Assistant fails closed if its a2 runtime is unavailable;
- text-only `ui:inline_buttons=false` still allocates no interaction session;
- shared view semantics are reused between native/Assistant via session-bound action IDs.

Important: the commit `1d00110...` is effectively a **known-completed P1-F migration slice for Settings**. Do not redo Settings during P1-F inventory; classify it as a known-zero domain and verify the fence still holds.

---

# 6. P1-E — CLOSED after full re-audit

## P1-E1 — native quota refresh -> a2

Commit:

`bc09289b83da32417772ac66fde2001ed4af1742`

Current behavior:

```text
.kuota / .myxl kuota
  -> fresh account/quota read
  -> native a2 Begin
  -> state = MSISDN + masking flag only
  -> 10m TTL
  -> a2 refresh callback
  -> TaskEngine, 30s profile
  -> fresh account/quota read
  -> Transition -> new revision
```

No legacy state allocation.

## P1-E2 — purchase state hardening

Commit:

`886d0ecb83f9e0e6560fc8b94349bef5fe281545`

Canonical retained intent:

```text
MSISDN
OptionCode
Method
WalletNumber
QuotedPrice        # consent fence, not settlement authority
OverwritePrice
HasOverwrite
```

Not retained:

```text
TokenConfirmation
PackageName as authority
AccessToken
RefreshToken
IDToken
provider response payload
payment token
```

Immediately before reservation:

```text
normalize intent
 -> fresh account
 -> fresh package details
 -> verify option
 -> fresh TokenConfirmation
 -> verify canonical price == QuotedPrice
 -> derive idempotency key
 -> ReservePurchase
 -> Settlement
```

Price drift fails before reserve.

## P1-E3 — native buy confirm/cancel -> a2

Commit:

`b29ea43d0e2c184ac1450401883ed5d1f4eceed2`

Native flow:

```text
.beli / .myxl buy
 -> preparePurchaseIntent
 -> a2 checkout, TTL 5m
 -> Confirm / Cancel
 -> TaskEngine
```

Confirm:

```text
fresh resolve
 -> Transition(processing)
 -> old revision stale
 -> ReservePurchase
 -> Settlement
 -> FinishPurchase
 -> Terminate(final result)
```

Native purchase execution profile: 55s.

## P1-E4 — legacy MyXL callback reclamation

Commit:

`f72f3e4d36a373e7a90245a4cc984b8e85c69f18`

Removed from MyXL production:

- `callback.Handler`;
- `callback.HandlerWithOptions`;
- `SetStateStore`;
- `RequiresCallbackState`;
- `CallbackOptions`;
- `HandleCallback`;
- `ScopedCallbackStore`;
- `StateStore`;
- legacy `refresh`;
- legacy `buy_confirm`;
- legacy `buy_cancel`;
- `purchaseDraftState`;
- `EncodeCallbackData`;
- `ParseCallbackData`;
- `v1:myxl`.

## P1-E final audit closure

Commit:

`122991c611c1fff6dd81745d8c6595d552fb0db4` — `fix(myxl): close p1-e assistant purchase gaps`

The full re-audit found two real Assistant gaps and fixed them:

1. Assistant purchase confirmation previously inherited the 15s default TaskEngine profile while the purchase path can legitimately take longer.
   - now Assistant action slots use `RegisterPreparedAction`;
   - purchase confirm gets a 65s execution profile;
   - ordinary Assistant actions keep default profile.

2. Assistant confirm previously reserved before revision invalidation.
   - now it fresh-resolves;
   - transitions to shared 2m processing state;
   - old button revision becomes stale;
   - only then calls `ReservePurchase`.

Final MyXL production scan at closure: 14 production Go files, zero legacy callback violations.

**P1-E is CLOSED.**

---

# 7. P1-F — repo-wide legacy callback stack reclamation

## Critical current state

P1-F is **globally CLOSED through P1-F5**.

Final current truth:

```text
plugins/myxl       zero legacy callback production surface
plugins/settings   zero legacy callback production surface
production repo    zero legacy callback package/Router/Handler/StateStore/v1 authority
native callback    a2 -> noop silent ACK / unknown expired ACK
Assistant callback a2 -> bounded dedupe + InteractionIngress; non-a2 -> noop/expired ACK
```

The P1-F5 architecture fence prevents reintroduction of the deleted production authority. Historical P1-F1/P1-F2/P1-F3/P1-F4 sections below remain migration history, not open work.

---

# 8. P1-F1 — inventory/freeze all repo-wide legacy callback callers

P1-F1 is itself split into five sequential subphases.

**Execution rule: complete exactly one subphase, summarize findings + commit if appropriate, then STOP and wait for user confirmation.**

## P1-F1-A — production import / ownership inventory — CLOSED

Goal: build an authoritative list of production packages that still depend on the legacy callback subsystem.

Search every production Go file, excluding tests/generated/vendor where appropriate, for:

```text
internal/services/callback
callback.
callback.Handler
callback.HandlerWithOptions
callback.StateStore
callback.StateReader
callback.StateWriter
callback.ScopedCallbackStore
callback.CallbackContext
callback.CallbackOptions
SetStateStore
RequiresCallbackState
HandleCallback
Namespace() callback ownership
```

For every match classify:

```text
PRODUCTION_ACTIVE
PRODUCTION_COMPATIBILITY
GENERIC_UTILITY
TEST_ONLY
DEAD
FALSE_POSITIVE
```

Required output:

- one inventory table/document checked into `docs/design/`;
- exact file + symbol + owner namespace;
- whether migration is needed;
- expected replacement authority;
- whether deletion is blocked.

Known baselines that should classify zero:

```text
plugins/settings
plugins/myxl
```

Do not modify production callback behavior in F1-A.

Acceptance:

- repo-wide production file enumeration, not code-search alone;
- every legacy callback import/type assertion has an owner/classification;
- no deletion yet;
- architecture freeze may be added only if it can allowlist known current callers exactly.

### Stop point

After F1-A: summarize inventory and **wait for user confirmation before F1-B**.

---

## P1-F1-B — legacy producer inventory

Goal: find every production site that **creates/emits** legacy callback payloads.

Search for:

```text
EncodeCallbackData
v1:
NewCallbackButton
BuildStateToggle
BuildStepper
BuildDurationPicker
BuildSelector
raw callback Data literals
namespace/action/opaque legacy composition
```

Separate:

- actual Telegram callback payload producer;
- internal semantic intent string that merely contains `foo:bar`;
- test fixture;
- dead helper.

Required matrix:

```text
namespace
producer file/function
consumer
state requirement
target surface
planned a2 replacement
migration risk
```

Known non-producers:

- MyXL internal a2 intents like `myxl:checkout` are not legacy protocol by themselves.
- Settings semantic/session action identifiers are not legacy v1 payloads.

Acceptance:

- every production v1 producer accounted for;
- zero unowned namespace;
- exact migration order proposed for F2.

### Stop point

After F1-B: summarize and wait for confirmation before F1-C.

---

## P1-F1-C — consumer/router/bootstrap inventory

Goal: map who still consumes legacy callbacks and who keeps the Router alive.

Audit:

```text
callback.Router creation
callback.Router registration
callback.Handler assertions
HandlerWithOptions assertions
Telegram Dispatcher callback routing
dispatcher SetCallbackRouter/getCallbackRouter
app/bootstrap construction
plugin.Manager legacy registration
plugin disable/reload cleanup
Assistant callback bridge compatibility
inline callback coexistence
```

Produce a dependency chain, for example:

```text
Telegram callback ingress
 -> native a2 claim
 -> legacy Router fallback
 -> namespace handler
 -> StateStore
```

For each edge mark whether still production-required.

Acceptance:

- exact blockers to Router deletion known;
- exact blockers to StateStore deletion known;
- exact bootstrap/wiring files known;
- no deletion yet.

### Stop point

After F1-C: summarize and wait for confirmation before F1-D.

---

## P1-F1-D — state/protocol/resource ownership inventory

Goal: understand what can be deleted versus what must be moved.

Audit all types/functions under `internal/services/callback/` and classify:

```text
protocol parsing/encoding
Router
handler interfaces
StateStore
state scope ownership
callback answer helpers
rate limiting / answer ownership
generic Telegram utility
test helper
dead code
```

For generic utilities still useful elsewhere:

- move them to the correct authority package;
- do not keep a dead callback subsystem merely to retain helpers.

Resource audit:

- StateStore max cardinality / TTL behavior;
- cleanup worker/ticker if any;
- Router retained maps;
- callback limiter state;
- shutdown lifecycle;
- idle goroutines.

Acceptance:

- deletion/move plan for every file in `internal/services/callback/`;
- resource savings expected from reclamation documented;
- no speculative rewrite.

### Stop point

After F1-D: summarize and wait for confirmation before F1-E.

---

## P1-F1-E — inventory closure + freeze fence

Goal: freeze the exact remaining legacy surface before migrations.

Create/strengthen architecture tests so that:

- no new production package may import legacy callback outside the F1 allowlist;
- no new v1 namespace producer may appear outside the producer allowlist;
- Settings/MyXL remain permanently zero-legacy;
- the allowlist can only shrink during F2.

Produce final F1 migration order.

Recommended prioritization:

```text
small/read-only namespace
 -> bounded stateful namespace
 -> mutation namespace
 -> high-risk purchase/admin namespace
 -> bootstrap-only residual
```

Do not order by filename; order by state/mutation risk.

Acceptance:

- authoritative inventory checked in;
- freeze architecture test checked in;
- exact F2 worklist established;
- zero unknown production caller.

### P1-F1 closure

Only after F1-A/B/C/D/E are each complete and confirmed may P1-F1 be marked CLOSED.

---

# 9. P1-F2 — migrate every remaining production legacy namespace

**F1-E authoritative result: no remaining production legacy namespace exists. The P1-F2 worklist is EMPTY; no namespace migration subphase should be invented. Proceed to P1-F3 only after explicit user confirmation.**

**Do not invent F2 namespace names before F1 is complete. F1-E matrix is authoritative.**

Settings and MyXL are already completed slices and must not be migrated again.

For each remaining namespace, create one reviewable subphase:

```text
P1-F2-<namespace>
```

Migration template:

1. identify current producer/consumer/state;
2. declare FeatureSpec interactions;
3. bind to existing native/Assistant a2 driver as appropriate;
4. retain only minimal bounded session state;
5. fresh-revalidate immediately before mutation;
6. execute physical work through TaskEngine;
7. preserve Telegram RPCExecutor boundaries;
8. bind actor/chat/message/revision/generation;
9. migrate producer to a2;
10. keep old consumer temporarily only if already-issued token compatibility is genuinely needed;
11. then remove old namespace producer/consumer/state;
12. shrink F1 allowlist.

Never create a second interaction runtime or namespace-specific callback engine.

Per-namespace acceptance:

- no production legacy import in migrated feature;
- no v1 producer;
- wrong actor/target fails closed;
- stale revision fails closed;
- disable/reload kills old actions;
- bounded state;
- no permanent worker/ticker;
- mutation semantics unchanged;
- user-facing errors safe.

Execution cadence: one F2 namespace at a time, conclude, stop, wait for confirmation.

P1-F2 closes when F1 allowlist has no production feature namespace entries.

---

# 10. P1-F3 — remove legacy StateStore + legacy v1 protocol — CLOSED

Preconditions:

- no production feature emits legacy payloads;
- no production feature needs legacy state;
- already-issued compatibility window is intentionally ended;
- F1 freeze fence confirms producer allowlist empty.

Candidate removals:

```text
callback.StateStore
StateReader/StateWriter interfaces
ScopedCallbackStore
state scope structs/helpers
opaque ID allocation
Store / StoreWithScope
TTL cleanup owned only by legacy state
EncodeCallbackData
ParseCallbackData
v1 protocol constants/parsers
legacy callback data validation
```

Before deletion:

- move any generic reusable helper to the correct package only if current production still uses it;
- do not carry opaque-ID semantics into a2.

Acceptance:

- repo-wide zero production StateStore reference;
- repo-wide zero production `v1:` producer/parser;
- no legacy-state idle worker/resource budget;
- a2 state/resource baseline unchanged.

Stop after F3 and wait for confirmation before F4.

---

# 11. P1-F4 — remove legacy Router + bootstrap/plugin wiring — CLOSED

Preconditions:

- no production legacy namespace handler;
- no StateStore/protocol dependency;
- native a2 and Assistant a2 cover all interactive production surfaces.

Candidate removals:

```text
callback.Router
callback.Handler
callback.HandlerWithOptions
CallbackOptions
legacy callback registration in plugin.Manager
callback Router construction in app/bootstrap
dispatcher callbackRouter field/accessors
legacy fallback branch in Telegram callback ingress
legacy shutdown/cleanup wiring
callback-only metrics/limiter if no other owner
```

Important ingress target after removal:

```text
Telegram callback update
 -> a2 ownership/dispatch
 -> unknown callback reject/ignore policy
```

Do not accidentally consume inline-bot callback surfaces that are owned by another current subsystem; audit actual ingress variants first.

Acceptance:

- no Router object constructed;
- no plugin callback.Handler assertion;
- no callback router setter/getter;
- no legacy fallback in callback ingress;
- disable/reload lifecycle remains correct;
- unknown data policy explicit and safe.

Stop after F4 and wait for confirmation before F5.

---

# 12. P1-F5 — repo-wide legacy callback final acceptance — CLOSED

Final code acceptance baseline: `985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e`.

P1-F5 completed source/lifecycle acceptance and repaired the only production regression found during the audit: P1-F4 had accidentally dropped the native inline callback method while deleting the legacy Router branch. The restored method preserves ingress admission, callback idempotency, EventBus publication, native a2 ownership, and explicit unknown/noop ACK behavior without restoring Router, Handler, StateStore, plugin-scope legacy dispatch, or callback-specific TaskEngine execution.

Final regression gates cover zero production legacy symbols/imports, Settings/MyXL zero-legacy invariants, native + Assistant unknown/noop policy, stale/wrong binding behavior through existing a2 tests, plugin generation invalidation, shutdown rejection, bounded callback dedupe, and a2 session/state settling.

Historical acceptance checklist follows for reference.

Architecture scan must show zero production references to the deleted legacy subsystem.

Suggested forbidden production tokens after closure:

```text
internal/services/callback
callback.Router
callback.Handler
callback.HandlerWithOptions
callback.StateStore
callback.ScopedCallbackStore
EncodeCallbackData
ParseCallbackData
v1:
SetStateStore
RequiresCallbackState
HandleCallback
```

Be careful: `v1:` may have unrelated protocols. Fence exact legacy callback semantics rather than blindly banning every unrelated string.

Behavior matrix:

| Surface | Acceptance |
|---|---|
| Native Settings | a2; text-only still zero-session |
| Assistant Settings | a2 |
| Native MyXL quota | a2 |
| Native MyXL purchase | a2 |
| Assistant MyXL | a2 |
| Other migrated namespaces | a2 |
| Wrong actor/target | fail closed |
| Stale revision | fail closed |
| Plugin reload | old tokens dead |
| Shutdown | sessions settle |
| Unknown callback | explicit safe policy |

Resource evidence:

- StateStore allocation gone;
- Router namespace maps gone;
- callback-only cleanup worker/ticker gone if it existed;
- idle goroutine count not increased;
- a2 session count settles after workload.

P1-F5 closure result:

- P1-F is CLOSED;
- this refinement handoff is updated;
- the main technical handoff pointer is updated;
- implementation acceptance baseline was `985b2ea5de7f4b3e7f7bc326fb4ef56deba4f57e`;
- P2-A was the next step at that closure and has since been completed.

See the P2-A closure section above for current continuation.

---

# 13. P2-A — compress plugin hook registration API — CLOSED

Final code acceptance baseline: `0b3dac3b45e8b1117657c9fafd8a96a03ffe4fa7`.

P2-A audited the current registration topology rather than copying the approximate roadmap struct. Before the change, `plugin.Manager` depended on one base `HookRegistrar` plus nine optional registrar capability interfaces to negotiate:

```text
raw vs canonical
scoped vs unscoped
routed vs legacy routing
state-aware vs no state gate
```

The final boundary is:

```go
core.MessageHookRegistration {
    Scope
    Priority
    Routing
    StateGate
    Handler
    RawHandler
    LegacyRouting
}

plugin.HookRegistrar {
    RegisterMessageHook(core.MessageHookRegistration) (cleanup, error)
}
```

Current behavior:

- canonical `MessageEventPlugin` is still preferred over raw compatibility;
- canonical plugins must declare routing;
- optional canonical state gate is copied into the registration;
- raw compatibility remains privileged and capability-gated;
- routed/stateful raw hooks carry explicit routing;
- old unrouted raw hooks carry `LegacyRouting`, so Dispatcher derives the same historical lane from priority + scope;
- plugin generation scope is always explicit in the registration;
- register and enable/reload paths share the same `registerMessageHook` helper;
- existing hook cleanup ownership in `plugin.Manager` is unchanged;
- Dispatcher still stores/indexes hooks through one `addMessageHandler` implementation.

Production inventory at closure:

```text
AFK        canonical + routed
Blacklist  canonical + routed + state gate
Filters    canonical + routed + state gate
PMPermit   canonical + routed + state gate
UserLog    canonical + routed
raw production hook implementations found: 0
```

Acceptance evidence:

- nine registrar capability interfaces removed;
- zero `registrar.(...)` capability probing in `registerMessageHook`;
- one `HookRegistrar` method;
- one shared registration struct;
- direct application wiring `SetHookRegistrar(tgRuntime.dispatcher)` preserved;
- explicit canonical/raw exclusivity validation;
- routing/state/scope cleanup tests updated;
- historical raw scoped feature lane has a regression test;
- architecture fence prevents re-expansion into registrar capability interfaces;
- no worker/ticker/cache/registry introduced by P2-A.

Verification constraint remains unchanged: no executable checkout was available in this environment, so no local Go command is claimed. CI was not inspected.

P2-A: **CLOSED**.

Stop and wait for explicit user confirmation before P2-B.

---

# 14. P2-B — expand localization into common userbot UX — CLOSED

Final code acceptance baseline: `0fd0d08314afe0689486aecd7ca5c507475ac3f6`.

P2-B reuses the existing localization authority rather than creating another locale registry or presentation layer. The final model is:

```text
settings.Service ui:locale
        ↓
localization.ResolveLocale(userID, chatID)
        ↓
localization.Bind(shared Localizer, locale)
        ↓
core.Context.Localizer
        ↓
ctx.T(key, args...)
```

Interactive surfaces that retain locale in bounded interaction state continue to use the existing explicit `localization.Translate(locale, ...)` helper. Assistant locale selection uses the same `ui:locale` identity and resolver.

Closed common UX surfaces:

```text
common navigation/status vocabulary
Help
Settings dashboard/native interaction
Settings CLI
Downloader interactive lifecycle
Admin/moderation
Media
Profile/contacts/dialogs
Assistant shared navigation/settings/help vocabulary
```

Acceptance:

- English remains the deterministic default/fallback;
- locale is selected by the existing settings hierarchy;
- EN/ID built-in catalog parity is enforced;
- current userbot-specific catalog parity is 136/136;
- `internal/presentation` imports neither localization nor settings;
- metrics do not depend on localization;
- no locale-specific worker/ticker/cache/registry was added;
- no dynamic metric-label localization was introduced;
- common Help/Settings/Downloader copy is shared with Assistant where appropriate;
- Settings/Admin/Media/Profile hardcoded residuals identified by the P2-B audit are removed;
- standalone unit-test contexts for these localized surfaces bind the English canonical Localizer;
- architecture fences prevent the selected common UX literals and localization authority from drifting back.

P2-B deliberately does **not** attempt P2-C work. Raw/internal error exposure that still exists in production paths belongs to the next response/error modernization phase and must not be conflated with localization.

Verification constraint: no executable checkout was available in this environment, so no local Go command is claimed. CI was not inspected.

P2-B: **CLOSED**.

Stop and wait for explicit user confirmation before P2-C.

---

# 15. P2-C — repo-wide response/error modernization — CLOSED

Status: **CLOSED** at code acceptance baseline `89790e517001ee1432a0e91927ac468d36a58a56`.

Canonical safe boundary remains P0-B: `Context.Fail`, `WithUserMessage`, `UserMessage`, plus semantic Status/Progress/Success/Result helpers. P2-C did not create a second sanitizer or response engine.

Fresh continuation audit closed indirect residuals beyond the original direct-pattern migration:

- Broadcast no longer exposes capture errors or TaskEngine `Failure.Message`.
- Filters no longer exposes media capture errors.
- Clone no longer exposes async state re-check errors.
- Scheduler no longer renders persisted `LastError` / history `ErrorMsg` to Sudo callers.
- Architecture fences now pin these residuals closed.
- `.exec` remains the only intentional raw diagnostic exception and is owner-only/userbot-only, bounded, and escaped.

Safe non-presentation uses of `err.Error()` remain allowed for classification, persistence/audit state, logging, and explicit bounded sanitizers such as OCR.

See `docs/design/goultroid-refinement-p2c-response-error-modernization.md`.

**NEXT = P2-D — remove dead legacy UI helpers.**

---

# 16. P2-D — remove dead legacy UI helpers — CLOSED

Status: **CLOSED** at `6261169c07de9c26f60304b0136c14a1e2a85b30`.

The current-source inventory showed that the broad callback-era UI builder surface was test-only/dead after P1-F, while several transport-neutral values and pure render/format helpers remain production dependencies.

Removed zero-production-call compatibility:

- action-bar/retry/loading callback builders;
- confirmation/preview callback cards;
- navigator re-exports and navigation button shims;
- stateless wizard compatibility renderer;
- toggle/stepper/selector/duration/nav callback builders;
- legacy presentation-role button adapters and convenience markup rows.

Preserved production/pure helpers:

- Card/format/progress helpers;
- Screen;
- Button/ButtonRow/Markup values;
- PaginateSlice;
- PresentUserError;
- ui/render -> presentation/telegram encoder delegation.

Architecture acceptance is fenced by `internal/architecture/ui_helper_reclamation_p2d_test.go`.

P2-D did not introduce another callback protocol, interaction runtime, Telegram serializer, or label authority.

---

# 17. P3-A — benchmark before optimization — HARNESS READY / MEASUREMENTS OPEN

Status: benchmark harness committed at `aeae302d125bf1e5ac39df9671c0ea6b026bd10e`; real measurement results are still required before P3-A can close.

Current high-cardinality matrix:

## Inline registry

```text
exact/1
exact/16
exact/64
exact/256
exact/1024
exact/4096

custom/1
custom/16
custom/64
custom/256
custom/1024
custom/4096
```

## Inline cache

```text
hit/1
hit/16
hit/64
hit/256
hit/500

fill/1
fill/16
fill/64
fill/256
fill/500

saturated churn working-set/501
saturated churn working-set/4096
next-expiry scan/500
```

The cache remains bounded by current production constants: 500 entries, 8 MiB total, 256 KiB per entry.

## Generic rate limiter

```text
hot/1
hot/16
hot/64
hot/256
hot/4096

insert/1
insert/16
insert/64
insert/256
insert/4096

saturated capacity lookup at 4096
forced capacity sweep at 4096
```

The generic limiter remains fail-closed at its current 4096-bucket cap.

## Existing related benchmark coverage

`internal/telegram/benchmarks_test.go` already measures the hierarchical RPC limiter at production-scale cardinality. Do not create another limiter implementation or duplicate that authority merely for P3-A.

## Commands for a real checkout

```bash
go test -run=^$ -bench='BenchmarkRegistryResolveOwnedExplicitP0D|BenchmarkCache.*P3A' \
  -benchmem -benchtime=1s -count=5 ./internal/services/inline

go test -run=^$ -bench='BenchmarkLimiter.*P3A' \
  -benchmem -benchtime=1s -count=5 ./internal/services/ratelimit
```

Record environment alongside results:

```text
HEAD
Go version
OS/arch
CPU
GOMAXPROCS
benchmark command
ns/op
B/op
allocs/op
custom metrics (handlers / entries/op / keys/op / buckets/op)
```

Interpretation rules:

- exact inline lookup should be evaluated for cardinality independence;
- custom inline matcher cost is expected to grow with matcher cardinality, but optimize only if absolute measured cost is material;
- cache hit cost should be separated from bounded clone cost;
- cache churn specifically measures the eviction path once working set exceeds the 500-entry cap;
- cache next-expiry measures the deadline coordinator's bounded 500-entry scan;
- limiter hot path should be compared across resident cardinalities;
- limiter forced capacity sweep intentionally isolates the bounded 4096-bucket scan;
- a bounded O(N) path is not automatically a defect if measured absolute cost is acceptable and invocation frequency is low.

Do not advance to P3-C until these measurements exist. Do not claim benchmark numbers that were not run.

---

# 18. P3-B — event-driven TaskEngine completion drain — CLOSED

Status: **CLOSED** at `440c9ba977ecaa95da4cf9ce232305915f2a3936`.

Current-source audit found one remaining polling drain:

```text
completionDelivery.drain()
  -> time.NewTicker(1ms)
  -> poll pending / active / queue
```

The admitted-task coordinator path was already event-driven with `drainDone`, so P3-B changed only completion callback settlement.

Current design:

```text
enqueue callback
  -> mark current drain generation busy

last pending/active callback settles
  -> close generation drainCh

Drain(ctx)
  -> wait on drainCh or ctx.Done()
```

Closing the generation channel broadcasts to all current waiters and avoids a one-consumer wakeup race. A later callback generation installs a new open channel before publishing pending work.

Acceptance:

- no polling ticker/sleep/after in `completionDelivery.drain()`;
- no permanent drain goroutine;
- multiple waiters are supported;
- callback ordering and bounded delivery queue are unchanged;
- shutdown deadline/context still wins;
- existing lazy-worker retirement behavior is unchanged;
- architecture regression fence prevents polling reintroduction.

Verification limitation: source/diff acceptance plus local `gofmt` only; no claim of executed repository tests/race/build in the current container.

---

# 19. P3-C — optimize only measured hotspots

Potential candidates only if P3-A proves material impact:

- Inline expiry management;
- generic limiter expiry management;
- presentation allocations;
- plugin registration overhead;
- a2 hot-path allocation;
- TaskEngine completion path.

Forbidden:

- unbounded caches;
- permanent cleanup workers;
- sync.Pool solely for benchmark cosmetics;
- lock-free complexity without measured contention;
- unsafe cross-session reuse.

If measurements show bounded scans are cheap, document that and skip optimization.

---

# 20. P4 — final refinement closure acceptance

P4 runs only after:

```text
P1-F CLOSED
P2-A/B/C/D CLOSED
P3-A completed
P3-B closed/skipped with source proof
P3-C completed or explicitly skipped from evidence
```

Functional matrix:

| Area | Acceptance |
|---|---|
| Userbot | ordinary commands work without Assistant |
| Assistant | navigation/action/input work and reload cleanly |
| Inline | exact/custom precedence unchanged |
| Settings | text-only zero-state; native + Assistant interactive a2 |
| MyXL | native + Assistant a2; fresh purchase authority; bounded TTL |
| Callback | legacy callback stack absent from production |
| Presentation | one Telegram keyboard serializer |
| Errors | ordinary UI does not expose raw internal causes |
| Edit UX | ambiguous edit never triggers duplicate fallback send |
| Plugins | disable/reload invalidates old sessions/work |
| Shutdown | tasks/sessions/resources settle within global deadline |

Combined workload:

```text
regular command burst
Assistant root/help/settings navigation
callback burst
native Settings navigation/mutation
native MyXL quota refresh
native/Assistant MyXL confirmation flow without real financial charge
inline exact queries
custom matcher queries
downloader cancel/retry
plugin disable/reload while sessions exist
shutdown
```

Measure before, peak, and after settling:

```text
goroutines
a2 sessions
TaskEngine active/pending
resource budget usage
Inline cache size
RPC limiter bucket count
completion state
heap/RSS when practical
```

Verification when possible:

```text
gofmt cleanliness
go build -o bin/goultroid ./cmd/goultroid
targeted package tests
go test ./...
go test -race for lifecycle/state-heavy packages when explicitly requested/feasible
P3-A benchmarks
```

Do not claim commands that did not run.
Do not inspect CI unless explicitly asked.

Final architecture target:

```text
interaction sessions       -> a2 only
native callbacks           -> a2 only
Assistant interactions     -> a2 only
Telegram keyboard encoding -> one encoder
inline routing             -> Inline vNext
Telegram retry             -> RPCExecutor
execution/resources        -> TaskEngine
plugin lifecycle           -> plugin.Manager generation scope
```

At P4 closure:

1. mark this handoff CLOSED;
2. update `docs/design/goultroid-next-technical-plan-ai-handoff.md`;
3. record exact final HEAD;
4. record benchmark/resource evidence;
5. record any optimization intentionally skipped;
6. declare refinement program complete and return to ordinary feature/product development.

---

# 21. High-risk regression checklist

Before closing any remaining subphase verify it did not introduce:

- second interaction runtime;
- second callback token protocol;
- second inline registry;
- second TaskEngine;
- plugin-local Telegram retry/FloodWait;
- direct Telegram RPC inside completion callback;
- unbounded per-user/chat/query state;
- permanent feature worker/ticker;
- stale generation resurrection;
- wrong-actor execution;
- wrong-target execution;
- stale-session mutation;
- purchase based on stale provider authority;
- duplicate settlement on replay;
- auto fallback after ambiguous send/edit;
- raw internal-error exposure;
- resource lease held while waiting for user click;
- download/process lease held during Telegram upload;
- dynamic user/query/error metric labels.

---

# 22. Files to read first in the next session

## P1-F1-A — immediate next task

```text
internal/services/callback/
internal/plugin/manager.go
internal/plugin/features.go
internal/module/
internal/app/
internal/telegram/dispatcher.go
internal/telegram/dispatcher_callback.go
internal/telegram/dispatcher_accessors.go
plugins/** production Go files importing internal/services/callback
internal/architecture/*callback*
```

Also verify known-zero baselines:

```text
plugins/settings/
plugins/myxl/
```

## Later P2

```text
internal/plugin/
internal/services/localization/
internal/core/context.go
internal/core/user_error.go
internal/presentation/
internal/ui/
plugins/
```

## P3

```text
internal/services/inline/
internal/services/ratelimit/
internal/interaction/
internal/taskengine/
```

---

# 23. Exact next-session instruction

Start with:

> Refresh `test-next` HEAD and current source. P1-F and P2-A/B/C/D are CLOSED. P3-B completion drain is CLOSED at `440c9ba977ecaa95da4cf9ce232305915f2a3936` with event-driven generation-channel broadcast and no polling in `completionDelivery.drain()`. P3-A high-cardinality benchmark harness is present, but P3-A measurements are still OPEN. Continue by executing and recording the P3-A Inline/cache/generic-limiter benchmark results on a real checkout. Do not start P3-C optimization until those measurements exist. Do not inspect CI unless explicitly requested.

Important baseline:

```text
P1-F: CLOSED
P2-A/B/C/D: CLOSED
P3-A harness: IMPLEMENTED
P3-A measurements: OPEN
P3-B: CLOSED at 440c9ba977ecaa95da4cf9ce232305915f2a3936
TaskEngine admitted-task drain: drainDone channel
TaskEngine completion drain: event-driven drainCh generation broadcast
completion drain polling: zero
P3-C: BLOCKED on P3-A measurements
```

---

# 24. Definition of done for the whole refinement program

The program is finished when:

1. presentation owns common UI semantics;
2. Telegram keyboard serialization has one implementation;
3. native rich interactions use a2 without Assistant dependency;
4. Settings native + Assistant interactions are a2 and zero-legacy;
5. MyXL native + Assistant interactions are a2 and zero-legacy;
6. repo-wide legacy callback stack has zero production callers and is removed;
7. plugin hook registration is materially simpler;
8. common userbot UX uses the existing localization authority;
9. remaining production paths do not casually expose raw internal errors;
10. dead callback/UI compatibility helpers are removed;
11. performance/resource work is benchmark-driven;
12. completion drain has no unnecessary polling if current source still had it;
13. combined workload settles resources cleanly;
14. no duplicate authority was introduced;
15. final acceptance and benchmark/resource evidence are documented.

At that point Goultroid returns to ordinary product development instead of architecture migration.

---

## One-line handoff

```text
P1-F and P2-A/B/C/D CLOSED; P3-B CLOSED at 440c9ba9 with event-driven completion drain.
P3-A benchmark harness exists but measurements remain OPEN.
NEXT = execute/capture P3-A measurements; P3-C stays blocked until evidence exists.
```
