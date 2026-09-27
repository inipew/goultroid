# Goultroid — P3-A High-Cardinality Benchmark Plan

Date: 2026-09-27  
Branch: `test-next`  
Harness baseline: `aeae302d125bf1e5ac39df9671c0ea6b026bd10e` — `bench(refinement): add p3-a high-cardinality probes`

## Status

**HARNESS READY / ISOLATED DIAGNOSTICS CAPTURED / REAL-CHECKOUT MEASUREMENTS OPEN.**

This P3-A slice adds reproducible measurement points for current Inline registry, Inline cache, and generic rate-limiter high-cardinality behavior. It deliberately changes no production algorithm.

The purpose is to answer one question before P3-C:

> Which current bounded path, if any, is materially expensive enough to justify optimization?

No production optimization decision is final from this document until the benchmark commands are run against a real checkout and results are recorded. A source-isolated diagnostic run is recorded below to narrow attention without pretending it is full repository acceptance.

## 1. Inline registry

Benchmark:

`BenchmarkRegistryResolveOwnedExplicitP0D`

Cardinality:

| Path | Cardinality |
|---|---|
| exact indexed handler | 1, **2**, 16, 64, 256, 1024, 4096 |
| custom matcher | 1, **2**, 16, 64, 256, 1024, 4096 |

The exact path exercises the first-token indexed map lookup. The custom path places the matching matcher at the end of the ordered custom matcher list so high cardinality exposes scan cost.

Record:

- ns/op
- B/op
- allocs/op
- handlers

Interpretation: exact lookup should be checked for cardinality-flat behavior. Custom matcher growth is expected structurally; absolute cost and real workload frequency determine whether it matters.

## 2. Inline cache

Benchmarks:

- `BenchmarkCacheHighCardinalityP3A`
- `BenchmarkCacheSaturatedChurnP3A`
- `BenchmarkCacheStartedSaturatedChurnP3A`
- `BenchmarkCacheNextExpiry500P3A`

Current production bounds:

```text
max entries        500
max retained bytes 8 MiB
max single entry   256 KiB
```

Matrix:

| Path | Cardinality / working set |
|---|---|
| GetScoped hit | 1, 16, 64, 256, 500 |
| fill from empty | 1, 16, 64, 256, 500 |
| saturated churn, cache lifecycle inactive | 501, 4096 |
| saturated churn, cache lifecycle active | 501, 4096 |
| next-expiry scan | 500 |

The churn benchmark pre-fills the cache to the production hard cap, then rotates a working set larger than capacity so inserts exercise bounded eviction instead of a steady-state overwrite.

The next-expiry benchmark isolates the current deadline coordinator scan across the maximum entry count.

Record:

- ns/op
- B/op
- allocs/op
- entries/op where reported

Interpretation: GetScoped includes the intentional defensive copy cost. Churn and next-expiry are bounded O(500) paths; do not classify them as defects solely from asymptotic form.

## 3. Generic rate limiter

Benchmarks:

- `BenchmarkLimiterHighCardinalityP3A`
- `BenchmarkLimiterSaturatedCapacityP3A`
- `BenchmarkLimiterCapacitySweep4096P3A`

Current production cap:

```text
max buckets 4096
```

Matrix:

| Path | Cardinality |
|---|---|
| hot existing bucket | 1, 16, 64, 256, 4096 |
| population/insert batch | 1, 16, 64, 256, 4096 |
| saturated fail-closed missing key, sweep suppressed | 4096 |
| forced capacity sweep | 4096 |

The saturated benchmark isolates the normal fail-closed path once at capacity without paying a sweep on every request. The forced sweep benchmark intentionally resets the sweep gate to measure the bounded full-map cleanup cost itself.

Record:

- ns/op
- B/op
- allocs/op
- keys/op or buckets/op where reported

Interpretation: hot existing-bucket cost should be compared across resident cardinality. A 4096-bucket capacity sweep can be linear by design; optimize only if measured cost multiplied by actual sweep frequency is material.

## 4. Related benchmark already present

The production Telegram hierarchical RPC limiter already has dedicated cardinality benchmarks in `internal/telegram/benchmarks_test.go`, including high-cardinality hot-peer and reclamation cases. P3-A should reuse those results when needed rather than introduce another limiter or duplicate benchmark authority.

