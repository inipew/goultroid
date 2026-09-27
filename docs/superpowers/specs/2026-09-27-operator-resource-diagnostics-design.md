# Operator Resource Diagnostics Bridge

## Intent

The `.diagnostics` card in `plugins/sysinfo` currently reads TaskEngine and EventBus directly. It therefore omits the process, lane, persistence, retry, session, cache, and resource totals added to `App.Diagnostics()` in R1. An operator must be able to capture the same resource state at startup, after a burst, and during settling through the existing owner-only command. The supplied card is a low-pressure point-in-time snapshot; it does not establish peak or settled goroutine/RSS behavior.

## Boundary and source of truth

`plugins/sysinfo` cannot import `internal/app` because the app imports builtin plugins. Keep `App.Diagnostics()` canonical. Define a small, immutable `sysinfo.ResourceSnapshot` presentation DTO containing only bounded numeric diagnostics needed by the command. After `App` is constructed and builtin modules are registered, app wiring finds the sysinfo plugin and injects `func() sysinfo.ResourceSnapshot`. The closure calls `App.Diagnostics()` once per command and maps its values to the DTO. It is installed before the runtime starts; the plugin never holds an `*app.App` reference.

The bridge must not copy payloads, owner maps, goroutine stacks, or arbitrary error strings. A missing provider keeps the existing direct TaskEngine/EventBus card useful in isolated plugin tests or partial compositions. A provider snapshot that marks TaskEngine unavailable shows an explicit unavailable status rather than zero workers.

## Card content

Preserve existing Task Pools, Execution Resources, Task Memory, resource leak indicator, and EventBus telemetry. The provider-backed card shows aggregate resource totals in place of the per-owner resource listing, keeping its size independent of owner count. When the provider is installed, add compact fields for:

- Process: goroutines, heap alloc/inuse/idle/released, stack inuse/sys, total sys, heap objects, GC count, and RSS when available; show `RSS unavailable` otherwise.
- TaskEngine completion and durability lanes: configured limit, physical workers, pending, active; include terminal count and retained bytes.
- EventBus: general/ordered physical workers and queue depth/capacity alongside existing publication counters.
- PersistencePump and Jobs retry: worker limit, physical workers, queued, active, retained bytes for the pump, tracked occurrences for Jobs.
- Interaction sessions, input claims, and retained state bytes; Inline cache entries and bytes; ResourceManager active and leaked totals.
- DB open/in-use/idle connections and Telegram resolver/peer cache counts plus bounded RPC metrics already in `App.Diagnostics()`.

Use the existing `ui.Card` renderer and HTML escaping for any dynamic labels. The provider-backed card contains only fixed-cardinality fields and must stay below 3,500 rendered characters with maximum-width numeric values used in tests. Do not truncate a value silently. The `.botinfo` command continues to provide extended host details.

## Collection and lifecycle

The handler invokes the provider once, before rendering; no ticker, persistent cache, or asynchronous dump is introduced. Sampling is safe during startup and shutdown and may return zero-value sections for absent components. The Interaction section uses the passive `SnapshotStats()` already wired by R1. The command remains owner-only. On-demand goroutine profiles remain the application API from R1; this card does not embed stacks or introduce a Telegram file-export flow.

## Verification

Test that a synthetic provider snapshot with nonzero values renders every new section and the same pool/EventBus values returned by the canonical snapshot. Test unavailable RSS and TaskEngine status, missing provider fallback, and bounded output length. App wiring tests verify the sysinfo plugin receives a provider after `New()` and that invoking it matches `App.Diagnostics()` for representative fields. Run focused sysinfo/app tests with race detection, `go vet ./...`, and build. The repository's unrelated full-suite failures remain a separate integration gate.
