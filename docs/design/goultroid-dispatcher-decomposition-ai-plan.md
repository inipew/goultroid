# P1 — Dispatcher Decomposition
## AI implementation plan, from baseline audit through final acceptance

**Status:** READY FOR IMPLEMENTATION  
**Target branch:** `test-next`  
**Baseline HEAD reviewed:** `fe62714ebfd06b690ae8ea7b548d949b9cad469c`  
**Baseline source:** `internal/telegram/dispatcher.go`, `internal/telegram/dispatcher_dispatch.go`, `internal/telegram/dispatcher_handlers.go`, and `internal/telegram/dispatcher_test.go`  
**Scope:** Refactor the Telegram message-dispatch path without changing externally observable behavior, security policy, execution ownership, or runtime topology.

> This document is the implementation contract. An implementing AI must first refresh the branch and inspect the actual current code. The baseline SHA above is a review anchor, not permission to overwrite newer work. If the branch has advanced, rebase the plan onto the new HEAD and preserve all intervening changes. Do not assume the source still matches the baseline.

---

## 1. Executive objective

The current `(*Dispatcher).dispatch` function is an oversized orchestration function. At the reviewed baseline it spans roughly 292 lines inside `internal/telegram/dispatcher_dispatch.go`. It performs ingress admission, transport deduplication, sender validation, command parsing and lookup, origin classification, message-decision setup, peer-entity enqueueing, synchronous decision hooks, invocation authorization, durable command idempotency, event publication, message extraction, peer/chat/sender resolution, command-context construction, TaskEngine admission, and observer dispatch.

The problem is not that the Dispatcher has too many files or that every helper is inherently wrong. The problem is that one function owns too many *ordered policy decisions*. That makes it difficult to verify that a future change preserves security ordering, dedupe semantics, lifecycle ownership, and the difference between synchronous decisions and asynchronous observers.

The goal is to make the dispatch pipeline readable as a sequence of explicit stages, each with a narrow input/output contract and tests, while retaining one canonical Dispatcher and the existing execution infrastructure.

### Required end state

1. The top-level dispatch method is a short, linear orchestration of named stages.
2. Each stage has one coherent responsibility and an explicit contract.
3. Security-sensitive ordering is documented and mechanically tested.
4. The refactor introduces no new runtime, registry, worker pool, queue, retry loop, RPC executor, callback protocol, or durable store.
5. The current TaskEngine remains the execution/admission/fairness/resource authority.
6. Existing JobManager, EventBus, router, permissions, resolver, idempotency manager, album buffer, and hook registration remain authoritative for their current domains.
7. Message handling remains low-overhead for ordinary non-command traffic.
8. Existing public constructors, setters, hook registration APIs, command behavior, and Telegram update entry points remain compatible unless a concrete incompatibility is discovered and separately justified.
9. Tests prove behavior and ordering, not merely that helper methods return expected values.
10. The final result is smaller in cognitive complexity, not simply the same large function distributed across many tiny files.

---

## 2. Non-goals and prohibited changes

The following are explicit non-goals:

- Do not redesign or replace TaskEngine.
- Do not add a second dispatcher/runtime/engine or another execution registry.
- Do not add a new message queue, worker pool, goroutine-per-message mechanism, retry engine, or callback/session protocol.
- Do not move command execution out of TaskEngine or duplicate its admission/fairness/resource logic.
- Do not move Telegram FloodWait/retry policy into Dispatcher; managed Telegram RPC remains under the existing shared `telegram.RPCExecutor`.
- Do not move durable logical work into Dispatcher; Jobs Manager remains the durable work/attempt authority.
- Do not replace EventBus or create a second event delivery mechanism.
- Do not introduce an interface for every helper or concrete type. Add an abstraction only where a real seam is needed for testing or ownership.
- Do not make all message handling asynchronous. Security/moderation decisions that currently execute synchronously and fail closed must retain that contract.
- Do not make observers synchronous or wait for their completion.
- Do not perform SQLite/durable idempotency writes for ordinary text, unknown commands, or suppressed commands.
- Do not extract media eagerly for every incoming message.
- Do not add unbounded maps, caches, dedupe entries, per-peer goroutines, or retained message state.
- Do not change command syntax, aliases, permission semantics, owner/sudo policy, origin classification, message-hook routing, album semantics, topic/reply semantics, or output behavior as part of a structural refactor.
- Do not perform unrelated plugin, configuration, documentation, or package-wide cleanup.
- Do not optimize based only on synthetic microbenchmarks. Preserve the low-cost fast path and measure representative behavior if a performance concern is found.
- Do not inspect or poll CI. Run local tests/build/vet/race commands as specified below; CI inspection is outside this task unless the user explicitly asks.

