# O1 — C3 Alternatives Study & Measurement Plan: `sendSubmitControl` & `sendControl`

## Status

- **Workstream:** O1 — Runtime Performance & Hot-Path Optimization
- **Phase:** Gate C — **C3 (`sendSubmitControl` Alternatives Study)**
- **Study status:** `C3-R1.1 study completed — pending final review/commit`
- **Baseline branch / HEAD:** `test-next` (`5cafd565b099326a84fb9d784428a3fd517c92ed`, verified matching `origin/test-next`)
- **Prerequisite:** C1 and C2 closed (`5cafd565b099326a84fb9d784428a3fd517c92ed`).
- **Production code changes:** None. Status Quo A is retained.

---

## 1. Constraints (Non-Negotiable)

- Do **not** add a channel pool (`sync.Pool` of reply channels), new goroutines, worker pools, a second control protocol, or a new retry mechanism.
- Do **not** change public APIs or production behavior during this study phase.
- Preserve `submitCell` linearization semantics (`decisionCancelled` vs `decisionAccepted` / `decisionRejected`) so `Submit` never returns a cancellation error after the coordinator has already admitted the task.
- Preserve `decisionTimeout` enforcement both while waiting on `inbox` handoff and while waiting on coordinator admission/reply.

---

## 2. Source Audit: Current `sendSubmitControl` & `sendControl` Paths

### 2.1 `sendSubmitControl` (`internal/taskengine/engine_admission.go:14-61`)

1. **Snapshot runtime handles (`engine_admission.go:15-22`):**
   - Acquires `e.mu.Lock()` to read `rootCtx` and `decisionTimeout`.
   - If `rootCtx == nil`, increments `admissionRejections` and returns `ErrEngineQuiescing`.
2. **Implicit deadline wrapping (`engine_admission.go:26-30`):**
   - If `ctx` has no deadline (`_, hasDeadline := ctx.Deadline(); !hasDeadline`), calls `context.WithTimeout(ctx, timeout)` and `defer cancel()`.
   - This allocates a `context.timerCtx` + `time.Timer` on every call whose caller passed `context.Background()` or an un-deadlined context.
3. **Envelope & synchronization allocation (`engine_admission.go:31-34`):**
   - `decision := &submitCell{}` (heap-allocated atomic `uint32` cell for linearization).
   - `reply := make(chan engineReply, 1)` (heap-allocated 1-slot buffered channel whose element size is `sizeof(engineReply)`).
   - `queued := e.acquireRequest(req)` borrows an `*engineRequest` envelope from `e.requestPool` (`sync.Pool`).
4. **Inbox handoff (`engine_admission.go:35-39`, `engine.go:120-142`):**
   - `enqueueRequest` takes `e.controlGate.RLock()`, reads `inbox` and `rootCtx` under `e.mu`, and selects on `inbox <- queued`, `<-ctx.Done()`, `<-rootCtx.Done()`.
   - On handoff failure, increments `admissionRejections`, returns `queued` to `e.requestPool` via `e.releaseRequest(queued)` (zeroing all fields), and returns the error.
5. **Coordinator processing & reply (`engine.go:216-218, 233-241`):**
   - `runLoop` copies `*reqPtr` onto its stack, immediately calls `e.releaseRequest(reqPtr)`, and runs `handleRequest(ctx, req)`.
   - `admitSubmit` checks `callerCtx.Err()` at entry (`engine_admission.go:64-68`) and again before the linearization CAS (`engine_admission.go:135-139`, `152`), then either rejects or admits.
   - `handleRequest` sends `req.reply <- engineReply{ticket: ticket, err: err}` (buffered capacity 1, non-blocking even if the caller already exited via `decisionCancelled`).
6. **Caller wait loop (`engine_admission.go:41-60`):**
   - Selects on `<-reply`, `<-ctxDone`, `<-rootDone`.
   - If `<-ctxDone` or `<-rootDone` fires first, calls `decision.decide(decisionCancelled)`. If CAS wins, returns immediately; if coordinator already published accepted/rejected, disables `ctxDone`/`rootDone` (`= nil`) and continues waiting for `<-reply`.

### 2.2 `sendControl` (`internal/taskengine/engine.go:384-415`)

- Used by `Cancel`, `CancelScope`, `Snapshot`, `Result`, `Stats`, `Quiesce`, `SetOwnerLimits`, `ConfigurePool`, `SetResourceCapacity`.
- Shares the same `context.WithTimeout` (line 397) + `req.reply = make(chan engineReply, 1)` (line 401) allocation pattern, without `submitCell` (except `controlDecision` for `opSetOwnerLimits`).
- Uses `select` on `<-req.reply`, `<-ctx.Done()`, `<-rootCtx.Done()`.

