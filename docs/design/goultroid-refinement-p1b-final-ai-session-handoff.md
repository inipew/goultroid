# Goultroid — Userbot / Assistant / Inline / UI Refinement AI Session Handoff

Date: 2026-09-26
Branch: test-next
Audited HEAD: 27b1f0fe7dd2e2d25d568daca2632a7d52a82bc9 — refactor(ui): establish canonical presentation vocabulary
Purpose: continue post-redesign engineering refinement from P1-B through final closure.

Authority rule: always refresh current source and HEAD first. Current source/tests override this handoff if branch drift exists.

---

## 0. Current state

The architecture is no longer in a broad redesign phase. The current goal is refinement: remove duplicate compatibility layers, simplify contracts, preserve one authority per concern, keep resources bounded, and improve UX consistency without adding new engines.

Completed refinement chain:

~~~text
1f6aaae5  P0-A  Settings zero-state text rendering
189aefe2  P0-B  typed safe user-facing error boundary
20b5532c  P0-C  commit-safe EditOrReply contract
b7892e13  P0-D  Inline exact-handler fast path + benchmark
27b1f0fe  P1-A  canonical presentation vocabulary
~~~

Earlier Assistant-optional userbot redesign is also closed:

~~~text
4733631b  stage-aware self-inline failure semantics
8ce89d6a  Assistant-optional composition acceptance
491f1f9e  Help progressive fallback
48af3fd3  Calculator progressive fallback
2262903e  Downloader URL progressive fallback
99669a0f  retained ownership acceptance
d1b8c4a3  Settings Assistant decoupling
502d5aeb  repo-wide SurfaceUserbot dependency fence
bd807592  final Assistant-optional userbot acceptance
0f88e543  closure handoff/docs
~~~

Do not reopen those phases unless fresh source/runtime evidence proves a regression.

---

## 1. Binding engineering rules

1. Work on test-next.
2. Refresh exact HEAD before every phase.
3. Read current source/tests before trusting this handoff.
4. Run gofmt before every Go commit.
5. Do not inspect or wait for CI unless the user explicitly asks.
6. Do not create a second TaskEngine.
7. Do not create a second Telegram RPC/retry/FloodWait executor.
8. Do not create a second interaction/session runtime.
9. Do not create a second inline registry.
10. Do not create a second downloader/provider registry.
11. Do not add permanent per-feature workers, tickers, or pollers.
12. Keep every cache, state store, queue, and retained resource bounded.
13. Prefer event-driven/lazy workers and zero-idle behavior.
14. Generation-scoped work must fail closed after plugin disable/reload.
15. Fresh authority is required immediately before mutations.
16. Never auto-send an alternate Telegram message after an ambiguous send/edit commit.
17. Ordinary user-facing errors must not expose raw provider/database/filesystem/process/RPC causes.
18. Simplify current boundaries; do not replace them with competing authorities.

Canonical authorities:

~~~text
Command routing                 core.Router
Execution/admission             TaskEngine + execution semantics
Telegram retry/FloodWait        telegram.RPCExecutor
Plugin lifecycle                plugin.Manager + generation scope
Interaction/session state       interaction.Runtime (a2)
Interaction orchestration       interaction/orchestration.Engine
Inline lifecycle                services/inline vNext
Presentation model              internal/presentation
Telegram rich presentation      presentation/telegram Bridge
Settings domain state           internal/settings service
Downloader/provider selection   download.Registry
Retained media ownership        shared media/storage infrastructure
~~~

---

# P1-B — collapse duplicate Telegram keyboard serializers

## Problem

Two production paths currently serialize generic button metadata into MTProto inline keyboards:

~~~text
internal/presentation/telegram/bridge.go
    markup(presentation.CompiledView)

internal/ui/render/telegram.go
    ToTelegramMarkup(ui.Markup)
~~~

Both construct KeyboardButtonCallback, KeyboardButtonURL, and KeyboardButtonSwitchInline independently.

## Goal

Make presentation/telegram the single Telegram keyboard serialization authority.

Preferred flow:

~~~text
presentation.CompiledButton/CompiledRow
                |
                v
presentation/telegram canonical encoder
                ^
                |
legacy ui.Markup adapter
~~~

Do not make internal/presentation import internal/ui.

## Safe migration

1. Extract bridge markup conversion into one reusable encoder.
2. Keep Bridge.Send/Edit using it.
3. Convert legacy ui.Markup into canonical transport-ready button rows.
4. Delegate legacy ui/render to the same encoder.
5. Preserve callback Data bytes exactly.
6. Preserve URL, InlineQuery, and SamePeer semantics.
7. Preserve empty row / nil markup behavior.
8. Do not compile legacy callback bytes through a2; they are already callback payloads.

## Acceptance

Test equivalent legacy and presentation keyboards for:

- callback data;
- URL;
- switch-inline SamePeer false/true;
- mixed rows;
- empty rows;
- nil markup;
- callback byte ownership/copy behavior.

Architecture fence:

- one production encoder constructs generic inline button MTProto types;
- internal/ui/render delegates;
- internal/presentation never imports internal/ui.

Likely files:

~~~text
internal/presentation/telegram/bridge.go
internal/presentation/telegram/bridge_markup_test.go
internal/ui/render/telegram.go
internal/ui/render/telegram_test.go
internal/architecture/
~~~

---

# P1-C — native/userbot a2 interaction adapter

## Goal

Allow native userbot rich interactions to use the existing a2 runtime without depending on Assistant.

Target:

~~~text
userbot message
   -> native message target adapter
   -> interaction.Runtime + Dispatcher + orchestration.Engine
   -> presentation.Compiler
   -> presentation/telegram Bridge
~~~

Assistant identity must not be required.

## Why

Most modern interactions already use a2, but Settings and residual MyXL native callbacks still depend on legacy services/callback.

Do not delete the legacy stack in P1-C. First make native a2 usable.

## Required binding

Native a2 must bind at least:

~~~text
ActorID
ChatID
MessageID
plugin generation/scope
session revision
~~~

Reuse presentation/telegram.MessageTarget, interaction.Binding, and interaction.TargetBinding where possible.

## Callback ingress coexistence

During migration:

~~~text
known a2 callback/token
    -> a2 dispatcher

known legacy v1 namespace callback
    -> legacy callback router

unknown
    -> current reject/ignore policy
~~~

Do not let failed a2 parsing consume or mutate legacy state.

## Lifecycle acceptance

- native session works with Assistant absent;
- wrong actor rejected;
- wrong chat/message rejected;
- stale revision rejected;
- plugin disable invalidates action;
- reload gets new generation;
- old token never revives;
- shutdown clears/settles sessions;
- no permanent feature worker;
- physical work still goes through TaskEngine.

---

# P1-D — migrate native Settings callbacks to a2

## Goal

Remove production dependency from plugins/settings to internal/services/callback.

P0-A invariant must remain:

~~~text
ui:inline_buttons=false
    -> text-only Settings
    -> zero callback/a2 session allocation
~~~

Interactive mode becomes:

~~~text
.settings
    -> bounded native a2 session
    -> presentation.View
    -> typed actions
    -> existing Settings service
~~~

## Preserve domain authority

Do not duplicate setting values or definitions into session state.

Session state should contain only minimal navigation data, for example:

~~~text
scope
scope id
category
page
selected namespace:key
revision/navigation state
~~~

Immediately before persistence, re-resolve current definition and revalidate authority.

## Preserve parity

Native Settings must retain:

- home;
- categories;
- pagination;
- detail;
- bool toggle;
- steppers/selectors/duration controls where applicable;
- reset;
- scope switching;
- Home;
- Back;
- Close;
- command category/page arguments;
- text-only mode.

Assistant Settings remains a separate enhancement over the same Settings domain service.

## Acceptance

- no production internal/services/callback import in plugins/settings;
- text-only mode still allocates zero interaction state;
- interactive native path uses a2;
- wrong actor/target and stale revision fail closed;
- old buttons die on reload;
- native command works with Assistant absent/stopped;
- sessions settle after Close/expiry/reload/shutdown.