A behavior change discovered to be necessary must be proposed as a separate, explicitly justified change with a regression test. Do not silently bundle it into decomposition.

---

## 3. Baseline responsibility map

### 3.1 Dispatcher composition root

`internal/telegram/dispatcher.go` owns the `Dispatcher` struct, construction, dependencies, lifecycle state, and public configuration methods. Its fields include router/permissions, service capabilities, executor, resolver, root context, EventBus, album buffer, localizer, native interaction dispatcher, inline engine, normalizer, idempotency manager, ingress dedupe, TaskEngine client, message-hook registrations/index, in-flight counters, and peer-entity persistence worker state.

Do not split the Dispatcher into multiple competing owners. Its composition root may remain a single struct. This task is about decomposition of the message pipeline, not a wholesale redesign of Dispatcher lifecycle or all its fields.

### 3.2 Hook registration and routing

`internal/telegram/dispatcher_handlers.go` owns hook registration, priority/failure policy, immutable route-index publication, and the distinction between decision and observer lanes.

Keep registration/index ownership here. The dispatch refactor may consume a selected handler snapshot, but must not create a second registration list or duplicate route-index logic.

### 3.3 Current message dispatch flow

At the baseline, `internal/telegram/dispatcher_dispatch.go` contains the top-level `dispatch` method and helpers for:
- decision handler execution,
- observer handler submission,
- message hook state-interest gates,
- chat construction,
- peer resolution,
- core message extraction,
- Telegram message identity,
- invocation sender identity,
- private sender consistency.

These helpers are already meaningful boundaries. Keep and improve them where appropriate rather than moving code solely to reduce line count.

### 3.4 Current ordered pipeline

The following order is a security and behavior contract. The implementation may introduce typed stage-result structs, but must not reorder these operations without an explicit proof and tests.

1. **Ingress admission:** acquire the current ingress/lifecycle admission lease; reject when not accepting.
2. **Transport dedupe:** begin bounded in-memory ingress dedupe and retain ownership of the exact claim generation.
3. **Sender consistency:** reject malformed/inconsistent incoming private sender identity before any command authority or PM automation can run.
4. **Parse and lookup:** parse message text, classify syntax errors safely, identify command candidate, and resolve command registration.
5. **Origin and decision context:** classify interactive versus automation origin using peer-aware origin tracking; attach one shared message decision to context.
6. **Peer entity enqueue:** enqueue received peer entities only through the existing bounded persistence path.
7. **Canonical envelope and hook snapshot:** normalize the Telegram message and select decision/observer handlers from the existing immutable index.
8. **Synchronous decision lane:** execute decision handlers under the shared five-second deadline and their existing failure policy. A handled/suppressed decision stops command processing.
9. **Invocation authorization:** for a registered command, require stable Telegram message identity and call the command's invocation policy before durable idempotency, peer resolution, or TaskEngine submission.
10. **Durable command idempotency:** only a recognized, authorized command may acquire the durable claim. Preserve legacy-key compatibility window and fail-closed behavior on storage errors.
11. **Event/message preparation:** detect actual EventBus subscribers; extract `core.Message` only when a command, album aggregation, or subscriber needs it; publish the canonical message event when applicable.
12. **Non-command/unknown-command observer path:** dispatch observers asynchronously and return. Unknown commands must not become executable candidates.
13. **Command execution preparation:** resolve peer/chat/sender, preserve self-peer behavior, resolve localizer, build `core.Context`, and derive stable task/correlation/owner IDs.
14. **TaskEngine admission:** submit through the existing interactive command admission method; commit/accept the durable claim through the existing admission callback.
15. **Post-admission observers:** dispatch event handlers using the established observer lane and return.