### 2.3 Baseline Allocation Evidence (from P2 `pprof` on `internal/taskengine`)

From `te_mem.prof` (`3801.75 MB` total across `BenchmarkB0`–`BenchmarkB8` + `BenchmarkTaskEngineFirstTaskAfterIdle`):

| Source | Flat | Cum | Notes |
|---|---:|---:|---|
| `sendSubmitControl` total | `685.91 MB` (`18.04%`) | `1004.95 MB` (`26.43%`) | `6.44M` objects (`24.36%`) |
| ↳ `reply := make(chan engineReply, 1)` (`engine_admission.go:32`) | `681.91 MB` (`17.94%`) | `681.91 MB` | Allocates `hchan` + 1-slot `engineReply` union buffer |
| ↳ `context.WithTimeout` (`engine_admission.go:28`) | — | `221.52 MB` | `timerCtx` + `cancelCtx.Done` + `time.Timer` |
| ↳ `decision := &submitCell{}` (`engine_admission.go:31`) | `4.00 MB` | `4.00 MB` | Tiny atomic state cell |
| `sendControl` total | `483.79 MB` (`12.73%`) | `551.30 MB` (`14.50%`) | Same `make(chan engineReply, 1)` + `WithTimeout` pattern |

---

## 3. Alternatives Comparison

### Alternative A — Status Quo (Baseline)
- **Mechanism:** `make(chan engineReply, 1)` + `context.WithTimeout` when caller has no deadline.
- **Pros:** Zero lifecycle or stale-reply risk; simple ownership (channel garbage-collected after single send/receive).
- **Cons:** Allocates full `sizeof(engineReply)` channel buffer on every `Submit` and `Stats`/`Snapshot`/`Cancel`, even though `Submit` only returns `(tasks.Ticket, error)` and `Snapshot` only returns `(TaskSnapshot, bool)`.

### Alternative B — Embed `(ticket, err)` + `done chan struct{}` inside `submitCell` (for `opSubmit` only)
- **Mechanism:** Instead of allocating both `&submitCell{}` AND `make(chan engineReply, 1)` (with full `engineReply` union buffer), store `ticket tasks.Ticket` and `err error` on `submitCell` (written by `runLoop` before closing or sending on a 1-slot `chan struct{}` — 0-byte element!).
- **Semantics & safety:**
  - `submitCell` is still per-call (never pooled or reused) → **zero stale-reply or cross-caller contamination risk**.
  - Channel buffer shrinks from `sizeof(engineReply)` to `0` bytes (`chan struct{}`).
  - Closing/sending on `done` happens-before caller reads `cell.ticket` and `cell.err` (standard Go channel synchronization).
- **Complexity / risk:** Low. Only touches `submitCell`, `sendSubmitControl`, and `case opSubmit` in `handleRequest`.

### Alternative C — Slim per-operation reply channels (or `chan submitReply` for `opSubmit`)
- **Mechanism:** Use `chan submitReply` (2 words: `ticket`, `err`) for `opSubmit` instead of `chan engineReply` (entire union).
- **Comparison with Alt B:** Similar idea, but Alt B goes further (`chan struct{}` 0-byte buffer + co-located in `submitCell`). Alt C still allocates both `submitCell` and a 2-word channel buffer.

### Alternative D — Pass `deadline time.Time` instead of `context.WithTimeout` when caller `ctx` has no deadline
- **Mechanism:** When `ctx.Deadline()` is false, instead of allocating `context.WithTimeout(ctx, timeout)` (`timerCtx` + `time.Timer` + `Done` channel), pass an explicit `deadline := time.Now().Add(timeout)` on `engineRequest` and use a local `time.NewTimer(timeout)` only when waiting (or check `time.Now().After(deadline)` in `admitSubmit`).
- **Caveat:** `admitSubmit` currently passes `callerCtx` to `callerCtx.Err()` and `freezePayload`/etc. Wait: if `callerCtx` has no deadline, `callerCtx.Err()` only checks parent cancellation; `admitSubmit` would additionally need to check `!req.deadline.IsZero() && !time.Now().Before(req.deadline)`.
- **Complexity / risk:** Medium. Must verify all `callerCtx` deadline checks in `admitSubmit` and `enqueueRequest` preserve exact `ErrLinearizationCancel` / `context.DeadlineExceeded` error wrapping.

