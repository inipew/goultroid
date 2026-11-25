# Goultroid Existing Plugin Enhancement Roadmap

## Status

- Status: **PLANNING — no implementation authorized by this document**
- Repository: `inipew/goultroid`
- Target branch: `test-next`
- Planning baseline HEAD: `de601a0f4dc6329890cbbaeaa5e08e72d87861cb`
- Baseline commit: `docs(audit): close P3 runtime lifecycle and resource accounting audit`
- Scope: improve and modularize existing plugins without introducing parallel runtime infrastructure.
- First implementation candidate: **Downloader domain separation**.

This roadmap is an inventory and sequencing authority. It does not assert that every listed plugin has a defect, and it does not authorize production changes. Each plugin must receive a current-source audit and a focused implementation plan before refactoring begins.

---

## 1. Purpose

Goultroid should gain richer, more complete, and more consistent plugin capabilities without becoming a collection of independently managed subsystems.

The next improvement cycle should prioritize existing plugins before adding a large number of new plugins. The work should:

1. Separate responsibilities that have grown together inside one plugin.
2. Preserve shared execution, lifecycle, storage, network, and Telegram boundaries.
3. Improve command, Assistant-optional, inline, and a2 interaction consistency.
4. Improve correctness across cancellation, retry, restart, reload, and delivery.
5. Keep CPU, memory, goroutine, queue, cache, disk, and network usage bounded.
6. Reduce duplicated business logic and sources of truth.
7. Preserve existing user-facing behavior unless a reviewed migration explicitly changes it.

The roadmap is intended to guide audits and focused implementation plans, not to prescribe a big-bang rewrite.

## 2. Product and architecture principles

### 2.1 Product principles

- Prefer useful depth and reliability over plugin count.
- A feature should have one clear domain owner even when it is exposed through multiple surfaces.
- Native commands must remain usable when Assistant is absent, disabled, or not ready.
- Command, Assistant, inline, and a2 paths should call the same domain logic rather than implement parallel workflows.
- User-facing errors should explain the failed stage and the next safe action without exposing raw internal errors or secrets.
- Existing command aliases and interaction tokens should remain compatible where practical.
- New features must have an explicit user value, bounded resource cost, and testable acceptance criteria.

### 2.2 Non-negotiable architecture constraints

Preserve the established Goultroid boundaries:

- **TaskEngine** owns execution admission, fairness, worker/resource limits, and task execution.
- **JobManager** owns durable logical jobs, attempts, claims, leases, and recovery semantics.
- **Scheduler** determines when work becomes due; it must not execute plugin handlers outside the managed job/task path.
- Telegram FloodWait and retry behavior remains in the shared **Telegram RPCExecutor**.
- Typed **a2 interaction/runtime** remains the authority for callback/session lifecycle and generation fencing.
- **Plugin Scope / resource ownership** remains the boundary for plugin-owned subscriptions, tasks, temporary resources, and cleanup.
- Shared network, process, filesystem, storage, retained-media, and Telegram services remain the platform boundaries.
- Existing capability and permission checks remain separate: user authorization decides who may invoke an action; plugin capabilities decide which platform resources an implementation may access.

Do not introduce:

- a second runtime or lifecycle manager;
- a second TaskEngine, JobManager, Scheduler, worker pool, or retry engine;
- a plugin-local Telegram RPC executor;
- a parallel callback/session protocol or ad-hoc callback registry;
- a second download orchestration engine or media registry;
- unbounded goroutines, queues, maps, caches, histories, or retained files;
- duplicate token, account, configuration, or interaction state;
- direct Telegram, HTTP, process, or filesystem bypasses where an existing managed service already owns that boundary.

### 2.3 Refactoring rule

Separate **domain responsibility and feature UX** before deciding whether code must move into a new Go package or plugin module.

A package split is justified only when the audit proves that it improves ownership, lifecycle, dependency direction, testability, or release/enablement behavior. Do not create a new package merely to make the directory tree look modular.

---

## 3. Prioritization