The exact current code is the source of truth for edge cases. The list above describes the intended policy and ordering, not permission to remove existing defensive branches.

---

## 4. Target decomposition

The preferred end state is a **pipeline of a small number of cohesive stage functions**. Avoid a helper for every assignment or branch.

Suggested conceptual boundaries (names may be adapted to existing naming conventions):

| Stage | Responsibility | Typical output |
|---|---|---|
| Ingress admission | lifecycle admission + bounded transport claim | admission lease / claim |
| Message classification | sender validation, parse, command lookup, origin and decision context | classified message |
| Hook decision | envelope, selected handler snapshot, synchronous decision execution | continue / stop |
| Command admission claim | invocation authorization + durable idempotency claim | command claim or non-command result |
| Event preparation | subscriber check, conditional core message extraction, album/event publication | prepared message |
| Command preparation | peer/chat/sender/localizer/context and stable IDs | prepared command |
| Execution submission | existing TaskEngine admission and claim acceptance | admitted / rejected |
| Observer dispatch | existing asynchronous hook submission | fire-and-forget admission |

These boundaries are conceptual, not a requirement to create eight new files. Prefer keeping the stages in the existing `internal/telegram/dispatcher_dispatch.go` initially. Split into additional files only when a stage has a distinct, stable domain and the resulting file organization is easier to navigate.

### 4.1 Typed internal data

Use small unexported structs only when they materially clarify stage contracts and reduce parameter sprawl. Examples:

- A classified message may carry the original Telegram message/entities, parsed command, command registration/existence, stable peer-aware message identity, chat ID, origin, and decision context.
- A command claim result may carry the durable claim plus explicit ownership/release semantics.
- A prepared command may carry the canonical `core.Context`, task owner, task ID, correlation ID, and the claim-accept callback.

Do not copy large Telegram entities or media payloads unnecessarily. Prefer pointers/references to the original update data where safe. Do not expose these structs outside package `telegram`.

### 4.2 Claim ownership must be explicit

Ingress and durable command claims are not interchangeable:

- The ingress dedupe claim protects transport-level duplicate update delivery. It is bounded and memory-only.
- The command idempotency claim protects recognized command side effects durably.
- The exact claim generation must be retained; release/accept operations must not accidentally release a newer claim.
- On a pre-admission failure where the current behavior releases a claim, preserve that release.
- On a duplicate or successfully admitted path, preserve current ownership/finalization behavior.
- Avoid generic cleanup helpers that release claims in cases where the current code intentionally retains them.
- If introducing a cleanup guard, its state transitions must be explicit and covered by tests for success, duplicate, parse rejection, authorization denial, storage error, peer resolution failure, TaskEngine rejection, and cancellation.

### 4.3 Preserve low-cost paths

- Ordinary non-command messages without relevant handlers/subscribers must not allocate a full `core.Message` or resolve a peer.
- Unknown commands must not trigger durable command idempotency writes or TaskEngine command admission.
- EventBus subscriber checks must remain cheap and precede expensive message extraction.
- Peer entity persistence must use the existing bounded enqueue mechanism.
- No database access should be introduced for routine transport dedupe.
- Avoid closures/allocations on paths where they can be skipped without changing semantics; measure before attempting clever optimizations.

---

## 5. Implementation phases and exit criteria

Each phase is a separate reviewable unit. Before every phase, refresh `test-next`, record the exact HEAD, and inspect the affected code. Never assume the previous phase's HEAD is still current. If another commit landed, integrate it before proceeding.

### Phase 0 — Rebaseline and behavioral inventory

