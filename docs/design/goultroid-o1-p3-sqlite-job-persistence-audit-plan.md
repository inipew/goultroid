# O1 — P3 SQLite & Job Persistence Audit Plan

## Status

- **Workstream:** O1 — Runtime Performance & Hot-Path Optimization
- **Phase:** P3 — SQLite & Job Persistence
- **Status:** PLAN CREATED — audit execution pending
- **Branch:** `test-next`
- **Baseline HEAD:** `02dd4559e934161e61a158108c8d520deca3a9e8` (verified from the remote branch before this plan)
- **Prior phases:** P1 and P2 audit work reported closed in commit `02dd4559e934161e61a158108c8d520deca3a9e8`; C1–C3 TaskEngine study/optimization decisions retained as recorded.
- **Production changes:** Not authorized by this plan.
- **CI:** Do not inspect or poll CI unless explicitly requested.

---

## 1. Objective

Determine how SQLite and durable job persistence costs scale as the number of schedules, occurrences, attempts, and outbox records increases.

The audit must identify measured database work on the actual active paths, including:
- scheduler due-time lookup, schedule claiming, and job reconciliation;
- redesigned JobManager recovery, attempt leasing, state commits, and outbox delivery;
- indexes actually selected by SQLite for production query shapes;
- transaction duration, writer-lock wait, busy/locked retries, and connection-pool wait;
- batch claim, recovery, completion, and pruning behavior;
- repeated database reads performed by diagnostics or operational status paths.

The goal is to reduce database cost as durable job cardinality grows without weakening durability, fencing, recovery, or scheduler semantics.

This phase is an audit and measurement phase. A source-level suspicion is not a finding until query-plan, benchmark, trace, or reproducible test evidence supports it.

## 2. Non-Negotiable Constraints

1. Preserve ownership:
   - Scheduler decides when scheduled work is due.
   - JobManager owns durable logical job state, occurrence/attempt transitions, claims, leases, and recovery.
   - TaskEngine owns admission, execution, fairness, and runtime resource constraints.
2. Do not add a parallel job store, second scheduler/recovery loop, second worker pool, retry engine, or new persistence protocol.
3. Do not weaken transaction atomicity, unique constraints, occurrence idempotency, lease fencing, revision/CAS checks, or crash recovery to improve benchmark numbers.
4. Do not change SQLite pool limits, WAL/synchronous/busy-timeout policy, transaction mode, batch sizes, scan intervals, or retention policy during baseline collection.
5. Do not add an unbounded metrics label, per-job diagnostic series, public pprof endpoint, or production-only polling loop.
6. Prefer existing DB metrics, scheduler/JobManager diagnostics, package benchmarks, and test-only harnesses. Any new instrumentation must be bounded and separately approved.
7. Separate the redesigned JobManager path from legacy `scheduled_jobs` compatibility paths. Do not combine their query counts or claim that a legacy query is on the active cutover path without proving it.
8. Preserve all user data. Never run destructive cleanup or migration experiments against the live database.
9. Format all changed Go files with `gofmt`; update tests whenever production logic or APIs change.
10. Do not inspect/poll CI unless explicitly requested.

## 3. Initial Source Inventory (Verified at Baseline HEAD)

These are source observations that define the audit surface, not performance conclusions.

### 3.1 SQLite connection and pool configuration

`internal/database/db.go` currently configures:
- `busy_timeout(5000)`;
- `foreign_keys(ON)`;
- `synchronous(NORMAL)`;
- WAL journal mode for file-backed databases;
- in-memory DB: one open and one idle connection;
- file-backed DB: bounded max-open connections from 4 to 8 based on CPU count, max-idle 2, 5-minute max idle time, and 1-hour max lifetime.

The audit must measure the effect of these existing settings; it must not tune them during baseline collection.

### 3.2 Redesigned durable jobs schema

`internal/jobs/sqlite/schema.go` creates:
- `job_definitions`: indexes `idx_job_defs_scope(scope_owner)`, `idx_job_defs_quota(quota_owner)`;
- `job_schedules`: `idx_job_sched_next(enabled, next_due_at)`;
- `job_occurrences`: `idx_job_occ_state(state, ready_at)`, `idx_job_occ_job(job_id)`, and unique `occurrence_key`;
- `job_attempts`: `idx_job_attempts_lease(state, lease_until)`, unique `task_id`, and unique `(occurrence_id, attempt_no)`;
- `job_outbox`: `idx_job_outbox_undelivered(delivery_state, committed_at)`;
- `job_migration_map`: composite primary key `(legacy_domain, legacy_id, migration_revision)`.

The schema inventory is not evidence that each index is used or optimally ordered. P3 must capture `EXPLAIN QUERY PLAN` for real query shapes at multiple row counts and report selected indexes, scans, temporary B-trees, and estimated/observed work.

