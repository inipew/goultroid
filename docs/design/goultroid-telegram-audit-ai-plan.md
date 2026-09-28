# Goultroid Telegram Runtime Audit — AI Session Plan

Status: **IMPLEMENTED — T1–T4 source changes are present; fresh local acceptance and benchmark execution pending**

Audit baseline:

- Branch: `test-next`
- Baseline HEAD: `5ab5a1196c0aaa1d1a57eb68dbd25898086b740c`
- Baseline commit: `docs(maint): close boundary refactor milestones`
- Audit date: 28 September 2026
- Evidence: original source inspection and targeted package-test baseline plus implementation source review.
- Implementation lineage:
  - `295b26a6b7e8166ad7b28e5335b5ae975c88ba22` — `fix(telegram): make command identity peer safe`
  - `7efb886816d050263b4f4f254e2d6dfd6515eddc` — `fix(telegram): make callback claims admission safe`
  - `f7b295bb4366330af0b2d6a692cf58c0edd8f251` — `fix(assistant): classify callbacks before peer resolution`
  - `ec41a80a329e1d5a56a3b8de78632c57a6094ad4` — `test(telegram): define callback observation ownership`
- Current implementation HEAD before this document update: `ec41a80a329e1d5a56a3b8de78632c57a6094ad4`.
- No production trace, CPU/heap profile, fresh benchmark result, full race-suite result, or CI result is claimed by this implementation session.

This document hands the Telegram audit to a later AI session. Its goal is to make callback ownership, command identity, and failure handling reliable while preserving Goultroid's existing runtime boundaries. Findings below distinguish observed behavior from risks that need a reproducer or measurement.

## 1. Repository rules and hard constraints

1. Keep Telegram integration in `internal/telegram` and assistant-specific ingress in `internal/assistant/client`; respect the package boundaries checked by `internal/architecture`.
2. Keep TaskEngine as the execution authority for finite work. Do not introduce a second dispatcher, task queue, Telegram client, or callback protocol.
3. Keep caches, queues, and retained callback state bounded.
4. Preserve userbot and assistant bot behavior unless a phase explicitly changes it, including a2 session binding, authorization, and Telegram callback acknowledgements.
5. Use narrow consumer-owned capabilities and the existing presentation and interaction layers; do not route plugin behavior through raw Telegram clients when a boundary already exists.
6. Add focused regression coverage for changed behavior and relevant error paths. Run `gofmt` on changed Go files and the full race suite before a pull request.
7. Treat the failing tests in Section 3 as a baseline requiring investigation, not as evidence that any proposed fix already works.

## 2. Runtime map and audit findings

### 2.1 Live update and callback paths

- The userbot client installs gotd update hooks and persistent update state through `internal/telegram/client.go`. `Dispatcher` routes new messages, edits, deletes, callback queries, inline queries, and reactions from `internal/telegram/dispatcher_callback.go`.
- Message ingress passes a bounded in-memory dedupe, lightweight envelope and indexed hook selection, synchronous decision handlers, then optional durable command admission and TaskEngine submission in `dispatcher_dispatch.go`.
- Userbot callback ingress performs a durable query-ID claim, constructs a canonical callback event, publishes it to EventBus when subscribed, then tries the native a2 adapter or answers the callback as unknown.
- Assistant bot callback ingress lives separately in `internal/assistant/client/updates.go`. It classifies a2 data, deduplicates query IDs in memory, and dispatches through `interaction_ingress.go`, orchestration, TaskEngine, and the assistant interaction transport.
- `internal/presentation` validates typed buttons and compiles a2 action data; `internal/presentation/telegram` serializes markup and delivers views through narrow transport ports.

These are two Telegram accounts/surfaces with separate ingress ownership. Any fix must cover message-origin and inline-origin callbacks where applicable.

### 2.2 High priority: command identity loses peer kind

The transport dedupe key includes peer kind, peer ID, and message ID (`internal/telegram/dispatcher_ingress_dedupe.go`). Downstream command identity does not: the durable claim uses `msg:<chatID>:<messageID>`, the TaskEngine ID uses `cmd:<chatID>:<messageID>`, and correlation uses the same untyped numeric pair (`dispatcher_dispatch.go`). Telegram user, basic-chat, and channel ID namespaces are represented by different peer types. If their numeric IDs and message IDs coincide, distinct commands receive the same downstream identity and one can be rejected as a duplicate or task collision.

This is a confirmed key-construction inconsistency. The cross-peer collision scenario still needs a focused regression test; no observed production collision is claimed.