**Work**
1. Fetch latest `test-next` HEAD and record the full SHA in the implementation notes/commit description.
2. Read current `dispatcher.go`, `dispatcher_dispatch.go`, `dispatcher_handlers.go`, all relevant dispatcher tests, hook routing tests, origin tests, idempotency tests, TaskEngine admission tests, and lifecycle/shutdown tests.
3. Locate every caller of `dispatch`, `executeDecisionHandlersEnvelope`, `dispatchEventHandlersEnvelope`, `resolveDispatchPeer`, and `submitInteractiveCommandWithAdmission`.
4. Write down the actual current branch behavior for all early returns and claim ownership.
5. Establish local baseline tests and record commands/results. Do not inspect CI.

**Exit criteria**
- A concise behavior matrix exists before code movement.
- No behavior is inferred from the baseline document where current code differs.
- Baseline tests pass or pre-existing failures are recorded and isolated.

### Phase 1 — Lock down contracts with tests

Add or strengthen tests before moving code. Tests should exercise the public update ingress path where practical, not only unexported helper functions.

Required test matrix:

| Area | Required cases |
|---|---|
| Ingress lifecycle | rejected when stopping/not accepting; admitted lease released on every return |
| Transport dedupe | duplicate update dropped; bounded dedupe behavior; failed pre-admission command can be retried when current semantics require release; stale claim generation cannot release a newer claim |
| Sender consistency | valid private sender; omitted private FromID fallback; mismatched user; non-user FromID; outgoing private message |
| Parsing | valid command; plain text; unknown command; unclosed quote; trailing escape; parser error does not bypass observers/security hooks |
| Origin | interactive incoming message; outgoing automation only when peer-aware tracker confirms; same message ID in another peer does not collide |
| Decision lane | security priority ordering; fail-closed on timeout/unavailable TaskEngine/admission failure; fail-open behavior for non-security handlers; handled/suppressed decision prevents command |
| Invocation | permission/invocation denial occurs before durable claim and peer resolution; missing stable identity fails closed |
| Idempotency | only recognized authorized command claims; duplicate durable claim dropped; storage error fails closed; legacy-key compatibility window preserved |
| Event preparation | event only when subscriber exists; core message not extracted on plain unobserved traffic; album path still extracts/aggregates; subscriber gets canonical event |
| Peer/context | private/group/supergroup/channel; access hash from entities; resolver fallback; outgoing self peer; sender identity; localizer binding; reply/topic metadata |
| TaskEngine | stable task/correlation/owner IDs; admission callback accepts claim; rejected admission releases claims according to existing contract; cancellation/lifecycle ownership unchanged |
| Observers | asynchronous submission; correct priority/pool/owner/order key; observer failure does not block command; no duplicate dispatch |
| Shutdown | in-flight decisions/commands/observers respect existing lifecycle wait/cancel semantics |

Tests must not depend on arbitrary sleeps when a channel, wait group, ticket, or deterministic fake can be used. Do not weaken assertions to make a refactor pass.

**Exit criteria**
- Tests capture the externally important order and failure policy.
- The test suite fails if durable idempotency is moved before invocation authorization.
- The test suite fails if a security decision is made fail-open.
- The test suite fails if ordinary unobserved text begins expensive extraction or durable work.

### Phase 2 — Extract classification and decision boundary

**Work**
1. Extract sender validation, parse/lookup, origin classification, decision context, and canonical envelope creation into one cohesive classification stage or a small set of cohesive helpers.
2. Keep peer entity enqueueing at its current relative point unless tests and code inspection prove it can safely move.
3. Keep decision handler selection sourced from the existing immutable message handler index.
4. Preserve the single shared five-second decision deadline; do not give each handler a fresh timeout.
5. Preserve handler ordering, state-gate behavior, panic policy, TaskEngine scopes, ordering keys, queue deadline, execution timeout, and fail-open/fail-closed behavior.
6. Preserve the distinction between unscoped compatibility handlers and scoped handlers.

**Exit criteria**
- Top-level flow reads as classification → decision.
- All Phase 1 tests pass.
- No new goroutine, queue, registry, or runtime appears.
- No change to hook registration public API.

### Phase 3 — Extract command candidate authorization and durable claim