### 3.3 Legacy scheduler persistence

`internal/scheduler/repository.go` retains `scheduled_jobs` and `scheduled_job_history`, with indexes:
- `idx_scheduled_jobs_next(next_run_at)`;
- `idx_scheduled_jobs_chat_id(chat_id)`;
- `idx_scheduled_jobs_claim(status, next_run_at, lease_until)`;
- `idx_scheduled_job_history_job(job_id, ran_at)`.

Legacy operations include earliest-due lookup, due-job selection/claim, lease renewal, completion/failure, and history insertion. The claim implementation uses a dedicated connection and `BEGIN IMMEDIATE`, selects a bounded due batch, updates claims, optionally records expired-lease history, then commits.

The active runtime may use redesigned schedule cutover instead. P3 must establish which path is active in each test/runtime configuration and measure legacy compatibility separately.

### 3.4 Existing query-shaping work to validate

The current redesigned store already contains query consolidation intended to reduce repeated reads:
- `AttemptSummary` loads occurrence state, latest attempt, and attempt/deferral counters in one round trip.
- `ListRecoveryCandidates` pages unresolved occurrences and joins latest-attempt/counter data in one query, avoiding a recovery N+1 pattern.
- `PrepareNextAttemptLease` derives the next attempt number and canonical TaskID inside the writer-fenced transaction rather than requiring a caller-side `CountAttempts` round trip.

These are existing design properties. P3 must verify their real query plans and cost under increasing cardinality; do not assume they remain efficient as tables grow.

### 3.5 Existing reconciliation and diagnostics paths to inspect

- `internal/scheduler/engine.go`: scheduler loop, due-time lookup, legacy `reconcileSettledClaims`, `reconcileClaim`, claim lease renewal, and occurrence pruning.
- `internal/jobs/manager_recovery.go`: durable coordinator, deferred deadline lookup, bounded recovery pass, and outbox safety/wake handling.
- `internal/jobs/manager_attempt.go`: attempt preparation and durable completion/defer commits.
- `internal/jobs/sqlite/store.go`: recovery candidates, attempt summary, attempt lease transactions, state commits, blocked-occurrence review, and terminal pruning.
- `internal/database/metrics.go`: bounded in-memory DB operation metrics (maximum 256 labels plus overflow bucket). Confirm whether and where the collector is attached, which operation names are recorded, and whether transaction duration/lock wait is observable.
- Search all operator diagnostics and health/status snapshots for repeated DB reads, especially per-job/per-occurrence loops and calls that independently fetch state already available in a prior snapshot.

---

## 4. Audit Workstreams

### P3-A — Query inventory and actual index usage

1. Enumerate SQL statements on the active redesigned schedule path:
   - schedule cutover/mode reads;
   - earliest due schedule lookup;
   - due schedule batch selection/materialization;
   - unresolved recovery candidate selection;
   - deferred occurrence deadline lookup;
   - latest attempt/attempt summary;
   - claim/lease preparation and renewal;
   - attempt completion, failure, deferral, and occurrence finalization;
   - outbox pending selection/acknowledgement;
   - terminal occurrence pruning.
2. Enumerate the legacy `scheduled_jobs` compatibility queries separately:
   - `GetEarliestDueTime`;
   - `ClaimDueScheduledJobs`;
   - `GetScheduledJob` / reconciliation;
   - lease renewal;
   - completion/failure/history.
3. For each hot query, record:
   - exact SQL and bind predicates;
   - expected cardinality and ordering;
   - relevant schema constraints/indexes;
   - `EXPLAIN QUERY PLAN` at small, medium, and large seeded cardinalities;
   - whether SQLite uses an index, performs a table/index scan, or builds a temporary B-tree;
   - rows returned and rows changed where observable.
4. Check whether index prefix/order matches the actual WHERE + ORDER BY + LIMIT shape. Identify redundant indexes and write amplification, but do not drop or add any index in this phase.
5. Verify foreign-key cascade assumptions against the actual connection configuration and test setup.

Deliverables:
- SQL inventory with owning package/symbol;
- query-plan evidence per hot query;
- index-use matrix: used / partially used / not used / not yet measured;
- cardinality and workload parameters.

### P3-B — Transaction duration and lock contention

Inspect all transaction boundaries in both redesigned and legacy stores:
- `BeginTx` deferred transactions that first execute a no-op UPDATE to acquire writer intent;
- explicit `BEGIN IMMEDIATE` legacy claim transaction;
- multi-row claim/lease operations;
- attempt preparation and completion/defer/finalize operations;
- outbox delivery and acknowledgement;
- schedule registration/publish/recovery transactions;
- migration transactions only where startup cost is relevant.

