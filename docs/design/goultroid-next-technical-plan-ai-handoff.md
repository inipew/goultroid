# Goultroid — Next Technical Plan & AI Handoff Guide

Panduan implementasi lanjutan berbasis real condition repository

| Field | Value |
|---|---|
| Repository | github.com/inipew/goultroid |
| Branch | test-next |
| Authoritative HEAD | 08626042b0645df958fc5b9e95c10906e4441813 |
| HEAD message | fix(telegram): bind resolver to shared rpc executor |
| Snapshot date | 19 September 2026 (Asia/Jakarta) |

> **Tujuan dokumen**
> Dokumen ini adalah source-of-context untuk sesi AI lanjutan. AI berikutnya harus memulai dari HEAD aktual, membandingkan drift terhadap baseline ini, lalu menggunakan source code sebagai source of truth. Dokumen audit lama hanya boleh dipakai sebagai histori; setiap temuan wajib diverifikasi ulang terhadap HEAD.

## 1. Cara Menggunakan Dokumen Ini

- Gunakan dokumen ini sebagai handoff, bukan sebagai pengganti source code.

- Sebelum membuat patch apa pun, fetch refs/heads/test-next dan catat HEAD baru.

- Jika HEAD berbeda dari 08626042, compare commit range dan baca ulang file yang tersentuh sebelum menerapkan rencana.

- Prioritas di bawah adalah urutan teknis, bukan kewajiban untuk memaksakan desain lama bila source sudah berubah.

- Setiap commit harus kecil, punya invariant yang jelas, test/regression gate, dan tidak boleh mengklaim CI green tanpa bukti run aktual.

## 2. Hierarki Source of Truth

| Urutan | Sumber | Aturan |
| --- | --- | --- |
| 1 | Source code pada HEAD test-next | Paling authoritative. Jika dokumen berbeda dengan code, code menang. |
| 2 | Architecture/regression tests | Menjelaskan invariant yang sengaja diproteksi dari regresi. |
| 3 | ADR execution redesign | Menjelaskan ownership boundary dan alasan desain. |
| 4 | Dokumen audit/benchmark | Evidence historis; wajib cek commit basisnya sebelum dipakai. |
| 5 | Chat/memory sesi sebelumnya | Hanya petunjuk. Jangan dijadikan fakta tanpa verifikasi source. |

## 3. Real Condition Snapshot

### 3.1 Architecture authority yang sekarang berlaku

| Subsystem | Authority | Current invariant |
| --- | --- | --- |
| TaskEngine | Pemilik physical execution/admission/fairness/resources | Satu coordinator mutable execution state; workers hanya mengeksekusi assignment yang sudah diberi permit. |
| JobManager | Pemilik durable logical work, occurrence, attempt, retry/recovery | Tidak boleh menjadi physical worker pool kedua. |
| Scheduler | Pemilik WHEN/deadline/wake | Tidak boleh kembali memiliki concurrency semaphore eksekusi sendiri. |
| RPCExecutor | Pemilik Telegram timeout/retry/FloodWait/rate-limit/metrics | Production Service dan Resolver berbagi executor/limiter yang sama. |
| Runtime | Pemilik lifecycle/quiesce/drain/stop | Shutdown memakai satu global budget; plugin/resource cleanup dibatasi. |
| Persistence | Durability/control infrastructure | Queue bounded count + retained bytes; bukan feature execution owner. |

### 3.2 Status perubahan penting yang SUDAH tertutup

