# R1 Production Resource Observability

## Intent and scope

Give Goultroid operators a low-cost, read-only view of who owns goroutines and retained memory at startup, after a burst, and during settling. The motivating observations are about 120 goroutines after burst and RSS around twice the expected level. R1 measures these claims before changing concurrency or retention policy.

R1 extends `App.Diagnostics()` and the existing subsystem statistics. It does not change worker limits, idle timeouts, terminal retention, DLQ contents, audit retention, or runtime memory limits. Those belong to R2–R6 and should use R1 measurements.

## Snapshot contract

`DiagnosticsSnapshot` gains `ProcessMemory`, `Interaction`, `Inline`, and resource totals. Its existing `Process` field describes the OS process runner and stays compatible. All memory values are bytes. Counters are bounded-cardinality and contain no message, session, audit, or task payloads.

`ProcessMemory` contains `NumGoroutine`, `HeapAlloc`, `HeapInuse`, `HeapIdle`, `HeapReleased`, `HeapObjects`, `StackInuse`, `StackSys`, `Sys`, `NextGC`, `NumGC`, and `RSSBytes`. `runtime.ReadMemStats` and `runtime.NumGoroutine` supply the Go values. On Linux, obtain RSS from `/proc/self/statm` resident pages times page size. If unavailable or malformed, report `RSSBytes=0` and an explicit availability flag; do not fail the whole snapshot. Document that RSS and Go heap values are sampled at slightly different instants and are not additive.

`EventBusDiagnostics` forwards `ActiveWorkers` and `OrderedWorkers` from `EventBus.Stats()`. TaskEngine's existing per-pool workers, running, idle, waiting, waiting bytes, retained bytes, and terminal count stay as-is. Add a small lane snapshot for each of completion delivery and durability with configured worker limit, physical workers, queued/pending work, and active work. Read their existing atomic counters; zero-value lanes report zero safely.

`PersistencePumpStats` gains configured worker limit, physical workers, queued and active counts from its existing fields. `jobs.Diagnostics` gains retry worker limit, physical workers, queued and active retry work, plus the length of the existing `tracked` map under its owning lock; durable occurrence counts remain the existing store-backed snapshot. An unavailable or timed-out store snapshot must remain distinguishable through `DurableSnapshotOK`.

`App.Diagnostics()` includes a passive snapshot of `plugin.Manager.InteractionRuntime()` and `inline.Engine.RuntimeStats()` where available. The existing interaction `Stats()` method prunes expired entries, so add a read-only `SnapshotStats()` method and use it here; diagnostics must not change retention timing. The snapshot reports entries still physically retained, including entries awaiting lazy expiration. Resource totals are aggregated from `resource.Manager.AllSnapshots()` as active and leaked counts, without exposing owner names in the top-level totals. Existing plugin resource details remain available. Existing DB, Telegram cache/RPC, scheduler, supervisor, and media/process snapshots remain in place.

## On-demand goroutine dump

Provide an explicit application diagnostic method that writes `pprof.Lookup("goroutine")` with debug level 2 to a supplied `io.Writer`. It is called only by an operator-requested diagnostic action. The method must return lookup/write errors, avoid a background ticker, and never include a goroutine dump in `DiagnosticsSnapshot`. If there is no current operator-facing dump command, the method is the integration point; wiring a new command is outside R1.

## Collection behavior and safety

Snapshot collection remains safe during startup, normal running, and shutdown. Absent components produce zero-value sections. Keep lock hold times short; aggregate resource owner snapshots after releasing the manager's lock. Do not introduce a shared global metric registry or periodic sampling. Existing 200 ms timeouts around TaskEngine and durable jobs remain; a timed-out section must be identifiable instead of being mistaken for an idle section. RSS parsing is Linux-specific and must compile on other supported platforms.

The diagnostic API is descriptive, not a new readiness policy. `Health()` behavior and thresholds are unchanged. Sampling may allocate briefly, especially `runtime.ReadMemStats` and a requested goroutine dump, but steady-state overhead is zero without calls.

## Verification and operating protocol

Focused tests cover process-memory field population, malformed/unavailable RSS, propagation of EventBus worker counters, lane and pump worker/queue/active counters during work and after retirement, retry worker counters, and nil/stopped app snapshots. A goroutine dump test checks that the on-demand method writes a profile and propagates writer errors. Run `go test -race` for the affected packages and `go test -race ./...` before integration.

Collect snapshots at startup, immediately after a representative burst, then at +10 s, +30 s, +90 s, and +5 min. Compare physical workers with active work and queue pressure; compare `HeapAlloc`, `HeapInuse`, `HeapReleased`, and RSS without treating RSS as proof of live object retention. Use those measurements to choose R2 idle retirement and R3/R4 changes. R5 will add the production-composition resource gate after behavior changes are decided.
