# Goultroid Telegram Architecture, Callback, UI/UX & Performance — AI Session Plan

Status: **OPEN — deep audit consolidated; implementation and acceptance pending**

Audit authority:

- Branch: `test-next`
- Source audit baseline HEAD: `5ab5a1196c0aaa1d1a57eb68dbd25898086b740c`
- Source audit baseline commit: `docs(maint): close boundary refactor milestones`
- Initial audit-plan commit: `d709ac47e3e8a414b4c71dd43c73aa3dded01516` — `docs(telegram): add audit handoff plan`
- Audit date: 28 September 2026
- Scope: Telegram userbot + Assistant bot ingress, callbacks/a2 interaction, dispatcher, presentation/UI transport, peer resolution/cache, RPC executor/limiter, media transport, capability adapters, command response semantics, lifecycle/resource ownership.
- Evidence class: source audit plus previously recorded targeted package-test evidence. No claim of current green full repository suite, current CPU/heap profile, production trace, or CI result.

This document is the execution handoff for the next AI session. It consolidates the Telegram audit into one implementation plan comparable in rigor to `docs/design/goultroid-maintainability-boundaries-ai-plan.md`.

The target is **not** another Telegram redesign. The current architecture already has the correct major owners. The work below should close concrete correctness, bounded-state, callback latency, and physical-RPC coverage defects while preserving those owners.

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

Status: **PENDING**

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

Status: **PENDING**

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

**Gate:** P1-A closed.

---

### T2 — Hard-bound bot-origin and resolver cache metadata

Status: **PENDING**

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

**Gate:** P1-B and P1-C closed with explicit hard-bound assertions.

---

### T3 — Normalize callback ACK RPC lane

Status: **PENDING**

Scope:

- userbot callback answer transport.

Implementation:

- use family `callback` explicitly for `messages.setBotCallbackAnswer`;
- retain global/method limiter dimensions and normal executor error classification.

Tests/benchmark:

- inspect captured RPC meta in a fake executor;
- optional deterministic limiter-pressure test proving callback family does not consume the `messages` family bucket.

**Gate:** P1-D closed.

---

### T4 — Make userbot callback durable admission retry-safe

Status: **PENDING**

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

Status: **PENDING**

This phase combines P1-F, P1-G, P2-B, and P2-C where possible.

#### T5-A — classify/dedupe before peer resolution

Reorder message callback ingress so resolver work is only done for a fresh callback path that needs a target.

#### T5-B — TaskEngine owns action lifetime after admission

Remove synchronous wait from the update ingress after successful submission.

Use TaskEngine completion callback/ticket completion ownership already available; do not add another goroutine pool.

#### T5-C — bounded detached final ACK

Final/fallback ACK should use a short runtime-owned context after the original update context is gone.

#### T5-D — decide callback flight semantics

Audit feature behavior and tests:

- if flight suppression is unnecessary, remove it and rely on TaskEngine ordering + revisions;
- if required, retain it but return explicit busy/stale feedback rather than silently dropping a different query.

Tests:

- immediate ACK action;
- handler-owned ACK action;
- completion error;
- original ctx cancellation;
- duplicate query;
- rapid distinct clicks;
- inline target;
- message target;
- shutdown/quiesce.

**Gate:** Assistant callback update handler does not own action duration; zero unnecessary resolver calls; final ACK remains attempted under bounded context; no silent valid-click loss.

---

### T6 — Put every Assistant upload part behind the shared physical RPC executor

Status: **PENDING**

Scope:

- Assistant media upload;
- Assistant inline media upload;
- any other Assistant `uploader.FromPath` path using raw `tg.Client`.

Implementation direction:

- introduce/reuse a consumer-appropriate managed uploader client whose physical methods call the same injected application RPC executor;
- do not create a second executor;
- do not wrap an entire multipart transfer as if it were one `upload.saveFilePart`;
- preserve media timeout and non-idempotent final `messages.sendMedia` semantics.

Tests:

- small file part path;
- big file part path;
- multiple parts counted individually;
- shared executor fake receives each physical operation;
- final send still has correct idempotency kind.

**Gate:** P1-E closed.

---

### T7 — Make decision-handler infrastructure failure obey explicit policy

