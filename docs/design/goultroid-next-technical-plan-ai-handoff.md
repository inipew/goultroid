# Goultroid — AI Session Handoff

Current-head implementation context, frozen Assistant parity state, and post-parity work guide.

| Field | Value |
|---|---|
| Repository | `github.com/inipew/goultroid` |
| Branch | `test-next` |
| Implementation baseline audited for this handoff | `b5ceb8338ea14182bf58466b9e663a05b4db5b07` |
| Baseline message | `docs(assistant): close P8 parity freeze` |
| Snapshot date | 23 September 2026 (Asia/Jakarta) |
| Assistant parity state | **P1–P8 CLOSED; P8-J final freeze CLOSED** |
| Primary historical parity spec | `docs/bug/4.md` |
| Final P8 authority | `docs/design/assistant-parity-p8j-closure.md` |

> **Purpose**
>
> This document is the current AI-session handoff for Goultroid. It replaces the old 19 September 2026 handoff plan whose implementation baseline was `08626042...`.
>
> The previous document contained a then-current execution/RPC/Job roadmap. That plan must **not** be resumed mechanically. The repository has changed substantially since that snapshot, especially through the Assistant vNext/parity P1–P8 program. Any older finding must be re-audited against the current `test-next` source before it becomes work again.

---

## 1. Mandatory bootstrap for the next AI session

Do this before proposing or editing code:

1. Fetch/refresh `test-next`.
2. Record the exact current HEAD.
3. Compare it to the implementation baseline in this document.
4. Read the files touched by the drift.
5. Use source + architecture/regression tests as the current truth.
6. Do not assume an older audit item still exists just because it is mentioned in old chat/docs.
7. Before committing Go changes, run `gofmt` on all changed Go files.
8. Do not inspect or wait on CI unless the user explicitly asks.

Canonical priority of evidence:

```text
current source at test-next
        ↓
architecture / integration / regression tests
        ↓
final closure docs for the relevant phase
        ↓
design docs / ADRs
        ↓
historical audits and benchmark reports
        ↓
old chat/session memory
```

If a document and current source disagree, current source wins and the stale document should be corrected.

---

## 2. Executive state summary

The major Assistant rewrite/parity program described by `docs/bug/4.md` is complete.

The original product objective was:

> Goultroid should provide the Assistant UX breadth demonstrated by Ultroid—control bot, dual command surface, rich inline, typed callbacks, interactive workflows, PM relay, custom responses, deep links, manager/group behavior, and self-inline rendering—without copying Ultroid's global/decorator architecture.

The final implementation reached that target with Goultroid-native authorities:

```text
core.Router
    canonical command execution

feature.Registry
    canonical cross-surface feature/interaction metadata

interaction.Runtime
    bounded a2 session + callback-token authority

interaction.Dispatcher
    typed callback handler ownership

interaction/orchestration.Engine
    feature-facing state/presentation transaction boundary

Inline vNext
    inline lookup/cache/lifecycle authority

TaskEngine
    execution / backpressure / resource authority

telegram.RPCExecutor
    Telegram retry / FloodWait / RPC metrics authority

selfinline.Renderer
    userbot → own Assistant inline-rendering authority
```

There is no accepted second Assistant command registry, second TaskEngine, feature-local retry/FloodWait engine, feature-local callback protocol, or per-user workflow worker model.

---

## 3. Roadmap reconciliation: original docs/bug/4 vs actual implementation

The early `docs/bug/4.md` roadmap proposed:

```text
P0 contract
P1 session runtime
P2 presentation/rendering
P3 Assistant shell
P4 Inline vNext
P5 SavedResponse
P6 PM Relay
P7 group/manager + advanced workflows
P8 migration/parity/reclamation
```

The implementation evolved while preserving the same architecture goal.

Actual sequence:

```text
P0  Feature Surface / Interaction Contract

P1  bounded interaction session runtime + a2 token foundation

P2  transport-neutral presentation + typed action dispatcher

P3  interaction/orchestration engine

P4  live a2 Assistant ingress

P5  real Assistant shell migration
    ├ root/home
    ├ Help + Status
    ├ Settings foundation
    ├ stable settings mutation contract
    ├ free-form Settings input
    ├ Settings cutover
    └ shell/help/public parity before a1 reclamation

P6  PM Relay / public messaging plane
    ├ durable relay domain
    ├ ingress + delivery
    ├ owner reply/control
    ├ audience registry
    ├ force-sub
    └ durability/resource/lifecycle acceptance

P7  complete Group / Manager Plane
    ├ group execution/context
    ├ Telegram role resolver
    ├ contextual authorization
    ├ manager read/query + foundation
    ├ group state
    ├ moderation mutations
    ├ event-driven group features
    ├ rules / filters / warnings
    ├ reply/topic/media context
    ├ lifecycle/resource hardening
    └ final acceptance

P8  remaining advanced parity + reclamation + closure
    ├ parity inventory/freeze
    ├ self-inline RenderBridge
    ├ calculator canary
    ├ representative rich lookup
    ├ representative interactive downloader
    ├ locale + final behavior matrix
    ├ reference-driven dead-stack reclamation
    ├ unload/reload/generation acceptance
    ├ mixed resource/idle/high-load acceptance
    └ final P8-J freeze
```

The important point for a future session: **do not try to restore the old phase numbering literally.** Use the actual current architecture and the final closure documents.

---

## 4. Assistant parity P1–P8 closure summary

### P1 — Interaction Session Runtime — CLOSED

Authority:

`internal/interaction`

Key frozen properties:

- callback protocol is versioned `a2`;
- session ownership is exact plugin `tasks.ScopeIdentity`;
- session state is copied on ingress/egress;
- optimistic revision fencing rejects stale UI;
- actor/chat/message/inline target binding is explicit;
- plugin generation is revalidated;
- capacity is fail-closed; live sessions are not evicted to admit new ones;
- no permanent cleanup ticker or interaction goroutine.

Current default bounds:

| Bound | Default |
|---|---:|
| live sessions | 4096 |
| per plugin generation | 512 |
| per actor | 64 |
| state per session | 64 KiB |
| total retained state | 8 MiB |
| default TTL | 15 min |
| maximum TTL | 24 h |

Do not replace this with a feature-local session map or conversation goroutine.

### P2 — Presentation / Render Bridge primitives — CLOSED

Authorities:

- `internal/presentation`
- `internal/presentation/telegram`
- `interaction.Dispatcher`

Features emit transport-neutral `presentation.View` and typed `ActionID` values.

Feature code does not construct arbitrary callback bytes.

Telegram-specific target/send/edit/answer behavior remains behind the presentation adapter.

### P3 — Interaction orchestration — CLOSED

Authority:

`internal/interaction/orchestration`

Features use one orchestration context for:

- session state;
- target;
- optimistic update;
- transition;
- edit;
- callback answer;
- cancellation.

The orchestration layer preserves caller + session cancellation without creating a long-lived goroutine.

### P4 — live a2 ingress — CLOSED

Assistant callback ingress distinguishes a2 from old compatibility traffic.

a2 callbacks go through the shared P1/P2/P3 foundation.

Malformed/stale a2 input never falls through as a legacy callback.

The old a1/menu stack was subsequently removed and is now regression-fenced.

### P5 — Assistant shell / Settings / Help migration — CLOSED

Major completed surfaces:

- owner `/start` shell;
- read-only root destinations;
- Help;
- Status;
- Settings navigation;
- stable `namespace:key` mutation binding;
- schema-version revalidation;
- optimistic UI revision before persistence;
- bounded free-form Settings input;
- Settings legacy write cutover;
- command-detail Help;
- Close/delete;
- public/non-owner `/start`.

The old Settings-specific pending-input map was removed.

Normal shell output no longer depends on the old a1 menu stack.

Useful design docs:

- `assistant-vnext-p5-assistant-shell.md`
- `assistant-vnext-p5b-readonly-root-destinations.md`
- `assistant-vnext-p5c-settings-foundation.md`
- `assistant-vnext-p5d-settings-mutation-contract.md`
- `assistant-vnext-p5e-settings-input-session.md`
- `assistant-vnext-p5f-settings-parity-cutover.md`
- `assistant-vnext-p5g-shell-parity.md`

### P6 — PM Relay / public messaging plane — CLOSED

Final acceptance authority:

`docs/design/assistant-parity-p6h-pm-relay-acceptance.md`

Implemented behavior includes:

- visitor → owner relay;
- owner reply → visitor;
- durable delivery intent;
- durable random-id/idempotency behavior;
- mapping recovery;
- owner control commands;
- audience registry;
- visitor block/ban policy;
- optional force-sub;
- bounded broadcast through shared execution infrastructure;
- shutdown/restart/resource acceptance.

P6 intentionally did **not** add another worker pool, retry scheduler, or PM-specific recovery poller.

Recovery is occurrence-driven and idempotent, not a permanent PM relay scanner.

### P7 — Group / Manager Plane — CLOSED A→L

Final acceptance authority:

`docs/design/assistant-parity-p7l-final-acceptance.md`

Frozen high-level flow:

```text
Telegram group update
    ↓
cheap surface / feature-interest classification
    ↓
canonical group context
    ↓
global identity + contextual Telegram authority
    ↓
shared TaskEngine admission
    ↓
fresh authorization / target / bot-right validation
    ↓
shared managed Telegram RPC
    ↓
topic/reply-aware output or mutation
```

Critical invariants:

- Owner/Sudo does not manufacture Telegram administrator authority;
- cached role is preflight only where permitted;
- fresh role/right/hierarchy checks happen immediately before sensitive mutation;
- TaskEngine rejection stops execution before handler/RPC;
- no per-chat/per-topic permanent worker;
- topic ordering does not multiply chat quota;
- one shared Telegram mutation/RPC path;
- rule/filter/warning lookup is chat-scoped and bounded;
- high-cardinality cold traffic has resource regression coverage.

P7 closed the Group/Manager plane. Do not create a second manager-bot subsystem.

### P8 — Advanced parity + final reclamation — CLOSED A→J

Final authority:

`docs/design/assistant-parity-p8j-closure.md`

#### P8-A — inventory/freeze — CLOSED

Canonical target classes were frozen as:

- MUST;
- REPRESENTATIVE;
- BASELINE-CLOSED;
- EXCLUDED.

This prevents provider-count parity from becoming the architecture target.

#### P8-B — production self-inline / RenderBridge — CLOSED

Feature-facing authority:

`selfinline.Renderer`

Flow:

```text
feature
  ↓
selfinline.Renderer
  ↓
own Assistant inline query
  ↓
select one result
  ↓
insert into target
  ↓
a2 callback continuity
```

Important bounds:

- no retained self-inline result map;
- no self-inline worker/ticker;
- query/offset/result selection bounded;
- Telegram query/send uses managed service + shared RPCExecutor;
- topic/reply context preserved.

P8-H later hardened authorization to check current plugin enable state on every render in addition to Telegram capabilities.

#### P8-C — calculator callback-heavy canary — CLOSED

Plugin:

`plugins/calculator`

Proves:

- userbot command → self-inline rendering;
- Inline vNext → bounded a2 state;
- typed calculator actions;
- session revision fencing;
- cross-user/target binding checks;
- no global CALC map;
- no worker/ticker;
- bounded recursive-descent arithmetic evaluator;
- no Python/JS/shell/eval/remote arbitrary execution.

#### P8-D — representative rich lookup — CLOSED

Plugin:

`plugins/wikipedia`

Proves:

- one canonical lookup backend reused by command + inline;
- managed `CapHTTP`;
- bounded query/result cardinality;
- explicit cache policy;
- sanitized rich results;
- no plugin-owned search cache/worker.

This satisfies the platform capability. Google/F-Droid/Play Store/OrangeFox/Saavn/Twitter/etc. are ordinary future feature work, not missing P8 parity.

