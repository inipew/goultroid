# R1 Resource Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make app diagnostics account for process memory, physical lazy workers, and bounded runtime state so burst and settle measurements identify resource owners.

**Architecture:** Extend existing read-only snapshot APIs at their owning packages, then compose them in `App.Diagnostics()`. Keep process sampling and goroutine profile creation in app diagnostic helpers. No periodic sampler, retention-policy change, or new health threshold.

**Tech Stack:** Go 1.27.0 standard `runtime`, `runtime/pprof`, Linux `/proc/self/statm`, existing package statistics.

**Spec:** `docs/superpowers/specs/2026-09-27-resource-observability-design.md`

## Global Constraints

- Keep package boundaries enforced by `internal/architecture`.
- All diagnostic counters are read-only, bounded-cardinality, and payload-free; memory units are bytes.
- Preserve existing `DiagnosticsSnapshot.Process` (OS process runner) and `Health()` behavior.
- Missing components yield zero-value sections; unavailable RSS yields zero plus `RSSAvailable=false`.
- Keep existing 200 ms TaskEngine and durable-job snapshot deadlines; indicate TaskEngine timeout with `TaskEngineSnapshotOK=false`.
- No new periodic goroutine profile or background sampler.

## Review Focus

- `statm` missing or malformed: `RSSAvailable=false`, other process fields still populate (Task 1).
- TaskEngine not started or control request times out: app snapshot returns promptly and marks the section unavailable (Task 4).
- Concurrent worker retirement during sampling: nonnegative counters and race-free reads (Tasks 2–3).
- Nil plugin interaction runtime or inline cache: app snapshot returns without panic (Task 4).
- Goroutine profile writer fails: the error reaches the caller (Task 5).

---

## File map

- `internal/app/process_memory.go`, `process_memory_linux.go`, `process_memory_other.go`: process snapshot and portable RSS boundary.
- `internal/app/diagnostics.go`: compose snapshots and expose status flags.
- `internal/interaction/session.go`: provide passive statistics that do not prune on health probes.
- `internal/app/goroutine_profile.go`: explicit on-demand profile output.
- `internal/taskengine/engine.go`: expose existing completion and durability lane atomics.
- `internal/jobs/persistence_pump.go`, `manager.go`: expose existing persistence and retry worker counters.
- Nearby `*_test.go` files: focused behavioral and error-path verification.

### Task 1: Process memory snapshot

**Files:** Create `internal/app/process_memory.go`, `internal/app/process_memory_linux.go`, `internal/app/process_memory_other.go`, `internal/app/process_memory_test.go`.

**Interfaces:** Produce `ProcessMemoryDiagnostics` with `NumGoroutine int`, Go memory fields `uint64` except `NumGC uint32`, `RSSBytes uint64`, `RSSAvailable bool`; `processMemoryDiagnostics() ProcessMemoryDiagnostics`; `readRSSBytes() (uint64, bool)` using Linux build tags and a non-Linux zero/false implementation.

- [ ] Write `TestProcessMemoryDiagnostics` asserting goroutines positive and `Sys >= HeapAlloc`, and Linux RSS parser tests for valid, missing, and malformed `statm` input using an injectable parser/helper.
- [ ] Run `go test ./internal/app -run 'TestProcessMemory|TestRSS'` and confirm the new tests fail.
- [ ] Implement the interfaces; Linux resident pages are parsed from field 2 and multiplied by `os.Getpagesize()`. Do not treat a zero RSS as available.
- [ ] Run the focused command again; expect PASS. Run `gofmt` on changed Go files.
- [ ] Commit with `feat(app): expose process memory diagnostics`.

### Task 2: TaskEngine lane worker snapshots

**Files:** Modify `internal/taskengine/engine.go`; test in `internal/taskengine/diagnostics_test.go` or `internal/taskengine/lazy_lane_test.go`.

**Interfaces:** Produce `LaneRuntimeStats{WorkerLimit, Workers, Pending, Active int}` and `RuntimeStats.DeliveryLane`, `RuntimeStats.DurabilityLane`. Populate `Workers` from each lane's `remaining`, `Pending` from `pending`, `Active` from `active`, and limit from `workers`; nil lanes report zero. Preserve existing queue/capacity/failure fields.

- [ ] Write `TestStats_LaneWorkers` using blocked delivery and durability callbacks to assert physical/active/pending counts, then assert workers return to zero after the configured short test idle timeout.
- [ ] Run `go test ./internal/taskengine -run TestStats_LaneWorkers`; expect FAIL.
- [ ] Implement lane snapshots without taking locks around the atomic counters; read them as a best-effort point-in-time view.
- [ ] Run the focused test and `go test -race ./internal/taskengine`; expect PASS. Format changed Go files.
- [ ] Commit with `feat(taskengine): report delivery and durability workers`.