### Alternative E — Pooling `chan engineReply` in `sync.Pool` (**Not Recommended**)
- **Mechanism:** Retain `chan engineReply` in `engineRequest` across `acquireRequest`/`releaseRequest`.
- **Why rejected:**
  - Late coordinator reply after caller timeout/cancellation can land in a recycled channel unless drained on every return and fenced by generation.
  - `sync.Pool` gives no retention guarantee across GC cycles.
  - Violates C3 constraints ("jangan menambah channel pool").

---

## 4. Benchmark & Measurement Matrix (Study Phase)

To evaluate Alternatives A–D empirically across all required operating regimes without touching production behavior:

| Scenario | What it measures | Metrics |
|---|---|---|
| **1. Normal (no caller deadline)** | `Submit` with `context.Background()` (exercises implicit `decisionTimeout` + reply) | `ns/op`, `B/op`, `allocs/op`, p50/p95/p99 |
| **2. Normal (caller with deadline)** | `Submit` with pre-existing `context.WithTimeout` (isolates reply channel cost from `WithTimeout`) | `ns/op`, `B/op`, `allocs/op` |
| **3. Contention** | `b.RunParallel` concurrent submitters against `Engine` | throughput (ops/s), `B/op`, `allocs/op`, p95/p99 |
| **4. Cancellation** | Pre-cancelled `ctx` and mid-flight cancellation during `sendSubmitControl` | `ns/op`, `B/op`, `allocs/op`, zero leaked/blocked replies |
| **5. Control queue full (`InboxCapacity` saturation)** | `Submit` blocking/timing out when `inbox` is full | timeout/rejection latency, `B/op`, `allocs/op`, zero deadlocks |

---

## 5. Recommendation & Gate

- **Study execution complete:** The C3-R1.1 end-to-end production baseline (`Status Quo A`), exploratory primitive microbenchmarks (`B`, `C`, `D`), and deterministic `-race` lifecycle/linearization tests have all been executed (results in §6).
- **No production implementation authorized:** No changes to `engine_admission.go`, `engine.go`, `submitCell`, or reply/control protocols are authorized.
- **Final decision:** **Keep Alternative A (Status Quo).** Measured primitive differences do not provide end-to-end evidence sufficient to justify added admission/reply complexity.

---

## 6. Study Execution Results — C3-R1.1 (Measured on `5cafd565b099326a84fb9d784428a3fd517c92ed`)

### 6.1 Methodology & Limitations (C3-R1.1 Correction)
- **End-to-end vs primitive separation:**
  - `BenchmarkC3_EndToEnd_ProductionSubmitAndWait` exercises the **real production `Engine.Submit` + `Ticket.Wait` cycle** (`sendSubmitControl` → `controlGate` → `requestPool` → `enqueueRequest` → `runLoop` → `admitSubmit` → worker execution → `Wait`) for `normal_no_deadline`, `normal_with_deadline`, and `contention`, and `Submit`-only for error scenarios (`cancellation`, `inbox_full_timeout`). All reported metrics (`ns/op`, `p50`/`p95`/`p99`, `B/op`, `allocs/op`) consistently measure the full `Submit + Wait` cycle for the 3 success scenarios rather than pure `Submit` alone.
  - `BenchmarkC3_PrimitiveCost` provides **exploratory primitive-level cost estimates only** (reply channel & timer primitives). Alternatives B, C, and D are **not** semantically equivalent end-to-end implementations in this benchmark, and primitive deltas must not be interpreted as measured end-to-end production savings.
- **Explicit limitation for Alternative B:** `admitSubmit()` in `internal/taskengine/engine_admission.go` takes a concrete `*submitCell` and performs `decision.decide(decisionAccepted)` atomically inside admission. Benchmarking B end-to-end with identical linearization semantics is therefore not possible without modifying production code, so B is evaluated only at the primitive level alongside C and D.
- **Contention percentile methodology:** `p50`/`p95`/`p99` under `contention` are computed from bounded per-goroutine sample buffers (capped at `50,000` samples total across `b.RunParallel` workers) and serve as indicative per-operation latency distributions under synthetic parallel load, not production SLAs. Note that `ns/op` in `b.RunParallel` reflects average CPU/wall cost per operation derived from parallel throughput, whereas `p50`/`p95`/`p99` reflect individual operation wall-clock latencies; they measure different dimensions.