#### P8-E — representative interactive downloader — CLOSED

Plugin:

`plugins/downloader`

Canonical workflow:

```text
inline/query source
   ↓
select media/result
   ↓
choose audio/video or equivalent variant
   ↓
choose format
   ↓
prepared a2 action
   ↓
TaskEngine
   ├ download:1
   └ process:1 only for extractor/provider path
   ↓
canonical downloader/storage/media ownership
```

Do not create `AssistantDownloader`, `InlineDownloader`, or `YouTubeDownloader` engines.

Direct HTTP must not hold the `process` resource.

#### P8-F — locale-aware UI + behavioral matrix — CLOSED

Canonical locale setting:

```text
ui:locale
type    = enum
values  = en | id
default = en
```

Locale resolution uses central Settings hierarchy.

Assistant locale presentation covers shell/Status/Help/Settings/public start/shell inline structural labels.

There is no separate locale preference map/runtime.

#### P8-G — reference-driven reclamation — CLOSED

Reclaimed production shims include:

- `assistant.NewBotClient`;
- `Router.Dispatch`;
- `Router.DispatchMessage`;
- `legacyChatForPeer`.

Canonical production Assistant command ingress is:

`Router.DispatchMessageContext`

with authoritative chat/message context.

KEEP decisions were reference-driven. In particular:

- `internal/assistant/interaction` is production-live and not a1 legacy;
- `interaction_ingress.go` is canonical a2 ingress;
- `callback_dispatch.go` remains a live generic callback adapter;
- `telegram_menu.go` is transport presentation, not a second command registry;
- DB/data migration files remain valid upgrade compatibility;
- SavedResponse/media compatibility files are not Assistant legacy automatically.

Mandatory fence:

`internal/assistant/legacy_stack_test.go`

#### P8-H — unload/reload/generation cross-surface acceptance — CLOSED

One generation-sensitive acceptance matrix now covers:

- canonical command;
- FeatureSpec screen/action/inline/deep-link declaration;
- Inline vNext registration;
- SavedResponse resolver/deep-link lease;
- a2 session/callback;
- pending input;
- scoped TaskEngine client;
- resource ownership;
- self-inline enable-state fencing.

Disable semantics:

```text
generation N disabled
  ↓
TaskEngine scope cancelled
  ↓
feature registrations removed
  ↓
sessions/input invalidated
  ↓
old callback/action/prepared execution cannot revive
```

Re-enable creates generation N+1.

Old generation remains dead.

A durable deep-link token may survive reload, but an old prepared execution lease may not. It must be re-prepared against the current generation.

#### P8-I — resource / idle / high-load acceptance — CLOSED

Combined acceptance includes:

- 10,000 inline queries;
- 512 typed calculator callbacks;
- per-actor interaction-capacity pressure;
- Wikipedia managed HTTP/cache load;
- Downloader `download:1 + process:1`;
- RPC metrics cardinality pressure;
- active scope cancellation;
- Assistant interaction-runtime restart;
- zero-idle worker retirement;
- bounded shutdown;
- goroutine/heap/RSS settle sampling.

P8-I production delta is only read-only Inline vNext cache diagnostics.

Important acceptance bounds include:

| Metric | Gate |
|---|---:|
| Inline cache entries | <= 500 |
| Inline cache retained bytes | <= 8 MiB |
| RPC method metric labels | <= 512 + overflow |
| RPC wait-scope labels | <= 32 + overflow |
| settled goroutines | baseline + 32 |
| settled heap | baseline + 64 MiB |
| settled RSS when measurable | baseline + 128 MiB |

These are regression-oriented gates, not performance guarantees for all hardware.

#### P8-J — final parity freeze — CLOSED

Final commit at implementation baseline:

`b5ceb8338ea14182bf58466b9e663a05b4db5b07`

P8-J:

- reconciled P8-A inventory to CLOSED;
- reconciled P8-F matrix through P8-I;
- froze intentional differences from Ultroid;
- added `internal/architecture/assistant_p8j_test.go`;
- kept all P8-B→I phase fences;
- kept `internal/assistant/legacy_stack_test.go`;
- added no new production runtime.

