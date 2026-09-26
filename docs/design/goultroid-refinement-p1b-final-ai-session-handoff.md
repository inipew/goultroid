# Goultroid — Userbot / Assistant / Inline / UI Refinement AI Session Handoff

Date: 2026-09-26
Branch: `test-next`
Current audited baseline before P1-F1-E closure: `4fb5bb77d9a0a1186eae358699b9402f7eb74db3` — `docs(callback): classify legacy ownership`
Purpose: continue refinement from **P1-F5** through final **P4 closure** after P1-F4 removed the legacy Router/bootstrap/plugin wiring; P1-F2 had no namespace migration work.

Authority rule: **always refresh current HEAD and current source first. Source/tests win over this handoff if the branch has moved.**

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
- native callback ingress checks a2 before legacy router;
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

P1-F is **not yet globally closed**.

Known-zero domains:

```text
plugins/myxl      zero legacy callback production surface
plugins/settings  zero legacy callback production surface
```

Unknown:

- remaining plugins/features that still import or implement legacy callback APIs;
- remaining producers of v1 payloads;
- remaining consumers/Router registrations;
- remaining StateStore ownership;
- bootstrap/dispatcher dependencies on legacy Router;
- generic utilities inside `internal/services/callback` that may still have production callers.

Therefore the next session must **not** jump directly to deleting `internal/services/callback`.

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

# 12. P1-F5 — repo-wide legacy callback final acceptance

Run source and lifecycle acceptance.

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

At F5 closure:

- mark P1-F CLOSED;
- update this handoff;
- update the main technical handoff pointer if needed;
- record exact HEAD.

Stop and wait for confirmation before P2-A.

---

# 13. P2-A — compress plugin hook registration API

Status: NOT STARTED in this refinement continuation.

Problem: plugin hook registration has accumulated capability-interface variants.

Goal: move toward one explicit registration spec, based on current source, approximately:

```go
type MessageHookRegistration struct {
    Scope     tasks.ScopeIdentity
    Priority  int
    Routing   core.MessageHookRouting
    StateGate func(int64) bool
    Handler   CanonicalMessageHookHandler
}
```

Do not copy this exact struct without refreshing source.

Constraints:

- canonical normalized message handler remains default;
- raw Telegram hooks remain privileged compatibility;
- lifecycle scope explicit;
- no full-plugin hot-path scan;
- no reflection-heavy registration.

Acceptance:

- materially fewer interface assertions;
- routing unchanged;
- reload/unregister unchanged;
- no worker added;
- no hot-path fan-out regression.

Execute as reviewable subphases if source shows multiple independent hook families.

---

# 14. P2-B — expand localization into common userbot UX

Status: NOT STARTED here.

Use the existing localization authority only.

Recommended order:

```text
common navigation
common status/error/success/progress
usage/help templates
Help
Settings
Downloader
admin/moderation
media/profile
plugin-specific copy
```

Do not make `internal/presentation` depend on localization/global state.

Acceptance:

- required EN/ID key parity;
- deterministic fallback;
- current settings authority selects locale;
- no dynamic metric-label localization;
- avoid duplicate native/Assistant copy where reasonable.

---

# 15. P2-C — repo-wide response/error modernization

Status: PARTIAL FOUNDATION exists via P0-B; repo-wide migration remains.

Search production code for unsafe patterns:

```text
ctx.Error(err.Error())
ctx.Error(fmt.Sprintf(... err ...))
ctx.Status(err.Error())
ctx.Reply(fmt.Sprintf(... err ...))
ctx.EditOrReply(fmt.Sprintf(... err ...))
html.EscapeString(err.Error())
```

Escaping is not sanitization.

Prefer semantic/safe boundaries:

```text
ctx.Status(...)
ctx.Progress(...)
ctx.Success(...)
ctx.Result(...)
ctx.Fail(cause, safeMessage)
```

For owner diagnostics: explicit authorization, size cap, escaping, and redaction.

Migration model:

```text
existing debt -> temporary exact allowlist
new debt      -> architecture test failure
allowlist     -> shrink per migration
```

---

# 16. P2-D — remove dead legacy UI helpers

Run only after P1-F is closed, because callback reclamation may make additional UI helpers dead.

Classify `internal/ui` helpers:

```text
production
pure formatting/value helper
compatibility
test-only
dead
```

Remove only zero-production-call helpers.

Preserve useful pure cards/formatters.

Acceptance:

- smaller UI surface;
- no callback-only builders after legacy callback removal;
- no second Telegram serializer;
- common labels remain owned by presentation vocabulary.

---

# 17. P3-A — benchmark before optimization

Status: NOT YET RUN for current post-P1-F/P2 code.

Do not optimize based on historical suspicion.

Required measurements when a real executable checkout/toolchain is available:

## Inline

```text
exact/1
exact/16
exact/64
exact/256
custom/1
custom/16
custom/64
custom/256
```

Record:

```text
ns/op
B/op
allocs/op
```

## a2 runtime

Measure:

- create;
- callback prepare/dispatch;
- transition;
- terminate;
- expiry;
- 1 / 64 / 512 / 4096 sessions;
- memory/session;
- reload cancellation;
- close settling.

## TaskEngine

Measure:

- cold admission;
- warm admission;
- completion delivery;
- resource contention;
- drain/shutdown.

## Combined workload

```text
normal userbot command
Assistant navigation
inline exact query
inline custom query
a2 callback burst
Settings mutation
MyXL refresh
MyXL purchase confirmation without real charge
downloader long op/cancel path
plugin reload
shutdown
```

Observe before/peak/after settling:

```text
goroutines
heap/RSS when practical
TaskEngine active/pending
a2 sessions
Inline cache
RPC limiter buckets
resource usage
completion state
```

Create a benchmark/acceptance document. Do not claim optimization benefits without before/after evidence.

---

# 18. P3-B — event-driven TaskEngine completion drain if polling still exists

Refresh source first. Historical audit mentioned short polling in completion drain/shutdown; it may already be changed.

Only if current source still polls:

```text
pending/active transition
 -> drained signal/channel/condition
```

Constraints:

- no permanent worker;
- no deadlock;
- global shutdown deadline wins;
- completion ordering preserved;
- shutdown idempotent.

If no polling remains, record P3-B as intentionally skipped/already satisfied.

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

> Refresh `test-next` HEAD and current source. P1-F1 is CLOSED, P1-F2 was EMPTY, P1-F3 is CLOSED, and P1-F4 is CLOSED. Continue **P1-F5 — repo-wide legacy callback final acceptance** only. Prove zero production import/reference to the deleted callback subsystem, verify native + Assistant a2 callback behavior and explicit unknown/noop ACK policy, verify Settings/MyXL remain zero-legacy, check plugin reload/shutdown/session invalidation/resource settling, and update the closure documentation. Do not start P2-A. Run gofmt/build/tests when an executable checkout is available; do not claim commands that did not run. Do not check CI unless explicitly requested.

Important baseline:

```text
P1-F1: CLOSED
P1-F2: EMPTY
P1-F3: CLOSED
P1-F4: CLOSED
legacy callback package: removed
legacy Router/Handler/bootstrap wiring: removed
native unknown callback: noop silent ACK / otherwise expired ACK
Assistant unknown callback: bounded dedupe + noop silent ACK / otherwise expired ACK
NEXT executable phase: P1-F5
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
P1-F1 CLOSED; P1-F2 EMPTY; P1-F3 CLOSED; P1-F4 CLOSED at 41148cd5 with legacy callback package/Router/bootstrap removed.
NEXT = P1-F5 repo-wide legacy callback final acceptance only -> STOP before P2-A.
```
