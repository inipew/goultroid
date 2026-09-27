# Goultroid — P3-A High-Cardinality Benchmark Plan

Date: 2026-09-27  
Branch: `test-next`  
Harness baseline: `aeae302d125bf1e5ac39df9671c0ea6b026bd10e` — `bench(refinement): add p3-a high-cardinality probes`

## Status

**HARNESS READY / MEASUREMENTS OPEN.**

This P3-A slice adds reproducible measurement points for current Inline registry, Inline cache, and generic rate-limiter high-cardinality behavior. It deliberately changes no production algorithm.

The purpose is to answer one question before P3-C:

> Which current bounded path, if any, is materially expensive enough to justify optimization?

No optimization decision is valid from this document until the benchmark commands are run against a real checkout and results are recorded.

## 1. Inline registry

Benchmark:

`BenchmarkRegistryResolveOwnedExplicitP0D`

Cardinality:

| Path | Cardinality |
|---|---|
| exact indexed handler | 1, 16, 64, 256, 1024, 4096 |
| custom matcher | 1, 16, 64, 256, 1024, 4096 |

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
| saturated churn | 501, 4096 |
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

## 5. Reproducible commands

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

## 6. Result table

No measured values are recorded yet.

| Area | Case | ns/op | B/op | allocs/op | Decision |
|---|---|---:|---:|---:|---|
| Inline | exact/1..4096 | PENDING | PENDING | PENDING | PENDING |
| Inline | custom/1..4096 | PENDING | PENDING | PENDING | PENDING |
| Cache | hit/1..500 | PENDING | PENDING | PENDING | PENDING |
| Cache | fill/1..500 | PENDING | PENDING | PENDING | PENDING |
| Cache | churn/501,4096 | PENDING | PENDING | PENDING | PENDING |
| Cache | next-expiry/500 | PENDING | PENDING | PENDING | PENDING |
| Limiter | hot/1..4096 | PENDING | PENDING | PENDING | PENDING |
| Limiter | insert/1..4096 | PENDING | PENDING | PENDING | PENDING |
| Limiter | saturated/4096 | PENDING | PENDING | PENDING | PENDING |
| Limiter | forced-sweep/4096 | PENDING | PENDING | PENDING | PENDING |

## 7. Environment limitation in this session

The current model container cannot resolve github.com and does not contain a complete Goultroid checkout or dependency module cache. CI was intentionally not used.

Therefore this session can verify source/diff structure and run `gofmt` on the changed benchmark files, but it cannot honestly produce repository benchmark numbers.

P3-A remains **OPEN** until measurements are executed on a real checkout.

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