No P8 capability remains deferred.

---

## 5. Legacy stack and compatibility state

The old Assistant a1/menu architecture is gone from normal production.

Forbidden/reclaimed concepts include:

- `LegacyAssistantMenu`;
- `CompatibilityHost`;
- `MenuInstanceStore`;
- retired `internal/assistant/menu`;
- retired `internal/assistant/presentation`;
- retired `internal/assistant/callback`;
- `a1:` callback envelopes;
- `NewBotClient`;
- `legacyChatForPeer`;
- old Assistant Router convenience dispatch wrappers.

Do **not** delete compatibility merely by filename.

Examples that are not automatically Assistant legacy:

- `internal/services/savedresponse/ledger_compat.go`;
- `internal/services/savedresponse/registry_compat.go`;
- media ownership/reconciliation migration compatibility;
- database/data migrations still required for upgrades.

Deletion must remain reference-driven.

---

## 6. Frozen architecture rules for all new work

### 6.1 Command authority

Use `core.Command` + `core.Router`.

Do not add:

- Assistant-only command registries;
- plugin-local Telegram command dispatchers;
- duplicate Help registries.

### 6.2 Interaction authority

Use:

- `feature.Registry`;
- `interaction.Runtime`;
- `interaction.Dispatcher`;
- `orchestration.Engine`;
- `presentation.View`.

Do not add:

- raw callback protocols in feature code;
- per-feature global callback maps;
- feature-local conversation goroutines;
- unbounded state maps.

### 6.3 Execution/resource authority

Use shared TaskEngine.

Do not add:

- feature worker pools for ordinary feature execution;
- Assistant-specific TaskEngine;
- downloader-specific execution scheduler;
- per-topic/per-user execution workers.

Resource-bearing work must declare canonical TaskEngine resources.

### 6.4 Telegram RPC authority

Use shared managed Telegram service/RPCExecutor.

Do not add:

- feature-local FloodWait handling;
- feature retry loops around Telegram RPC;
- plugin-owned limiter;
- raw Telegram client access when a canonical service boundary exists.

### 6.5 Lifecycle

Everything generation-owned must become unusable after disable.

Do not cache capability/lifecycle authorization across reload unless the cache itself is generation-scoped and invalidated by the lifecycle owner.

### 6.6 Boundedness / idle

Every retained collection must have a cardinality and/or byte bound appropriate to retained object size.

Avoid periodic polling when event/deadline-driven behavior exists.

Feature idle expectation remains approximately:

```text
no active feature work
  → no per-feature worker
  → no per-user worker
  → no session cleanup ticker
  → no unbounded callback/search/downloader cache
```

---

## 7. Definition of Done from docs/bug/4 — current result

The original target said that loading one plugin/feature should make one canonical declaration visible across relevant surfaces and unloading it should remove/invalidate all generation-owned behavior.

That target is now represented by the P8-H acceptance matrix.

The original request path target was:

```text
Telegram update
   ↓
cheap classification
   ↓
surface / feature lookup
   ↓
policy / admission
   ↓
TaskEngine if execution/resources are needed
   ↓
feature use-case
   ↓
Presentation / managed transport
   ↓
shared RPC policy
```

This remains the north-star.

The following old architecture patterns remain unacceptable:

- plugin directly registering its own Telegram event tree for ordinary feature behavior;
- plugin inventing callback protocol bytes;
- plugin owning a private conversation worker;
- Assistant command registry #2;
- feature-local RPC/retry policy;
- unbounded per-user/per-query workflow maps.

---

## 8. Verification state and caveat at handoff

A key limitation of the AI environment used for P8-H/P8-I/P8-J:

- the shell could not resolve `github.com`;
- a complete local repository checkout could not be materialized there;
- therefore those sessions did **not** claim `go build`, `go vet`, `go test ./...`, or CI green.

The user previously reported concrete Go syntax errors in:

- `internal/assistant/shell/inline.go`;
- `plugins/downloader/interactive.go`;
- `internal/architecture/assistant_p8f_test.go`.

Those corrections are present in:

`e7297b1940b481a59c03b06213504443293c1e7d` — `fix(assistant): restore P8-E/P8-F Go syntax`.

However, the next session should still treat full current-HEAD verification as mandatory before claiming runtime acceptance.

Recommended local gate:

```bash
gofmt -l .
go build -o bin/goultroid ./cmd/goultroid
go vet ./...
go test ./...
go test ./internal/architecture -count=1
```

If practical after normal tests pass:

```bash
go test -race ./...
```

Useful focused P8 gates include the P8-H lifecycle tests, P8-I combined resource acceptance, and P8-J architecture closure test.

Do not claim green CI unless an actual run was inspected.

---

## 9. What comes after P8?

### Important: docs/bug/4 defines no P9 parity phase

The Assistant parity program is complete.

Do **not** invent “P9 parity” merely because P8 ended.

Future work falls into one of three categories:

1. regression/stabilization against the frozen P8 matrix;
2. repository-wide performance/correctness work independent of parity;
3. ordinary new product features built on the frozen framework.

P8 should only be reopened if a regression violates the frozen behavioral/lifecycle/resource matrix.

---

## 10. Recommended next work — post-parity stabilization

This is the recommended order for the next session unless the user selects a different feature.

### N0 — current-HEAD build/test verification

First priority.

Run the verification gate in section 8 against the exact current HEAD.

If anything is red:

1. fix compile/vet/test failures first;
2. keep fixes minimal;
3. run `gofmt` before every Go commit;
4. re-run the narrow failing package;
5. then re-run the wider gate.

Do not start another architecture redesign while the branch is not known-buildable.

### N1 — P1→P8 integration re-audit against final HEAD

Purpose: catch cross-phase regressions that individual phase tests may not expose.

Audit actual source, not old checklists.

High-value questions:

- Does every normal Assistant command still project from canonical `core.Router`?
- Can any feature bypass TaskEngine despite declaring resources?
- Can any feature construct raw callback data or retain its own workflow map?
- Does disable remove command/inline/action/session/input/resource ownership consistently?
- Can a stale generation execute through deep-link, self-inline, SavedResponse, or prepared action paths?
- Are contextual Telegram permissions still revalidated immediately before mutation?
- Does any new inline/search/downloader path add its own worker/cache/retry engine?
- Are any feature caches count-bounded but not byte-bounded where object graphs can be large?
- Has a new periodic ticker/poller appeared in an otherwise event-driven path?
- Are all raw goroutines owned, cancellable, joinable, panic-safe, and cardinality-bounded?

Do not re-score the project using an old audit without reading current code.

### N2 — real runtime performance/soak evidence

P8-I is an executable source-level combined acceptance harness. It is not a substitute for real deployment profiling.

Recommended real-condition runs:

#### Idle

30–60 minutes:

- goroutines baseline/settled;
- CPU;
- heap/RSS;
- DB activity;
- TaskEngine workers;
- inline cache;
- interaction sessions;
- RPC metrics cardinality.

Expected: no monotonic growth and no feature worker at idle.

#### Mixed Assistant load

Combine:

- inline Help/search;
- calculator callbacks;
- SavedResponse;
- PM Relay;
- group commands;
- Wikipedia lookup;
- interactive downloader.

Measure:

- p50/p95/p99 response latency;
- TaskEngine queue/rejection;
- worker count;
- interaction session count/bytes;
- inline cache count/bytes;
- RPC attempts/FloodWait;
- resource grants;
- heap/RSS plateau.

#### Heavy media

Test direct HTTP and extractor paths separately.

Verify:

```text
direct HTTP → download only
extractor    → download + process only while required
```

Include shutdown/cancel while extraction/upload is active.

#### Reload/restart

Exercise:

- plugin disable during active workflow;
- re-enable;
- Assistant restart mid-session;
- application shutdown with active extraction/network/RPC.