| Finding lama | Status | Real condition HEAD |
| --- | --- | --- |
| RPC limiter O(N) cleanup per Reserve | CLOSED | Hierarchical limiter memakai bounded structures; old full-map hot-path audit tidak lagi berlaku. |
| Limiter fail-open / active penalty eviction | CLOSED | Capacity/overflow semantics sekarang fail-closed dan menjaga cooldown. |
| Media RPC logical-only admission | CLOSED | Physical upload/download chunk melewati shared RPCExecutor. |
| Serial broadcast send + sleep | CLOSED | Broadcast fan-out melalui TaskEngine dengan bounded in-flight. |
| Jobs timer per deferred occurrence | CLOSED | Semua retry backoff positif sekarang dipersist sebagai ready_at dan coordinator deadline tunggal. |
| Outbox 100-item stall sampai ticker | CLOSED | Drain multi-batch + self-wake. |
| Plugin cleanup melewati hard shutdown | SUBSTANTIALLY CLOSED | Shared CallbackExecutor + context-aware cleanup + in-flight fencing. |
| Scope.Close waiter goroutine per close | CLOSED | Scope memakai idle channel owned by scope, bukan wg waiter goroutine. |
| Delayed action count-only retention | CLOSED | Count + 4 MiB retained-byte budget dan shutdown drops runtime channel references. |
| PersistencePump count-only retention | CLOSED | 64 MiB queued+in-flight retained-byte budget; TaskEngine uses sized admission. |
| Downloader JobDefinition per invocation | CLOSED | Ephemeral scoped TaskEngine continuations; direct HTTP download-only, extractor download+process. |
| Peer entity cache count-only | CLOSED | Entity cache sekarang punya byte cap dan eviction/oversize counters. |
| Resolver standalone/unshared RPC state | CLOSED | Production resolver constructed with client shared executor; standalone path tetap bounded. |

> **Evidence caveat**
> Dokumen benchmark B0-B8 masih berbasis commit 0311e6b (15 Sep 2026), bukan HEAD 08626042. GitHub juga tidak menunjukkan commit status maupun Actions run untuk HEAD saat snapshot ini dibuat. Karena itu benchmark lama adalah historical evidence, bukan acceptance evidence untuk HEAD sekarang.

## 4. Prioritas Lanjutan

| Priority | Workstream | Type | Why now |
| --- | --- | --- | --- |
| P0 | Durable RateLimit/FloodWait yield protocol | Correctness + worker occupancy | Typed defer signal hilang di TaskResult; Job memakai in-memory lastErr side channel; deferral tetap dapat menghabiskan MaxAttempts. |
| P0 | Current-HEAD verification gate | Evidence | Belum ada CI/Actions status untuk HEAD; benchmark report stale. |
| P1 | RPC worker occupancy under limiter wait | Performance | RPCExecutor masih dapat tidur di physical TaskEngine handler; perlu yield hanya untuk durable context. |
| P1 | Recovery/attempt accounting redesign | Durability | Perlu memisahkan physical attempt number, retry budget, dan deferral budget. |
| P1 | Goroutine ownership audit | Lifecycle | Bukan semua goroutine harus Supervisor, tetapi semua harus owner+cancel+join+panic+bound. |
| P2 | RPC/Resolver observability | Observability | Latency distribution, retry attempts, limiter saturation, cache hit/miss belum cukup untuk profiling. |
| P2 | Full soak/profile refresh | Performance evidence | Perlu idle, same-peer saturation, media, outbox, shutdown failure injection, heap plateau. |
| P3 | Docs/conformance cleanup | Maintenance | Update audit lama supaya tidak terus menghidupkan false-positive yang sudah closed. |

## 5. Phase 0 - Session Bootstrap & Drift Control

> **Mandatory first action for every AI session**
> Jangan langsung patch. Fetch refs/heads/test-next, compare HEAD terhadap baseline dokumen, lalu baca ulang file yang berubah. Patch yang dibuat terhadap parent lama tidak boleh di-fast-forward jika branch sudah bergerak.

1. Fetch exact branch ref dan simpan SHA parent yang akan dipakai commit.

2. Compare baseline dokumen -> HEAD baru. Kelompokkan file yang menyentuh jobs/taskengine/telegram/runtime/plugin/resource/app.

3. Re-run source probes untuk finding yang akan dikerjakan. Jangan menganggap dokumen audit masih benar.

4. Sebelum update ref, fetch ref sekali lagi. Bila parent berubah, rebuild patch di atas HEAD baru.

```text
Baseline document HEAD: 08626042b0645df958fc5b9e95c10906e4441813
Branch: test-next
Rule: source > architecture tests > ADR > audit docs > chat context
```

## 6. Phase 1 - Structured RateLimit Signal Across TaskEngine

### 6.1 Current problem

- core.RateLimitError sudah punya RateLimitWait(), tetapi executeAssignment mengubah error menjadi OutcomeFailed + Failure.Message.

- JobManager saat ini menyimpan typed error di trackedOccurrence.lastErr untuk membaca RateLimitWait. Itu hanya in-memory dan tidak survive restart.

- TaskResult tidak punya CauseRateLimited atau RetryAfter, sehingga durable commit tidak dapat menjadi source of truth untuk deferral.

