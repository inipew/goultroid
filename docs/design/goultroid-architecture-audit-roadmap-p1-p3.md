# Goultroid Architecture Audit Roadmap — P1–P3

## Status

- Status: **CLOSED (P1, P2, and P3 Complete)**
- Branch: `test-next`
- Planning baseline: `3b6cfaa5840e7d8360d3f368343238694f025090`
- Scope: architecture and execution-path audit; implementation is not authorized merely by this roadmap.
- Ordering: P1 → P2 → P3. Later phases start only after the previous phase is reviewed and explicitly closed.

## 1. Purpose

Continue the main Goultroid architecture review after closure of the MyXL Token Maintenance (M1–M8), Notification Routing (N1–N9), and numeric supergroup-ID resolution correction.

The audit should establish whether the current implementation remains:

- fast and responsive under concurrent work;
- resource-bounded and predictable at idle and under load;
- correct across cancellation, retries, failures, restart, and shutdown;
- consistent with the existing ownership boundaries;
- observable and testable without introducing parallel infrastructure.

This is an investigation roadmap, not a statement that defects exist.

## 2. Non-negotiable architecture constraints

Preserve the existing system boundaries:

- **TaskEngine** owns execution admission, fairness, worker/resource limits, and execution scheduling.
- **JobManager** owns durable logical jobs, attempts, claims, leases, and recovery semantics.
- Telegram RPC FloodWait/retry behavior continues through the shared **Telegram RPCExecutor**.
- Typed a2 interaction/runtime remains the authority for callback/session lifecycle.
- Downloader work must reuse the existing downloader, TaskEngine, retained-media ownership, and Telegram RPC path.
- Runtime lifecycle must use existing ownership and lifecycle mechanisms.

Do not introduce a second runtime, registry, TaskEngine, JobManager, RPC executor, callback protocol, downloader engine, retry engine, or hidden worker/ticker loop. Avoid duplicate sources of truth, unbounded maps/queues/cache cardinality, unnecessary goroutines, and detached work.

## 3. Audit order

| Priority | Workstream | Main concern | Expected outcome |
|---|---|---|---|
| P1 | Scheduler → Jobs → TaskEngine | Admission, durable state, fairness, contention, cancellation, recovery | Prove job execution correctness and bounded behavior |
| P2 | Downloader → Retained Media → Telegram RPC | Download lifecycle, media ownership, disk bounds, upload and FloodWait | Prove media transfer and cleanup are safe and reliable |
| P3 | Runtime Lifecycle → Resource Accounting | Startup/reload/shutdown, goroutine ownership, resource accumulation | Prove runtime resources remain bounded over repeated lifecycle cycles |

### P1 — Scheduler → Jobs → TaskEngine

Detailed scope and execution plan: [P1 Scheduler → Jobs → TaskEngine Audit](./goultroid-p1-scheduler-jobs-taskengine-audit.md).

Focus:
- scheduler due-time and dispatch behavior;
- job admission and TaskEngine queue/worker interaction;
- claims, leases, attempts, state transitions, and crash recovery;
- priority, fairness, starvation, backpressure, and queue saturation;
- cancellation and shutdown semantics;
- FloodWait, retry ownership, and durable yield;
- SQLite contention and query/write amplification;
- latency, allocation, goroutine, and memory behavior.

P1 is complete only when the audit report records verified flows, findings with severity/evidence, test gaps, accepted risks, and an explicit closure decision.

### P2 — Downloader → Retained Media → Telegram RPC

Start only after P1 closure.

Focus:
- command/inline entry points and download admission;
- URL/input validation and metadata/format selection;
- bounded download concurrency, file size, disk/workspace usage;
- progress reporting without excessive edits or polling;
- retained-media ownership, reference lifetime, and cleanup;
- MIME type, extension, filename, and media classification;
- cancellation during download, conversion, retention, and upload;
- Telegram upload through the shared RPCExecutor;
- FloodWait, retry, partial failure, and duplicate-delivery prevention;
- restart/recovery behavior for retained artifacts and interrupted jobs.

Do not add another downloader runtime, queue, retry mechanism, or media registry. Use the existing TaskEngine and retained-media contracts.

