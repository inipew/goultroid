# Goultroid Telegram Runtime Hardening — AI Implementation Plan

Status: **OPEN — T1-T10 CLOSED. T11 source cleanup is implemented through `13961fc07f87830a3949d5be120e679a458115ce`. The first T11 race run exposed an over-broad architecture fence rather than a demonstrated production race; that fence is corrected through `4613fa4b17dbb4c8328b24f941d075f3009b01a6`. Clean executable acceptance remains pending.**

Audit authority:

- Branch: `test-next`
- Source audit baseline HEAD: `5ab5a1196c0aaa1d1a57eb68dbd25898086b740c`
- Source audit baseline commit: `docs(maint): close boundary refactor milestones`
- Initial audit-plan commit: `d709ac47e3e8a414b4c71dd43c73aa3dded01516` — `docs(telegram): add audit handoff plan`
- Audit date: 28 September 2026
- Runtime-hardening reconciliation HEAD: `17e1af27b740ffc588e50c889e4c1884f805d8dc` — `fix(telegram): reserve callback claims before admission`
- Scope: Telegram userbot + Assistant bot ingress, callbacks/a2 interaction, dispatcher, presentation/UI transport, peer resolution/cache, RPC executor/limiter, media transport, capability adapters, command response semantics, lifecycle/resource ownership.
- Evidence class: source audit plus previously recorded targeted package-test evidence. No claim of current green full repository suite, current CPU/heap profile, production trace, or CI result.

This document is the implementation plan produced by the later deep Telegram architecture/performance audit. It is intentionally separate from `docs/design/goultroid-telegram-audit-ai-plan.md`, which preserves the earlier audit of command identity, callback claim ownership, Assistant resolver ordering, EventBus ownership, and its targeted-test baseline. Read both documents before implementation. This plan is comparable in execution rigor to `docs/design/goultroid-maintainability-boundaries-ai-plan.md`.

The target is **not** another Telegram redesign. The current architecture already has the correct major owners. The work below should close concrete correctness, bounded-state, callback latency, and physical-RPC coverage defects while preserving those owners.

### Reconciliation with the closed Telegram audit

At reconciliation HEAD `17e1af27b740ffc588e50c889e4c1884f805d8dc`, four findings in this later hardening plan are already closed by `docs/design/goultroid-telegram-audit-ai-plan.md` and must not be reimplemented:

- **P1-I / typed command identity** — CLOSED by the audit-plan lineage, including invalid-peer fail-closed behavior and bounded legacy command-key rollout compatibility.
- **P1-J / userbot callback durable admission** — CLOSED by the audit-plan lineage. The final design is `Begin -> Reserve -> native/TaskEngine admission`; there is no post-admission `AcceptClaim` write. Known pre-admission failures may release the generation-owned reservation, while uncertain reservation failure remains fail-closed.
- **P2-B / Assistant callback classification before peer resolution** — CLOSED at `f7b295bb4366330af0b2d6a692cf58c0edd8f251` with zero resolver calls for unknown/noop/duplicate callbacks and preserved resolution for fresh a2 message callbacks.
- **P2-D / EventBus callback answer ownership** — CLOSED at `ec41a80a329e1d5a56a3b8de78632c57a6094ad4`: callback EventBus publication is observation-only; native a2 or dispatcher fallback retains synchronous answer ownership.

The first truly open implementation phase after this reconciliation is **T1 / P1-A — single-attempt non-idempotent text send correctness**.

---

## 1. User rules and hard constraints

The next AI session must preserve these rules:

1. Refresh `test-next` HEAD before starting any phase and record the exact source HEAD being modified.
2. Run `gofmt` **before every commit that changes Go code**.
3. Do **not** inspect, poll, or wait for CI unless explicitly requested by the user.
4. Do not create a second TaskEngine, callback runtime/protocol, interaction session store, Telegram dispatcher, Telegram client, RPC executor, retry engine, downloader engine, registry, or background worker pool.
5. TaskEngine remains the execution authority for finite work.
6. The existing Telegram RPC executor remains the shared authority for limiter, retry classification, FloodWait handling, and RPC metrics.
7. a2 remains the canonical interactive callback protocol. Do not resurrect legacy callback namespaces to solve a2 problems.
8. Keep caches, queues, callback state, session state, timers, goroutines, and retained transport metadata **hard bounded**.
9. Prefer consumer-owned capability interfaces. Do not expand compatibility aggregates to make a local fix easier.
10. Do not move business authorization into transport code. Fresh authorization remains at the canonical command/action execution boundary.
11. Preserve topic/reply semantics, callback target binding, revision fencing, plugin generation fencing, TaskEngine ordering, shutdown/quiesce semantics, and existing user-visible behavior unless a phase explicitly changes it.
12. No speculative optimization. Correctness defects may be fixed directly; performance changes must have a focused benchmark, instrumented fake, or deterministic before/after evidence where practical.
13. Treat existing non-green repository tests as baseline debt until their relationship to the Telegram delta is demonstrated. Do not weaken assertions merely to obtain green output.
14. Documentation-only changes do not require `gofmt`; any later Go-changing commit does.

---

## 2. Current architecture map

### 2.1 Userbot message path

The current production message path is approximately:

```text
gotd update
  -> Dispatcher ingress admission
  -> bounded memory ingress dedupe
  -> command parse
  -> immutable message-route index lookup
  -> synchronous decision/security lane
  -> cheap invocation admission
  -> durable command claim only for recognized surviving commands
  -> lazy core.Message materialization
  -> peer/chat/sender resolution
  -> canonical core.Context + capability container
  -> TaskEngine interactive admission
  -> fresh command middleware authorization
  -> shared Telegram RPC executor
  -> Telegram MTProto
```

Important source areas:

- `internal/telegram/client.go`
- `internal/telegram/dispatcher_dispatch.go`
- `internal/telegram/dispatcher_handlers.go`
- `internal/telegram/dispatcher_ingress_dedupe.go`
- `internal/telegram/dispatcher_command_claim.go`
- `internal/core/executor.go`
- `internal/core/context_telegram.go`

### 2.2 Userbot callback path

The userbot callback path is approximately:

```text
Telegram callback update
  -> durable query-ID claim
  -> canonical callback event
  -> optional EventBus publication
  -> native a2 adapter
  -> target/session/revision/generation validation
  -> TaskEngine admission
  -> optional immediate ACK
  -> action execution
  -> completion/fallback ACK
```

Important source areas:

- `internal/telegram/dispatcher_callback.go`
- `internal/interaction/native/adapter.go`
- `internal/interaction/dispatcher.go`
- `internal/interaction/runtime.go`
- `internal/interaction/token.go`
- `internal/interaction/orchestration/engine.go`

### 2.3 Assistant callback path

The Assistant callback path is separately owned by:

- `internal/assistant/client/updates.go`
- `internal/assistant/client/interaction_ingress.go`
- `internal/assistant/interaction/message.go`
- `internal/presentation/telegram/bridge.go`

It uses the same canonical a2 session/action runtime and the same application-owned TaskEngine/RPC executor, but has its own Telegram account/update dispatcher and Assistant-specific ingress.

### 2.4 Presentation/UI path

The intended canonical UI path is:

```text
feature semantic state
  -> presentation.View
  -> presentation.Compiler
  -> a2 callback data
  -> presentation/telegram.Bridge
  -> Telegram capability transport
```

The legacy `internal/ui` layer remains a value/presentation helper and compatibility adapter. It must not become another callback/session runtime.

### 2.5 RPC/media path

Production userbot composition owns one `telegram.RPCExecutor` and shares it with:

- Telegram Service;
- Resolver;
- self-inline transport;
- Assistant through `assistantRPCExecutor`.

The userbot uploader already uses a managed physical-RPC client that places every upload part behind the shared executor. Assistant media currently does not provide equivalent per-part coverage; this is one of the concrete findings below.

---

## 3. Architecture strengths that must not regress

The audit found several parts that are already strong and should be treated as invariants rather than redesign targets.

### 3.1 Ordinary message hot path is appropriately cheap

`dispatcher_dispatch.go` performs:

- memory-only transport dedupe;
- prefix parsing and one router lookup;
- one atomic immutable-route-index lookup;
- only interested decision/event handlers;
- no durable SQLite claim for ordinary traffic;
- no heavy `core.Message` extraction until a command, album, or EventBus subscriber actually needs it.

Do not replace this with global plugin scans or durable ingress claims.

### 3.2 Message hook indexing is a good design

`internal/telegram/dispatcher_handlers.go` builds an immutable 256-class routing index at registration time and publishes it atomically.

Registration/removal is a cold path. Ingress performs one atomic load and fixed-array lookup. Preserve this approach.

### 3.3 Command durability is correctly placed after cheap admission

Recognized commands claim durable idempotency only after:

- decision/security handling;
- invocation admission.

The claim is accepted only around TaskEngine admission, and pre-admission failures can release the generation-aware ingress claim.

This prevents ordinary messages from paying database cost and prevents a failed admission from permanently consuming the command.

### 3.4 a2 session model is strong

The canonical interaction runtime already provides:

- compact Telegram callback tokens under the 64-byte limit;
- server-side feature identity;
- actor/target binding;
- session revision fencing;
- plugin scope/generation validation;
- bounded state;
- bounded TTL;
- TaskEngine execution ownership;
- stale-action fail-closed behavior;
- target claim semantics for inline messages.

Do not create a replacement callback state machine.

### 3.5 Interaction retained state is hard bounded

Default interaction limits include:

- 4096 sessions;
- 512 sessions per scope;
- 64 sessions per actor;
- 64 KiB state per session;
- 8 MiB total retained state;
- bounded TTL;
- heap-based expiry rather than permanent cleanup workers.

Preserve this model.

### 3.6 Shared RPC executor architecture is correct

The application composition gives the userbot Service, Resolver, and Assistant the same RPC executor authority.

The executor already handles:

- global/family/method/peer dimensions;
- hard limiter cardinality;
- penalty state;
- idempotent vs non-idempotent operations;
- transient classification;
- FloodWait;
- durable-yield behavior;
- bounded retry policy.

The fixes below must improve coverage/wiring, not introduce another executor.

### 3.7 Self-inline architecture is already appropriately stateless

`presentation/selfinline.RenderBridge`:

- resolves current transport lazily;
- resolves current Assistant identity lazily;
- keeps no retained session registry;
- distinguishes preflight/query/select/send failure stages;
- treats send-stage failure as potentially committed.

Do not recreate YT/self-inline transport state elsewhere.

---

## 4. Consolidated confirmed findings

The priorities below distinguish concrete source defects from extension-contract risks that still need a reproducer.

### P1-A — Non-idempotent text send can perform a second physical send after an ambiguous error

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — production fix `a59c075557ac24346c1837546a9945b429ad3aa0`; expanded regression `e91c466e7ef41cf36b386bc6284356f57a9398cd`.

Affected source:

- `internal/telegram/service.go`
- `SendMessage`
- `SendMessageWithMarkup`

Current behavior performs `StyledText(...)` first and, for every non-FloodWait error, performs a second `Text(...)` send.

The outer RPC executor correctly classifies `messages.sendMessage` as a non-idempotent mutation and avoids generic retry. However, the fallback occurs **inside** the operation closure, so an ambiguous first physical send can be followed by a second send.

Possible failure sequence:

```text
messages.sendMessage #1 reaches Telegram and commits
  -> response/connection fails
  -> StyledText returns error
  -> code assumes formatting failure
  -> Text performs messages.sendMessage #2
  -> duplicate user-visible message
```

The newer contextual send path already uses the safer model: parse/prepare locally, then issue exactly one physical send.

**Target:**

- determine the text/entities locally before transport;
- issue exactly one `messages.sendMessage` physical mutation per logical non-durable send;
- do not fallback to another send after an arbitrary RPC error;
- preserve HTML/plain compatibility for locally invalid formatting;
- add a regression where the first transport call returns an ambiguous/transient error after recording that it was invoked, and prove there is no second physical send.

**Gate:** one logical non-idempotent send produces at most one physical send attempt unless a durable caller explicitly owns a stable Telegram random ID and uses the idempotent lane.

---

### P1-B — Bot-origin tracking map is not hard bounded and becomes O(N) per insert after the threshold

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `77f1c5078ea95810567b71625c79e661aeba118f`.

Affected source:

- `internal/telegram/service.go`
- `recordBotSent`
- `IsBotSentForPeer`
- compatibility `IsBotSent`

Current behavior:

- stores every sent message for a five-minute window;
- when length exceeds 200, scans the entire map and removes only expired entries;
- does not evict fresh entries when cardinality remains above 200.

Therefore a burst of thousands of sends inside five minutes can retain thousands of entries. Worse, every insertion after the threshold may scan the entire retained map.

This turns a high-cardinality burst into roughly quadratic work over the burst.

The production peer-aware lookup itself is O(1); the retained-state maintenance is the defect.

**Target:**

- hard maximum cardinality;
- exact peer-aware lookup semantics;
- TTL retention;
- O(1) or amortized O(1) insert/eviction;
- no permanent cleanup goroutine;
- compatibility `IsBotSent` may remain slower if no production hot-path caller depends on it, but it must share the same bounded storage.

**Gate:** a burst far above the configured cap keeps retained entries at or below the cap and does not perform full-map work on every insertion.

---

### P1-C — Resolver PeerCache eviction order is not generation-aware

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — production fix `016f8912658cfed101d325b194ca423cf281d2d5`; reinsertion edge coverage `d844d67dc9d0c048a76d8b81b783aee12d9450f0`.

Affected source:

- `internal/telegram/resolver_cache.go`

`entries` is capped, but `order []peerCacheKey` retains stale keys after:

- expiry;
- `Invalidate`;
- `InvalidateID`.

If a key expires and is reinserted, the queue can contain both an old and new occurrence of the same key. `evictOneUnderLock` checks only whether the key currently exists, so an old queue occurrence can delete the newer live generation.

`compactOrderUnderLock` also preserves duplicate occurrences for any key that currently exists, so repeated expire/reinsert churn can grow the order slice independently of the bounded entries map.

**Target:**

Use one generation-safe ordering node per live cache entry. Acceptable implementations include:

- intrusive list node stored by entry;
- key + generation token;
- another exact one-live-node structure.

Do not add a cleanup worker.

**Gate:**

- repeated expire/reinsert cannot grow ordering metadata without bound;
- stale queue metadata cannot evict a newer generation;
- `len(entries) <= MaxEntries` and retained ordering metadata is O(MaxEntries).

---

### P1-D — Userbot callback acknowledgements do not use the dedicated callback limiter family

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `de00f725febffb2870ef95ceb6ff922eb53c4310`.

Affected source:

- `internal/telegram/service.go`
- `internal/telegram/rpc_limiter.go`
- compare `internal/assistant/client/managed_api.go`

The limiter explicitly defines a high-priority `"callback"` family so callback ACK RPCs do not queue behind ordinary `messages.*` edits/sends.

Assistant correctly routes `messages.setBotCallbackAnswer` through family `"callback"`.

Userbot `Service.AnswerCallbackQuery` calls the generic helper, which derives family `"messages"` from the method name.

Result: native/userbot callback ACKs compete with ordinary message family pressure despite the limiter design saying they should not.

**Target:**

- explicitly mark userbot callback answer RPC as family `"callback"`;
- retain global and method dimensions;
- do not exempt ACKs from the shared limiter/executor.

**Gate:** callback ACKs use the callback family on both Telegram surfaces, while global and method rate policy still applies.

---

### P1-E — Assistant upload coverage is outer-operation only, not per physical upload RPC

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `4f751ec42499ff46ce207cf481d14aac361592cf`.

Affected source:

- `internal/assistant/client/client.go`
- `internal/assistant/interaction/message.go`
- compare `internal/telegram/media_rpc.go`

Production composition correctly injects the application-owned RPC executor into Assistant.

However, Assistant creates its uploader against raw `tdClient.API()` and wraps the whole `uploader.FromPath()` call in one executor operation labelled `upload.saveFilePart`.

A large upload may issue many physical:

- `upload.saveFilePart`;
- `upload.saveBigFilePart`;

calls internally. Those individual physical calls do not each reserve limiter capacity or produce executor metrics.

The userbot path already has the correct design through a managed uploader client that wraps every physical upload part.

**Target:**

- reuse the same physical-RPC boundary concept for Assistant upload;
- every physical upload part must pass through the shared executor;
- do not create an Assistant-specific executor or retry engine;
- avoid double-wrapping the whole file transfer as though it were one physical RPC.

**Gate:** N uploaded parts produce N executor/limiter physical-operation observations, with the same shared executor instance used by the userbot surface.

---

### P1-F — Assistant callback ingress waits synchronously for TaskEngine completion

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `e4e9e193bad196d224a89fdc0dbec3335a9aee16`.

Affected source:

- `internal/assistant/client/interaction_ingress.go`
- compare `internal/interaction/native/adapter.go`

After TaskEngine submission, Assistant callback ingress waits on:

- a custom `doneCh`;
- ticket completion;
- original update context cancellation.

This keeps the Telegram update callback handler blocked for the action lifetime, up to the execution timeout.

The native adapter already demonstrates a better ownership model:

```text
prepare
  -> submit to TaskEngine
  -> immediate ACK when policy allows
  -> return ingress
  -> OnComplete performs bounded final/fallback ACK
```

TaskEngine should remain the action lifetime owner after successful admission.

**Target:**

- after successful TaskEngine admission, Assistant ingress should not synchronously wait for full action completion;
- use TaskEngine completion to perform final/fallback ACK;
- completion ACK must use a short detached lifecycle-safe context, not the already-cancelled update context;
- do not add another callback worker pool;
- preserve action ordering, execution timeout, resource requirements, and handler-owned ACK semantics.

**Gate:** update ingress returns after admission/immediate ACK while action completion remains observable and exactly-one ACK semantics are preserved.

---

### P1-G — Final Assistant fallback ACK can reuse an already-cancelled update context

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — folded into `e4e9e193bad196d224a89fdc0dbec3335a9aee16`.