**Target:** derive one canonical typed peer identity from `msg.PeerID` and use it consistently for command claim, task ID, and correlation. Keep the existing ingress dedupe identity semantics and avoid a schema migration unless inspection of the idempotency store proves one necessary. Define how existing untyped claims behave during rollout before changing persistent key format.

### 2.3 High priority: userbot callback claim precedes processing

Both userbot callback paths call `CheckAndSet` with a five-minute TTL before target construction and native adapter admission (`dispatcher_callback.go`). A duplicate query ID returns without a callback answer. The native adapter can fail during target construction, action preparation, or task submission (`internal/interaction/native/adapter.go`), but callback ingress has no release or accepted/completed distinction for the durable query claim.

The code establishes that a failed first attempt can leave the query ID claimed for five minutes. Whether gotd or Telegram will redeliver that exact query in a given failure case must be tested; do not describe redelivery frequency as known. The adapter also ignores some answer-RPC errors, so observability is needed before claiming the user was acknowledged.

**Target:** specify callback claim states and ownership around TaskEngine admission. A retry before task acceptance must remain possible without allowing the action to execute twice. A callback rejected at any stage must receive one clear answer when the transport is available. Preserve a2 session/binding checks and the immediate-ack policy.

### 2.4 Medium priority: assistant resolves peer before callback classification

For message-origin callbacks, assistant ingress resolves the peer before checking whether data belongs to a2 or is a duplicate (`internal/assistant/client/updates.go`). Unknown, `noop`, stale, and repeat callbacks can therefore incur a resolver lookup. A resolver error answers “Unable to resolve chat” before the unknown/expired callback policy can run.

**Target:** classify and deduplicate first. Resolve a peer only when the selected callback handler needs a message target. Preserve a clear answer for unknown callbacks and the current a2 authorization and target-binding rules. Measure resolver calls and callback latency before claiming a performance win.

### 2.5 Medium priority: EventBus callback ownership is ambiguous for extensions

Userbot ingress publishes callback events asynchronously, then independently dispatches native a2 or answers unknown (`dispatcher_callback.go`; `internal/core/events.go`). A subscriber that also answers a query could race with the dispatcher response. The source search found callback subscriptions in tests, but no production subscriber in the inspected tree. This is an extension-contract risk, not a demonstrated live double-answer bug.

**Target:** document and enforce one answer owner per callback namespace. Decide whether EventBus callback events are observation-only or whether a registered handler can claim a namespace synchronously. Do not make an asynchronous publish result determine an immediate Telegram acknowledgement.

### 2.6 Current strengths and performance tradeoffs

- Ordinary message ingress uses a bounded in-memory dedupe; only recognized commands surviving the decision lane reach durable idempotency. Core message/media extraction is delayed until a command, album, or EventBus subscriber needs it (`dispatcher_dispatch.go`).
- Hook routing uses an immutable class index published through an atomic pointer, avoiding a full handler scan on each message (`dispatcher_handlers.go`).
- Decision handlers are sequential on the ingress path with a shared five-second deadline (`dispatcher_dispatch.go`). That preserves security/moderation order but can raise update latency when active handlers are slow. Treat this as a measurable tradeoff, not a proven bottleneck.
- RPC execution has rate limiting, retry classification, FloodWait handling, and operation-kind distinctions (`rpc_executor.go`). The client uses persistent gotd update recovery when the database is configured (`client.go`).
- Typed presentation buttons, view validation, a2 compilation, copied callback data, and explicit expired-interaction responses provide a sound UI foundation (`internal/presentation` and `internal/presentation/telegram`).

## 3. Test baseline and evidence limits

Command run during audit:

```text
go test ./internal/telegram ./internal/assistant/client ./internal/interaction/native ./internal/presentation/...
```

Observed result at the baseline HEAD:

- `internal/telegram`: four failures — three `TestDispatcherBehaviorMatrix` cases and `TestDispatcherBehaviorDurableClaimPrecedesCommandAdmission`. The observed matrix stopped after the decision lane; command, hook, event, and durable-claim counts were below the expected counts. The cause was not established by this audit.
- `internal/assistant/client`: four failures — Settings navigation effective-value reads, self-inline downloader continuation input, self-inline Help selection reported stale, and direct Help detail output.
- `internal/interaction/native` and the tested `internal/presentation/...` packages passed.

Do not attribute these failures to a specific implementation change without a reproducer and baseline comparison. A full repository suite, race suite, and performance profile were not run for this audit.

