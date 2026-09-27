# Goultroid — P3-C Optimization Decision Record

Date: 2026-09-27  
Branch: `test-next`  
Decision state: **PREPARED / BLOCKED**  
Production optimization changes: **NONE**

## 1. Decision

Do not optimize any P3-C candidate from current evidence.

P3-C exists to remove measured material cost, not to reward asymptotic cleverness. Current diagnostics show bounded behavior whose absolute cost is small at the current production shape.

Formal closure is intentionally withheld until P3-A is executed against a complete real checkout.

## 2. Evidence considered

### Generic rate limiter

Source-isolated current implementation:

```text
hot/1       ~261.5 ns/op
hot/4096    ~299.8 ns/op
saturated missing key / 4096
            ~249.5 ns/op
forced capacity sweep / 4096
            ~110 µs/op
```

The forced sweep is gated by `capacitySweepInterval = 30s`; it is not paid on each existing-bucket hot request.

Decision: **no optimization**.

### Inline registry

Synthetic matrix:

```text
exact/1      ~135.7 ns/op
exact/4096   ~160.3 ns/op
custom/4096  ~16.5 µs/op
```

Fresh production source inventory found only two feature-owned Inline providers:

```text
Calculator -> 1 custom matcher, CacheNone
Wikipedia  -> 1 custom matcher, CacheGlobal
```

SavedResponse is a separate dynamic source and does not add a custom registry matcher.

Production-shaped diagnostic:

```text
exact/2   ~140.5 ns/op
custom/2  ~169.2 ns/op
```

Decision: **no optimization**. The 4096-custom-matcher probe is a capacity stress point, not current runtime shape.

### Inline cache

Current hard bounds:

```text
entries          500
retained bytes   8 MiB
single entry     256 KiB
engine TTL       30 s
```

Diagnostics:

```text
hit/1..500                   ~195–215 ns/op
nextExpiry/500               ~7.2 µs/op
saturated churn inactive     ~15–21 µs/insert
saturated churn lifecycle on ~32.6–40.9 µs/insert
```

Production source shape:

- Calculator: `CacheNone`
- SavedResponse dynamic Inline: `CacheNone`
- Wikipedia: `CacheGlobal`

A cache permanently at the 500-entry ceiling under the 30-second local TTL requires roughly:

```text
500 / 30 ≈ 16.7
```

new distinct cacheable keys per second, assuming insufficient key reuse.

At ~40.9 µs/insert, that threshold corresponds to roughly 0.68 ms CPU per second, about 0.068% of one core caller-side. Adding one full 7.2 µs expiry scan per insert as a conservative approximation is still about 0.080% of one core.

These are diagnostic calculations, not observed traffic.

Decision: **no preemptive cache data-structure optimization**.

## 3. P3-B separation

TaskEngine completion drain polling was a confirmed lifecycle/resource defect, not a benchmark speculation. It was fixed independently in P3-B with event-driven drain signalling.

Do not reopen that change as a P3-C performance rewrite without new evidence.

## 4. Forbidden speculative changes

Do not introduce:

- unbounded cache state;
- permanent cleanup workers;
- a second cache or rate limiter;
- `sync.Pool` solely to lower benchmark allocation numbers;
- lock-free structures without measured contention;
- unsafe object reuse across sessions/queries;
- weakened fail-closed capacity behavior;
- architecture changes justified only by synthetic 4096-cardinality worst cases.

## 5. Closure gate

P3-C is currently:

```text
PREPARED
BLOCKED by P3-A real-checkout measurement
NO production optimization pending
```

When real-checkout P3-A exists:

### If results confirm current magnitude

Close P3-C as:

```text
INTENTIONALLY SKIPPED — no measured material hotspot
```

and continue to P4.

### If results materially contradict current magnitude

Open only the measured path, capture a before baseline, make the smallest bounded change, then record before/after `ns/op`, `B/op`, `allocs/op`, and any lifecycle/resource effect.

## 6. Verification limitations

This decision uses current-source audit plus source-isolated executable diagnostics.

The current environment cannot materialize a complete Goultroid checkout: GitHub archive endpoints are not exposed by the connector, the repository has no `vendor/`, local module cache is unavailable, and direct external DNS is unavailable.

CI was not inspected.

Therefore this document does not claim full-package/full-repository benchmark acceptance.