Affected source:

- `internal/assistant/client/interaction_ingress.go`
- `interactionPresentationServicer.ensureAnswered`

When the original callback context is cancelled, ingress may cancel the ticket and then call `ensureAnswered(ctx, ...)` with that cancelled context.

This makes the fallback acknowledgement least reliable exactly when it is most needed.

**Target:**

Fold this into P1-F. Completion/fallback ACK should use a bounded detached context owned by the Assistant runtime/TaskEngine completion path.

**Gate:** cancellation of the original update context does not automatically prevent the final callback answer attempt.

---

### P1-H — Scoped decision-handler infrastructure failures ignore the registered fail-open/fail-closed policy

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `dbe9405f68f63b90ca8469a77a849d578ea86e50`.

Affected source:

- `internal/telegram/dispatcher_handlers.go`
- `internal/telegram/dispatcher_dispatch.go`

Handler execution errors/panics respect `HandlerFailurePolicy`.

However, for scoped decision handlers, the following infrastructure failures currently stop the message pipeline unconditionally:

- TaskEngine client unavailable;
- task submission rejected;
- ticket wait deadline exceeded.

This effectively treats those failures as handled/fail-closed even for handlers whose registered policy is fail-open.

The behavior is inconsistent with `failurePolicyForPriority`.

**Target:**

- infrastructure failure must resolve through the same explicit failure policy as handler failure;
- if moderation is intended to be fail-closed, encode that in its policy explicitly rather than as accidental scheduler behavior;
- preserve security ordering and the shared five-second total decision deadline.

**Gate:** each decision handler has one consistent failure policy regardless of whether failure occurs inside the handler or in its TaskEngine admission/wait path.

---

### P1-I — Command identity loses Telegram peer kind

Status: **CLOSED — superseded by the closed Telegram audit plan; do not reimplement**

Affected source:

- `internal/telegram/dispatcher_ingress_dedupe.go`
- `internal/telegram/dispatcher_dispatch.go`

Transport ingress dedupe uses typed peer identity, but downstream persistent command identity currently uses numeric `chatID + messageID` for:

- durable command claim;
- TaskEngine command ID;
- correlation ID.

Telegram user, basic-chat, and channel namespaces are represented by different peer kinds. Numeric overlap can therefore produce identity collisions across peer kinds.

This is a confirmed key-construction inconsistency; a real production collision has not been claimed.

**Target:**

- derive one canonical typed peer identity from `msg.PeerID`;
- use it consistently for command durable claim, TaskEngine ID, and correlation;
- inspect compatibility implications for already persisted untyped command claims before changing persistent key format.

**Gate:** same numeric peer/message identifiers in different peer kinds cannot suppress each other, while replay within the same typed peer remains deduplicated.

---

### P1-J — Userbot callback durable claim is consumed before successful action admission

Status: **CLOSED — superseded by the closed Telegram audit plan; final design reserves durably before admission and has no post-admission AcceptClaim write**

Affected source:

- `internal/telegram/dispatcher_callback.go`
- `internal/interaction/native/adapter.go`

Userbot callback query IDs are durably claimed before target construction/action preparation/TaskEngine admission.

A failure before accepted work can leave the query ID claimed for the TTL. The current ingress lacks an explicit provisional-claim -> accepted-work transition comparable to command admission.

Whether Telegram/gotd redelivers the same query under each failure mode still needs a focused reproducer; the source-level ownership mismatch is real.

**Target:**

- define provisional callback claim ownership;
- release pre-admission failures safely;
- once work is admitted, retain dedupe and at-most-once action execution;
- duplicate callbacks should still receive a defined acknowledgement response where transport is available.

**Gate:** pre-admission failure does not permanently consume retryability; accepted action executes at most once.

---

### P2-A — Presentation compiler reads session revision independently for each action button

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `67898bd1fadef81c13d2c11e9d4d8bb4ed1d3c87`, compatibility follow-up `628c4368d99f9de35f8124528c840f9d71c3cebf`.

Affected source:

- `internal/presentation/compiler.go`
- interaction callback-data generation

For every action button, the compiler independently calls into the interaction runtime for callback data.

Besides repeated locking/catalog/session lookup, a concurrent detached transition can advance the session revision between buttons and produce one keyboard containing tokens from multiple revisions.

Normal TaskEngine ordering makes this uncommon but does not make the compiler atomic.

**Target:**

- snapshot the authoritative session identity/scope/revision once per compiled view/row set;
- encode all action buttons from that same snapshot;
- URL and switch-inline buttons remain stateless;
- retain validation of action identifiers and current feature scope.

**Gate:** one compiled keyboard has exactly one session revision for all action buttons, and compile cost does not perform redundant current-session resolution per button.

---

### P2-B — Assistant message callback resolves peer before callback classification/dedupe

Status: **CLOSED — implemented at `f7b295bb4366330af0b2d6a692cf58c0edd8f251`**

Affected source:

- `internal/assistant/client/updates.go`

Assistant message-origin callback handling resolves the target peer before it determines whether:

- callback data belongs to a2;
- query ID is duplicate;
- callback is unknown/noop.

Unknown and duplicate callbacks can therefore perform unnecessary resolver work and can surface a peer-resolution error before their real callback policy runs.

**Target:**

- shutdown gate;
- cache entities if needed;
- classify callback namespace;
- dedupe;
- only then resolve the peer for a fresh callback path that actually needs a message target.

**Gate:** unknown/noop/duplicate callback tests prove zero peer resolver calls, while valid a2 message callbacks retain correct target binding.

---

### P2-C — Assistant callback flight gate can silently drop a second valid click

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `e4e9e193bad196d224a89fdc0dbec3335a9aee16`; bounded flight gate retained with explicit busy feedback.

Affected source:

- `internal/assistant/client/interaction_ingress.go`

The Assistant acquires a per-target/actor flight key before preparing a callback. When the key is already active, the new query is acknowledged and discarded.

TaskEngine ordering plus a2 revision fencing already provide sequencing/stale-action correctness. The flight gate therefore creates a UX behavior where a user can click action A and quickly click action B, see the second spinner clear, but have action B silently ignored.

This may be intentional debounce, but the current behavior is not user-visible as such.

**Target:**

Before changing behavior, decide from feature semantics whether the flight gate is:

- required duplicate suppression; or
- redundant with TaskEngine ordering/revision fencing.

If retained, give a deterministic user-visible busy response. If removed, prove TaskEngine ordering and revision fencing provide the intended correctness.

**Gate:** no distinct valid click is silently dropped without a defined UX response.

---

### P2-D — Callback EventBus answer ownership is not explicit

Status: **CLOSED — observation-only EventBus contract verified at `ec41a80a329e1d5a56a3b8de78632c57a6094ad4` and later acceptance**

Affected source:

- `internal/telegram/dispatcher_callback.go`
- callback EventBus publication contract

Userbot callback ingress may asynchronously publish the callback event and separately proceed with a2 dispatch/unknown fallback.

A subscriber that also attempts to answer a query can race the dispatcher-owned answer. No production subscriber was identified in the previous audit; this is an extension-contract risk, not a demonstrated production double-answer bug.

**Target:**

Choose one explicit contract:

1. callback EventBus events are observational only; or
2. a synchronous namespace/ownership claim occurs before dispatcher fallback.

Do not let asynchronous EventBus completion determine an immediate Telegram ACK.

**Gate:** exactly one answer owner is defined for a2, unknown, noop, and extension callback namespaces.

---

### P2-E — Compatibility aggregate paths remain after capability refactor

Affected source:

- `internal/core/context.go`
- `internal/core/context_telegram.go`
- dispatcher/service compatibility boundaries

Current production command wiring uses `TelegramCapabilities`, which is correct.

However, `Context.Svc` and broad aggregate compatibility paths still exist as fallback/test/external compatibility surfaces.

This is not currently a runtime defect and was intentionally accepted by the maintainability plan, but it is architectural debt that can regress if new production code starts depending on it again.

**Target:**

- do not remove compatibility paths merely for aesthetic purity;
- first inventory current callers on the refreshed HEAD;
- add no new production callers;
- only retire a compatibility aggregate after all remaining consumers and tests are intentionally migrated.

**Gate:** production handlers/features remain capability-sized; any retained broad aggregate is explicitly compatibility-only.

---

## 5. Resource and performance assessment

### 5.1 Components currently judged healthy

The audit found these structures appropriately bounded or ownership-safe:

- interaction runtime sessions/state;
- Assistant peer cache;
- Assistant per-user rate limiter;
- RPC limiter buckets and penalties;
- resolver network concurrency;
- TaskEngine action execution ownership;
- self-inline renderer state;
- immutable message hook route index.

Do not rewrite these components simply because adjacent code changes.

### 5.2 Known bounded-state exceptions

The concrete exceptions identified by source audit are:

1. bot-origin tracking map;
2. resolver cache ordering metadata.

These must be fixed before a final Telegram resource acceptance claim.

### 5.3 Callback latency-sensitive operations

