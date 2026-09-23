# Assistant Parity P8-I — resource / idle / high-load acceptance

## Status

**P8-I is CLOSED for implementation/source acceptance.**

Baseline:

`e7297b1940b481a59c03b06213504443293c1e7d` — `fix(assistant): restore P8-E/P8-F Go syntax`.

That baseline already repairs the reported malformed multiline strings in Assistant inline presentation, escaped downloader button literals, and the P8-F architecture-test composite literal. P8-I is built on that corrected source.

## Objective

P8-I validates the remaining P8 resource requirement as one combined workload rather than isolated subsystem tests.

The acceptance harness exercises the production authorities already introduced by P8:

```text
Calculator typed a2 callbacks
        +
Interaction session pressure
        +
Wikipedia rich Inline vNext lookup/cache
        +
Downloader-scoped download+process TaskEngine lease
        +
RPC metrics cardinality/load
        +
managed HTTP resource tracking
        +
Assistant interaction restart
        +
TaskEngine shutdown/worker retirement
        ↓
goroutine / heap / RSS settle checks
```

No second lifecycle engine, callback runtime, task engine, RPC limiter, cache, worker pool, or metrics store is introduced.

## Representative workload

The executable acceptance test is:

`internal/assistant/p8i_resource_acceptance_test.go`

It uses the actual production feature declarations and handlers from:

- `plugins/calculator`;
- `plugins/wikipedia`;
- `plugins/downloader`.

The test uses a managed in-memory HTTP transport for Wikipedia so high-load acceptance is deterministic and does not depend on the public internet.

## Idle acceptance

Before load the harness requires:

- TaskEngine physical workers = 0 for every configured zero-idle pool;
- interaction sessions = 0;
- pending inputs = 0;
- retained interaction state bytes = 0;
- Inline vNext cache entries = 0;
- Inline vNext cache bytes = 0;
- ResourceManager active ownership = 0.

This preserves the zero-idle architecture rather than hiding baseline workers in the acceptance harness.

## Calculator callback burst

The actual calculator driver is bound to the shared orchestration engine.

P8-I performs:

```text
512 typed a2 callback transitions
```

using alternating bounded key/back actions.

Every callback obtains callback data from the current interaction revision and transitions through the same bounded session runtime used in production.

No raw callback bytes or calculator-owned session map are created.

## Interaction pressure

After the callback burst, Calculator inline execution is driven through Inline vNext until the canonical per-actor bound is reached:

```text
DefaultMaxSessionsPerActor = 64
```

The next interactive query must fail closed with:

`interaction.ErrCapacity`

P8-I then verifies:

- retained sessions equal the configured bound;
- the capacity-rejected counter advanced;
- `CancelScope(calculator generation)` releases all retained sessions.

## Rich lookup / inline load

P8-I executes:

```text
10,000 inline queries
```

against the actual Wikipedia inline handler.

The first phase creates more unique lookup keys than the cache entry bound, followed by repeated queries that must be absorbed by the bounded cache.

Acceptance requires:

- cache entries <= 500;
- cache retained bytes <= 8 MiB;
- repeated load does not produce one HTTP request per query;
- managed Wikipedia HTTP resources settle back to zero.

## Inline diagnostics

P8-I adds:

`internal/services/inline/diagnostics.go`

with a read-only `RuntimeStats()` snapshot:

```go
type RuntimeStats struct {
    CacheEntries int
    CacheBytes   int64
}
```

It reads directly from the existing canonical cache:

- `Cache.Len()`;
- `Cache.RetainedBytes()`.

No counters or duplicate retention registry are added.

## Downloader download + process ownership

A downloader-generation TaskEngine workload is admitted with:

```text
download:1
process:1
```

while calculator and inline workloads execute.

P8-I verifies the resource snapshot reports both leases as used.

The active downloader generation is then cancelled with:

`TaskEngine.CancelScope(..., CauseScopeClosed)`

This is the execution-authority equivalent used by the real PluginManager disable path already accepted in P8-H.

Acceptance requires:

- active downloader task becomes `OutcomeCancelled`;
- cancellation cause is `CauseScopeClosed`;
- `download` usage returns to zero;
- `process` usage returns to zero.

P8-H remains the proof that PluginManager disable invokes this scope cancellation before feature teardown. P8-I proves the same resource authority remains correct under simultaneous mixed load.

## RPC metrics high-cardinality pressure

The shared `telegram.InMemoryRPCMetrics` collector receives 10,000 observations with deliberately excessive synthetic method/scope labels.

Acceptance locks the existing cardinality bounds:

```text
method labels <= 512 + overflow
wait scopes   <= 32 + overflow
```

It also records bounded attempt distributions and FloodWait observations.

This specifically checks that high traffic does not turn observability into an unbounded allocation source.

## Assistant restart acceptance

P8-I creates a live Calculator a2 session and callback, then closes the interaction runtime as the Assistant session runtime would be torn down.

A fresh interaction runtime is created from the same canonical feature catalog.

Required behavior:

- the old callback cannot resolve in the new runtime;
- it fails with `interaction.ErrNotFound`;
- a new interaction session can be created immediately;
- no migration/global callback map resurrects old session state.

## Shutdown and worker retirement

After the active downloader scope is cancelled, P8-I waits for zero-idle TaskEngine pools to retire all physical workers.

It then performs bounded TaskEngine shutdown.

Acceptance requires:

```text
all configured pool workers = 0
download used               = 0
process used                = 0
```

before final process settle sampling.

## Process metrics

P8-I samples:

- `runtime.NumGoroutine()`;
- `runtime.MemStats.HeapAlloc`;
- Linux RSS from `/proc/self/statm` when available.

RSS is treated as unavailable on platforms without `/proc/self/statm`; heap and goroutine acceptance remain portable.

The post-load settle gates are intentionally regression-oriented rather than microbenchmark-sensitive:

```text
goroutines <= baseline + 32
heap       <= baseline + 64 MiB
RSS        <= baseline + 128 MiB, when measurable
```

The large margins avoid test flakes from the Go runtime while still catching retained session/cache/task graphs or runaway workers.

## Architecture fence

`internal/architecture/assistant_p8i_test.go` verifies P8-I remains tied to canonical authorities and does not silently grow a second runtime.

The fence requires coverage for:

- 10,000 inline queries;
- 512 calculator callbacks;
- Calculator, Wikipedia, Downloader production features;
- TaskEngine `download/process` capacities;
- scope cancellation;
- interaction capacity rejection;
- shared RPC metrics;
- process memory/goroutine sampling;
- Assistant restart callback invalidation.

It also verifies Inline vNext diagnostics read existing cache state directly.

## Existing subsystem acceptance retained

P8-I complements rather than replaces:

- Inline cache zero-idle retirement tests;
- TaskEngine zero-idle worker tests;
- TaskEngine resource reservation tests;
- interaction lifecycle/capacity tests;
- RPC metrics bound tests;
- P8-H real PluginManager disable/re-enable acceptance.

## Resource cost introduced by P8-I

Production delta is only a read-only Inline vNext diagnostic view.

```text
new goroutines        = 0
new workers           = 0
new tickers           = 0
new timers            = 0
new caches            = 0
new retained maps     = 0
new callback runtime  = 0
new task runtime      = 0
new RPC metrics store = 0
```

## Verification constraint

All new Go source is formatted with `gofmt` before commit preparation.

The model shell cannot resolve `github.com`, so a full repository checkout cannot be materialized here to execute `go build`, `go vet`, or the new package tests. This document therefore records source/acceptance implementation without claiming those commands executed in this environment.

CI is not inspected.

## P8 final status — P8-J reconciliation

```text
P8-A CLOSED
P8-B CLOSED
P8-C CLOSED
P8-D CLOSED
P8-E CLOSED
P8-F CLOSED
P8-G CLOSED
P8-H CLOSED
P8-I CLOSED
P8-J CLOSED
```

P8-J freezes this acceptance as the combined resource/load evidence for the final Assistant parity closure.