- Akibatnya attempt yang sebenarnya "defer dan coba lagi setelah server/limiter mengizinkan" tetap terlihat sebagai failed physical attempt biasa.

### 6.2 Recommended design

```text
Task handler error
    -> TaskEngine classifies structured deferral
    -> TaskResult {
         Outcome: Failed (or a dedicated Deferred outcome if chosen),
         Cause: RateLimited,
         RetryAfter: duration,
         Failure.Message: diagnostic only
       }
    -> Job Commit atomically records AttemptDeferred + occurrence.ready_at
    -> physical worker is released
    -> recovery coordinator owns wake/deadline
```

- Tambahkan structured signal pada tasks.TaskResult. Minimum: CauseRateLimited + RetryAfter. Hindari menjadikan Failure.Message sebagai protocol field.

- TaskEngine mendeteksi interface kecil RateLimitWait() time.Duration sebelum generic failure branch. Jangan import concrete Telegram error type ke TaskEngine.

- FinishedAt + RetryAfter dapat digunakan untuk menghitung deadline deterministik; store sebaiknya menerima readyAt eksplisit pada commit deferred.

- Tetap simpan Failure.Message untuk diagnosis, tetapi recovery tidak boleh bergantung pada parsing string.

### 6.3 Non-goals / safety

- Jangan menganggap timeout/network error sebagai free deferral. Hanya explicit limiter/server RateLimit signal yang aman diperlakukan sebagai deferral.

- Jangan menghapus retry accounting terlebih dahulu. Attempt numbering harus tetap monoton untuk fencing/audit.

- Jangan membuat Telegram package menjadi owner Job retry semantics; Telegram hanya mengeluarkan structured signal.

## 7. Phase 2 - Durable Deferral Protocol & Attempt Accounting

### 7.1 Domain changes

- Tambahkan AttemptDeferred pada jobs.AttemptState. State ini terminal untuk satu physical attempt, tetapi tidak otomatis mengonsumsi retry budget.

- Pertahankan attempt_no sebagai physical attempt sequence. Jangan reuse attempt number setelah defer.

- Pisahkan CountAttempts() dari retry budget consumption. Tambahkan query/method khusus seperti CountRetryBudgetUses().

- Tambahkan deferral budget yang eksplisit supaya occurrence tidak immortal. Rekomendasi domain: MaxDeferrals dan/atau MaxDeferredElapsed; nilai final harus dipilih setelah benchmark/soak, bukan asal hardcode.

### 7.2 Atomic store protocol

```text
CommitAttemptDeferred(attemptID, leaseEpoch, readyAt, reason)
SQLite transaction:
  1. acquire writer intent
  2. verify attempt exists + lease_epoch matches
  3. verify attempt is latest physical attempt
  4. verify occurrence.state == dispatched
  5. update attempt.state = deferred, finished_at, error/reason
  6. update occurrence.ready_at = readyAt, revision++
  7. commit atomically
```

- Atomicity menghilangkan crash window "attempt sudah selesai tetapi ready_at belum tersimpan".

- Replay dengan attemptID/epoch/result yang sama harus idempotent; conflicting replay harus fail fenced.

- Cancellation epoch tetap menang. Deferred commit tidak boleh menghidupkan occurrence yang sudah cancelled.

### 7.3 Recovery changes

- Recover harus menerima latest AttemptDeferred sebagai predecessor valid bila ready_at <= now.

- MaxAttempts harus dibandingkan terhadap retry-budget uses, bukan physical row count.

- Deferral budget diperiksa terpisah. Bila terlampaui, finalize dengan alasan eksplisit agar diagnosis tidak hilang.

- Crash after deferred commit but before in-memory untrack harus converge tanpa membuat duplicate simultaneous attempt.

## 8. Phase 3 - Yield RPC Waits Only for Durable Work

### 8.1 Why not make all RPC waits fail-fast

- Interactive command dan broadcast saat ini tidak memiliki durable continuation semantics yang sama dengan Job occurrence.

- Membuat seluruh limiter denial return segera akan menurunkan worker occupancy tetapi meningkatkan user-visible drops/retries yang tidak terkoordinasi.

- Karena itu yield harus capability/context-aware, bukan global behavior.

### 8.2 Proposed execution context