| Priority | Plugin / domain | Main improvement target | Planned outcome |
|---|---|---|---|
| P1 | Downloader + YouTube | Separate general URL/file downloading from YouTube-specific product workflow | Clear domain boundary with shared download and delivery infrastructure |
| P1 | MyXL | Keep account, token lifecycle, quota/package, purchase, and notification responsibilities coherent | One source of truth with smaller, testable domain services |
| P1 | Assistant | Separate provider/conversation orchestration from native userbot features and optional presentation | Assistant remains optional; provider failures do not break native commands |
| P2 | AFK | Improve status, mention/welcome policy, per-chat behavior, and UX | Predictable, non-blocking AFK behavior |
| P2 | PMPermit | Improve approval policy, trusted contacts, expiration, and abuse controls | Safe and understandable private-message admission |
| P2 | Filters / Message Hooks | Improve matching, ordering, bounded action behavior, and configuration UX | Deterministic message automation without blocking dispatch |
| P2 | Userlog | Improve event selection, origin/actor accuracy, destination policy, and bounded history | Useful, privacy-aware, low-noise logging |
| P3 | Calculator | Improve parser diagnostics, native interaction, and optional history | Small, local, deterministic utility |
| P3 | Other existing utility plugins | Discover from the actual plugin inventory and user value | Only promote after inventory and duplication checks |

Priority is an execution order, not a severity claim. Work on the next plugin begins only after the preceding plugin's audit and implementation phase is reviewed and explicitly closed.

---

## 4. P1 workstreams

### 4.1 Downloader and YouTube — first target

#### Current source context

The current repository has a Downloader plugin with general URL handling, URL pipeline, progress, retained-media registration, delivery, interactive flows, and YouTube search-related code. The shared download service has provider-level behavior, including HTTP and an extractor provider backed by yt-dlp.

Relevant existing material includes:

- **plugins/downloader/** — command, URL pipeline, interactive UX, progress, retained-media registration, and delivery.
- **internal/services/download/** — shared download client/provider boundary.
- **internal/services/storage/** and retained-media infrastructure — file persistence, quota, and ownership.
- **docs/design/goultroid-p2-downloader-media-rpc-audit.md** — previous Downloader → retained media → Telegram RPC audit and closure evidence.
- **docs/design/goultroid-youtube-downloader-ai-session-handoff.md** — YouTube search-first UX, format selection, metadata, and delivery handoff.
- **docs/design/goultroid-selfinline-help-downloader-p2-ai-session-handoff.md** — Assistant-optional Downloader interaction context.

The previous P2 media/RPC audit is closed. This roadmap does not reopen that audit automatically. The new work is a product/domain-boundary enhancement and must revalidate the current source because the existing audit documents may describe earlier commits.

#### Target domain boundary

The intended product split is:

**General Downloader**
- Owns generic direct URL and supported file download behavior.
- Owns generic URL validation and user-facing generic download status.
- Handles generic metadata and file classification.
- Does not own YouTube search, YouTube-specific format menus, or YouTube-specific interaction state.

**YouTube feature**
- Owns YouTube search and search-result selection.
- Owns YouTube metadata presentation.
- Owns audio/video choice, format choice, quality selection, and YouTube-specific progress/status UX.
- Owns YouTube-specific command and a2 interaction semantics.
- Uses the shared download provider/service boundary for actual extraction and transfer.

**Shared download and delivery infrastructure**
- Owns provider registration and execution contracts.
- Owns HTTP/extractor execution, input and destination safety, timeout/cancellation, and temporary workspace cleanup.
- Reuses TaskEngine admission and resource budgets.
- Reuses existing retained-media ownership and storage quotas.
- Reuses the existing Telegram delivery path and shared RPCExecutor.
- Does not duplicate retry, media registry, scheduler, or worker infrastructure.

The YouTube-facing feature may use yt-dlp through the existing extractor provider. yt-dlp is an extraction/provider integration, not a second downloader runtime. Do not assume that every URL supported by yt-dlp is a YouTube URL; provider capability and product UX are separate concerns.

#### Proposed user-facing contract

| Surface | General Downloader | YouTube |
|---|---|---|
| Native command | Existing generic download command/aliases | `.yt <query-or-url>` |
| Input | Direct URL or supported Telegram media | YouTube URL or search query |
| Selection | Only when generic input requires it | Search result, audio/video, format, quality |
| Metadata | Generic filename, MIME/type, size | Title, duration, channel, selected format/quality |
| Progress | Download progress and final delivery status | Extraction/download progress and final delivery status |
| Cancellation | Cancel current owned download | Cancel current YouTube workflow and its owned work |
| Delivery | Shared retained-media delivery | Same shared retained-media delivery |

The exact command aliases, menu layout, and migration behavior must be confirmed against current source before implementation. Preserve existing `.dl`, `.download`, and `.yt` behavior where currently supported; do not silently remove a command or callback route.

#### Audit questions

1. Which code currently decides whether an input is generic HTTP, an extractor URL, or YouTube?
2. Does the Downloader plugin own both generic and YouTube product state?
3. Which YouTube functions can be moved behind a feature boundary without duplicating provider logic?
4. Does the current module registration support a separate YouTube feature module cleanly?
5. Which shared service APIs should remain in internal/services/download?
6. Are provider errors mapped into stable, user-facing error categories?
7. Are filename, MIME type, extension, duration, and media type preserved from extraction through retained storage and Telegram delivery?
8. Are cancellation, panic cleanup, process termination, workspace cleanup, retained asset ownership, and upload failure behavior covered?
9. Are progress updates bounded and joined on every terminal path?
10. Can general downloads continue when the YouTube feature is disabled, and can YouTube be disabled without breaking generic downloads?
11. Are current Assistant, native command, inline, and a2 routes using one domain workflow?
12. Does the existing resource model remain bounded under concurrent extraction and delivery?

#### Proposed implementation phases

- **DL-A — Current-source inventory:** refresh branch HEAD; map commands, module registration, handlers, providers, state, TaskEngine submissions, retained media, and delivery. No production changes.
- **DL-B — Domain and API design:** define General Downloader and YouTube ownership, shared provider contracts, command compatibility, module dependency, and migration strategy. No production changes.
- **DL-C — Shared contract extraction:** only if DL-A proves a concrete coupling problem, extract or stabilize the minimal shared download/extraction contract. Avoid speculative abstractions.
- **DL-D — YouTube feature separation:** move YouTube search, metadata, format selection, and interaction ownership behind the chosen feature boundary while preserving shared execution and delivery.
- **DL-E — General Downloader boundary:** ensure generic URL/file handling has no dependency on YouTube presentation or state.
- **DL-F — UX and failure semantics:** align native command, Assistant-optional, inline, and a2 behavior; improve stage-specific errors, progress, cancellation, and retry presentation.
- **DL-G — Reliability/resource acceptance:** test concurrency, cancellation, process cleanup, metadata propagation, retained media, delivery ambiguity, reload/disable, and resource settling.
- **DL-H — Documentation and closure:** update architecture docs and handoff; record exact commit, tests, measurements, residual risks, and closure decision.

Do not start DL-C or later phases until DL-A and DL-B are reviewed. A package/module split is the intended direction, but its exact shape must follow the source inventory and generated module-registration constraints.

#### Downloader acceptance criteria

- Generic downloads work without YouTube feature initialization.
- YouTube search and selection work without generic Downloader presentation state.
- Both features use the same managed download/extraction, TaskEngine, retained-media, storage, and Telegram delivery infrastructure.
- No second worker pool, scheduler, retry engine, download registry, or media registry is introduced.
- Existing supported command aliases and callbacks remain compatible or have an explicit migration.
- Cancellation reaches queued/running extraction and delivery work according to ownership semantics.
- Process, progress reporter, workspace, and retained-media resources are cleaned up on success, error, panic, cancellation, reload, and shutdown.
- Metadata and file classification survive the entire pipeline.
- No duplicate visible delivery is introduced by retry or ambiguous Telegram send outcomes.
- Resource bounds and representative performance are measured; no optimization is accepted solely on intuition.
- Focused tests and relevant race tests cover the changed boundaries.

### 4.2 MyXL

#### Current source context

MyXL already contains substantial account/client, token refresh, scheduled maintenance, purchase, quota/package, notification routing, menu, native interaction, persistence, and observability code. Recent token-maintenance and notification-routing work must be treated as existing functionality, not reimplemented.

Relevant areas include **plugins/myxl/**, the existing MyXL token-maintenance AI plan, notification routing design/audit documents, and current tests.

#### Enhancement goals

- Keep account identity and token lifecycle under one canonical account/client authority.
- Separate logical areas: account/session, token lifecycle, quota/package discovery, purchase workflow, notification routing, and Telegram presentation.
- Keep command, Assistant, native menu, and a2 interactions on the same service/workflow decisions.
- Keep scheduled token maintenance on the existing Scheduler → JobManager → TaskEngine path.
- Preserve singleflight/concurrency protections and explicit expiry semantics.
- Keep purchase states conservative: unknown/ambiguous outcomes must not be presented as success or blindly retried.
- Preserve safe QR/payment handling and cancellation rules.
- Keep credentials, tokens, OTP, phone numbers, and sensitive request fields out of logs and user-facing diagnostics.
- Measure API request count, latency, concurrency, cache behavior, and maintenance overhead.

#### Audit questions

1. Are account, token, purchase, and notification state transitions owned by one service each?
2. Are command and menu workflows duplicated?
3. Are token refresh and scheduled maintenance sharing the same canonical refresh decision?
4. Are token refresh, API request, and purchase concurrency bounded?
5. Are unknown purchase outcomes represented conservatively?
6. Are notification destinations, peer resolution, and delivery failures explicit?
7. Are logs and diagnostics redacted and low-cardinality?
8. Can each feature be disabled/reloaded without leaving scheduled jobs, callbacks, or retained resources?

Do not reopen already-closed token maintenance or notification phases unless current-source evidence shows a regression or a clearly scoped new requirement.

### 4.3 Assistant

#### Enhancement goals

- Keep provider adapters, conversation orchestration, prompt/context handling, and Telegram presentation as distinct responsibilities.
- Keep Assistant optional for native userbot command availability.
- Ensure provider outages, invalid credentials, rate limits, and timeouts do not block unrelated native commands.
- Bound context size, token budgets, request concurrency, history retention, and provider retries.
- Use existing a2 session and callback lifecycle; do not add another interaction protocol.
- Keep provider-specific configuration and diagnostics isolated from plugin domain logic.
- Preserve cancellation and stale-generation protections during reload or disable.

#### Audit questions

1. Which features are true Assistant domain features and which are native userbot commands?
2. Are provider adapters isolated from Telegram and command routing?
3. Is conversation state bounded and correctly scoped by user/chat/session?
4. Are provider failures mapped to useful, sanitized diagnostics?
5. Can Assistant be disabled while native commands, settings, and Downloader workflows continue?
6. Are a2 sessions and pending actions cancelled or invalidated on reload?

The existing Assistant-optional and parity closure documents remain historical acceptance authorities. Revalidate current source before proposing changes; do not reopen completed work without evidence.

---

## 5. P2 workstreams

P2 begins only after P1 workstreams have been reviewed and explicitly closed.

### 5.1 AFK

Enhancement areas:
- Explicit AFK state transitions and duration handling.
- Mention/reply tracking and duplicate suppression.
- Welcome/reply policy by chat type and origin.
- Per-chat exclusions and configurable response behavior.
- Bounded cooldown and state cardinality.
- Ensure AFK side effects never delay command dispatch.
- Consistent command and a2 status/control UX.

Audit the already completed message-origin, lifecycle, and non-blocking work before proposing changes. Treat existing regression tests as required compatibility gates.

### 5.2 PMPermit

Enhancement areas:
- Clear approval/request state machine.
- Trusted-contact and allow/deny policy.
- Expiration, cleanup, and bounded request history.
- Anti-spam and per-sender/per-chat rate controls.
- Explicit behavior for missing peer/access information.
- Safe approval/decline delivery and duplicate suppression.
- Consistent command and a2 navigation.

Do not create a second private-message admission path. Revalidate the current dispatcher and canonical message-origin/authorization behavior.

### 5.3 Filters and Message Hooks

Enhancement areas:
- Deterministic matching and filter precedence.
- Clear separation between match conditions and actions.
- Bounded regex/input complexity and compiled matcher lifecycle.
- Per-chat configuration and predictable enable/disable behavior.
- Safe action limits and prevention of recursive message loops.
- Explicit conflict resolution and observability.
- Dispatcher non-blocking guarantees.

Do not add a general workflow engine or arbitrary plugin execution language as part of routine filter enhancement.

### 5.4 Userlog

Enhancement areas:
- Accurate actor, peer, origin, reply, and topic context.
- Event selection and destination policy.
- Deduplication and edit/delete behavior.
- Bounded history and retention.
- Sensitive-field redaction and low-cardinality diagnostics.
- Delivery failure behavior that does not block message handling.

---

## 6. P3 workstreams

P3 begins only after P2 review and closure.

### 6.1 Calculator

Enhancement areas:
- Deterministic parser and bounded expression complexity.
- Clear syntax and evaluation errors.
- Native command fallback independent of Assistant.
- a2 keypad as progressive enhancement, not a hard dependency.
- Optional bounded history only if user value justifies persistence.

### 6.2 Other existing utility plugins

Do not pre-commit to a large feature list. First inventory every plugin currently registered in the repository and classify it by:
- user-facing purpose and command surface;
- domain owner and shared dependencies;
- existing tests and known acceptance status;
- persistence and resource ownership;
- user value and usage frequency;
- overlap with another plugin or Assistant feature.

Only promote a utility plugin into P3 when the inventory identifies a concrete improvement with clear acceptance criteria.

---

## 7. Standard workflow for every plugin

Each plugin follows the same gates. A phase cannot silently skip a gate.

### Gate 0 — Repository baseline

1. Fetch current GitHub branch HEAD before each phase.
2. Run `git pull --ff-only` in the working checkout.
3. Record the exact remote/local HEAD and branch.
4. Inspect `git status --short`; do not overwrite unrelated changes.
5. Do not inspect or poll CI unless explicitly requested.

### Gate 1 — Inventory

Record:
- plugin module and generated registration;
- commands, aliases, Assistant surfaces, inline routes, and a2 actions;
- public/internal APIs and call graph;
- domain services, providers, and shared service dependencies;
- state stores, schemas, migrations, and ownership;
- TaskEngine pools/resources, Jobs, schedules, callbacks, and subscriptions;
- network, process, filesystem, storage, retained-media, and Telegram RPC paths;
- tests, benchmarks, documentation, and known accepted risks.

### Gate 2 — Evidence-based audit

Classify each finding as one of:
- confirmed correctness defect;
- architecture/ownership risk;
- performance/resource inefficiency backed by measurement;
- missing test or acceptance coverage;
- UX/functionality gap;
- valid design choice / no change required.

Each finding must include source references, reproduction or evidence, impact, severity, and whether a fix is actually needed. Do not turn observations into defects without proof.

### Gate 3 — Focused AI implementation plan

For every plugin approved for implementation, create a separate design/AI plan document containing:
- baseline and scope;
- goals and explicit non-goals;
- current verified flow;
- target ownership and interfaces;
- phases and dependencies;
- compatibility and migration strategy;
- resource/security constraints;
- test matrix and verification commands;
- acceptance criteria;
- rollback and residual-risk strategy.

The plan must be reviewed before production implementation.

### Gate 4 — Implementation

- Implement one bounded phase at a time.
- Prefer minimal changes at proven ownership boundaries.
- Keep command, Assistant, inline, and a2 routes on one domain workflow.
- Update tests whenever production logic, behavior, or APIs change.
- Update migrations and restart tests when durable state changes.
- Do not mix unrelated plugin refactors in one phase.

### Gate 5 — Verification and closure

- Format all changed Go files with `gofmt` before every Go commit.
- Run focused tests first, then broader tests/race checks appropriate to the changed boundaries.
- Run build, vet, architecture guards, or benchmarks when relevant.
- Do not claim tests or benchmarks that were not run.
- Do not inspect CI unless explicitly requested.
- Record exact commit/HEAD, changed files, test evidence, measurements, compatibility, and accepted residual risks.
- Start the next plugin only after explicit review and closure.

---

## 8. Cross-plugin acceptance checklist

Every enhancement must answer yes to the applicable items:

### Domain and UX
- Does each state transition have one canonical owner?
- Do native commands work without Assistant?
- Do command, Assistant, inline, and a2 routes share domain logic?
- Are errors stage-aware, actionable, and sanitized?
- Are existing commands and callbacks compatible or explicitly migrated?

### Execution and lifecycle
- Does heavy work enter through the existing TaskEngine?
- Are durable schedules/jobs owned by existing Scheduler and JobManager?
- Is cancellation propagated to queued and running work?
- Are owned goroutines, processes, subscriptions, and temporary files stopped and joined?
- Does reload/disable invalidate old interactions and clean owned resources?
- Are retry and idempotency semantics appropriate for external side effects?

### Resource and performance
- Are queue depth, concurrency, payload size, cache/history cardinality, file size, and retention bounded?
- Is idle overhead near zero where practical?
- Does overload reject/defer work predictably instead of growing memory?
- Are measurements taken before accepting an optimization?
- Does plugin activity avoid starving interactive commands?

### Security and observability
- Are user authorization and plugin capabilities checked at the correct boundaries?
- Are SSRF, path traversal, process execution, and file-size constraints handled where relevant?
- Are tokens, OTP, credentials, session data, and private identifiers redacted?
- Are diagnostics useful without high-cardinality labels or secret leakage?

---

## 9. Progress register

| Priority | Workstream | Status | Next action |
|---|---|---|---|
| P1 | Downloader / YouTube domain separation | **PLANNED** | Start DL-A current-source inventory |
| P1 | MyXL domain and workflow refinement | **PLANNED** | Audit only after Downloader closure |
| P1 | Assistant provider/orchestration refinement | **PLANNED** | Audit only after MyXL closure |
| P2 | AFK | **PLANNED** | Revalidate current source and existing hardening |
| P2 | PMPermit | **PLANNED** | Revalidate current source and existing hardening |
| P2 | Filters / Message Hooks | **PLANNED** | Inventory matching/action ownership |
| P2 | Userlog | **PLANNED** | Inventory event/origin/destination ownership |
| P3 | Calculator | **PLANNED** | Audit after P2 closure |
| P3 | Other existing utilities | **NOT INVENTORIED** | Discover from current registered plugin inventory |

No implementation phase is marked complete by this planning document.

---

## 10. Existing documentation and authorities

Re-read these documents at the current source HEAD before relying on historical details:

- `docs/adr/0001-runtime-ownership-and-lifecycle.md`
- `docs/adr/0002-plugin-scope-resource-ownership.md`
- `docs/adr/0003-task-queue-worker-job-semantics.md`
- `docs/adr/0004-plugin-capability-and-platform-boundaries.md`
- `docs/adr/0005-storage-migration-and-namespace-compatibility.md`
- `docs/adr/0006-execution-runtime-redesign.md`
- `docs/design/goultroid-p2-downloader-media-rpc-audit.md`
- `docs/design/goultroid-youtube-downloader-ai-session-handoff.md`
- `docs/design/goultroid-selfinline-help-downloader-p2-ai-session-handoff.md`
- `docs/design/goultroid-myxl-token-maintenance-ai-plan.md`
- `docs/design/myxl-notification-routing-design.md`
- `docs/design/myxl-notification-routing-audit.md`
- `docs/design/goultroid-assistant-optional-userbot-ai-session-handoff.md`

Historical HEADs inside these documents are evidence of past work only. Current source, current tests, and the freshly verified branch HEAD take precedence.

---

## 11. Immediate next step

Start **DL-A — Downloader Current-Source Inventory** as a read-only phase.

Required output:
1. Exact current branch HEAD and clean/dirty worktree state from the working checkout.
2. Complete map of General Downloader and YouTube entry points.
3. Current plugin/module registration and whether separate feature enablement is supported.
4. Call graph for HTTP provider, extractor/yt-dlp provider, TaskEngine submission, progress, retained-media registration, storage, and Telegram delivery.
5. State/resource ownership matrix.
6. Command and callback compatibility matrix.
7. Existing tests and gaps.
8. Findings classified with evidence; no speculative production changes.
9. Recommendation for the smallest safe domain boundary and a separate DL-B design plan.

Do not implement the package split during DL-A. The audit must establish the target boundary first.