P2 closure evidence:
- lifecycle map from request to final delivery/cleanup;
- boundedness analysis for bytes, files, concurrent transfers, and retained lifetime;
- correctness tests for cancellation, malformed metadata, large files, partial failures, and cleanup;
- upload/RPC ownership and FloodWait evidence;
- performance/resource measurements under representative workloads;
- findings and accepted-risk register.

### P3 — Runtime Lifecycle → Resource Accounting

Start only after P2 closure.

Focus:
- application startup and dependency initialization order;
- service start/stop ownership and error propagation;
- reload semantics and prevention of duplicate registrations/subscriptions;
- goroutine ownership, cancellation, and join/wait behavior;
- worker pools, queues, timers, tickers, subscriptions, caches, and retained resources;
- shutdown ordering and bounded shutdown duration;
- repeated start/reload/stop cycles;
- idle baseline versus peak and post-load recovery;
- accuracy and bounded cardinality of diagnostics/resource metrics.

Do not introduce a parallel lifecycle manager or generic resource framework unless a demonstrated gap cannot be solved within existing ownership boundaries.

P3 closure evidence:
- lifecycle/ownership inventory;
- repeated-cycle tests and leak-oriented checks;
- idle/load/post-load measurements for goroutines, memory, queues, workers, and caches;
- proof that cancellation and shutdown join owned work;
- observability coverage and known blind spots;
- findings and accepted-risk register.

## 4. Standard phase workflow

Each workstream follows the same gates:

1. **Baseline:** verify current remote `test-next` HEAD, run `git pull --ff-only`, record exact HEAD, and inspect `git status --short`.
2. **Inventory:** map real code paths, owners, interfaces, state stores, and call edges before proposing changes.
3. **Contract review:** write down expected invariants and compare them with implementation and tests.
4. **Evidence collection:** inspect source and tests; run focused tests/benchmarks only as appropriate. Do not inspect or poll CI unless explicitly requested.
5. **Findings:** classify each item as confirmed bug, architecture risk, performance/resource inefficiency, test gap, or valid design choice. Include file/symbol references and evidence.
6. **Plan:** for nontrivial changes, prepare a separate implementation plan with scope, phases, constraints, tests, and acceptance criteria. Do not mix broad refactoring into an audit.
7. **Hardening:** implement only approved, evidence-backed fixes. Update tests whenever production logic/API changes.
8. **Verification:** format all changed Go files with `gofmt`; run relevant tests, race tests, vet, architecture guards, and diff checks as applicable. Do not claim tests were run unless actually run.
9. **Closure:** document commit(s), exact HEAD, changed files, test evidence, remaining risks, and whether the next priority may start.

## 5. Cross-cutting review checklist

- Is there one clear owner for each state transition and resource?
- Can queues, maps, histories, caches, and retained artifacts grow without a hard bound?
- Can cancellation stop queued and active work, and is owned work joined?
- Can retries or recovery duplicate externally visible side effects?
- Are deadlines and context propagation preserved end to end?
- Are Telegram RPC calls routed through the shared RPCExecutor?
- Are SQLite operations safe under concurrent readers/writers?
- Are logs/metrics useful but free of credentials, tokens, AccessHash, and high-cardinality identifiers?
- Do tests prove negative paths and concurrency invariants, not only happy paths?
- Does the proposed fix reuse existing architecture rather than creating a parallel subsystem?

## 6. Explicit exclusions

Unless a concrete finding requires otherwise, this roadmap does not authorize:
- broad feature parity work or new user-facing features;
- a full voice feature;
- replacing TaskEngine, JobManager, RPCExecutor, or retained-media ownership;
- a general rewrite of scheduler, downloader, or runtime;
- speculative optimization without measurements;
- CI polling or CI configuration changes.

## 7. Progress register

| Workstream | Status | Closure reference |
|---|---|---|
| P1 — Scheduler → Jobs → TaskEngine | **CLOSED** | `229780a4` (P1-A..E closed; no prod changes) |
| P2 — Downloader → Retained Media → Telegram RPC | **CLOSED** | `88e176f1` (P2-D1/D2) + P2-C closure (`goultroid-p2-downloader-media-rpc-audit.md`) |
| P3 — Runtime Lifecycle → Resource Accounting | **CLOSED** | `goultroid-p3-runtime-lifecycle-resource-accounting-audit.md` |

Update this register only when a workstream has been reviewed and its closure evidence is recorded.