**Work**
1. Isolate command invocation eligibility and stable identity validation.
2. Isolate legacy identity compatibility lookup and durable idempotency claim acquisition.
3. Make the order explicit in code: invocation policy first, durable idempotency second.
4. Keep storage failures fail-closed and retain privacy-safe logging (do not log message body, raw arguments, or sensitive private content).
5. Preserve claim ownership and release/finalization behavior on every branch.

**Exit criteria**
- The code makes it visually impossible to mistake authorization for idempotency.
- Tests prove denied commands never write a durable claim.
- Duplicate command and legacy-key behavior remain unchanged.
- No database write occurs for plain text, unknown commands, or suppressed messages.

### Phase 4 — Extract event preparation and conditional message materialization

**Work**
1. Isolate EventBus subscriber detection.
2. Preserve conditional `core.Message` extraction.
3. Preserve album aggregation and canonical message-created event behavior.
4. Keep non-command and unknown-command observer dispatch asynchronous.
5. Ensure no event or observer is published twice during the transition.

**Exit criteria**
- Ordinary traffic remains on the lightweight envelope path.
- Album and subscriber paths still materialize the required message exactly when needed.
- Event ordering relative to decision and command admission is unchanged.
- Tests cover subscriber/no-subscriber and album/no-album paths.

### Phase 5 — Extract command context preparation

**Work**
1. Isolate peer and chat resolution without changing resolution order or fallback rules.
2. Preserve outgoing `InputPeerSelf` behavior.
3. Preserve unresolved-peer fail-closed command skip and command claim release.
4. Preserve sender construction and entity metadata.
5. Preserve root context ownership. Do not introduce an independent background context except the existing documented defensive fallback; do not hide a missing root lifecycle wiring problem.
6. Preserve localizer resolver invocation and arguments.
7. Preserve reply/topic/media/album metadata and the same Telegram capabilities/resolver/EventBus references in `core.Context`.
8. Derive task owner, task ID, and correlation ID exactly as before.

**Exit criteria**
- A named command-preparation stage produces the same canonical `core.Context` and IDs.
- Tests cover private, group, supergroup, channel, outgoing self, resolver fallback, sender, localizer, reply/topic and album context.

### Phase 6 — Extract execution submission and make top-level orchestration linear

**Work**
1. Extract the existing `submitInteractiveCommandWithAdmission` call behind a narrow internal stage.
2. Preserve the existing admission callback that accepts the durable command claim.
3. Preserve TaskEngine as the only execution admission and resource/fairness authority.
4. Preserve cancellation context and cancel function ownership; do not leak a context or cancel too early.
5. Preserve rejection behavior and claim release.
6. Keep post-admission observer submission at the same logical point.
7. Reduce `dispatch` to a readable sequence of stage calls with explicit early-stop results.

**Target shape (illustrative, not literal API requirement):**

```go
func (d *Dispatcher) dispatch(ctx context.Context, entities tg.Entities, msg *tg.Message) error {
    admission, ok := d.admitMessage(ctx, msg)
    if !ok {
        return nil
    }
    defer admission.Release()

    classified, ok := d.classifyMessage(ctx, entities, msg, admission)
    if !ok {
        return nil
    }

    if d.runMessageDecision(ctx, entities, msg, classified) {
        return nil
    }

    candidate, ok := d.authorizeAndClaimCommand(ctx, msg, classified)
    if !ok {
        return nil
    }

    prepared, ok := d.prepareMessageEffects(ctx, entities, msg, classified, candidate)
    if !ok {
        return nil
    }

    if !candidate.IsExecutable() {
        d.dispatchObservers(ctx, prepared)
        return nil
    }

    command, ok := d.prepareCommandContext(ctx, entities, msg, prepared, candidate)
    if !ok {
        return nil
    }

    if err := d.submitCommand(ctx, command); err != nil {
        return nil // preserve current logged rejection contract
    }
    d.dispatchObservers(ctx, prepared)
    return nil
}
```