### Task 3: Persistence and retry worker snapshots

**Files:** Modify `internal/jobs/persistence_pump.go`, `internal/jobs/manager.go`; test in `internal/jobs/persistence_pump_test.go` and `internal/jobs/retry_workers_test.go`.

**Interfaces:** Add `WorkerLimit`, `Workers`, `Queued`, `Active` to `PersistencePumpStats`; read `concurrency`, `remaining`, `queued`, `active`. Add `RetryWorkerLimit`, `RetryWorkers`, `RetryQueued`, `RetryActive`, `TrackedOccurrences` to `jobs.Diagnostics`; read the retry atomics and `len(tracked)` under `m.mu`, with limit `retryWorkers`.

- [ ] Write `TestPersistencePumpStats_Workers` for idle, queued, active, and retired states; extend retry worker tests to assert `Diagnostics()` counters and tracked count while an occurrence is live.
- [ ] Run focused `go test ./internal/jobs -run 'TestPersistencePumpStats_Workers|TestRetryWorkers'`; expect FAIL.
- [ ] Implement the new fields with existing locks and atomics; avoid holding `m.mu` while querying the durable store.
- [ ] Run the focused tests and `go test -race ./internal/jobs`; expect PASS. Format changed Go files.
- [ ] Commit with `feat(jobs): report persistence and retry workers`.

### Task 4: Compose app diagnostics

**Files:** Modify `internal/app/diagnostics.go`, `internal/app/diagnostics_test.go`, `internal/interaction/session.go`, `internal/interaction/runtime_lifecycle_test.go`.

**Interfaces:** Add `ProcessMemory ProcessMemoryDiagnostics`, `Interaction interaction.Stats`, `Inline inline.RuntimeStats`, `Resources ResourceDiagnostics{TotalActive, Leaked int}`, and `TaskEngineSnapshotOK bool` to `DiagnosticsSnapshot`; add `ActiveWorkers`, `OrderedWorkers` to `EventBusDiagnostics`. Add `interaction.Runtime.SnapshotStats() Stats` to read retained counters without pruning. Consume Tasks 1–3 plus `plugin.Manager.InteractionRuntime().SnapshotStats()`, `inline.Engine.RuntimeStats()`, and `resource.Manager.AllSnapshots()`.

- [ ] Extend `TestApp_DiagnosticsCentralizedMetrics` to assert process fields, EventBus worker forwarding, interaction and inline snapshots, and resource totals; add `TestApp_DiagnosticsUnavailableComponents` covering nil/stopped components and a failed TaskEngine stats request.
- [ ] Run `go test ./internal/app -run 'TestApp_Diagnostics'`; expect FAIL.
- [ ] Implement snapshot composition, summing owner resource snapshots after their API returns; set `TaskEngineSnapshotOK` only on a successful engine query.
- [ ] Run focused tests and `go test -race ./internal/app ./internal/architecture`; expect PASS. Format changed Go files.
- [ ] Commit with `feat(app): compose production resource diagnostics`.

### Task 5: Operator-requested goroutine dump

**Files:** Create `internal/app/goroutine_profile.go`, `internal/app/goroutine_profile_test.go`.

**Interfaces:** Produce `func (a *App) WriteGoroutineProfile(w io.Writer) error`, using `pprof.Lookup("goroutine").WriteTo(w, 2)`; reject nil writers with an error. No command registration or timer.

- [ ] Write `TestWriteGoroutineProfile` asserting a nonempty profile containing goroutine stacks, and `TestWriteGoroutineProfile_WriterError` asserting the supplied writer error is returned.
- [ ] Run `go test ./internal/app -run TestWriteGoroutineProfile`; expect FAIL.
- [ ] Implement the on-demand method and nil writer guard.
- [ ] Run focused tests and `go test -race ./...`; expect PASS. Run `go vet ./...` and `go build ./cmd/goultroid`; expect success.
- [ ] Commit with `feat(app): add on-demand goroutine profile`.

## Measurement handoff

After R1 lands, sample startup, immediate post-burst, +10 s, +30 s, +90 s, and +5 min using the same workload. Record pool workers/running/waiting, lane workers/pending/active, EventBus workers, pump/retry workers, sessions/inputs, inline cache bytes, resource totals, process heap fields, and RSS. This data decides R2–R4; this task does not assign new limits.