## 5. Canonical real-checkout evidence runner

The final P3-A gate now has a repo-local runner:

```bash
tools/bench-p3a.sh
```

It is intentionally independent of CI and performs three benchmark groups on the current checkout:

1. Inline registry + cache, including production matcher cardinality and lifecycle-active saturated churn;
2. generic multi-dimensional rate limiter;
3. existing production hierarchical Telegram RPC limiter.

The runner:

- requires a clean checkout by default;
- records exact HEAD, branch, commit message, Go version, GOOS/GOARCH/CGO, CPU model, logical CPU count and kernel;
- uses `-benchmem -benchtime=1s -count=5`;
- writes raw benchmark output plus a manifest into a timestamped evidence directory under `${TMPDIR:-/tmp}`;
- never invokes CI.

A dirty checkout can be run only for diagnostics with `P3A_ALLOW_DIRTY=1`; such output must not be used for formal P3-A closure.

The evidence bundle contains:

```text
manifest.txt
inline.txt
ratelimit.txt
telegram-rpc-limiter.txt
README.txt
```

For formal closure, preserve the complete bundle and verify `dirty=no` in `manifest.txt`.

## 5.1 Manual equivalent commands

Run from a real checkout at the exact commit being evaluated:

```bash
go test -run=^$ -bench='BenchmarkRegistryResolveOwnedExplicitP0D|BenchmarkCache.*P3A' \
  -benchmem -benchtime=1s -count=5 ./internal/services/inline

go test -run=^$ -bench='BenchmarkLimiter.*P3A' \
  -benchmem -benchtime=1s -count=5 ./internal/services/ratelimit
```

Also record:

```bash
git rev-parse HEAD
go version
go env GOOS GOARCH GOMAXPROCS
```

Record CPU model separately from the host.

## 6. Source-isolated diagnostic measurements

These numbers are **directional evidence only**, not P3-A closure.

The container still cannot resolve external hosts and cannot build the complete repository dependency graph. To avoid inventing numbers:

- generic rate-limiter logic was compiled as a local source-isolated reproduction of the current implementation and current benchmark;
- Inline cache logic was compiled from the current implementation with minimal local stubs only for unrelated presentation/runtime/UI dependency types;
- Inline registry resolution was compiled from the current Registry/ResolveOwnedExplicit implementation with minimal local task/type stubs;
- no CI result was used;
- no result below is represented as a full-repository benchmark.

Environment:

```text
OS/kernel     Linux 6.18.44 amd64
CPU           Intel Xeon Platinum 8573C
logical CPUs  5
Go            go1.23.2 linux/amd64
GOMAXPROCS     5 during benchmark runs
runs          median of 3
```

### 6.1 Generic rate limiter — source-isolated current implementation

Median of three runs:

| Case | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| hot/1 | 261.5 | 48 | 3 |
| hot/16 | 287.0 | 48 | 3 |
| hot/64 | 289.4 | 48 | 3 |
| hot/256 | 284.1 | 48 | 3 |
| hot/4096 | 299.8 | 48 | 3 |
| saturated missing key / 4096 | 249.5 | 48 | 3 |
| forced capacity sweep / 4096 | 110,242 | 48 | 3 |

Population batch:

| Keys/op | batch ns/op | approx ns/key | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 1,314 | 1,314 | 355 | 5 |
| 16 | 10,411 | 651 | 3,666 | 67 |
| 64 | 32,500 | 508 | 16,244 | 266 |
| 256 | 131,989 | 516 | 63,591 | 1,040 |
| 4096 | 2,050,872 | 501 | 1,015,298 | 16,688 |

Diagnostic interpretation:

- existing-bucket hot cost is effectively cardinality-flat for this scale: 261.5 ns at 1 bucket versus 299.8 ns at 4096;
- saturated fail-closed lookup remains in the same range at 249.5 ns/op;
- the intentionally forced 4096-bucket cleanup sweep costs roughly 110 µs, but it is gated by `capacitySweepInterval` rather than paid on each hot lookup;
- current diagnostic evidence does **not** justify optimizing the generic limiter hot path.

### 6.2 Inline cache — source-isolated diagnostic proxy

Median of three runs:

| Case | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| hit/1 | 194.9 | 320 | 1 |
| hit/16 | 192.4 | 320 | 1 |
| hit/64 | 200.8 | 320 | 1 |
| hit/256 | 209.0 | 320 | 1 |
| hit/500 | 215.4 | 320 | 1 |
| saturated churn / working-set 501 | 15,010 | 320 | 1 |
| saturated churn / working-set 4096 | 21,333 | 322 | 1 |
| next-expiry scan / 500 | 7,218 | 0 | 0 |

Fill from empty:

| Entries/op | batch ns/op | approx ns/entry | B/op | allocs/op |
|---:|---:|---:|---:|---:|
| 1 | 938.8 | 938.8 | 979 | 2 |
| 16 | 9,941 | 621 | 9,949 | 19 |
| 64 | 28,227 | 441 | 41,878 | 73 |
| 256 | 124,211 | 485 | 169,427 | 278 |
| 500 | 222,544 | 445 | 334,987 | 542 |

Diagnostic interpretation:

- cache hit cost remains approximately flat through the production 500-entry cap;
- `nextExpiry()` scans the bounded 500-entry cache in about 7.2 µs with zero allocation in this environment;
- the expensive path is saturated insertion/churn: each insert beyond the cap currently scans for expired entries and then searches the retained set for the oldest expiry, producing roughly 15–21 µs per churn insert in this proxy;
- this makes saturated churn a **candidate to confirm**, not yet a P3-C authorization. Real-checkout numbers and realistic churn frequency are still required.

### 6.3 Inline registry — source-isolated diagnostic proxy

Median of three runs:

| Cardinality | exact ns/op | custom ns/op | exact B/op | custom B/op |
|---:|---:|---:|---:|---:|
| 1 | 135.7 | 162.8 | 48 | 64 |
| 16 | 144.2 | 228.5 | 48 | 64 |
| 64 | 139.4 | 430.0 | 48 | 64 |
| 256 | 148.6 | 1,184 | 48 | 64 |
| 1024 | 147.7 | 4,121 | 48 | 64 |
| 4096 | 160.3 | 16,483 | 48 | 64 |

Allocation count remains 2 allocs/op for exact and 3 allocs/op for custom in this proxy.

Diagnostic interpretation:

- indexed exact resolution remains effectively cardinality-flat;
- custom matcher lookup grows approximately linearly because the benchmark intentionally places the matching matcher at the end of the ordered custom slice;
- 4096 custom matchers are about 16.5 µs/op in this environment;
- this is expected structurally, but it only becomes a P3-C concern if real production custom matcher cardinality/frequency is high enough to matter.

### 6.4 Provisional decision

```text
generic limiter hot path     -> NO optimization evidence
inline exact lookup          -> NO optimization evidence
inline cache hit             -> NO optimization evidence
limiter forced sweep         -> bounded diagnostic cost; no action yet
inline cache saturated churn -> CONFIRM on real checkout / realistic churn
inline custom matcher scan   -> NO current optimization evidence; production cardinality is 2
```

P3-C remains blocked.

---

## 6.5 Production-shape source audit

Fresh current-source inventory was performed because the repository code-search index is unavailable and zero-result search responses are not authoritative.

The production Inline registry is created empty in `internal/app/wiring_core.go`, then handed to `plugin.Manager`. Feature-owned Inline registrations flow through `internal/plugin/features.go`; SavedResponse is installed separately as one `DynamicSource`.

Primary production plugin inventory found only two `InlineFeatureProvider` implementations:

| Plugin | Binding count | Matcher | Priority | Cache policy |
|---|---:|---|---:|---|
| Calculator | 1 | prefix `calc` | 20 | `CacheNone` |
| Wikipedia | 1 | `wikiMatcher{}` | 10 | `CacheGlobal` |

Therefore current production feature custom-matcher cardinality is **2**, not hundreds or thousands.

A production-shaped isolated Registry run at cardinality 2 produced:

| Case | median ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| exact/2 | 140.5 | 48 | 2 |
| custom/2 | 169.2 | 64 | 3 |

The official benchmark matrix now includes cardinality 2 at commit `f265c010a1515a3e6b730622e23dc7c8c00d61e9` (`bench(inline): add production matcher cardinality`).

### Cache production shape

Current source shows:

- Calculator is `CacheNone`;
- SavedResponse dynamic Inline execution is `CacheNone`;
- Wikipedia is the only current feature provider using `CacheGlobal`;
- the engine stores materialized cache entries locally with a hard-coded 30-second TTL;
- the key includes handler pattern plus normalized query string (and scope dimensions when applicable).