This pseudocode is a readability target only. Do not force this exact number of structs/functions if the real code is clearer with fewer boundaries. In particular, the actual pipeline may publish the canonical message event before determining whether the message is a command; preserve the established order.

**Exit criteria**
- Top-level method is a linear coordinator, ideally around 40–80 lines, but correctness and clarity matter more than a hard line limit.
- Each helper is cohesive and not merely a wrapper around one statement.
- No helper has a broad generic name such as `processMessage` that hides multiple policies.
- No helper duplicates or bypasses TaskEngine admission.

### Phase 7 — Consolidate tests, remove transitional duplication, and review complexity

**Work**
1. Move tests only when it improves discoverability; do not split tests mechanically.
2. Remove obsolete duplicate code and transitional compatibility helpers only if no caller remains and API compatibility is proven.
3. Review every new helper for cohesive responsibility, unnecessary parameters, hidden mutable state, and avoidable allocations.
4. Review logs for privacy and useful error classification.
5. Review comments for current truth; update stale comments and package docs.
6. Confirm no new dependencies or exported APIs were added without justification.

**Exit criteria**
- No duplicate path remains.
- New code is easier to follow than the baseline.
- No unexplained increase in allocations, goroutines, retained state, or TaskEngine submissions.
- Diff is scoped to Dispatcher decomposition and its tests/docs.

### Phase 8 — Final verification and completion record

Run locally (not CI):

```sh
gofmt -w <all changed Go files>
gofmt -l <all changed Go files>
git diff --check
go test ./internal/telegram/... -count=1
go test ./internal/core/... -count=1
go test ./internal/taskengine/... -count=1
go test ./... -count=1
go test -race ./... -count=1 -timeout=180s
go vet ./...
go build ./cmd/goultroid
```

If the repository's current Go version or test layout requires adjusted package commands, inspect `go.mod` and use the valid equivalent. Do not run CI inspection. If a command fails, diagnose it; do not claim completion or hide failures. Record the exact command, result, and any known environmental limitation.

Where performance-sensitive paths changed, compare before/after using the same representative benchmark or focused allocation test. Do not invent a performance target. At minimum confirm no regression in ordinary non-command ingress, command admission, and hook fan-out that is material to the project's low-resource objective.

**Final completion record must include**
- Starting HEAD and final HEAD.
- Commit(s) and concise summary.
- Files changed.
- Final top-level pipeline in order.
- Behavior matrix and tests added/updated.
- Exact local verification commands/results.
- Whether race, vet, and build passed.
- Any deferred issue, with reason and evidence.
- Explicit confirmation that no second runtime/queue/retry engine/RPC executor was introduced.
- Explicit confirmation that TaskEngine remains the sole execution admission/fairness/resource authority.

---

## 6. Behavioral invariants — must remain true

### Security and authority
- Incoming private sender consistency is validated before privileged command or PM automation handling.
- Invocation policy is evaluated before durable idempotency and before peer resolution/TaskEngine submission.
- A command without stable peer-aware Telegram identity fails closed.
- Decision handlers run in priority order under one shared deadline.
- Security-priority decision failures remain fail-closed.
- Non-security observer/feature failures retain their existing fail-open/isolated behavior.
- A handled or command-suppressed decision prevents command execution.
- Automation origin is only assigned using peer-aware origin evidence; never fall back to message-ID-only matching across peers.

### Idempotency and side effects
- Transport dedupe stays bounded and memory-only.
- Durable idempotency applies only to recognized, authorized command candidates.
- Storage errors fail closed.
- Legacy identity compatibility remains until its configured expiry.
- Claims are accepted/released exactly according to current admission outcome.
- No accidental double command or duplicate observer execution is introduced.

### Execution and lifecycle
- TaskEngine remains the only execution admission/fairness/resource authority.
- Decision tasks use their existing interactive pool/priority/scope/order semantics.
- Observer tasks use their existing general pool and priority/owner/order semantics.
- Command tasks use the existing interactive admission path.
- Root context, cancellation, in-flight accounting, and shutdown waiting remain correct.
- No new long-lived goroutine or independently managed lifecycle is introduced.