Callback ACK is special because Telegram clients display a button spinner until the answer RPC completes.

Therefore:

- ACK must not queue behind ordinary message-family pressure;
- unnecessary resolver work should precede ACK only when required;
- update ingress should not remain the action lifetime owner;
- final/fallback ACK must have a usable context.

### 5.4 Physical RPC accounting

A logical operation that internally emits multiple MTProto calls must not be counted as one physical RPC for limiter purposes.

The canonical pattern already exists in userbot upload/download managed clients.

Any Assistant media fix should reuse that concept.

---

## 6. Target architecture after this plan

The target remains the same overall architecture, with corrected boundaries:

```text
Telegram update
  -> cheap ingress classification
  -> bounded dedupe / provisional claim when needed
  -> canonical typed identity
  -> canonical presentation / a2 session validation
  -> TaskEngine admission
  -> fresh authorization / generation validation
  -> shared RPC executor per physical RPC
  -> Telegram transport
  -> bounded completion / acknowledgement
```

Required invariants:

- exactly one execution authority: TaskEngine;
- exactly one RPC policy authority: shared Telegram RPC executor;
- exactly one interactive callback protocol: a2;
- exactly one interaction state authority: `internal/interaction.Runtime`;
- exactly one answer owner for a callback query at a time;
- no retry of an ambiguous non-idempotent send without a durable Telegram idempotency key;
- all retained state hard bounded;
- no permanent worker/ticker introduced for cache cleanup;
- userbot and Assistant may have separate Telegram clients/ingress because they are separate Telegram accounts/surfaces, but they share application execution/RPC policy where designed.

---

## 7. Implementation phases

### T0 — Refresh baseline, reconcile tests, and freeze regression contracts

Status: **VERIFIED — reconciled against `17e1af27b740ffc588e50c889e4c1884f805d8dc`; prior-audit overlap fenced from reimplementation**

Before coding:

1. Refresh `test-next` HEAD.
2. Re-read this plan and `goultroid-maintainability-boundaries-ai-plan.md`.
3. Inventory the exact current callers and tests for every affected function.
4. Re-run the previously recorded targeted Telegram/Assistant/interaction/presentation test command if local execution is available.
5. Record failures without attributing them to this plan until baseline comparison proves the relationship.
6. Add focused reproducer tests for defects where the current source alone is not sufficient to define behavior.

Required targeted baseline areas:

- non-idempotent send transport;
- bot-origin tracking burst/cardinality;
- resolver cache expire/reinsert;
- callback limiter family;
- Assistant callback completion/ACK;
- decision-handler infrastructure failure policy;
- compiler multi-button revision;
- typed peer command identity;
- callback pre-admission durable claim;
- Assistant callback resolver ordering.

**Gate:** every later phase has a focused regression demonstrating the old defect or the exact invariant being preserved.

---

### T1 — Fix non-idempotent message-send correctness

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING**

Scope:

- `Service.SendMessage`
- `Service.SendMessageWithMarkup`
- any equivalent helper that performs a second physical send after arbitrary RPC failure.

Implementation direction:

- move HTML parse/entity preparation before the network mutation;
- perform one physical `messages.sendMessage`;
- preserve markup and existing error mapping;
- do not alter durable Assistant PM-relay paths that intentionally reuse persisted random IDs.

Tests:

- valid rich text;
- invalid/malformed formatting local fallback;
- transport permanent error;
- transport transient/ambiguous error;
- assert exactly one physical send invocation.

**Implementation evidence:**

- `a59c075557ac24346c1837546a9945b429ad3aa0` moves HTML parsing/entity construction before transport and routes both `SendMessage` and `SendMessageWithMarkup` through one helper that emits one `MessagesSendMessageRequest` inside the existing shared non-idempotent RPC executor.
- The logical send gets one locally generated Telegram `RandomID`; an RPC error is returned directly instead of being converted into a second plain-text send.
- The regression uses a recording `tg.Invoker` to count physical `messages.sendMessage` calls and inspect the actual request. It covers rich HTML, markup preservation, and malformed HTML fallback before transport.
- `e91c466e7ef41cf36b386bc6284356f57a9398cd` extends the regression across a transient/ambiguous connection-reset error and a permanent `MESSAGE_TOO_LONG` error. Both paths require exactly one physical call; the transient case must retain `RPCFailure.Ambiguous=true`.
- Changed Go blocks were passed through `gofmt` before their commits and the committed diffs were inspected for unrelated changes.
- Fresh focused Go test execution is not claimed in this session: the shell environment has Go 1.23.2 while this repository declares Go 1.27.0, and the repository checkout cannot be materialized because shell network access is unavailable. CI was not inspected or used as a substitute.

**Gate status:** source defect is patched and regressions are committed, but P1-A remains pending executed acceptance until the focused tests run in a valid repository/toolchain environment.

---

### T2 — Hard-bound bot-origin and resolver cache metadata

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING**

#### T2-A — bot-origin tracking

Implement hard capacity + TTL with O(1)/amortized O(1) maintenance.

Tests:

- >10x capacity fresh burst;
- expiry;
- peer-aware identity;
- self identity;
- capacity after sustained churn.

#### T2-B — resolver PeerCache ordering

Replace duplicate-prone stale ordering metadata with one live generation/node per entry.

Tests:

- expire -> reinsert same key;
- invalidate -> reinsert same key;
- repeated negative cache churn;
- capacity eviction;
- newer generation survives stale metadata;
- ordering metadata remains O(MaxEntries).

**Implementation evidence:**

- **T2-A / P1-B:** `77f1c5078ea95810567b71625c79e661aeba118f` replaces timestamp-only bot-origin cleanup with a map plus ordered list. Retention is hard-capped at 200 entries, TTL remains five minutes, same-key refresh reuses one node, and expiry/capacity pruning removes only from the ordered head. No cleanup goroutine or ticker was introduced. Regressions cover >10x fresh-cardinality burst, exact peer behavior, Saved Messages behavior, same-key churn, TTL expiry, and cap/order cardinality.
- **T2-B / P1-C:** `016f8912658cfed101d325b194ca423cf281d2d5` replaces duplicate-prone ordering tombstones with exactly one live `container/list` node and index entry per cached peer. Expiry, `Invalidate`, and `InvalidateID` remove the matching ordering node synchronously; capacity eviction removes the actual oldest live entry.
- `d844d67dc9d0c048a76d8b81b783aee12d9450f0` adds explicit `InvalidateID -> reinsert` and negative-cache expire/reinsert coverage. The broader T2-B regression set also covers ordinary expiry/reinsert, direct invalidate/reinsert, capacity eviction, churn, and zero ordering metadata when no entries remain.
- Changed Go blocks were passed through `gofmt` before their commits and the committed diffs were inspected. No second cache framework, worker, timer, or cleanup goroutine was introduced.
- Fresh focused execution remains unclaimed for the same session-environment reason recorded under T1: repository checkout is unavailable and the shell toolchain is Go 1.23.2 while `go.mod` requires Go 1.27.0. CI was not inspected.

**Gate status:** source defects are patched and hard-bound/generation regressions are committed; P1-B/P1-C await executed focused acceptance in a valid checkout/toolchain.

---

### T3 — Normalize callback ACK RPC lane

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING**

Scope:

- userbot callback answer transport.

Implementation:

- use family `callback` explicitly for `messages.setBotCallbackAnswer`;
- retain global/method limiter dimensions and normal executor error classification.

Tests/benchmark:

- inspect captured RPC meta in a fake executor;
- optional deterministic limiter-pressure test proving callback family does not consume the `messages` family bucket.

**Implementation evidence:**

- `de00f725febffb2870ef95ceb6ff922eb53c4310` keeps `messages.setBotCallbackAnswer` inside the existing shared `RPCExecutor` but supplies `RPCMeta.Family="callback"` explicitly.
- The regression captures limiter dimensions from `Service.AnswerCallbackQuery` and requires global, callback-family, and exact method dimensions while rejecting the generic `messages` family for this RPC.
- No bypass, second limiter, or callback-specific executor was introduced; normal RPC error classification remains unchanged.
- Changed Go blocks were passed through `gofmt` and the diff was inspected. Fresh focused execution remains pending for the previously recorded checkout/toolchain limitation; CI was not inspected.

**Gate status:** source policy is corrected and regression is committed; P1-D awaits executed focused acceptance.

---

### T4 — Make userbot callback durable admission retry-safe

Status: **CLOSED — satisfied by the prior audit-plan callback reservation hardening at `17e1af27b740ffc588e50c889e4c1884f805d8dc`**

Scope:

- message and inline callbacks;
- durable callback query claim;
- native adapter admission boundary.

Required design:

```text
provisional durable query claim
  -> validate/prepare
  -> TaskEngine admission
      failure -> release provisional claim
      accepted -> claim becomes consumed
  -> exactly-once action
```

Do not release a claim after work has been accepted.

Tests:

- invalid/stale target before TaskEngine;
- action preparation failure;
- TaskEngine unavailable;
- TaskEngine admission rejection;
- accepted action;
- duplicate accepted callback;
- ACK failure must not cause duplicate action execution.