Measure separately:
1. time waiting to acquire a DB connection;
2. time waiting for SQLite writer intent / busy lock;
3. transaction body duration;
4. commit duration;
5. total operation duration;
6. busy/locked retries, timeout, and cancellation outcomes.

Use concurrent reader/writer workloads and competing claimers. Report transaction p50/p95/p99 with sample counts, throughput, error rate, and maximum observed duration. A 5-second busy timeout must not be mistaken for normal query latency.

Inspect lock scope: no network calls, Telegram RPC, TaskEngine waits, handler execution, JSON-heavy unrelated work, or long application-level loops should be held inside a SQLite write transaction unless the existing atomicity contract explicitly requires it.

Deliverables:
- transaction map and lock-acquisition strategy;
- contention benchmark results;
- long-transaction and retry evidence;
- concurrency correctness findings.

### P3-C — Batch claim, recovery, completion, and cleanup

1. Trace redesigned `ProcessDueSchedules` and the underlying schedule store claim/materialization operations.
2. Trace recovery page size, maximum scan limit, candidate query, per-candidate DB work, and wake/safety cadence.
3. Trace legacy scheduler claim batch limits and per-claimed-row writes/history.
4. Trace completion and failure paths: number of SQL statements, transactions, and round trips per logical completion.
5. Trace durable deferral and retry path: verify whether attempt + occurrence changes are atomic and how next wake is found.
6. Trace outbox delivery: bounded page size, per-row vs batch acknowledgement, replay behavior, and idle wake behavior.
7. Trace pruning/retention: batch limits, index support, cascades, and repeated scans when there is no eligible data.
8. Compare batch sizes and total DB operations at 1, 100, 1,000, and 10,000 relevant rows where practical. Use isolated disposable databases and deterministic fixtures.

Deliverables:
- per-lifecycle SQL round-trip count;
- batch-size and throughput table;
- recovery/prune work-per-pass and backlog-drain measurements;
- boundedness and idle-work observations.

### P3-D — Repeated reads in diagnostics and operational paths

1. Inventory diagnostics, health, status, scheduler snapshots, and JobManager recovery reports that query SQLite.
2. Identify repeated reads of the same job, occurrence, attempt, or outbox row within one diagnostics request or periodic pass.
3. Determine whether a diagnostic path performs O(N) queries, repeated COUNT/LatestAttempt lookups, or per-row lookups after a bulk query already returned the required fields.
4. Separate user-triggered diagnostics from periodic background reads.
5. Confirm labels and metric dimensions remain bounded; never introduce raw job IDs, user IDs, peer IDs, or occurrence IDs as metric labels.
6. Measure diagnostics cost at increasing job counts and while concurrent claim/completion traffic is active.

Deliverables:
- diagnostics read map;
- repeated/N+1 query evidence;
- DB query count and duration per diagnostic request/pass;
- recommendations for reusing existing snapshots or consolidating reads, only where consistency semantics permit.

### P3-E — Benchmark, regression coverage, and findings

Before adding benchmarks, inventory existing tests and benchmarks, especially:
- `internal/database/pool_benchmark_test.go`;
- `internal/jobs/sqlite/*_test.go` (concurrency, protocol, recovery, resource persistence, store);
- `internal/jobs/*_test.go` (recovery, persistence pump, retries, attempts);
- `internal/scheduler/*_test.go` (repository concurrency, reconciliation, timing, recovery);
- existing scheduler runtime execution E3 benchmarks and diagnostics tests.

Add only narrowly scoped test/benchmark coverage if existing coverage cannot answer the audit question. Do not put brittle absolute latency thresholds in unit tests. Correctness tests should assert atomicity, fencing, no duplicate claims, bounded batch behavior, and safe cancellation.

Every benchmark report must record:
- exact HEAD, Go version, OS/kernel, architecture, CPU/RAM, GOMAXPROCS;
- SQLite driver/version and relevant PRAGMAs;
- database mode (file-backed WAL vs in-memory);
- row cardinalities, distribution/state mix, batch size, reader/writer concurrency;
- warm-up, iteration count, `-benchmem`, and race/non-race mode;
- whether data is synthetic and whether the DB is disposable.

Deliverables:
- reproducible commands and raw outputs;
- findings ranked by severity, likelihood, impact, and confidence;
- explicit non-findings and unmeasured areas;
- separate optimization proposals with expected benefit, correctness risk, resource impact, and before/after validation plan.

---

## 5. Required Workload Matrix

Use deterministic fixtures and disposable databases. Preserve the same workload shape when comparing changes.