### Message fidelity
- Private/group/supergroup/channel classification remains correct.
- Entity access hashes and resolver fallbacks remain correct.
- Outgoing self messages resolve to `InputPeerSelf`.
- Sender identity and user metadata remain correct.
- Reply ID, topic ID, media, grouped ID, album, and message entities remain preserved.
- Canonical MessageCreated event behavior remains unchanged.
- Localizer resolution receives the same sender/chat identity.
- Topic/reply-aware output behavior downstream is not modified by this task.

### Performance and resource bounds
- No full `core.Message` extraction for ordinary unobserved plain text.
- No durable database operation for plain text or unknown commands.
- No unbounded state, queue, map, or cache.
- No goroutine per message/handler.
- No duplicate handler index or duplicate event fan-out.
- Avoid unnecessary allocations in the hot path; verify rather than speculate.

---

## 7. Suggested test organization

Use existing package test files first. Add files only where they improve clarity, for example:

- `dispatcher_test.go`: public ingress behavior and broad integration scenarios.
- `dispatcher_pipeline_test.go`: ordering, early returns, claim lifecycle, and stage contracts.
- `dispatcher_handlers_test.go`: decision/observer lane policy and TaskEngine behavior.
- Existing origin, dedupe, idempotency, peer-resolution, and lifecycle tests should remain with their owning domain.

Do not create a new test file for every extracted helper. Prefer table-driven tests for policy matrices and deterministic synchronization for asynchronous behavior.

Test helpers/fakes should be narrow and local to tests. Do not add production interfaces solely to satisfy a test if a concrete dependency can be exercised safely.

---

## 8. Logging and privacy requirements

- Never log raw message bodies, command arguments, private chat contents, access hashes, or full Telegram update payloads.
- Preserve structured error-type/reason logging where it is useful.
- Keep security decisions observable without exposing sensitive content.
- Avoid logging the same failure at every stage; choose one owner for each failure log.
- Do not turn expected duplicate/rejected ingress into noisy error logs.
- Preserve correlation identifiers where safe and already available.

---

## 9. Documentation updates

After implementation:
1. Update comments in `dispatcher_dispatch.go` so they describe the actual stage contracts.
2. Update any Dispatcher architecture documentation or diagrams that explicitly describe the old monolithic dispatch path.
3. Do not rewrite unrelated architecture documentation.
4. Add a short implementation note to this plan or a linked completion report containing the final commit and verification evidence.
5. If implementation deviates from this plan, document each deviation and why it is safer/correcter. An undocumented deviation is not accepted.

---

## 10. Definition of Done

P1 is complete only when all conditions below are met:

- [x] Current `test-next` HEAD was refreshed and recorded before implementation.
- [x] Baseline behavior and claim ownership were audited from current source.
- [x] Security/ordering regression tests were added before or alongside extraction.
- [x] The top-level dispatch method is a linear coordinator of named stages.
- [x] Classification, decision, authorization/idempotency, event preparation, command preparation, and TaskEngine submission have clear ownership boundaries.
- [x] Existing public APIs and message-hook registration semantics are preserved.
- [x] All behavioral invariants in Section 6 are covered by tests or existing verified tests.
- [x] Ordinary-message fast path remains lightweight.
- [x] No new runtime, queue, worker pool, registry, retry engine, RPC executor, or durable store exists.
- [x] TaskEngine remains the only execution admission/fairness/resource authority.
- [x] All changed Go files are gofmt-clean.
- [x] Focused tests, full tests, race tests, vet, build, and diff check pass locally, or any failure is explicitly reported and resolved before completion.
- [x] No CI polling/inspection was performed.
- [x] Final HEAD and verification evidence are recorded.
- [x] Any deviation from this document is documented and justified.

A partial refactor, a pure file split, or a passing build without the behavioral matrix does **not** satisfy P1.

---

## 11. Completion record and verification evidence