**Gate:** P1-J closed.

---

### T5 — Align Assistant callback lifecycle with native ownership

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING**

This phase combines P1-F, P1-G, P2-B, and P2-C where possible.

#### T5-A — classify/dedupe before peer resolution

Status: **CLOSED — implemented at `f7b295bb4366330af0b2d6a692cf58c0edd8f251`**

Message callback ingress now performs namespace classification and a2 query-ID dedupe before resolver work; only a fresh callback path that needs a target resolves the peer.

#### T5-B — TaskEngine owns action lifetime after admission

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `e4e9e193bad196d224a89fdc0dbec3335a9aee16`.

Assistant callback ingress now returns immediately after successful TaskEngine submission. The existing `WorkSpec.OnComplete` hook owns completion; no callback worker, goroutine pool, or second execution authority was added. The prepared action still executes inside TaskEngine with the existing scope, ordering key, resources, queue deadline, and execution timeout.

#### T5-C — bounded detached final ACK

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `e4e9e193bad196d224a89fdc0dbec3335a9aee16`.

Completion releases the bounded per-target flight and invokes final/fallback acknowledgement with a five-second `context.Background()` timeout. The original update context is therefore no longer the completion ACK lifetime owner.

#### T5-D — decide callback flight semantics

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `e4e9e193bad196d224a89fdc0dbec3335a9aee16`.

The bounded flight gate is retained because it prevents concurrent same-target action admission without creating retained work. A second click while the target/actor flight is active is no longer silently acknowledged: it receives the explicit user-safe message `⏳ Another action is still running. Please try again.`. Capacity exhaustion follows the same deterministic busy response.

Regression updates in `interaction_prepared_admission_test.go` now require:

- ingress returns after TaskEngine admission while the action is still blocked;
- rapid same-target callbacks are not admitted a second time and receive explicit busy feedback;
- completion drains after task release;
- cancelling the original update context after admission does not make the completion acknowledgement observe a cancelled context;
- the synchronous TaskEngine fake invokes `OnComplete` so existing prepared-action admission coverage follows production completion ownership.

Existing resolver-order regressions continue to cover T5-A. Existing integration/ACK-policy tests remain part of the required focused acceptance for message/inline and handler-owned acknowledgement semantics.

Fresh focused execution is not claimed from this session: shell network access cannot materialize the repository checkout, so the available local Go 1.23.2 environment cannot run this Go 1.27.0 module. The committed diff was inspected directly through the repository API. CI was not inspected.

**Gate status:** T5 source behavior and regressions are implemented; executed focused/race acceptance remains pending in a valid checkout/toolchain environment.

---

### T6 — Put every Assistant upload part behind the shared physical RPC executor

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `4f751ec42499ff46ce207cf481d14aac361592cf`.

Scope:

- Assistant media upload;
- Assistant inline/media delivery paths that share `ClientInteraction`;
- physical `upload.saveFilePart` and `upload.saveBigFilePart` operations emitted by gotd uploader.

Implementation evidence:

- Production Assistant startup now calls `SetManagedMediaSender(..., tdClient.API())` instead of constructing `uploader.NewUploader(tdClient.API())` directly.
- `managedUploadRPCClient` implements gotd's existing `uploader.Client` and delegates every physical small or big upload part through the already-injected `assistentrpc.Executor`, family `upload`, kind `IdempotentMutation`.
- The executor is resolved lazily from `ClientInteraction`, so a later `SetRPCExecutor` update is observed rather than capturing a stale compatibility executor.
- The previous outer `executeValue(... "upload.saveFilePart" ... uploader.FromPath)` wrapper was removed from both `SendMedia` and `SendMediaContext`. The whole transfer retains a 30-minute parent timeout, while the final `messages.sendMedia` remains separately classified as a non-idempotent physical mutation.
- The physical uploader boundary deliberately hides the executor's final underlying Telegram/network cause from gotd's uploader retry classification, matching the existing userbot media-boundary principle and preventing a second retry authority.
- Regression coverage in `internal/assistant/interaction/media_rpc_test.go` requires three small + two big physical parts to produce exactly five executor observations with the correct method/family/kind, and verifies an executor rejection cannot be reclassified by gotd through `errors.Is` while remaining recoverable at the Assistant outer boundary.
- No second RPC executor, retry engine, uploader registry, worker, or timer was introduced.

Fresh focused execution is not claimed in this session because the repository checkout/toolchain remains unavailable locally. The committed GitHub diff was inspected directly; CI was not inspected.

**Gate status:** P1-E source coverage is corrected and focused regressions are committed; executed focused/race acceptance remains pending in a valid Go 1.27 checkout/toolchain.

---

### T7 — Make decision-handler infrastructure failure obey explicit policy

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `dbe9405f68f63b90ca8469a77a849d578ea86e50`.

Implementation evidence:

- Scoped decision-handler TaskEngine absence, Submit rejection, and ticket Wait failure now derive their pipeline result from the exact `registered.failurePolicy` already used by handler error/panic execution.
- Fail-closed registrations terminate the decision pipeline as handled; fail-open registrations log the infrastructure failure and continue to the next registered decision handler.
- The existing five-second shared decision context, TaskEngine ownership, handler ordering, scope, quota owner, and ordering key remain unchanged.
- No implicit moderation override was introduced: current policy remains explicit through `failurePolicyForPriority` (security defaults fail-closed; later priorities fail-open).
- New matrix regression `dispatcher_decision_failure_policy_test.go` covers security fail-closed versus feature fail-open for missing TaskEngine, Submit rejection, Wait failure, handler error, and handler panic.
- Logging now records `handler_id` and the resolved `fail_closed` decision for infrastructure failures.

Fresh focused execution is not claimed in this session because the valid checkout/Go 1.27 toolchain is unavailable locally. The committed diff was inspected directly; CI was not inspected.

**Gate status:** P1-H source semantics and regression coverage are implemented; executed focused/race acceptance remains pending.

---

### T8 — Make presentation compilation revision-atomic

Status: **IMPLEMENTED / EXECUTED ACCEPTANCE PENDING** — `67898bd1fadef81c13d2c11e9d4d8bb4ed1d3c87`, compatibility follow-up `628c4368d99f9de35f8124528c840f9d71c3cebf`.

Implementation evidence:

- Interaction runtime now exposes narrow `CallbackDataBatch`, which loads authoritative session metadata once and emits all requested action tokens from the same feature/session/revision snapshot.
- Existing `CallbackData` delegates to the batch API, so presentation does not gain mutable runtime access.
- `presentation.Compiler.CompileRows` validates the semantic rows first, gathers action identities, requests one token batch, and then assembles the transport-neutral compiled rows.
- A concurrent state transition may stale the whole rendered keyboard, preserving existing stale-token semantics, but cannot produce a keyboard containing two revisions of one session.
- Stateless URL/switch-inline-only row compilation preserves prior behavior: no session lookup is introduced when there are zero action buttons.
- Deterministic regression `compiler_revision_test.go` advances session state while the first action validation is blocked. Both action tokens must retain the pre-update snapshot revision, while URL and switch-inline metadata remain unchanged.
- Existing token encoding remains the canonical encoder, so the existing <=64-byte and callback-byte ownership coverage remains applicable rather than duplicating token logic.

Fresh focused execution is not claimed in this session because the valid checkout/Go 1.27 toolchain is unavailable locally. The committed diffs were inspected directly; CI was not inspected.

**Gate status:** P2-A source race and stateless compatibility behavior are addressed; executed focused/race acceptance remains pending.

---

### T9 — Define callback extension ownership and compatibility exit conditions

Status: **CLOSED FOR SOURCE/OWNERSHIP AUDIT — T9-A closed by prior audit; T9-B retained compatibility surfaces based on current caller/fence evidence**

#### T9-A — EventBus callback contract

Status: **CLOSED — observation-only contract and one-answer regressions verified by the prior audit plan**

The selected contract is that callback EventBus publication is observation-only. Historical alternatives retained below for audit context were:

- observation only; or
- an explicit synchronous namespace claim.

Do not create another generic callback router.

#### T9-B — capability compatibility inventory

Status: **AUDITED / RETAIN COMPATIBILITY SURFACES** — no speculative deletion justified on current evidence.

Current caller/fence evidence on refreshed HEAD:

- `Context.Svc` remains a `CommandTelegramServicer` compatibility fallback inside `internal/core/context_telegram.go`; it is not the production command transport.
- `TestM2ProductionContextLiteralsDoNotBindSvc` walks production Go source and rejects any production `core.Context` literal that binds `Svc`.
- `TestM2ProductionHandlersDoNotReadContextSvc` walks production Go source and rejects direct `ctx.Svc` reads outside the core compatibility adapter.
- `TestM2CoreOnlyCompatibilityAdapterReadsContextSvc` fences core reads so the fallback remains centralized in `context_telegram.go`.
- `TestM2ProductionCommandPathUsesCapabilityBundle` requires command execution and dispatcher storage to use `TelegramCapabilities` rather than `CommandTelegramServicer`.
- `TestM2AppDoesNotUseLegacyTelegramAggregates` forbids application wiring from returning to `.client.Service()`, `.dispatcher.Service()`, or `.dispatcher.CommandService()`.
- Assistant command transport advertises only its supported narrow capabilities; the architecture fence rejects restoration of the former broad mock/mega-interface behavior.
- `internal/ui/render.ToTelegramMarkup` remains an explicit compatibility adapter, but delegates physical Telegram keyboard construction to canonical `presentation/telegram.EncodeMarkup`; the P1-B architecture fence prevents it from becoming a second Telegram keyboard serializer.