## 4. Implementation phases

### T0 — Freeze baseline and diagnose existing failures

Status: **SOURCE BASELINE REFRESHED — fresh local execution still pending**

Implementation-session findings:

- refreshed `test-next` from `64735688...` before T1 and refreshed HEAD again before each later phase;
- source inspection confirmed all four audit findings were still present before their corresponding changes;
- no unrelated production Telegram redesign had landed between the original audit baseline and T1;
- the session environment did not provide a runnable repository checkout, and shell network access could not materialize one, so the targeted command from Section 3 was not re-executed here;
- the historical failures in Section 3 therefore remain baseline evidence, not fresh acceptance evidence.

- Re-run the exact targeted command in Section 3 and preserve failing test names and output.
- Trace why the dispatcher behavior matrix stops after decision handlers. Test the decision result, message decision flags, and TaskEngine wait/admission path before proposing a fix. Classify each assistant failure separately.
- Add or adjust tests only when the expected contract is established from production behavior and current design documents; do not weaken assertions merely to obtain a green suite.

**Gate:** the next session can state which failures are pre-existing, which are newly introduced, and what each failing assertion represents.

### T1 — Make command identity peer-safe

Status: **IMPLEMENTED at `295b26a6b7e8166ad7b28e5335b5ae975c88ba22`; fresh local test execution pending**

Implemented:

- introduced one canonical typed Telegram message identity: `user:<id>:<msg>`, `chat:<id>:<msg>`, or `channel:<id>:<msg>`;
- durable command keys now use `msg:<typed-identity>`;
- TaskEngine command IDs now use `cmd:<typed-identity>`;
- command correlation/ordering IDs now use the same typed identity;
- added rollout compatibility: old `msg:<numericChatID>:<messageID>` claims are read as a temporary replay fence but are no longer written;
- added focused regressions for namespace separation, same-numeric-ID chat/channel admission, and legacy-claim rollout behavior.

- Add a regression with two commands whose peer kinds differ but whose numeric peer IDs and message IDs match. Verify both can reach distinct durable claims and task admissions.
- Use a canonical typed key across durable claim, TaskEngine ID, and correlation. Review any consumers of event metadata or correlation strings before changing their format.
- Validate behavior with and without the idempotency manager, and across user, basic-chat, and channel peers.

**Gate:** distinct Telegram peers cannot suppress each other's command; replay of the same peer/message remains deduplicated.

### T2 — Make userbot callback admission retry-safe

Status: **IMPLEMENTED at `7efb886816d050263b4f4f254e2d6dfd6515eddc`; fresh local test execution pending**

Implemented:

- replaced callback `CheckAndSet` ownership with the existing two-phase `idempotency.ExecutionClaim`;
- callback query IDs are provisional before native a2 admission;
- pre-admission native failures release the generation-owned claim through a bounded detached context;
- successful native admission accepts the claim and keeps at-most-once action semantics;
- unknown/noop callbacks accept the claim before the dispatcher-owned terminal answer;
- duplicate callback queries receive a silent terminal ACK instead of returning with the Telegram spinner potentially active;
- message and inline callback paths share the same lifecycle;
- added focused regressions for release/retry, accepted duplicate suppression, and duplicate ACK behavior.

- Cover message and inline callback failures before task acceptance, including invalid target, stale action, unavailable TaskEngine, and rejected admission. Assert the acknowledgement behavior and query-claim state for each.
- Establish a single transition from provisional claim to accepted work, with release on pre-acceptance failure. Keep accepted work deduplicated and preserve immediate ACK behavior.
- Record answer-RPC failures in diagnostics without turning a successful action into a duplicate execution.

**Gate:** each callback action executes at most once; a failed pre-admission attempt does not block a safe retry; each rejected callback has a defined user-visible answer.

### T3 — Order assistant callback work by need

Status: **IMPLEMENTED at `f7b295bb4366330af0b2d6a692cf58c0edd8f251`; fresh local test execution pending**

Implemented:

- Assistant message callback namespace classification now precedes peer resolution;
- a2 query-ID dedupe now precedes peer resolution;
- interaction-ingress availability is checked before resolver work;
- unknown/noop callbacks perform no peer resolution;
- duplicate a2 callbacks perform no peer resolution;
- fresh a2 message callbacks still resolve the peer before target-bound interaction ingress;
- added instrumented-resolver regressions proving the expected 0/1 resolver-call behavior.