- **Starting HEAD:** `1c9969b53f0a0cdb31bc8627ef1184266c86d74a`
- **Commits:**
  1. `5e0c8aa2` — `test(telegram): lock down dispatcher pipeline contracts before decomposition`
  2. `da680acc` — `refactor(telegram): extract ingress classification and decision boundary`
  3. `6ccd7927` — `refactor(telegram): isolate command authorization and durable idempotency claim`
  4. `fb78c45b` — `refactor(telegram): extract event preparation and conditional message materialization`
  5. `bca95b40` — `refactor(telegram): extract command context preparation stage`
  6. `06416b61` — `refactor(telegram): extract execution submission and linearize message dispatch`
  7. `17f35596` — `docs(telegram): document stage contracts in dispatcher pipeline`
- **Files changed:**
  - `internal/telegram/dispatcher_dispatch.go`: monolithic `dispatch` decomposed into 7 explicit, cohesive stage methods and a linear ~40-line `dispatch` coordinator.
  - `internal/telegram/dispatcher_pipeline_test.go`: added explicit contract and ordering tests for invocation-before-idempotency, decision suppression, sender consistency, stable identity, unresolvable peer claims release, and storage error fail-closed handling.
- **Final top-level pipeline in order:**
  1. `admitMessage` (lifecycle lease + transport dedupe claim)
  2. `classifyMessage` (sender consistency, parse, command lookup, origin, decision context, envelope)
  3. `runMessageDecision` (synchronous decision lane with shared 5s timeout)
  4. `authorizeAndClaimCommand` (invocation policy first, durable SQLite idempotency claim second)
  5. `prepareMessageEffects` (subscriber check, conditional core.Message extraction, album aggregation, MessageCreated publication)
  6. `dispatchObservers` (non-command / unknown-command async observer lane)
  7. `prepareCommandContext` (peer/chat resolution, sender metadata, core.Context, task/correlation IDs)
  8. `submitCommand` (TaskEngine interactive admission, claim acceptance callback, error release)
  9. `dispatchObservers` (post-admission async observer lane)
- **Local verification commands & results:**
  - `gofmt -l internal/telegram/dispatcher_dispatch.go internal/telegram/dispatcher_pipeline_test.go` — clean (0 unformatted files)
  - `git diff --check` — clean
  - `go test ./internal/telegram/... -count=1` — PASS (0.531s)
  - `go test ./internal/core/... -count=1` — PASS (0.955s)
  - `go test ./internal/taskengine/... -count=1` — PASS (2.738s)
  - `go test ./... -count=1` — PASS across all packages
  - `go test -race ./... -count=1 -timeout=180s` — PASS across all packages
  - `go vet ./...` — PASS
  - `go build ./cmd/goultroid` — PASS
- **Architectural confirmations:**
  - Zero second runtimes, queues, worker pools, retry engines, RPC executors, or durable stores introduced.
  - TaskEngine remains the sole execution admission, fairness, and resource authority.

---

## 12. Instructions to the implementing AI

1. Read this entire document before editing.
2. Refresh `test-next` and inspect current HEAD. Do not assume the baseline SHA is still current.
3. Produce a short implementation checklist with the current HEAD and exact files to touch.
4. Work phase by phase. Do not skip Phase 0 or Phase 1.
5. Before each phase, refresh/check HEAD and integrate any intervening commit.
6. Keep each phase reviewable and run the relevant tests after each meaningful extraction.
7. Format all changed Go code before every commit.
8. Update tests whenever logic, function contracts, or APIs change.
9. Never inspect or poll CI unless explicitly requested.
10. Do not claim a phase is complete without the phase's exit criteria and evidence.
11. If current code contradicts this plan, stop and document the discrepancy; preserve the safer existing behavior rather than blindly following stale pseudocode.
12. Do not implement unrelated improvements under the P1 label.
13. At the end, provide the completion record from Phase 8 and confirm every Definition of Done item.

**Priority:** P1 — high-value maintainability and correctness-risk reduction.  
**Implementation mode:** staged, test-first, behavior-preserving decomposition.  
**Success criterion:** the dispatcher becomes easier to reason about while the Telegram userbot behaves the same, remains fast/low-resource, and retains the existing architecture boundaries.