```text
TaskEngine sees spec.Job != nil
    -> attaches execution metadata to runCtx: CanDurablyYield=true
RPCExecutor sees wait/FloodWait
    -> durable context: return RateLimitError immediately when policy says yield
    -> interactive context: existing inline wait/retry remains bounded
Job durable commit
    -> AttemptDeferred + ready_at
```

- Gunakan package kecil/generic execution context; hindari RPCExecutor bergantung langsung ke JobManager.

- Explicit server FloodWait sebaiknya selalu yield untuk durable context.

- Limiter reservation wait pendek perlu threshold berbasis benchmark. Jangan pilih angka permanen sebelum mengukur DB churn vs worker occupancy.

- Non-idempotent mutation tetap mengikuti existing retry safety. Yield hanya pada rate-limit signal yang jelas, bukan ambiguous timeout.

## 9. Phase 4 - Correctness & Recovery Test Matrix

| ID | Scenario | Required result |
| --- | --- | --- |
| R1 | MaxAttempts=1 + explicit FloodWait | Occurrence deferred, bukan failed; retry budget tetap tersedia. |
| R2 | Deferred commit then process restart | Recovery menunggu ready_at lalu membuat attempt baru tepat satu kali. |
| R3 | Crash between physical completion and acknowledgement | Commit replay idempotent; tidak ada duplicate live attempt. |
| R4 | Cancellation while deferred | Cancelled wins; deadline wake tidak redrive. |
| R5 | Repeated FloodWait | Physical attempt_no naik; retry budget tidak naik; deferral budget naik. |
| R6 | Deferral budget exceeded | Occurrence terminal failed dengan reason yang dapat diobservasi. |
| R7 | Network timeout after non-idempotent mutation | Tetap failure/ambiguous semantics; jangan diubah menjadi free defer. |
| R8 | Limiter short wait interactive | Behavior existing tetap inline/bounded. |
| R9 | Limiter wait durable | TaskEngine worker dilepas cepat; ready_at tersimpan atomik. |
| R10 | Two recovery passes race | Lease/revision fencing mencegah duplicate attempt. |

## 10. Phase 5 - Worker Occupancy & Performance Gate

### 10.1 New benchmarks required

| ID | Workload | What to prove |
| --- | --- | --- |
| P-RPC-1 | Same peer, 1 task | Baseline overhead and limiter wait path. |
| P-RPC-2 | Same peer, 100 concurrent durable jobs | Verify workers do not sleep behind same peer limiter. |
| P-RPC-3 | Same peer, 1000 durable jobs | Queue/backpressure/goroutine/heap plateau. |
| P-RPC-4 | Limiter cardinality 1/100/1000/4096 keys | Confirm current hierarchical limiter scaling remains bounded. |
| P-RPC-5 | Interactive vs durable mixed traffic | Interactive latency not starved by deferred durable backlog. |
| P-MEDIA | 10/100/500 MiB upload/download | Count physical RPCs, limiter waits, CPU/RSS, per-chunk metrics. |
| P-IDLE | 30 minute idle | CPU, goroutines, DB activity, timers stable. |
| P-SHUT | Hung cleanup + active tasks | Global shutdown deadline remains authoritative; bounded orphan callbacks. |

### 10.2 Metrics that must be captured

- TaskEngine: queued/running/dispatching per pool, admission wait, worker active/idle, resource utilization.

- RPC: request latency p50/p95/p99, attempts/request, limiter wait by scope, FloodWait count/duration, yield count.

- Jobs: retry budget uses, deferral count, earliest ready_at, recovery redrives, stale/orphaned counts.

- Runtime: goroutine count, shutdown phases, lifecycle callback active/peak/saturated.

- Memory: RSS, heap in-use, retained bytes in TaskEngine/PersistencePump/delayed actions/cache.

- DB: open/in-use connections, write latency, outbox backlog, recovery query cadence.

### 10.3 Acceptance principles

- Durable wait >= yield threshold must not occupy a TaskEngine physical worker for the wait duration.

- No monotonic goroutine/RSS growth during repeated deferral/recovery cycles.

- Interactive p95 latency must remain within the same order of magnitude when durable same-peer backlog is saturated.

- DB write amplification from short deferrals must be measured before selecting the final yield threshold.

- Any performance claim must name exact commit, command, machine/Go version, and benchmark duration.

## 11. Phase 6 - Observability Upgrade