No compatibility surface is removed in T9-B because the current environment cannot produce an exhaustive fresh repository-wide symbol reference list: GitHub code-search returns no indexed results for this repository, while a local checkout is unavailable. Existing repository-wide AST/walk architecture tests provide concrete negative production-caller evidence for `Context.Svc` and app aggregate paths, but not sufficient evidence to delete the broad interface definitions or legacy renderer adapter themselves.

Therefore:

- keep `TelegramServicer`, `CommandTelegramServicer`, `Context.Svc`, dispatcher/service compatibility accessors, and legacy UI renderer only as fenced compatibility surfaces;
- add no new production caller;
- future deletion requires an executable repository-wide caller inventory plus migration of any remaining tests/external compatibility users.

**Gate:** callback answer ownership remains unambiguous from T9-A, production Context/app wiring remains capability-sized through existing architecture fences, and no compatibility deletion was performed without caller evidence.

---

### T10 — Performance/resource acceptance

Status: **CLOSED** — harness from `435c51081abf44c2ffa97234863493bfd56819ee` (`test(telegram): add T10 resource acceptance`), focused/race/resource evidence recorded below, full-race regressions remediated through the later cleanup lineage, and a final full repository race rerun was reported green on the current `test-next` lineage through `31445f849ab4a6ce617df70eda8a6e70d856f102`.

T10 remains an acceptance phase, not another architecture phase. The new harness composes the hardening-specific retained-state/performance checks with the already-existing P5/P8-H/P8-I lifecycle and resource acceptance instead of building a second runtime.

#### T10-A — hardening-specific bounded-state acceptance

`internal/telegram/runtime_hardening_t10_test.go` adds `TestT10TelegramBoundedStateHighCardinalityAcceptance`.

The deterministic workload now drives:

- bot-origin tracking through `100 * botSentCapacity` distinct sends and requires both the map and ordering list to remain exactly at the hard cap;
- resolver `PeerCache` through positive, negative, invalidate, and reinsert churn at `100 * MaxEntries`, requiring `entries <= MaxEntries` and exactly one ordering/index node per live entry;
- hierarchical RPC limiter bucket pressure to its configured hard cap and verifies one additional live identity fails closed rather than evicting rate state;
- FloodWait penalty pressure far beyond `MaxPenalties`, requiring retained explicit penalties to stay bounded while overflow remains represented by the existing conservative account-wide cooldown;
- process goroutine/heap samples before and after the deterministic retained-state workload for descriptive evidence. No new percentage/latency pass threshold is invented from those samples.

This complements, rather than replaces, the existing P8-I process-settling harness which already measures baseline/peak/settled goroutines, heap, RSS, TaskEngine workers/resources, interaction state, inline cache, and managed resource snapshots.

#### T10-B — hardening-specific benchmarks

The same commit adds:

- `BenchmarkT10BotOriginSteadyStateBurst` — measures steady-state high-cardinality bot-origin insertion once the hard cap is full and reports retained entries/order nodes;
- `BenchmarkT10PeerCacheBoundedChurn` — measures 256-entry and 4096-entry cache churn with invalidate/reinsert pressure while asserting ordering metadata remains exact;
- `BenchmarkT10AssistantCallbackIngressAdmission` — measures Assistant a2 callback prepare -> immediate ACK policy -> TaskEngine admission -> completion ownership with p50/p95/p99 ingress samples and exactly one TaskEngine admission per logical callback.

The Assistant benchmark deliberately uses a synthetic TaskEngine client whose completion callback fires immediately. Its numbers are **isolated ingress/admission measurements**, not Telegram network ACK latency and not production end-to-end action latency.

Existing benchmarks remain part of T10 evidence:

- `BenchmarkDispatcherCallbackIngressObservation`;
- `BenchmarkDispatcherDecisionIngressNoop`;
- `BenchmarkHierarchicalRPCLimiterCardinality`;
- `BenchmarkRPCExecutorSamePeerFloodWaitOccupancy`.

The previously recorded callback/decision benchmark numbers remain historical isolated measurements only; T10 must record fresh results from the closing HEAD rather than treating those values as pass thresholds.

#### T10-C — correctness/lifecycle composition matrix

T10 executable acceptance must include the existing focused regressions that prove the hard invariants behind the measurements:

- transport: `TestSendMessageSinglePhysicalAttemptOnTransportError`, `TestSendMessageMalformedHTMLFallsBackBeforeTransport`, `TestService_NonIdempotentMutationNoRetryOnTransient`;
- bot-origin: `TestService_BotSentTrackingHardBounded`, `TestService_BotSentTrackingRefreshDoesNotGrowOrder`, `TestService_BotSentTrackingTTL`;
- resolver cache: bounded eviction, expire/invalidate/reinsert generation safety, negative churn, exact ordering metadata, and lazy idle storage tests in `resolver_cache_test.go`;
- callback lane: `TestAnswerCallbackQueryUsesCallbackLimiterFamily`;
- userbot callback durable lifecycle/ownership: the callback claim and observation-only suites;
- Assistant callback lifecycle: `TestInteractionIngressCarriesPreparedActionAdmissionToTaskEngine` and `TestInteractionIngressSingleFlightCoalescesSameActorMessageBeforeTaskEngine`;
- physical Assistant upload accounting: `TestManagedUploadRPCClientAccountsEveryPhysicalPart` and retry-authority fence coverage;
- decision failure policy: both `dispatcher_decision_failure_policy_test.go` matrix tests;
- presentation revision atomicity: both `compiler_revision_test.go` tests;
- a2 navigation/input/callback burst/reload/shutdown: `TestP5FinalAssistantUXLifecycleAcceptance`;
- cross-surface generation unload/reload: `TestP8HCrossSurfaceUnloadReloadGenerationAcceptance`;
- combined idle/high-load/process settling: `TestP8ICombinedResourceIdleHighLoadAcceptance`;
- Telegram shutdown ingress/drain: `TestDispatcherDrainClosesIngressBeforeWaiting`.

The P5 acceptance already exercises rapid Assistant callback navigation, 512 callback queries with exactly-one acknowledgement, input arm/take/cancel, stale revision fencing, plugin disable while scoped TaskEngine work is active, generation reload, worker retirement, manager shutdown, TaskEngine stop, and zero remaining interaction sessions/inputs/state bytes.

#### T10-D — required local execution

On a valid checkout with the repository-required Go toolchain, execute at least:

```bash
gofmt -w \
  internal/telegram/runtime_hardening_t10_test.go \
  internal/assistant/client/interaction_latency_benchmark_test.go

git diff --check

go test ./internal/telegram ./internal/assistant/client ./internal/assistant/interaction ./internal/presentation ./internal/interaction -count=1 -timeout=120s

go test -race ./internal/telegram ./internal/assistant/client ./internal/assistant/interaction ./internal/presentation ./internal/interaction -count=1 -timeout=180s

go test ./internal/assistant/client -run '^TestP5FinalAssistantUXLifecycleAcceptance$' -count=1 -timeout=120s
go test ./internal/plugin -run '^TestP8HCrossSurfaceUnloadReloadGenerationAcceptance$' -count=1 -timeout=120s
go test ./internal/assistant -run '^TestP8ICombinedResourceIdleHighLoadAcceptance$' -count=1 -timeout=180s

go test ./internal/telegram -run '^TestT10TelegramBoundedStateHighCardinalityAcceptance$' -count=1 -v -timeout=120s
go test ./internal/telegram -run '^$' -bench '^BenchmarkT10(BotOriginSteadyStateBurst|PeerCacheBoundedChurn)$' -benchtime=200ms -count=1
go test ./internal/assistant/client -run '^$' -bench '^BenchmarkT10AssistantCallbackIngressAdmission$' -benchtime=200ms -count=1

go vet ./...
go build ./cmd/goultroid
```

Record the exact HEAD, toolchain, focused/race results, benchmark workload and hardware, and any unrelated full-suite failures before closing T10.

#### T10-E — local execution evidence (2026-09-28)

