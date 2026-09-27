# Operator Resource Diagnostics Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing owner-only `.diagnostics` command display the production R1 resource snapshot.

**Architecture:** `App.Diagnostics()` remains canonical. A narrow callback maps it to a numeric `sysinfo.ResourceSnapshot` after the app is constructed, avoiding an app-to-plugin import cycle. The plugin renders the callback result once per command and retains its existing direct-stat fallback for isolated use.

**Tech Stack:** Go 1.27.0, existing `ui.Card`, `internal/app`, `plugins/sysinfo`, `internal/taskengine` and subsystem stats.

**Spec:** `docs/superpowers/specs/2026-09-27-operator-resource-diagnostics-design.md`

## Global Constraints

- Keep `internal/architecture` package boundaries and leave `internal/app/generated_modules.go` generated-only.
- `.diagnostics` stays owner-only and never exposes payloads, owner maps, goroutine stacks, or arbitrary error strings.
- The provider is installed before app runtime start and invoked exactly once per command; no timer or cache.
- Missing provider retains the existing direct TaskEngine/EventBus output; a provider snapshot with `TaskEngineAvailable=false` marks TaskEngine unavailable.
- Provider-backed output starts with at most five sorted pools and five sorted resources, shortens names after 24 Unicode code points with an ellipsis, reduces rows when needed, states omitted counts, and stays below 3,500 rendered bytes with maximum-width numeric values.
- Existing full-suite failures are a separate integration gate; do not claim all tests pass if they remain.

## Review Focus

- Provider is nil in a standalone sysinfo plugin: legacy card remains usable (Task 1).
- Provider reports unavailable TaskEngine: show unavailable, not zero workers (Task 1).
- RSS is unavailable: show `RSS unavailable`, not `0 B` (Task 1).
- A provider with nonzero process/lane/queue/cache values: all values appear in the rendered card (Task 1).
- App is stopped or partially composed: wiring closure returns a safe snapshot without panic (Task 2).

---

## File map

- `plugins/sysinfo/diagnostics_snapshot.go`: numeric DTO and provider attachment.
- `plugins/sysinfo/sysinfo.go`: render provider-backed card and keep legacy fallback.
- `plugins/sysinfo/sysinfo_test.go`: rendered-card and provider-call tests.
- `internal/app/sysinfo_diagnostics.go`: map canonical app snapshot into sysinfo DTO and install callback.
- `internal/app/app.go`: call the wiring helper just before returning the constructed App.
- `internal/app/diagnostics_test.go`: verify composition and representative value equality.

### Task 1: Provider-backed diagnostics card

**Files:** Create `plugins/sysinfo/diagnostics_snapshot.go`; modify `plugins/sysinfo/sysinfo.go`, `plugins/sysinfo/sysinfo_test.go`.

**Interfaces:** Define `sysinfo.ProcessMemorySnapshot` with R1 fields `NumGoroutine`, `HeapAlloc`, `HeapInuse`, `HeapIdle`, `HeapReleased`, `HeapObjects`, `StackInuse`, `StackSys`, `Sys`, `NextGC`, `NumGC`, `RSSBytes`, and `RSSAvailable`. Define `sysinfo.ResourceSnapshot` with `ProcessMemory ProcessMemorySnapshot`, `TaskEngine taskengine.RuntimeStats`, `TaskEngineAvailable bool`, `EventBus core.EventBusStats`, `Persistence jobs.PersistencePumpStats`, `Jobs jobs.Diagnostics`, `Interaction interaction.Stats`, `Inline inline.RuntimeStats`, `ResourceActive`, `ResourceLeaked`, DB open/in-use/idle, resolver cache count, peer cache entries/bytes, and RPC total requests/flood waits. Add `(*Plugin).SetDiagnosticsProvider(func() ResourceSnapshot)` and `(*Plugin).ResourceSnapshot() (ResourceSnapshot, bool)`; the handler calls the latter exactly once. The existing direct fields supply fallback output and DLQ count.

- [ ] Write `TestDiagnosticsCard_ProviderSnapshot` with nonzero sample values for each section; assert key tokens and exactly one callback invocation. Write `TestDiagnosticsCard_Unavailable` for RSS and TaskEngine status, `TestDiagnosticsCard_Bounded` with maximum-width counters, and `TestDiagnosticsCard_BoundedLargeMaps` with 100 long names and explicit omission counts; assert rendered length <3,500. Keep `TestFormatPoolRuntimeStats` passing.
- [ ] Run `go test ./plugins/sysinfo -run 'TestDiagnosticsCard|TestFormatPoolRuntimeStats'`; expect FAIL for new tests.
- [ ] Implement DTO, setter, and card rendering with sorted pool/resource names, fixed numeric sections, and aggregate resource totals. Avoid owner lists in provider mode. Preserve legacy path when callback is nil.
- [ ] Run focused tests and `go test -race ./plugins/sysinfo`; expect PASS. Format changed Go files.
- [ ] Commit `feat(sysinfo): render production resource snapshot`.

### Task 2: App snapshot mapping and wiring

**Files:** Create `internal/app/sysinfo_diagnostics.go`; modify `internal/app/app.go`, `internal/app/diagnostics_test.go`.

**Interfaces:** Add `func (a *App) sysinfoResourceSnapshot() sysinfo.ResourceSnapshot` that calls `a.Diagnostics()` once and copies only the Task 1 fields. Add `func (a *App) wireSysinfoDiagnostics()` that finds plugin `sysinfo`, asserts `*sysinfo.Plugin`, and calls `SetDiagnosticsProvider(a.sysinfoResourceSnapshot)`. Invoke this after creating `*App` and before returning from `New`.

- [ ] Extend `TestApp_DiagnosticsCentralizedMetrics` to obtain the registered sysinfo plugin, invoke `ResourceSnapshot()`, and compare representative process, TaskEngine, EventBus, Jobs, and cache fields to `App.Diagnostics()`. Add `TestApp_SysinfoSnapshotUnavailable` for a zero-value app and stopped TaskEngine.
- [ ] Run `go test ./internal/app -run 'TestApp_Diagnostics|TestApp_Sysinfo'`; expect FAIL for new tests.
- [ ] Implement mapping/wiring and copy TaskEngine pool/resource maps; if sysinfo registration is absent, return safely without changing app startup behavior. Do not call diagnostics during wiring.
- [ ] Run focused app tests and `go test -race ./internal/app -run 'TestApp_Diagnostics|TestApp_Sysinfo'`; expect PASS. Run `go vet ./...` and `go build ./cmd/goultroid`; expect success. Format changed Go files.
- [ ] Commit `feat(app): wire operator resource diagnostics`.

## Acceptance note

The user-provided card had `interactive` workers 2, `general` workers 1, zero waiting, and 20.8 KB retained. The new card should show those existing values plus process RSS/goroutines and the R1 subsystem worker counts. A single card remains a point-in-time sample; burst/settle comparisons require repeated captures at T0, post-burst, +10 s, +30 s, +90 s, and +5 min.