| Area | Required telemetry |
| --- | --- |
| RPC latency | Per method/family latency histogram or bounded buckets; current request counts alone are insufficient. |
| RPC attempts | Attempts distribution; distinguish first-try success vs retries. |
| Limiter saturation | Denied/yielded reservations by scope + wait distribution. |
| Job deferrals | Current deferred occurrences, total deferrals, deferral-budget exhaustion, earliest ready_at. |
| Resolver | Memory/persistent/negative cache hit/miss, singleflight joins, network resolutions, evictions. |
| Cleanup executor | Already has capacity/active/peak/saturated; preserve in App.Diagnostics. |
| Persistence/delayed | Keep byte retention + rejection counters visible. |

> **Cardinality rule**
> Metrics labels must be bounded. Jangan gunakan raw peer ID, task ID, occurrence ID, URL, atau method variant yang tidak dibatasi sebagai metric label.

## 12. Phase 7 - Goroutine Ownership Audit

### 12.1 Audit rule

- Target bukan "semua goroutine harus Supervisor". Target adalah setiap goroutine mempunyai owner, cancellation path, join semantics, panic handling, dan hard cardinality bound.

- Supervisor Stop masih memakai satu waiter goroutine per supervisor lifetime; ini bounded dan bukan target prioritas kecuali profiling menunjukkan masalah.

- Scope.Go sekarang punya max 64 + idle signal waiter-free; jangan regress ke goroutine-per-Close waiter.

### 12.2 Inventory procedure

1. Search seluruh production `.go` untuk `go ` dan klasifikasikan: runtime supervisor, subsystem-owned fixed worker, bounded one-shot, callback executor, atau detached/unowned.

2. Untuk setiap direct goroutine, tulis owner/context/join/panic/bound pada audit table.

3. Jika tidak ada owner atau bound, migrate ke Supervisor/Scope/TaskEngine atau buat lifecycle owner eksplisit.

4. Tambahkan architecture test untuk pola raw goroutine yang dilarang pada jalur kritikal.

## 13. Phase 8 - Current-HEAD Verification & Evidence Refresh

### 13.1 Minimum gate before declaring conformance

```text
go test ./...
go test -race ./...
go test ./internal/architecture -count=1
go test -run=^$ -bench=BenchmarkB ./internal/taskengine -benchmem -benchtime=1s
# plus new RPC/yield benchmarks added by this roadmap
```

- Jika local environment tidak dapat clone/run, jangan klaim test green. Record limitation dan gunakan GitHub Actions hanya bila run aktual tersedia.

- Update docs/design/execution-redesign/05-benchmark-report.md dengan HEAD baru; benchmark 0311e6b tidak boleh menjadi final evidence untuk 08626042+.

- Simpan before/after data untuk ns/op, B/op, allocs/op, p95/p99 latency, goroutine peak, heap/RSS plateau, dan shutdown duration.

### 13.2 Soak scenarios

- Idle 30-60 min tanpa Telegram traffic: verify no periodic CPU/DB churn selain safety scans yang disengaja.

- Same-peer FloodWait storm: durable occurrence backlog + restart di tengah deferral.

- Outbox 1,000+ pending rows: no 30s stepwise stall.

- Plugin cleanup callbacks that never return: global shutdown returns by deadline; CallbackExecutor capacity stays bounded.

- Large media transfer: 500 MiB with per-chunk physical RPC metrics and no double retry policy.

- Downloader direct HTTP vs extractor: direct HTTP never holds process; extractor holds process for actual extractor execution.

- Peer resolver high-cardinality names: cache byte cap, singleflight, network slots, SQLite fallback.

## 14. Suggested Commit Sequence

| Order | Commit intent | Scope |
| --- | --- | --- |
| 1 | feat(tasks): preserve rate-limit deferral metadata | TaskResult CauseRateLimited + RetryAfter + executor classification + unit tests. |
| 2 | feat(jobs): persist deferred attempt disposition | AttemptDeferred + atomic store method + schema/store tests. |
| 3 | fix(jobs): separate retry and deferral budgets | CountRetryBudgetUses + deferral budget + recovery changes. |
| 4 | fix(telegram): yield durable rpc waits | Durable execution-context marker + RPCExecutor behavior + tests. |
| 5 | test(execution): add durable rate-limit recovery matrix | Crash/restart/cancel/fencing integration tests. |
| 6 | bench(execution): add limiter occupancy benchmarks | Same-peer durable vs interactive + cardinality benchmarks. |
| 7 | feat(observability): expose deferral and rpc latency metrics | Diagnostics/metrics only after protocol is stable. |
| 8 | test(runtime): complete goroutine ownership audit | Architecture guards and lifecycle tests. |
| 9 | docs: refresh current-head conformance evidence | Update audit/benchmark docs after tests are actually run. |