### 6.2 Correctness & Race Verification (Real `Engine.Submit`, Deterministic Barriers)
- `go test -race -count=1 ./internal/taskengine/...` — **PASS (`3.794s`)**, including `TestC3_ProductionSubmitLinearizationAndLifecycle`:
  - `normal_and_precancel`: `TaskID` integrity and `context.Canceled` semantics;
  - `midflight_cancel_at_admission_barrier`: uses a `HandlerResolver` channel barrier (`entered`/`proceed`) to hold the coordinator deterministically inside `admitSubmit()` before `decisionAccepted`, cancels the caller, verifies `ErrLinearizationCancel + context.Canceled`, releases the barrier, and verifies the task was not admitted (`e.Snapshot` absent);
  - `late_reply_after_caller_exit_and_inbox_full`: holds the coordinator at the `HandlerResolver` barrier, fills `InboxCapacity: 1`, waits for `len(e.inbox) >= 1`, cancels `lateCtx` and waits for `Submit` to return before releasing the coordinator, and verifies the coordinator sends the late reply without blocking `runLoop` and drains cleanly on `Stop()` (`<-e.runtimeDone`);
  - `shutdown_while_submit_inflight`: initiates `e.Stop()` while `Submit` is deterministically held at the `HandlerResolver` barrier inside `admitSubmit()`, waits for `len(e.inbox) > 0` (confirming `opQuiesce` entered `e.inbox`), then releases the barrier and verifies clean completion (`<-e.runtimeDone`).

### 6.3 End-to-End Production `Engine.Submit + Ticket.Wait` Baseline (`Status Quo A`, `-benchmem -benchtime=500ms`)

| Scenario | Scope | `ns/op` | `p50` (ns) | `p95` (ns) | `p99` (ns) | `B/op` | `allocs/op` |
|---|---|---:|---:|---:|---:|---:|---:|
| `normal_no_deadline` | `Submit + Wait` | `11662` | `9659` | `19667` | `32271` | `3185` | `26` |
| `normal_with_deadline` | `Submit + Wait` | `11090` | `9247` | `20078` | `34154` | `2800` | `21` |
| `contention` (`RunParallel`) | `Submit + Wait` | `9088` | `158569` | `226998` | `273406` | `3004` | `25` |
| `cancellation` (pre-cancelled) | `Submit` error | `1660` | `1322` | `4368` | `8576` | `1074` | `9` |
| `inbox_full_timeout` (`50µs` deadline) | `Submit` timeout | `1091988` | `1112681` | `1137287` | `1155241` | `1235` | `9` |

### 6.4 Exploratory Primitive Cost Estimates (`BenchmarkC3_PrimitiveCost`)

| Primitive | `ns/op` | `B/op` | `allocs/op` | Arithmetic delta vs A primitive |
|---|---:|---:|---:|---|
| `reply_A_engineReply_chan` | `338.1` | `816` | `2` | Baseline (`submitCell` + `chan engineReply, 1`) |
| `reply_B_submitCell_done_chan` | `81.54` | `112` | `1` | `-256.6 ns/op`, `-704 B/op`, `-1 alloc/op` |
| `reply_C_slim_submitReply_chan` | `134.0` | `144` | `2` | `-204.1 ns/op`, `-672 B/op`, `0 allocs/op` |
| `timeout_context_WithTimeout` | `565.4` | `384` | `5` | Baseline implicit deadline |
| `timeout_time_NewTimer` | `235.9` | `248` | `3` | `-329.5 ns/op`, `-136 B/op`, `-2 allocs/op` |

### 6.5 Evaluation & Final C3 Conclusion

1. **Alternative D (`time.NewTimer` without `WithTimeout`):** The primitive delta is only `136 B/op` and `2 allocs/op` without measuring full cancellation propagation, enqueue timeout, or `admitSubmit` deadline checks. **Not recommended.**
2. **Alternative B vs Alternative C (exploratory primitive estimates):** At the primitive level, B reduces channel+cell allocation by `704 B/op` (`22.1%` of the `3,185 B/op` end-to-end `Submit + Wait` `normal_no_deadline` baseline) and `1 alloc/op`, while C reduces it by `672 B/op` (`0 allocs/op`). The primitive CPU difference (`~257 ns/op` vs `11,662 ns/op`, or `~2.2%` arithmetically) is from an isolated microbenchmark, not an end-to-end `Engine.Submit` measurement.
3. **Final C3 Conclusion:** **Keep Status Quo A (no production code changes).** Pada benchmark serial dan contention sintetis yang dijalankan, baseline Status Quo A menunjukkan biaya `~9.1–11.7 µs/op` dan `~2.8–3.2 KB/op` untuk siklus `Submit + Wait`. Belum ada bukti bahwa penghematan primitif dari alternatif lain akan menghasilkan peningkatan berarti pada workload produksi Goultroid.