---

# P1-E — migrate residual MyXL native callbacks to a2

## Goal

MyXL Assistant path is already a2. Remove remaining legacy native callback state.

Historically residual native actions included refresh, buy_confirm, and buy_cancel; refresh current source before assuming this list is still exact.

## Security/resource rules

Keep sensitive state shorter lived than ordinary navigation.

Do not retain full provider/API payloads in session memory. Prefer stable IDs:

~~~text
account id
package id
option id
draft id
revision
~~~

Before purchase/mutation:

1. revalidate actor/session/generation;
2. load fresh account/session;
3. load fresh package/option/price when needed;
4. revalidate draft/revision;
5. execute through TaskEngine;
6. preserve shared retry/RPC boundaries;
7. use P0-B typed safe errors;
8. preserve idempotency under ambiguous outcomes.

## Acceptance

- no production legacy callback import in MyXL;
- native .myxl works without Assistant;
- Assistant MyXL remains a2;
- legacy payload cannot execute;
- sensitive TTLs bounded;
- wrong actor/target fails closed;
- reload kills old tokens;
- purchase/cancel semantics remain durable/idempotent.

---

# P1-F — remove legacy callback stack after zero production callers

Only after P1-D and P1-E are proven.

Candidate components:

~~~text
internal/services/callback.Router
internal/services/callback.StateStore
legacy v1 callback encoding/parsing
legacy bootstrap/plugin wiring
legacy callback-only state helpers
~~~

Required procedure:

1. Search all production Go source for internal/services/callback.
2. Separate test/history references from production callers.
3. Verify no plugin registers legacy Handler.
4. Verify no bootstrap route needs callback.Router.
5. Verify no production screen emits v1 payloads.
6. Run targeted lifecycle/interaction tests when possible.
7. Delete only after zero production callers.
8. Move generic utilities out if still useful rather than retaining the subsystem.

Add an architecture fence preventing new production legacy callback imports.

Resource acceptance:

- legacy StateStore budget gone;
- legacy callback limiter/router lifecycle gone;
- a2 sessions settle to baseline;
- Settings/MyXL behavior unchanged.

---

# P2-A — compress plugin hook registration API

## Problem

Plugin manager hook registration has accumulated a matrix of interfaces for scoped/routed/state-gated/canonical variants.

Behavior is mature, but the API is hard to reason about.

## Goal

Move toward one explicit registration specification, approximately:

~~~go
type MessageHookRegistration struct {
    Scope     tasks.ScopeIdentity
    Priority  int
    Routing   core.MessageHookRouting
    StateGate func(int64) bool
    Handler   CanonicalMessageHookHandler
}
~~~

Exact fields must follow current source.

## Constraints

- normalized/canonical message handler remains default;
- raw Telegram hooks stay privileged compatibility;
- lifecycle scope remains explicit;
- interest routing must not regress to global fan-out;
- no reflection-heavy hot-path registration.

## Acceptance

- materially fewer capability-interface assertions;
- registration code smaller;
- routing behavior unchanged;
- reload/unregister behavior unchanged;
- no new worker;
- no hot-path full plugin scan.

---

# P2-B — expand localization into common userbot UX

Use the existing localization service and Context.T(). Do not create another i18n system.

Recommended migration order:

~~~text
common navigation
common status/error/success/progress
usage/help templates
Help
Settings
Downloader
admin/moderation
media/profile
plugin-specific copy
~~~

P1-A established canonical English role labels in presentation.ButtonRole. Localized surfaces can keep using ActionButton with translated labels.

Do not make the base presentation package depend on localization/global state.

Acceptance:

- required built-in EN/ID key parity;
- deterministic fallback;
- no dynamic metric-label localization;
- userbot locale follows current settings authority;
- common text is not duplicated separately between Assistant/userbot where avoidable.

---

# P2-C — repo-wide response/error modernization

Extend P0-B to remaining production code.

Search for patterns such as:

~~~text
ctx.Error(err.Error())
ctx.Error(fmt.Sprintf(... err ...))
ctx.Status(err.Error())
ctx.Reply(fmt.Sprintf(... err ...))
ctx.EditOrReply(fmt.Sprintf(... err ...))
html.EscapeString(err.Error())
~~~

Escaping is not sanitization.

Prefer:

~~~text
ctx.Status(...)
ctx.Progress(...)
ctx.Success(...)
ctx.Result(...)
ctx.Fail(cause, safeMessage)
~~~

Use raw Reply/Edit only for genuine transport-specific behavior.

For owner-only diagnostics, use an explicit bounded diagnostic path with authorization, size cap, escaping, and secret/path redaction.

Migration strategy:

~~~text
existing debt -> temporary allowlist
new debt      -> architecture test fails
~~~

Reduce allowlist as plugins migrate.

Acceptance:

- internal causes are not passed directly to presentation methods;
- causes remain available to logs/metrics;
- Assistant does not duplicate already-presented errors;
- DispositionHandled remains correct;
- no raw internal details in ordinary Telegram UI.

---

# P2-D — remove dead legacy UI helpers

After Settings/MyXL leave legacy callbacks:

1. search production references to internal/ui helpers;
2. classify production / compatibility / test-only / dead;
3. remove only zero-production-call helpers;
4. keep pure formatting/card helpers that still add value;
5. use P1-A vocabulary for surviving common actions.

Do not rewrite stable screens just to reduce LOC.

Acceptance:

- smaller internal/ui surface;
- no duplicate common labels;
- no duplicate Telegram serializer after P1-B;
- no dead callback helpers after P1-F.

---

# P3-A — benchmark before further optimization

From this point forward, optimize only measured problems.

Run the real Go benchmark from P0-D when a full checkout/toolchain exists:

~~~text
exact/1
exact/16
exact/64
exact/256
custom/1
custom/16
custom/64
custom/256
~~~

Record ns/op, B/op, allocs/op.

Also benchmark:

### Inline cache
Current cache is bounded. Do not replace expiry scanning unless measurement shows meaningful CPU cost.

### Generic rate limiter
Current buckets are bounded and cleanup is lazy. Do not add heap/timing-wheel complexity without evidence.

### a2 runtime
Measure create/action/transition, expiry, 1/64/512/4096 sessions, memory/session, reload/close settling.

### TaskEngine
Measure cold/warm admission, completion delivery, drain, resource contention.

### Combined workload

~~~text
normal userbot command
Assistant navigation
inline exact query
inline custom query
a2 callback burst
Settings mutation
downloader long op
plugin reload
shutdown
~~~

Observe:

~~~text
goroutines
heap/RSS when practical
TaskEngine active/pending
a2 sessions
Inline cache
RPC limiter buckets
resource usage
settling after workload
~~~

Create a benchmark/acceptance document. Never claim an optimization without before/after evidence.

---

# P3-B — event-driven TaskEngine completion drain if polling still exists

Historical audit found a short ticker/poll loop in completion drain/shutdown. Refresh current source first.

If still present, target:

~~~text
pending/active state transition
    -> drained signal/channel/condition
~~~

Constraints:

- no permanent worker;
- no deadlock;
- global shutdown deadline still wins;
- current completion ordering/ownership preserved;
- shutdown idempotent.

Acceptance:

- empty drain immediate;
- pending work blocks until settled;
- deadline/cancellation correct;
- no goroutine leak;
- no short polling ticker in drain path;
- resource counts return to zero.

Do not redesign TaskEngine.

---

# P3-C — optimize only hotspots proven by P3-A

Potential candidates only if measurement justifies them:

- Inline cache expiry indexing/min-heap;
- generic rate-limiter expiry indexing;
- presentation adapter allocations after P1-B;
- compatibility registration overhead after P2-A.

Forbidden optimization styles:

- unbounded global caches;
- sync.Pool only to win microbenchmarks;
- permanent cleanup goroutines;
- unsafe reuse across session lifetimes;
- lock-free complexity without measured contention.

If P3-A shows bounded scans are cheap, record that and deliberately skip P3-C changes.