## 15. Risk Register

| Severity | Risk | Mitigation |
| --- | --- | --- |
| High | Free deferral creates immortal occurrence | Add MaxDeferrals/MaxDeferredElapsed and explicit terminal reason. |
| High | Crash window splits attempt result and ready_at | Commit deferred attempt + occurrence deadline atomically. |
| High | Retry semantics duplicate across RPC/gotd/Jobs | One authority per layer; do not add second timer/retry loop. |
| High | Non-idempotent mutation replay | Only explicit rate-limit is deferrable; ambiguous failures stay failure semantics. |
| Medium | Tiny limiter waits cause DB churn | Benchmark threshold before finalizing durable yield minimum. |
| Medium | Metrics cardinality growth | Bound labels; use aggregate scope/family/method only. |
| Medium | Docs resurrect obsolete findings | Every audit item must include verified-against commit SHA. |
| Medium | Branch moves during AI patch | Re-fetch ref before tree/commit/ref update; rebuild on new parent. |

## 16. Anti-Patterns: Jangan Dilakukan

- Jangan membuat timer/goroutine per delayed occurrence. Timing harus deadline/wake coordinator-owned.

- Jangan membuat JobDefinition unik per invocation untuk kerja ephemeral.

- Jangan membypass TaskEngine untuk command/resource-heavy execution.

- Jangan menambah retry loop kedua di service/plugin jika RPCExecutor sudah menjadi owner retry.

- Jangan menunggu FloodWait panjang di durable TaskEngine worker setelah durable-yield protocol tersedia.

- Jangan memakai error string parsing sebagai protocol durability.

- Jangan menghapus resource/attempt record sebelum cleanup/commit benar-benar sukses.

- Jangan hanya bound jumlah object bila object dapat menangkap payload/closure besar; gunakan retained-byte budget.

- Jangan memakai mutable package globals sebagai source runtime production config.

- Jangan menyatakan CI/test/benchmark lulus tanpa hasil aktual untuk commit yang sama.

## 17. Workflow Sesi AI Berikutnya

1. Refresh branch context dan record HEAD.

2. Read this document only to choose target area; then read actual source files for that target.

3. Write a short current-condition note: what code currently does, what invariant is violated, why old finding is still valid.

4. Design the smallest complete protocol change; avoid half-fix that improves performance but breaks restart/cancellation semantics.

5. Add unit + architecture/integration regression tests in the same commit or immediately adjacent commit.

6. Inspect full diff before moving branch ref.

7. Re-fetch branch ref immediately before fast-forward. Never force unless user explicitly requests history rewrite.

8. After commit, update context with exact new HEAD, what invariant is now closed, and what remains open.

9. Do not continue to the next phase if current change has an unresolved correctness ambiguity.

## 18. AI Handoff Prompt Template

```text
Repository: https://github.com/inipew/goultroid
Branch: test-next

First, fetch the current branch HEAD and compare it with the baseline in
'docs/design/goultroid-next-technical-plan-ai-handoff.md'. Do not assume the baseline
commit is still current.

Read the source for TaskEngine, Jobs, Scheduler, RPCExecutor/limiter, runtime,
plugin/resource lifecycle, and the files directly touched by the selected phase.
Source code is authoritative over old audit documents.

Continue the roadmap from the first still-open phase. For each phase:
1. describe the real current condition and invariant;
2. implement the smallest complete fix;
3. add regression/architecture/integration tests;
4. inspect the diff;
5. commit atomically to test-next only if the branch parent is unchanged;
6. report exact commit SHA and remaining risks.

Important design constraints:
- TaskEngine owns physical execution.
- Jobs own durable logical work/retry/recovery.
- Scheduler owns timing only.
- RPCExecutor owns Telegram retry/FloodWait/rate-limit.
- Long durable waits must eventually yield rather than occupy physical workers.
- No per-occurrence timers, unbounded goroutines, or count-only retention for
  closures/payload graphs.
- Never claim CI/benchmark success without evidence for the exact HEAD.
```