Status: **PENDING**

Scope:

- scoped decision-handler TaskEngine client missing;
- submit rejection;
- ticket wait timeout.

Implementation:

- centralize the result through `registered.failurePolicy`;
- retain security ordering and one shared ingress deadline;
- if policy defaults need adjustment for moderation, change the policy definition explicitly and cover it in tests.

Tests should cover at minimum:

- security fail-closed handler;
- feature/other fail-open handler;
- handler returns error;
- handler panics;
- TaskEngine missing;
- submit rejected;
- wait timeout.

**Gate:** the same handler policy applies consistently to execution and infrastructure failures.

---

### T8 — Make presentation compilation revision-atomic

Status: **PENDING**

Scope:

- `presentation.Compiler`;
- interaction runtime API only as narrowly required.

Implementation direction:

- obtain one authoritative session metadata snapshot for the compile;
- encode all action buttons from that same revision;
- avoid exposing mutable runtime internals;
- retain action validation and current-scope checks.

Tests:

- multiple action buttons use the same revision;
- concurrent revision advancement cannot produce a mixed-revision keyboard;
- URL and switch-inline buttons remain unchanged;
- callback data remains <=64 bytes;
- callback byte ownership/copying remains correct.

**Gate:** P2-A closed.

---

### T9 — Define callback extension ownership and compatibility exit conditions

Status: **PENDING**

#### T9-A — EventBus callback contract

Document and test whether callback EventBus publication is:

- observation only; or
- an explicit synchronous namespace claim.

Do not create another generic callback router.

#### T9-B — capability compatibility inventory

Re-audit current production callers of:

- `Context.Svc`;
- broad `TelegramServicer`;
- broad dispatcher service aggregates;
- legacy UI Telegram renderer adapters.

Only remove compatibility code with caller evidence. Otherwise keep it fenced and documented.

**Gate:** no ambiguous callback answer owner and no new production dependency on broad compatibility transport interfaces.

---

### T10 — Performance/resource acceptance

Status: **PENDING**

After correctness phases are complete, run representative local acceptance. Do not use this phase to introduce new architecture.

Workload should include:

- ordinary non-command messages;
- command bursts;
- mixed decision/event handlers;
- callback burst on userbot;
- callback burst on Assistant;
- rapid callback navigation;
- a2 input session arm/take/cancel;
- inline message edit;
- self-inline query/send;
- large media upload with many physical parts;
- resolver high-cardinality/churn;
- bot-origin high-cardinality send burst;
- plugin reload during queued callback;
- Assistant quiesce/stop;
- Telegram shutdown/drain.

Measure/record where available:

- callback ACK latency;
- dispatcher ingress latency;
- resolver network call count;
- TaskEngine queue/wait/execution outcomes;
- RPC limiter buckets/penalties;
- physical upload RPC count;
- interaction session count/state bytes;
- resolver cache entries + ordering metadata;
- bot-origin tracking count;
- goroutine count before/peak/settled;
- heap/RSS before/peak/settled.

Do not invent target percentages without baseline measurements.

Hard acceptance invariants:

- no retained structure exceeds its explicit bound;
- no permanent per-callback/per-peer goroutine;
- idle resource count settles;
- callback completion drains on shutdown;
- one physical send for non-idempotent normal text send;
- every physical multipart upload RPC is executor-managed.

---

### T11 — Final cleanup and closure

Status: **PENDING**

Only after T1–T10 acceptance:

- remove dead helper/fallback paths made unreachable by the fixes;
- update comments that still describe obsolete retry/fallback behavior;
- update architecture fences where a stable invariant now exists;
- re-scan for raw Telegram bypasses in the affected paths;
- verify no duplicate executor/task/callback/cache authority was introduced;
- update this document with exact closing HEADs and test/benchmark evidence.

Do not use final cleanup to start a new redesign.

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

Start with **T0**, then implement **T1 single-attempt non-idempotent send correctness** before performance tuning.

The first implementation session should not attempt to fix the entire plan at once. T1 is the highest-value safety correction because it can create a duplicate user-visible side effect despite the outer executor correctly classifying the operation as non-idempotent.

After T1, T2 should close the two concrete bounded-state defects before callback/latency optimization proceeds.