- Checkout before the fixes: `03a068ec27e1af7f022ef087bcbd0436795dd4c0` on `test-next`; toolchain: `go1.27.1-X:nodwarf5`; host: Linux amd64, AMD Ryzen 7 5700U. This evidence includes the source and test fixes in the following commit, so the checkout hash alone does not identify the tested tree.
- The five-package focused matrix and its race variant passed with `-count=1`. Standalone P5, P8-H, P8-I, and T10 bounded-state acceptance tests passed. `go vet ./...` and `go build ./cmd/goultroid` passed.
- T10 bounded-state workload settled at 200 bot-origin entries and ordering nodes, 256 peer-cache entries/order/index nodes, 128 limiter buckets, and 32 penalties. Goroutines stayed at 2; sampled heap changed from 1,029,056 to 836,096 bytes after settling.
- T10 benchmarks with `-benchtime=200ms -count=1`: bot-origin steady-state 425.7 ns/op with 200 resident entries/order nodes; peer-cache churn 481.3 ns/op at 256 entries and 562.0 ns/op at 4096 entries, with matching ordering nodes; Assistant callback ingress 3194 ns/op, p50/p95/p99 2204/4960/8015 ns and 1.000 TaskEngine admission/op. These are isolated local measurements, not Telegram network latency or a performance threshold.
- Existing comparison benchmarks also passed: dispatcher callback observation 1599 ns/op, decision no-op 3579 ns/op; limiter cardinality 587.0 ns/op at 1 bucket to 1207 ns/op at 4096 buckets; RPC executor same-peer flood-wait occupancy retained its expected interactive/durable physical-RPC and inline-wait counts.
- `go test -race ./... -count=1 -timeout=180s` failed outside the focused T10 packages. Failing packages included `internal/architecture`, `internal/assistant/command`, `internal/assistant/savedresponsecallback`, `internal/assistant/shell`, `internal/core`, `internal/services/localization`, and plugins `admin`, `afk`, `calculator`, `clone`, `downloader`, `help`, `media`, `myxl`, `pin`, `scheduler`, `settings`, and `sticker`. Several architecture tests referenced the absent `internal/services/callback` directory; other failures exposed either stale pre-a2/pre-localization expectations or concrete production/test-fixture defects. This run remains historical red evidence and is superseded only after a clean rerun.

#### T10-F — full-race failure remediation

The complete failure set reported from the local full-race run was audited rather than blanket-suppressed. Remediation is split between concrete production defects and stale acceptance assumptions:

- `cc2ecb069be94d728b040e0849ba17421f679db7` — `fix(runtime): close full-race production regressions`:
  - Assistant `ErrGroupOnly` preflight now emits the existing safe contextual group feedback before returning the typed error;
  - Addon grant and UserLog destination verification no longer interpolate internal errors into user-visible text; both preserve the internal cause through `Context.Fail`;
  - native Settings rows copy their button slice on insertion, removing backing-array aliasing that could mutate previously emitted action IDs.
- `221d05e3ad51508361bc9a8ca97467a0e397553f` and `46ad3ecb953d06adf522cadcc9eefc575617559a` refresh architecture fences to the intentional current contracts: public Help cutover, Settings native/Assistant dual path, 10-second lazy EventBus retirement, canonical a2 interaction runtime, canonical localization service, and expanded inline benchmark cardinalities.
- `1f05308d2917cde118d3eddcac12968fa8820858` aligns core/saved-response/shell acceptance with compact server-bound a2 identity and the canonical `internal/interaction` layer rather than the retired `internal/services/callback` package.
- `950b5a75dbf62be27a1800740e1663a17aa92294` updates shell navigation, localization mutation/cache invalidation, and Admin delayed-action test fixtures without removing their original lifecycle assertions.
- `4e7db89e09cb5edd9ba662c912de22ef2dbff46b` and `94dc74458439927fac33bf22ca43e868890efd3b` preserve the newer `Context.Fail` contract in Calculator/Clone/Media/Sticker tests: a presented failure remains non-nil for telemetry and must be recognized by `UserErrorWasPresented`; Downloader URL/retry fixtures and Help transport/localization fixtures are corrected without weakening side-effect assertions.
- `7e90be137982c74d7a454f418722c0916ff93e0b` refreshes exact presentation expectations for MyXL, Pin, Scheduler, and AFK to their current user-visible output.
- `cf109f3d527ed0ff3603e2820ecda8506c4273ac` performs final acceptance-fence/format cleanup after static review.

Two potentially ambiguous architecture failures were checked against repository history before accepting the new fence values: public Help is intentional from `01f9f670b9e21fe676936d5e2d11b943ac9dad0c` (`fix(assistant): preserve public help cutover policy`), and the 10-second EventBus worker idle is intentional from `02c57c659f2414e1b63acf7271285aba9195be29` (`perf(runtime): shorten lazy worker idle settling`).

No second runtime, executor, callback protocol, locale cache, or delayed-work engine was introduced by this remediation.

**Rerun gate:** T10 is still open until a clean checkout at or after this remediation lineage executes `go test -race ./... -count=1 -timeout=180s`. Any new failure must be classified from its exact test/error output rather than assumed to be baseline debt. Re-run the focused T10 matrix, `go vet ./...`, and `go build ./cmd/goultroid` if the full-race rerun exposes a production delta.

#### T10-G — second full-race rerun and seven-residual cleanup

The next local full-race rerun showed material progress: eleven previously failing packages passed, including all reported failures in `internal/architecture`, `internal/core`, `internal/assistant/command`, `internal/assistant/savedresponsecallback`, `internal/services/localization`, `plugins/admin`, `plugins/afk`, `plugins/calculator`, `plugins/clone`, `plugins/scheduler`, and `plugins/settings`.

Seven exact residual failures remained and were individually reconciled against the current source before patching:

- `plugins/sticker`: fix the test-local redeclaration introduced by the previous acceptance edit; reuse the existing `err` variable.
- `internal/assistant/shell`: expect the canonical localized locale-setting button title `Assistant Language`.
- `plugins/downloader`: make the direct-HTTP failed retry fixture use the only valid retained selection, `MediaModeDefault` + `MediaFormatDefault`.
- `plugins/help`: register the synthetic test commands on both Userbot and Assistant surfaces so the Assistant-source help filter sees the intended catalog.
- `plugins/media`: align the assertion with the current localized capitalization of `Unsupported target format`.
- `plugins/myxl`: assert the compact dashboard QRIS banner (`⏳ <b>QRIS:</b>`) rather than the detail-screen heading.
- `plugins/pin`: align the second permission-error assertion with the current capitalized `Bot/akun` text.

These seven residuals are fixed in:

- `617195727c0a2646e1a9da850ed781731105d4bf` — `test: close remaining full-race regressions`.

The batch changes test fixtures/expectations only; no production behavior, executor, callback runtime, task engine, cache, or transport path is changed. The commit diff was inspected after push and each edit maps to the reported failure.

**Closure evidence:** after the seven-residual cleanup, the full `go test -race ./... -count=1 -timeout=180s` rerun was reported green by the user. The branch subsequently includes `d997cb79283a269bd9e2239a7f48d51d2e4d4dfa` (`fix(sticker): present safety limit error on decoded image violations`) and `31445f849ab4a6ce617df70eda8a6e70d856f102` (`fix(lint): remove dead callback code and retain diagnostics`); the latter also removes confirmed dead callback/scope-resolver helpers without introducing a second runtime or authority. This session does not independently reproduce the local race output because its environment lacks the repository Go 1.27 checkout, so the full-race result is recorded as user-supplied executable evidence rather than re-executed evidence.

**T10 is CLOSED.** T11 may begin from `31445f849ab4a6ce617df70eda8a6e70d856f102` after refreshing HEAD.

---

### T11 — Final cleanup and closure

Status: **SOURCE IMPLEMENTED / FIRST ACCEPTANCE RED / FENCE REMEDIATED / RERUN PENDING** — source cleanup is at `13961fc07f87830a3949d5be120e679a458115ce`; T11 fence remediation is at `4613fa4b17dbb4c8328b24f941d075f3009b01a6`.

T11 audit/cleanup results:

- dead callback/scope-resolver helpers removed immediately before T11 by `31445f849ab4a6ce617df70eda8a6e70d856f102`; repository re-scan found no production references to `SetPluginScopeResolver`, `resolvePluginScope`, the retired `internal/services/callback` runtime, legacy `callback.EncodeCallbackData`, or post-admission `AcceptClaim`;
- retained compatibility surfaces were **not** deleted without caller evidence: `Context.Svc`, dispatcher/client aggregate accessors, `RetryRPC`, `DirectExecutor`, `NewService`, exported default-policy/cache values, and the legacy UI Telegram adapter remain compatibility-only and are already fenced away from production composition;
- the stale `SendMessage` comment that still described bounded FloodWait retry was corrected to the actual T1 invariant: non-idempotent text transport is single-attempt and is never replayed after a transport/RPC failure;
- raw Telegram/RPC re-scan confirms userbot feature/service/resolver calls remain inside `RPCExecutor` operations; Assistant production wiring installs `assistantRPCExecutor{executor: client.Executor()}`, constructs `managedAPI`, and then configures the interaction transport with the same executor;
- T11 found one concrete missed T6 boundary: `UploadInlineMedia` wrapped the whole `c.uploader.FromPath` transfer in a logical `upload.saveFilePart` executor call even though the managed uploader already sends every physical `saveFilePart/saveBigFilePart` through the shared executor. `13961fc07f87830a3949d5be120e679a458115ce` removes that duplicate whole-transfer executor boundary and reuses `uploadMediaFile`; final `messages.uploadMedia` remains separately executor-managed;
- `internal/architecture/telegram_runtime_t11_test.go` now fences production Assistant shared-executor wiring, managed physical media upload ownership, absence of whole-transfer `saveFilePart` wrapping, and non-reintroduction of retired callback/scope-resolver helpers;
- static post-commit inspection confirms three Assistant upload paths now call `c.uploadMediaFile(ctx, filePath)`, no `executeValue(... "upload.saveFilePart" ...)` whole-transfer wrapper remains, production Assistant wiring contains the shared executor adapter, and no `DirectExecutor` appears in production app wiring;
- no second TaskEngine, RPC executor authority, callback protocol/runtime, interaction runtime, locale cache, downloader engine, or retry authority was introduced.
- the first T11 race run was reported red. Audit found a concrete false-positive in the new architecture fence: it prohibited the generic substring `AcceptClaim(` across every production Go file, while `internal/idempotency/repository.go` legitimately exposes `AcceptClaim` as part of the reusable claim lifecycle API. T4 only forbids post-admission acceptance in the Telegram callback admission path.
- `16b28bba7ad52d3bd474ba09120da346bca0c015` first scopes `AcceptClaim` checking to Telegram callback files. `4613fa4b17dbb4c8328b24f941d075f3009b01a6` then removes the redundant whole-repository legacy-callback scan because P1-F4/P1-F5/producer architecture tests already own that invariant, leaving T11 responsible only for its newly introduced shared-RPC/media boundary and callback-admission fence.
- no evidence from the reported red run currently requires reverting the `UploadInlineMedia` production cleanup; the known deterministic T11 failure was the architecture false-positive above.

The T11 architecture test changes were run through local `gofmt` before commit. The two existing production files changed only by gofmt-neutral block/comment replacement; this environment still lacks a materialized repository checkout and the installed Go toolchain is 1.23.2 rather than the repository's Go 1.27, so no local compile/race result is claimed for `13961fc...`.

Before declaring T11 CLOSED, execute on a clean Go 1.27 checkout at or after `13961fc07f87830a3949d5be120e679a458115ce`:

```bash
gofmt -w \
  internal/assistant/interaction/message.go \
  internal/telegram/service.go \
  internal/architecture/telegram_runtime_t11_test.go

git diff --check

go test ./internal/assistant/interaction ./internal/telegram ./internal/architecture -count=1 -timeout=120s
go test -race ./internal/assistant/interaction ./internal/telegram ./internal/architecture -count=1 -timeout=180s
go test -race ./... -count=1 -timeout=180s

go vet ./...
go build ./cmd/goultroid
```

If that matrix is green, record the exact tested HEAD/toolchain and mark T11 plus this runtime-hardening plan CLOSED. Do not use final cleanup to start a new redesign.

---

## 8. Suggested commit sequence

Keep commits phase-scoped and reviewable. Suggested messages:

1. `docs(telegram): consolidate architecture and performance audit plan`
2. `fix(telegram): make text sends single-attempt`
3. `fix(telegram): bound bot origin tracking`
4. `fix(telegram): make resolver cache eviction generation safe`
5. `fix(telegram): isolate callback acknowledgement limiter lane`
6. `fix(telegram): make callback claims admission safe`
7. `refactor(assistant): detach callback completion from ingress`
8. `fix(assistant): manage physical upload RPC parts`
9. `fix(telegram): honor decision handler failure policy`
10. `fix(presentation): compile one interaction revision`
11. `fix(telegram): use typed command identity`
12. `refactor(telegram): define callback event ownership`
13. `test(telegram): add resource and lifecycle acceptance`
14. `docs(telegram): close runtime audit plan`

Exact grouping may change if two fixes share one minimal invariant. Do not combine unrelated P1 defects into a giant commit merely to reduce commit count.

For every Go-changing commit:

1. refresh/inspect the intended diff;
2. run `gofmt` on all changed Go files;
3. run focused tests;
4. inspect `git diff --check`;
5. commit;
6. push;
7. do **not** inspect CI unless the user asks.

---

## 9. Test and benchmark matrix

The next session should prefer focused tests before expensive broad runs.

### Transport correctness

- text HTML success;
- malformed HTML local fallback;
- ambiguous send error = one physical send;
- contextual reply/topic semantics unchanged;
- markup send unchanged.

### Callback correctness

- userbot message callback;
- userbot inline callback;
- Assistant message callback;
- Assistant inline callback;
- stale revision;
- stale feature generation;
- copied target mismatch;
- unknown callback;
- duplicate query;
- immediate ACK;
- handler-owned ACK;
- TaskEngine rejection;
- shutdown/quiesce.

### Cache/resource correctness

- bot-origin cap;
- bot-origin TTL;
- resolver cap;
- resolver same-key reinsertion;
- negative cache churn;
- Assistant peer cache cap remains unchanged;
- interaction state cap remains unchanged.

### RPC/media correctness

- callback family = callback;
- global/method limiter still active;
- upload part and big-file-part individually pass through shared executor;
- non-idempotent media final send remains one attempt unless durable random-ID semantics apply.

### Dispatcher policy

- fail-open handler + handler error;
- fail-open handler + TaskEngine infrastructure error;
- fail-closed handler + handler error;
- fail-closed handler + TaskEngine infrastructure error;
- shared decision deadline;
- event lane remains asynchronous/TaskEngine-owned.

### Presentation

- all action buttons use one revision;
- callback length <= Telegram 64-byte cap;
- URL/switch-inline unchanged;
- serializer remains single canonical Telegram keyboard encoder.

---

## 10. Non-goals

Do not use this plan to:

- redesign TaskEngine scheduling/fairness;
- replace the Telegram client library;
- replace a2 with a third callback protocol;
- merge userbot and Assistant Telegram clients into one account/session;
- create a second presentation runtime;
- create another cache framework for the whole repository;
- change downloader/provider architecture except where the Telegram media boundary itself requires the shared executor;
- rewrite every command UI;
- remove all compatibility APIs without caller evidence;
- optimize unrelated Jobs/Scheduler/DB code;
- fix unrelated baseline test failures unless they directly block a phase and are proven relevant.

---

## 11. Risks to watch

### Ambiguous send risk

Any fallback after a non-idempotent Telegram mutation must distinguish local preflight failure from remote ambiguous outcome. Never convert an arbitrary transport error into a second send.

### Callback duplicate risk

Releasing a callback claim too late can duplicate actions; never release after successful TaskEngine admission.

### ACK latency risk

ACK should be prioritized but still managed by the shared executor. Do not bypass global safety limits for latency.

### Cache generation risk

A capacity structure that keeps stale ordering metadata must not be considered bounded merely because its primary map has a cap.

### Assistant raw API risk

A raw Telegram client may be used as a low-level transport dependency only when every physical operation still crosses the intended managed boundary. Do not treat one wrapper around a multi-RPC library call as per-RPC management.

### Policy semantics risk

Fail-open/fail-closed is a business/security contract. It should not change depending on whether failure occurred in plugin code or scheduler infrastructure.

### Compiler concurrency risk

TaskEngine ordering reduces concurrent state changes but detached transitions exist. Presentation output must be internally consistent without relying on timing assumptions.

### Compatibility-removal risk

Broad compatibility types may still support tests or external callers. Fence new production usage first; delete only with explicit caller evidence.

---

## 12. Definition of done

This Telegram audit plan is closed only when all of the following are true:

- normal non-idempotent text send cannot issue a second physical send after an ambiguous first attempt;
- bot-origin tracking has a hard cardinality bound and bounded insertion cost;
- resolver cache ordering metadata is bounded and generation-safe;
- userbot and Assistant callback ACKs use the intended callback limiter family;
- userbot callback durable claim ownership is safe across pre-admission failure;
- Assistant callback ingress returns after TaskEngine admission rather than waiting for the action lifetime;
- final Assistant fallback ACK does not depend on an already-cancelled update context;
- Assistant multipart upload is managed per physical RPC by the same shared executor;
- decision-handler infrastructure failures obey explicit handler failure policy;
- command durable/task identity includes Telegram peer kind;
- one compiled presentation view cannot contain mixed callback revisions;
- unknown/noop/duplicate Assistant callbacks do not perform unnecessary peer resolution;
- callback extension answer ownership is explicit;
- no new production feature depends on a broad compatibility Telegram aggregate;
- a2 remains the only canonical interactive callback protocol;
- TaskEngine remains the single finite-work execution authority;
- RPC executor remains the single Telegram RPC policy authority;
- all changed retained state is hard bounded;
- targeted lifecycle/resource/performance acceptance is recorded with fresh evidence;
- every Go-changing commit was formatted with `gofmt`;
- CI was not inspected unless explicitly requested by the user.

---

## 13. Recommended next action

Run the full repository race suite again on a clean checkout containing the T10-F remediation lineage. If it is green, record the exact tested HEAD/toolchain and close T10; if it is red, audit only the newly reported exact failures. The focused T10 matrix, lifecycle tests, resource test, and benchmarks already have fresh local evidence. Start T11 only after T10 closure is justified.