## 19. Files to Read First by Workstream

| Workstream | Primary files |
| --- | --- |
| RateLimit/Task result | internal/taskengine/executor.go; internal/tasks/result.go; internal/tasks/types.go; internal/core/errors.go |
| Job protocol | internal/jobs/domain.go; internal/jobs/manager.go; internal/jobs/sqlite/store.go; internal/jobs/sqlite/schema.go |
| RPC execution | internal/telegram/rpc_executor.go; rpc_limiter.go; rpc_policy.go; service.go; resolver.go; media_rpc.go |
| Runtime/lifecycle | internal/runtime/runtime.go; supervisor.go; callback_executor.go; internal/plugin/scope.go; manager.go; internal/resource/manager.go |
| Timing/durability | internal/scheduler/*; internal/app/delayed_action.go; internal/jobs/persistence_pump.go |
| Execution guards | internal/architecture/execution_redesign_test.go; internal/architecture/telegram_rpc_test.go |
| Benchmark evidence | internal/taskengine/benchmarks_test.go; docs/design/execution-redesign/05-benchmark-report.md |
| Downloader reference | plugins/downloader/downloader.go; module.go; downloader_*_test.go |

## 20. Recent Commit Ledger Relevant to This Roadmap

| Commit | Message | Why it matters |
| --- | --- | --- |
| 03cf68cc | fix(telegram): enforce per-chunk media RPC policy | Physical media chunks now share RPC policy. |
| 17b11a45 | fix(jobs): drain outbox backlog per wake | Outbox backlog no longer waits safety ticker per 100 rows. |
| 1e9b51e3 | fix(lifecycle): share bounded cleanup budget | Plugin/Scope/Resource share lifecycle callback budget. |
| b1a2ae63 | fix(durability): bound persistence retained bytes | Persistence queued+in-flight memory admission. |
| c95be18c | fix(delayed): bound retained closure bytes | Delayed action byte accounting and channel cleanup. |
| 88909d82 | fix(downloader): use ephemeral task continuations | Removes per-request durable definition growth. |
| 64deb98f | fix(jobs): persist every retry backoff deadline | No retry-worker sleep for positive backoff. |
| b4ce7be0 | fix(plugin): remove scope drain waiter goroutine | Waiter-free scope drain signal. |
| 7e65b7ad | fix(taskengine): isolate runtime default config | Reduces mutable-global config coupling. |
| 3d156e23 | fix(telegram): fail closed on missing rpc executor | No physical Telegram op without executor. |
| 3a789e91 | fix(telegram): supervise restart notification | Moves restart work under lifecycle ownership. |
| 54cf67e1 | fix(telegram): bound peer cache retained bytes | Peer entity memory bounded by count + bytes. |
| e7cffa77 | fix(telegram): fence legacy retry helper | Legacy helper bounded; production shared executor remains authority. |
| 08626042 | fix(telegram): bind resolver to shared rpc executor | Current authoritative HEAD at document creation. |

## 21. Final Definition of Done

| Gate | Done when |
| --- | --- |
| Correctness | No durable rate-limit decision depends on in-memory-only lastErr or string parsing. |
| Durability | Deferred attempt + ready_at persisted atomically and restart-safe. |
| Budgeting | Retry budget and deferral budget are explicit and bounded. |
| Execution | Durable long waits release physical TaskEngine capacity. |
| Safety | Non-idempotent ambiguous failures are never silently replayed as free deferrals. |
| Lifecycle | All production goroutines have owner/cancel/join/panic/bound contract. |
| Memory | Queues/caches/closures are bounded by meaningful retained bytes where needed. |
| Observability | Latency/retry/deferral/saturation data can explain production behavior. |
| Evidence | Race tests, current-HEAD benchmarks, soak/profile results captured for exact final SHA. |
| Documentation | Old conformance docs updated so closed findings are not reported as active. |

> **Stop condition for future AI**
> Jika source HEAD sudah mengimplementasikan salah satu phase di atas dengan invariant yang sama atau lebih kuat, jangan implement ulang. Tandai phase sebagai closed, jelaskan bukti source/test, dan lanjut ke first still-open phase.

Document status: planning/handoff guide based on repository condition verified at test-next @ 08626042. Revalidate HEAD at the start of every future session.