---

# P4 — final refinement closure acceptance

After P1-B through P3-C, or after explicitly deciding measured P3-C work is unnecessary, run final closure.

Functional matrix:

| Area | Acceptance |
|---|---|
| Userbot | normal commands work without Assistant |
| Assistant | navigation/action/input works and reloads cleanly |
| Inline | exact/custom precedence unchanged |
| Settings | text-only zero-state; interactive native a2 |
| MyXL | native + Assistant a2; sensitive TTL/revalidation |
| Presentation | one Telegram keyboard serializer |
| Errors | no raw internal cause leakage |
| Edit UX | ambiguous edit never triggers duplicate reply |
| Plugins | disable/reload invalidates old tokens/work |
| Shutdown | sessions/tasks/resources settle under deadline |

Combined workload:

~~~text
regular command burst
Assistant root/help/settings navigation
callback burst
native Settings navigation/mutation
native MyXL navigation/confirmation
inline exact queries
custom matcher queries
downloader cancel/retry
plugin disable/reload while sessions exist
shutdown
~~~

Measure before, peak, and after settling:

~~~text
goroutines
a2 sessions
TaskEngine active/pending
resource budget usage
Inline cache size
limiter bucket count
completion state
heap/RSS when practical
~~~

Architectural closure target:

~~~text
interaction sessions -> a2
Telegram keyboard encoding -> one encoder
native callback lifecycle -> a2 after legacy removal
inline routing -> Inline vNext
Telegram retry -> RPCExecutor
execution/resources -> TaskEngine
~~~

Verification when a real checkout exists:

~~~text
gofmt cleanliness
go build -o bin/goultroid ./cmd/goultroid
targeted package tests
go test ./... when appropriate
go test -race for lifecycle/state-heavy packages when requested/feasible
P3-A benchmarks
~~~

Do not claim commands were run unless actually run.
Do not inspect CI unless the user explicitly asks.

At closure:

1. mark this handoff CLOSED;
2. update docs/design/goultroid-next-technical-plan-ai-handoff.md;
3. record final HEAD;
4. record benchmark/resource evidence;
5. record optimizations intentionally skipped because benchmarks did not justify them.

---

## 2. P1-A baseline inherited by the next session

HEAD 27b1f0fe establishes the canonical presentation vocabulary.

P1-A added presentation-side concepts including:

~~~text
ButtonRole
ButtonLabel
ActionButton
RoleActionButton
URLButton
RoleURLButton
SwitchInlineButton
RoleSwitchInlineButton
RowOf
~~~

Common roles cover:

~~~text
Action
Back
Home
Close
Confirm
Cancel
Edit
Open
Help
Previous
Next
Search
Refresh
Save
~~~

Warning/Information semantic rendering is also under internal/presentation.

Legacy internal/ui now adapts common button labels and alert formatting to presentation instead of maintaining separate canonical copies.

Important boundary:

P1-A unifies semantic UI vocabulary only. It does not move callback/session ownership into presentation.

---

## 3. High-risk regression checklist

Before closing any remaining phase, verify it did not introduce:

- second interaction runtime;
- second callback token protocol;
- second inline registry;
- second TaskEngine;
- plugin-local Telegram retry/FloodWait;
- direct Telegram RPC in completion callback;
- unbounded per-user/per-chat/per-query state;
- permanent feature worker/ticker;
- stale generation resurrection;
- wrong-actor execution;
- wrong-target execution;
- mutation based only on stale session state;
- automatic fallback after ambiguous send/edit;
- raw internal-error exposure;
- resource lease held while waiting for a user click;
- download/process lease held during Telegram upload;
- metrics labels containing dynamic query/user/error text.

---

## 4. Recommended next-session execution order