At the current 500-entry cache cap, continuously exercising the saturated eviction path requires on the order of:

```text
500 distinct cacheable keys / 30 seconds
≈ 16.7 new distinct keys/second
```

with little enough repetition for the cache to remain at capacity.

That threshold is a source-derived workload condition, not an observed production traffic rate. There is currently no telemetry evidence in this session that Wikipedia receives sustained >16.7 distinct cacheable query keys per second.

### Updated provisional decision

```text
generic limiter hot path     -> NO optimization evidence
inline exact lookup          -> NO optimization evidence
inline custom matcher scan   -> NO current production optimization evidence (cardinality = 2)
inline cache hit             -> NO optimization evidence
limiter forced sweep         -> bounded diagnostic cost; no action
inline cache saturated churn -> bounded diagnostic cost; no preemptive optimization
```

This further narrows P3-C, but does not close P3-A: a real checkout benchmark is still required by the refinement gate.

---

## 6.6 Production-lifecycle cache churn diagnostic

A second source audit confirmed that the Inline cache **is** started in production. `internal/app/app.go` registers `inlineEngine.Cache()` as a runtime component, so the deadline-driven prune coordinator is active during normal application lifecycle.

The original churn proxy measured `SetScoped` without `Cache.Start()`. P3-A therefore added a lifecycle-active benchmark at:

`da3527163d6078f0fb250893c902fee299709dab` — `bench(inline): cover active cache churn`.

Source-isolated lifecycle-active median of five runs:

| Case | median ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| started churn / working-set 501 | 32,640 | ~436 | 2 |
| started churn / working-set 4096 | 40,942 | ~457 | 2 |

This includes caller-visible lock/wakeup contention with the prune coordinator active. It still does not claim full-repository benchmark acceptance.

For context, the separately measured 500-entry `nextExpiry()` scan was ~7.2 µs/op. Treating the 4096 working-set caller cost plus a full expiry scan on **every** distinct insert as a deliberately conservative upper-bound approximation gives ~48.2 µs of CPU work per insert.

At the source-derived saturation threshold:

```text
500 unique cacheable keys / 30 s
≈ 16.7 distinct inserts/s
```

the diagnostic CPU budget is approximately:

```text
40.9 µs * 16.7/s ≈ 0.68 ms CPU/s ≈ 0.068% of one core
(40.9 + 7.2) µs * 16.7/s ≈ 0.80 ms CPU/s ≈ 0.080% of one core
```

For scale only, not as assumed traffic:

```text
100 distinct inserts/s  -> ~0.41% one core caller-side
1000 distinct inserts/s -> ~4.09% one core caller-side
```

These figures are diagnostic arithmetic, not observed production traffic.

### Updated optimization decision

Current evidence no longer identifies a measured-enough hotspot worth opening P3-C:

```text
generic limiter hot path     -> NO optimization evidence
inline exact lookup          -> NO optimization evidence
inline custom matcher scan   -> NO current production evidence; cardinality = 2
inline cache hit             -> NO optimization evidence
inline cache started churn   -> bounded absolute cost; NO preemptive optimization
limiter forced sweep         -> bounded / gated; NO preemptive optimization
```

P3-A remains formally OPEN only because the refinement acceptance contract requires execution against a real complete checkout. P3-C must still not start before that gate, but current diagnostics indicate that P3-C may ultimately be **intentionally skipped** if real-checkout numbers confirm the same order of magnitude.

---

## 7. Environment limitation in this session

The current model container cannot resolve github.com and does not contain a complete Goultroid checkout or dependency module cache. CI was intentionally not used.

Therefore the isolated measurements above are useful only to prioritize confirmation. They are not full-package/full-repository acceptance numbers.

P3-A remains **OPEN** until `tools/bench-p3a.sh` (or its exact manual equivalent) is executed on a clean real checkout at the actual Goultroid HEAD and the evidence bundle is reviewed.

## 8. Gate before P3-C

Do not optimize a path merely because it contains a bounded scan.

For each candidate, combine:

```text
measured cost per operation
x actual invocation frequency
x contention/concurrency characteristics
x retained resource effect
```

Only measured material impact should open P3-C work.