| Workload | Cardinality / concurrency | What it answers |
|---|---|---|
| Startup restore | 1 / 100 / 1,000 / 10,000 definitions and schedules where practical | Definition/schedule restore cost and startup query growth |
| Due schedule lookup | 1 / 100 / 1,000 / 10,000 schedules, mostly future with controlled due fraction | Index selection, rows scanned, due lookup latency |
| Batch claim | due batches of 1 / 10 / configured maximum; concurrent claimers | Claim throughput, lock wait, duplicate-claim safety |
| Recovery scan | unresolved occurrence sets of 1 / 100 / 1,000 / 10,000 with varied attempts | Candidate query plan, bounded scan, per-candidate extra reads |
| Attempt lifecycle | prepare + start + complete/fail/defer | Transactions and SQL round trips per attempt |
| Outbox drain | empty, small pending batch, large pending backlog | Batch behavior, acknowledgement writes, idle polling |
| Diagnostics | empty/small/large job state and concurrent writes | Repeated DB reads and snapshot consistency |
| Pruning | no eligible rows, sparse eligible rows, full eligible batch | Index use and bounded cleanup cost |
| Writer contention | concurrent readers plus 1 / 2 / 4 writers or claimers | Busy/locked rate, lock acquisition, transaction tail latency |
| Idle | no due jobs, no unresolved work, no pending outbox | Periodic database work and wake-driven behavior |

Do not force 10,000-row tests if fixture cost makes them impractical; record the largest tested cardinality and explain the limitation. Avoid using production data or sensitive payloads.

---

## 6. Evidence and Finding Rules

Classify each observation as one of:
- **Measured finding:** backed by benchmark, query plan, runtime metric, or reproducible test.
- **Source observation:** directly visible in code/schema but not yet measured.
- **Hypothesis:** plausible concern requiring a targeted experiment.
- **Non-finding:** examined and supported as acceptable under stated workload/constraints.

Severity:
- **Critical:** data corruption/loss, duplicate execution with material side effects, or broad outage.
- **High:** unbounded DB work/resource growth, stuck recovery, systemic lock contention, or broad job loss/duplication.
- **Medium:** material latency/throughput degradation, repeated query amplification, or significant test gap.
- **Low:** bounded inefficiency or diagnostics/maintainability issue without demonstrated user impact.

Every finding must include:
- ID and severity;
- exact package/file/symbol/query;
- active or legacy path designation;
- observed vs expected behavior;
- evidence and reproducible command/workload;
- impact and confidence;
- narrow proposed remediation;
- correctness/race regression test;
- status and commit reference.

Never describe an index as "used" based solely on its existence in schema. Never call a query N+1 without counting the actual round trips. Never call a transaction long without measuring its duration or demonstrating a lock held across unrelated work.

---

## 7. Phase Gates

### Gate A — P3 source and query inventory complete
All active and legacy persistence paths are mapped, SQL shapes are catalogued, and actual query plans are captured for the hot paths.

### Gate B — P3 performance and contention measured
Representative cardinalities, batch behavior, transaction durations, writer contention, recovery, diagnostics reads, and idle DB activity are measured with reproducible evidence.

### Gate C — Findings reviewed
Findings and non-findings are ranked. No production optimization is authorized by this plan.

### Gate D — Separate implementation approval
Only after review may a separate plan authorize a specific schema/index/query/transaction optimization. It must preserve fencing, idempotency, atomic state transitions, recovery, and ownership boundaries, and include before/after benchmarks plus regression/race tests.

---

## 8. Explicitly Out of Scope

- P4 Dispatcher & Message Processing (Telegram update → classification → context → middleware → command handler).
- P5 Cache & Background Housekeeping (long-lived caches, idempotency retention, inline state, rate limiter cleanup).
- TaskEngine control protocol changes or worker-pool redesign.
- Telegram RPCExecutor/FloodWait policy changes.
- New database engine, ORM, storage service, or parallel persistence runtime.
- Production schema changes, index changes, PRAGMA tuning, batch-size tuning, or transaction redesign before a separately approved implementation plan.

---

## 9. Progress Register

| Area | Status | Evidence |
|---|---|---|
| Remote HEAD verification | COMPLETE | `test-next` at `02dd4559e934161e61a158108c8d520deca3a9e8` |
| SQLite configuration inventory | SOURCE INVENTORY | `internal/database/db.go` |
| Redesigned job schema/index inventory | SOURCE INVENTORY | `internal/jobs/sqlite/schema.go` |
| Legacy scheduler schema/query inventory | SOURCE INVENTORY | `internal/scheduler/repository.go` |
| Active-vs-legacy route confirmation | PENDING | Must verify runtime cutover configuration and tests |
| Query plans / actual index usage | NOT STARTED | Requires EXPLAIN QUERY PLAN against seeded DBs |
| Transaction duration / lock contention | NOT STARTED | Requires instrumented local benchmarks |
| Batch claim/recovery/completion | NOT STARTED | Requires call-count and workload measurements |
| Diagnostics repeated reads | NOT STARTED | Requires diagnostics call-path inventory and DB query counts |
| Findings and optimization proposals | NOT STARTED | No production changes authorized |