~~~text
1. Refresh test-next HEAD.
2. Compare against audited HEAD 27b1f0fe...
3. Read P1-A vocabulary + both current Telegram serializers.
4. Implement P1-B only.
5. gofmt, commit, push.
6. Refresh HEAD.
7. Implement P1-C native a2 adapter.
8. Implement P1-D Settings migration.
9. Implement P1-E MyXL residual migration.
10. Verify zero production legacy callback callers.
11. Implement P1-F removal.
12. Continue P2-A/B/C/D.
13. Run P3-A measurements.
14. Implement P3-B if polling still exists.
15. Implement P3-C only for measured hotspots.
16. Run P4 final closure acceptance.
~~~

Do not combine P1-C through P1-F into one giant commit. Keep lifecycle migrations phase-separated for review and rollback.

---

## 5. Files to read first

P1-B:

~~~text
internal/presentation/vocabulary.go
internal/presentation/view.go
internal/presentation/compiler.go
internal/presentation/telegram/bridge.go
internal/presentation/telegram/bridge_markup_test.go
internal/ui/button.go
internal/ui/render/telegram.go
internal/ui/render/telegram_test.go
~~~

P1-C:

~~~text
internal/interaction/runtime.go
internal/interaction/runtime_internal.go
internal/interaction/dispatcher.go
internal/interaction/orchestration/
internal/presentation/compiler.go
internal/presentation/telegram/bridge.go
Telegram callback ingress/dispatcher
internal/plugin/manager.go
~~~

P1-D:

~~~text
plugins/settings/settings.go
plugins/settings/assistant_optional_test.go
internal/settings/
internal/assistant/shell/settings.go
~~~

P1-E:

~~~text
plugins/myxl/myxl.go
plugins/myxl/assistant_interaction.go
plugins/myxl/*test.go
~~~

P1-F:

~~~text
internal/services/callback/
app/bootstrap/plugin wiring constructing callback Router/StateStore
~~~

P2-A:

~~~text
internal/plugin/manager.go
internal/plugin/features.go
hook registration/routing tests
~~~

P2-B/P2-C:

~~~text
internal/services/localization/
internal/core/context.go
internal/core/user_error.go
internal/presentation/semantic.go
plugins/
~~~

P3:

~~~text
internal/services/inline/
internal/services/ratelimit/
internal/interaction/
internal/taskengine/
~~~

---

## 6. Areas already strong — preserve them

Telegram hot path is already oriented around cheap ingress, interest/admission decisions, lazy media extraction, and bounded TaskEngine work. Do not regress to full feature fan-out or ordinary-message DB claims.

a2 already provides bounded sessions, expiry heap, zero-idle behavior, binding/revision fences, input sessions, and generation ownership. Do not replace it.

TaskEngine already owns bounded execution/resources, adaptive/lazy workers, durability, scope cancellation, completion delivery, and shutdown. Improve only measured/local issues.

Inline registry P0-D already separates exact indexing from custom matcher scans. Do not reintroduce a full exact+custom scan.

P0-B/P0-C already established safe typed errors and commit-aware EditOrReply. Rich UI migrations must continue using those contracts.

---

## 7. Definition of done

The refinement program is finished when:

1. common UI semantics come from presentation;
2. Telegram keyboard serialization has one implementation;
3. native rich interactions use a2 without Assistant dependency;
4. Settings no longer uses legacy callback state;
5. MyXL no longer uses legacy callback state;
6. legacy callback stack has zero production callers and is removed;
7. plugin hook registration is materially simpler;
8. common userbot UX uses the existing localization authority;
9. remaining plugins no longer casually expose raw internal causes;
10. dead compatibility UI helpers are removed;
11. performance/resource changes are benchmark-driven;
12. completion drain has no unnecessary polling if current source still has it;
13. combined workload settles resources cleanly;
14. no duplicate authority was introduced;
15. closure matrix and benchmark/resource evidence are documented.

At that point Goultroid returns to ordinary product development rather than architecture migration.

---

## One-line handoff

~~~text
Current HEAD 27b1f0fe closes P1-A canonical UI vocabulary.
NEXT = P1-B single Telegram keyboard serializer.
Then native a2 adapter -> Settings migration -> MyXL migration -> legacy callback removal -> plugin API/localization/error/UI cleanup -> benchmark-driven refinement -> final closure acceptance.
~~~