- Add focused tests proving unknown/noop and duplicate callbacks do not invoke the peer resolver, and that a2 message callbacks still receive the required target.
- Move classification and dedupe ahead of resolution while keeping shutdown handling, inline callbacks, target binding, and authorization unchanged.
- Compare callback resolver-call counts and latency before and after using a deterministic benchmark or instrumented fake; report measurements rather than estimated gains.

**Gate:** callback outcomes remain correct, and callbacks that need no peer perform no resolution.

### T4 — Define callback event ownership and measure ingress

Status: **CONTRACT + REGRESSION/BENCHMARK HARNESS IMPLEMENTED at `ec41a80a329e1d5a56a3b8de78632c57a6094ad4`; benchmark execution pending**

Contract decision:

- `EventTypeCallbackQuery` is **observation-only**;
- EventBus subscribers do not own Telegram callback namespaces and cannot determine the synchronous ACK result;
- native a2 remains the answer owner for claimed a2 callbacks;
- dispatcher fallback remains the answer owner for unknown/noop callbacks;
- asynchronous EventBus publication remains independent of ACK ownership.

Implemented evidence:

- one-answer regressions with an active callback subscriber for message/inline unknown and noop callbacks;
- one-answer regressions proving an active callback subscriber does not steal a2 answer ownership for message or inline targets;
- bounded benchmark harnesses for callback ingress with an active observation subscriber and synchronous decision ingress;
- benchmark harness reports `p50-ns`, `p95-ns`, and `p99-ns` in addition to normal Go benchmark metrics.

No numerical latency improvement is claimed until those benchmarks are run on a real local checkout.

- Choose and document an explicit EventBus callback contract. If callback subscribers are observational, fence them from answering through the dispatcher-owned acknowledgement path. If claiming is required, use a synchronous ownership decision before fallback answering.
- Test one-answer behavior for known a2, unknown, noop, and subscriber-present callbacks on message and inline origins.
- Measure p50/p95/p99 decision-handler and callback-ingress latency under representative active-hook load. Optimize only a measured hot path while preserving moderation ordering and fail-closed behavior.

**Gate:** exactly one component owns the answer for each callback namespace, and any performance change has reproducible before/after evidence.

## 5. Acceptance and handoff rules

- Run focused tests after each phase, then `go test -v -race ./...`, `go vet ./...`, and `go build -v ./cmd/goultroid` before a pull request. Report unrelated baseline failures explicitly if the full suite remains red.
- Add architecture tests only for stable ownership or package-boundary contracts; avoid tests that merely mirror implementation text.
- Confirm no new unbounded cache, queue, timer, or goroutine was introduced, and that callbacks and commands still drain correctly during shutdown.
- Record configuration, persistence-key compatibility, user-visible callback text, and any migration effect in the pull request.
- Do not mark this handoff closed until targeted behavior and the relevant acceptance gates are demonstrated by fresh test output.
- Required fresh local acceptance for this implementation lineage:
  ```text
  gofmt -w internal/telegram/dispatcher_dispatch.go \
    internal/telegram/dispatcher_command_claim_test.go \
    internal/telegram/dispatcher_identity_test.go \
    internal/telegram/dispatcher_callback.go \
    internal/telegram/dispatcher_callback_claim.go \
    internal/telegram/dispatcher_callback_claim_test.go \
    internal/assistant/client/updates.go \
    internal/assistant/client/updates_callback_order_test.go \
    internal/telegram/dispatcher_callback_observation_test.go \
    internal/telegram/dispatcher_latency_benchmark_test.go

  gofmt -l <same files>   # must print nothing

  go test ./internal/telegram ./internal/assistant/client ./internal/interaction/native ./internal/presentation/...
  go test ./internal/telegram -run 'TestTelegramMessageIdentity|TestDispatcherCommandIdentity|TestDispatcher.*CallbackClaim|TestDispatcherAcceptedCallbackClaim|TestDispatcherCallbackEventBus' -count=1
  go test ./internal/assistant/client -run 'TestAssistant.*Callback.*Resolver' -count=1

  go test ./internal/telegram -run '^
 -bench 'BenchmarkDispatcher(CallbackIngressObservation|DecisionIngressNoop)
 -benchmem -count=3

  go test -race ./internal/telegram ./internal/assistant/client ./internal/interaction/native ./internal/presentation/... -count=1
  go vet ./...
  go build ./cmd/goultroid
  ```
- Record the benchmark p50/p95/p99 values and classify any failing tests against the Section 3 baseline before changing the top-level status to `CLOSED`.