Old tokens/prepared leases must remain invalid.

### N3 — evidence/document synchronization

Several older docs are historical snapshots.

Before using them as current guidance:

- check their baseline commit;
- compare against current source;
- update or explicitly mark superseded.

Especially important:

- old benchmark reports;
- old worker/limiter audits;
- the earlier version of this handoff;
- historical sections inside `docs/bug/4.md` that describe pre-P8 gaps.

For final Assistant parity status prefer:

1. `assistant-parity-p8j-closure.md`;
2. `assistant-parity-p8a-inventory.md` after P8-J reconciliation;
3. `assistant-parity-p8f-behavioral-matrix.md`;
4. phase-specific closure docs/tests.

### N4 — ordinary product feature expansion

Only after N0 is green and there is no selected regression blocker.

Examples that are valid future product work, not missing parity:

- more rich lookup providers;
- more locale packs;
- games/novelty inline features;
- additional interactive utilities;
- richer downloader search/provider UX;
- automatic Telegram delivery of retained downloader assets.

If implementing automatic downloader delivery, put it at the **canonical downloader/media delivery boundary** so userbot/Assistant/other surfaces reuse one path. Do not add an Assistant-only uploader.

Additional providers should reuse Inline vNext and existing network/resource boundaries rather than adding provider-specific runtimes.

Arbitrary remote code execution through a public/inline Assistant workflow remains intentionally excluded unless the product requirement is explicitly revisited with a separate security design.

---

## 11. Old execution/RPC handoff roadmap — historical only

The previous version of this document proposed a detailed sequence around:

- structured RateLimit signals;
- durable deferral;
- attempt accounting;
- yielding durable RPC waits;
- observability;
- goroutine ownership;
- benchmark refresh.

That plan was written for implementation baseline:

`08626042b0645df958fc5b9e95c10906e4441813`

on 19 September 2026.

Since then the repository has undergone substantial execution/resource fixes and the full Assistant P1–P8 implementation.

Therefore:

> **Do not resume that old Phase 1/2/3 list by name.**

If rate-limit deferral, Job attempt accounting, limiter waits, goroutine ownership, or RPC observability is selected again, start with a fresh current-HEAD audit and produce a new issue list from the source that exists now.

Historical docs/commits are useful for understanding intent, not for proving a current bug still exists.

---

## 12. Files/docs the next session should read first

### Always

- this file;
- `docs/bug/4.md`;
- current git diff since the handoff baseline.

### Final Assistant parity

- `docs/design/assistant-parity-p8j-closure.md`
- `docs/design/assistant-parity-p8a-inventory.md`
- `docs/design/assistant-parity-p8f-behavioral-matrix.md`
- `docs/design/assistant-parity-p8i-resource-acceptance.md`

### Foundation

- `docs/design/assistant-vnext-p1-interaction-session-runtime.md`
- `docs/design/assistant-vnext-p2-presentation-render-bridge.md`
- `docs/design/assistant-vnext-p3-interaction-orchestration.md`
- `docs/design/assistant-vnext-p4-a2-ingress.md`
- `docs/design/assistant-vnext-p5g-shell-parity.md`

### PM Relay / Group plane

- `docs/design/assistant-parity-p6h-pm-relay-acceptance.md`
- `docs/design/assistant-parity-p7l-final-acceptance.md`

### Architecture fences

- `internal/assistant/legacy_stack_test.go`
- `internal/architecture/assistant_p8b_test.go`
- `internal/architecture/assistant_p8c_test.go`
- `internal/architecture/assistant_p8d_test.go`
- `internal/architecture/assistant_p8e_test.go`
- `internal/architecture/assistant_p8f_test.go`
- `internal/architecture/assistant_p8g_reclamation_test.go`
- `internal/architecture/assistant_p8h_test.go`
- `internal/architecture/assistant_p8i_test.go`
- `internal/architecture/assistant_p8j_test.go`

---

## 13. Useful final commit ledger

Important milestones near final closure:

| Phase | Commit |
|---|---|
| P7 final group-plane closure | `7c1ce5e125503f00c08496a11a267e353f39857f` |
| P8-A inventory/freeze | `8cb0d9cadfd6599dadcaf92677a15e9fecc77094` |
| P8-B self-inline RenderBridge | `3ae37805080134f571c936293528eeb46b823cf6` |
| P8-C calculator canary | `aab6f94ef1cb0c90f59d0e62495fcba81b0dd93c` |
| P8-C recursion hardening | `aa77c1b6edd46c017848d557a106e9f8a68a1be9` |
| P8-C capability hardening | `cfd7af505608b7ea76481df0604cbcdf7907ccb3` |
| P8-D Wikipedia rich lookup | `ab6f98841a0b92062a785e3d2ececd84fe764727` |
| P8-E interactive downloader | `812b853e2a40da6ddd9a5e91d59cd39e95444ac7` |
| P8-F locale/matrix | `fb32d215b00045f0c38a4aff4c7f083808f00d44` |
| P8-G reclamation | `c0ef995d2dc238bfdb2006378e1e49c94ff743ce` |
| P8-H lifecycle acceptance | `d19c2a8e2494ab6568360cf6d9c7c3a242e81c6e` |
| P8-E/P8-F syntax restoration | `e7297b1940b481a59c03b06213504443293c1e7d` |
| P8-I resource acceptance | `13f9cd6fd1cb2b55f3d5c09fd319d886d952eed1` |
| P8-J final parity freeze | `b5ceb8338ea14182bf58466b9e663a05b4db5b07` |

For anything before these milestones, inspect git history/source rather than trusting a remembered SHA.

---

## 14. Commit/workflow rules for future AI sessions

1. Refresh `test-next` before starting a phase/fix.
2. Never build a patch against an old parent after the branch moves.
3. Keep commits scoped to one coherent invariant.
4. For Go changes:
   - run `gofmt` first;
   - then commit;
   - then push.
5. Prefer focused tests first, then wider tests.
6. Do not check CI unless explicitly requested.
7. Do not claim tests/benchmarks/CI succeeded unless they were actually run and observed.
8. Do not create a second subsystem when a canonical authority already exists.
9. When deleting compatibility, prove references are gone first.
10. Treat resource/idle behavior as part of correctness, not optional optimization.

---

## 15. Suggested prompt for the next AI session

```text
Refresh github.com/inipew/goultroid branch test-next and read:

- docs/design/goultroid-next-technical-plan-ai-handoff.md
- docs/bug/4.md
- docs/design/assistant-parity-p8j-closure.md
- relevant phase-specific architecture tests

Do not assume old audits are current.

Assistant parity P1-P8 is closed. P8-J is the frozen baseline.
Do not invent a P9 parity phase unless a new specification explicitly requires one.

First run/establish current-head verification:
gofmt -l .
go build -o bin/goultroid ./cmd/goultroid
go vet ./...
go test ./...
go test ./internal/architecture -count=1

If anything fails, fix that first.

If the tree is green and no specific feature was requested, perform a fresh post-parity integration/performance audit against the current source and propose only findings that still exist.

Preserve:
- one core.Router
- one feature.Registry
- bounded interaction.Runtime / a2
- shared TaskEngine
- shared telegram.RPCExecutor
- generation-scoped plugin lifecycle
- zero-idle / bounded-resource design

Before every Go commit:
gofmt changed Go files, then commit and push.
Do not inspect CI unless explicitly requested.
```

---

## 16. Final handoff statement

At the implementation baseline recorded here, **Assistant parity P1–P8 is complete and frozen by P8-J**.

The next session should not continue parity implementation by inertia.

The correct next move is:

```text
refresh current HEAD
    ↓
build/vet/test verification
    ↓
fix any real regression
    ↓
fresh post-parity integration/performance audit
    ↓
then ordinary product features or separately-scoped technical work
```

Any future architecture change should demonstrate why the existing canonical authorities cannot safely express the requirement before introducing a new runtime, registry, cache, worker pool, retry engine, or callback protocol.
