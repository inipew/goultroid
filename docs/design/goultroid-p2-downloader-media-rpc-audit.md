# P2-A Audit — Downloader → Retained Media → Telegram RPC

## Status

- Status: **P2-A Complete (Read-Only Architecture & Ownership Inventory)**
- Workstream: P2 of the Goultroid Architecture Audit Roadmap P1–P3
- Branch: `test-next`
- Baseline HEAD (post-P1 closure): `229780a4334853735e5fcb61f1131f2be7039b24`
- Scope: read-only inventory and ownership mapping. No production code, API, or configuration was modified.

---

## 1. Execution Flows (Source-Verified)

### Flow A — Telegram Media Download (`.download` / `.dl` reply or attached media)
`core.Router` → `downloader.Plugin.handleDownload` (`plugins/downloader/downloader.go:227`) → `ValidateMediaSize` + `CheckDiskSpace` → `ctx.Progress` → `detachDownloadContext` → `submitContinuation` (`Pool: "download"`, `PriorityNormal`, `Resources: [{download: 1}]`, `ExecutionTimeout: 10m`) → `executeMediaDownload` (`downloader.go:307`) → `createDownloadWorkspace` (`files.CreateTempDir`) → `ctx.DownloadMedia(workspace)` → `storage.Put` → `registerRetainedAsset(LifecycleRetained, "downloader.telegram")` (`plugins/downloader/media_registry.go:39`) → `ctx.Edit`.

### Flow B — Native URL Download (`.download <url>` without `selfinline.Renderer`)
`handleDownload` → `handleURLDownload` → `startNativeURLDownload` (`plugins/downloader/url_native.go:12`) → `submitURLPipeline` (`plugins/downloader/url_pipeline.go:34`, `Pool: "download"`, `Resources: [{download: 1}]` + `[{process: 1}]` if `extractor`) → `download.Registry.Download` (HTTP or `ExtractorProvider`/yt-dlp) + `downloadProgressReporter` (`2s` interval) → `registerRetainedAsset(LifecycleRetained, "downloader.<provider>")` → `OnComplete` → `submitRetainedDelivery` (`plugins/downloader/delivery.go:184`, `Pool: "general"`, `Resources: [{media: 1}]`, `ExecutionTimeout: 30m`) → `materializeDeliveryAsset` → `callCtx.SendMedia` → `telegram.Service.SendMedia` (`internal/telegram/service.go:1357`) → `uploader.FromPath` (`managedUploadRPCClient`) + `execNonIdempotentPeerVal("messages.sendMedia")`.

### Flow C — Interactive / Inline URL & `.yt` Search
`selfinline.Renderer` / inline query (`dl <url>` / `yt <query>` in `plugins/downloader/interactive.go`, `youtube_search.go`) → `interactiveState` (`phaseChoose` → `phaseAudioFormat`/`phaseVideoFormat` → `phaseRunning`) → `submitInteractivePipeline` → `submitURLPipeline` → `submitRetainedDelivery` → `orchestration.MediaDelivery` → `internal/presentation/telegram/bridge.go:120-123` → `telegram.Service.SendMedia`.

---

## 2. Component Ownership Matrix

| Component | Responsibility | Owner | State | Resource | Lifecycle |
|---|---|---|---|---|---|
| `downloader.Plugin` | Command/inline planning, TaskEngine submission, retained registration | `plugins/downloader` | Stateless (uses `interaction` / `TaskEngine`) | `filesystem.Scope`, `storage.Storage` | Initialized via `InitPlugin(pctx)` |
| `download.Registry` & Providers | URL validation (SSRF), HTTP download, `ExtractorProvider` (`yt-dlp`) | `internal/services/download` | `activeTemps` map (bounded by active tasks) | `process.Runner` (`2` concurrent, `5m`, `4MiB`), disk temp dirs | Per-download `ctx` cancellation & temp cleanup |
| `downloadProgressReporter` | Periodic progress edits (`2s` interval, `8s` edit timeout) | `plugins/downloader/progress.go` | Atomic counters (`downloaded`, `total`, `revision`) | 1 bounded ticker goroutine per active download | Closed via `reporter.Close()` |
| `storage.FileStorage` | Local file persistence (`data/downloads`, `0700`/`0600`), quota tracking | `internal/services/storage/fs.go` | Atomic `usage` (`maxQuota = 5GiB`) | Disk files under `baseDir` | `NewFileStorage` cleans `.part`/partial files at startup |
| `mediaregistry.Registry` & `Reclaimer` | Asset ownership & lifecycle (`LifecycleRetained`), startup orphan policies | `internal/services/mediaregistry` | SQLite (`media_assets`, `media_reclamation_*`) | Bounded batch (`Default=16`, `Max=128`) | Startup reconciliation in `internal/app/modules.go:124-159` |
| `taskengine.Engine` | Admission, worker pools (`download`, `general`), resource grants | `internal/taskengine` | Single-writer coordinator + `ZeroIdle` workers | `download: 3`, `general: 8` workers | `Start` / `Stop` managed by app runtime |
| `telegram.Service` & `RPCExecutor` | Chunked upload (`uploader.FromPath`), `messages.sendMedia`, FloodWait | `internal/telegram/service.go` | Shared `RPCExecutor` & `HierarchicalRPCLimiter` | `DefaultMaxUploadSize = 500MiB`, `mediaTransferTimeout` | Bound to Telegram service lifecycle |

---

## 3. Media Ownership Transition Matrix

| Transition | Current owner | New owner | Trigger | Cleanup responsibility |
|---|---|---|---|---|
| Temp workspace creation → download bytes | `downloader` (`files.CreateTempDir` / `ExtractorProvider`) | `downloader` / `ExtractorProvider` | `executeMediaDownload` / `Download` | `defer cleanup()` (`RemoveTempDir`) on exit/error/cancel |
| Temp bytes → `storage.Asset` | Temp workspace | `storage.Storage` (`FileStorage`) | `targetStore.Put(ctx, f, meta)` | `FileStorage` enforces quota; temp workspace cleaned after `Put` |
| `storage.Asset` → Retained registration | `storage.Storage` | `mediaregistry.Registry` (`Owner: "downloader"`, `LifecycleRetained`) | `registerRetainedAsset` (`media_registry.go:39`) | On registration error, `registerRetainedAsset` rolls back via `store.Delete` (`5s` timeout) |
| Retained asset → Delivery materialization | `storage.Storage` | `submitRetainedDelivery` (`delivery.go:184`) | `materializeDeliveryAsset` (`delivery.go:87`) | Reuses regular file path (no-op cleanup) or materializes temp copy cleaned by `defer cleanup()` |
| Telegram upload & send | `submitRetainedDelivery` | `telegram.Service.SendMedia` (`RPCExecutor`) | `delivery(taskCtx, presentation.Media)` | `FileStorage` retains asset (`LifecycleRetained`); delivery temp copy (if any) removed on `defer cleanup()` |

---

## 4. Resource Boundary Matrix

| Resource | Configured limit | Enforcement point | Actual scope | Evidence |
|---|---|---|---|---|
| `download` pool concurrency | `Concurrency: 3`, `BacklogLimit: 50`, `PayloadBudget: 200MiB`, `ZeroIdle: true` | `taskengine.Engine` | Process-wide | `internal/taskengine/engine_config.go:67` |
| `general` pool concurrency | `Concurrency: 8`, `BacklogLimit: 200`, `PayloadBudget: 100MiB`, `ZeroIdle: true` | `taskengine.Engine` | Process-wide | `internal/taskengine/engine_config.go:65` |
| Download / Upload max size | `500 * 1024 * 1024` (500 MiB) | `DownloadOptions.MaxBytes`, `ValidateMediaSize`, `Service.SendMedia` | Per asset | `url_pipeline.go:102`, `downloader.go:270`, `service.go:1362` |
| `FileStorage` disk quota | `5 * 1024 * 1024 * 1024` (5 GiB) | `storage.NewFileStorage` (`downloader.go:114`) | `data/downloads` | `plugins/downloader/downloader.go:114` |
| Download execution timeout | `10 * time.Minute` | `WorkSpec.ExecutionTimeout` | Per download task | `plugins/downloader/downloader.go:23` |
| Delivery execution timeout | `30 * time.Minute` | `WorkSpec.ExecutionTimeout` | Per delivery task | `plugins/downloader/delivery.go:23` |
| Extractor process runner | `2` concurrent, `5m` timeout, `4 MiB` output cap | `process.NewOSRunner(2, 5*time.Minute, 4*1024*1024)` | `ExtractorProvider` | `internal/services/download/provider_extractor.go:66` |
| Progress edit cadence | `2s` interval, `8s` per-edit timeout | `downloadProgressReporter` | Per active URL download | `plugins/downloader/progress.go:14-15` |
| Interactive state / URL | `maxInteractiveURLLen = 2048`, `maxInteractiveState = 2304`, `TTL = 24h` | `plugins/downloader/interactive.go` | Per interactive session | `plugins/downloader/interactive.go:26-28` |

---

## 5. Telegram RPC Ingress Verification

Verified from source (`internal/telegram/service.go:156, 1357-1409`):
- `Service.SendMedia` validates `stat.Size() <= core.DefaultMaxUploadSize` (`500 MiB`).
- Chunked upload uses `s.uploader.FromPath(transferCtx, filePath)` where `s.uploader` is initialized with `&managedUploadRPCClient{...}` (routing upload chunks through `RPCExecutor` / limiter).
- Final `messages.sendMedia` dispatch uses `s.execNonIdempotentPeerVal(ctx, "messages.sendMedia", peer, ...)` through `RPCExecutor` (non-idempotent guard, FloodWait classification, rate limiting).
- No second Telegram RPC executor or parallel upload client exists in `plugins/downloader/`.

---

## 6. Test Inventory (Executed Locally)

`go test -count=1 ./plugins/downloader/... ./internal/services/download/... ./internal/services/mediaregistry/... ./internal/services/storage/...`: **PASS**

---

## 7. Open Questions & Observations for P2-B

1. **Retained Asset Lifetime (`LifecycleRetained`):** `internal/app/modules.go:131-146` configures startup orphan reclamation policies for `"clone"` (`LifecyclePersistent`) and `"media"` (`LifecycleTransient`), but `"downloader"` (`LifecycleRetained`) relies on explicit `FileStorage` quota (`5 GiB`) rather than age-based startup reclamation. P2-B should verify quota-pressure eviction/rejection behavior when `data/downloads` approaches `5 GiB`.
2. **Detached Continuation Contexts (`url_pipeline.go:66`, `delivery.go:236, 241`):** `continuationCtx := context.Background()` is used in `OnComplete` to submit delivery and terminal status edits so parent command cancellation does not abort admitted delivery, bounded by `downloaderDeliveryTimeout` (`30m`) and `downloaderStatusTimeout` (`30s`). P2-B should verify shutdown drain behavior when `TaskEngine` quiesces during an active `30m` delivery.
3. **Progress Reporter Goroutine Lifecycle (`progress.go`):** `newDownloadProgressReporter` spawns a ticker goroutine; P2-B should verify cancellation/panic paths always call `reporter.Close()` and join cleanly.


---

## 8. P2-B — Correctness & Failure Path Audit (Read-Only)

### 8.1 P2-B1 — Retained Media Lifetime & Storage Quota
- **Quota Enforcement (`internal/services/storage/fs.go:212-300`):** `FileStorage.Put` acquires `f.mu.Lock()`, checks `currentSize >= f.maxQuota`, streams through `io.LimitReader(input, remaining+1)`, verifies `currentSize+written+len(metaBytes) <= f.maxQuota`, atomically renames `.part` files to final paths, and rolls back `os.RemoveAll(dir)` on any error or context cancellation.
- **No Automatic Eviction:** Quota exhaustion fails closed with `ErrQuotaExceeded` (verified by `TestFileStorageHardQuotaIncludesMetadata` and `TestFileStorage_StorageHealthQuotaExceeded`); `FileStorage` does **not** evict older retained assets.
- **Startup Partial Cleanup:** `NewFileStorage` runs `cleanupPartialFiles` on `.part` files and recomputes `usage` via `storageSizeAt`.
- **Registry Consistency:** `registerRetainedAsset` (`plugins/downloader/media_registry.go:51-65`) rolls back `store.Delete` (`5s` timeout) if `mediaRegistry.RegisterAsset` fails (`TestRegisterRetainedAssetConflictRollsBackPhysicalAsset`). `LifecycleRetained` is intentionally excluded from startup orphan/age-based reclamation (`internal/app/modules.go:131-146`).

### 8.2 P2-B2 — Detached Continuation & Shutdown
- `continuationCtx := context.Background()` in `plugins/downloader/url_pipeline.go:66` and `delivery.go:236, 241` is used **only** as the admission context for `p.tasks.Submit(...)` inside `OnComplete` so parent command cancellation does not abort already-completed downloads before delivery/terminal-edit admission.
- **Shutdown Safety:** During shutdown/quiescing, `TaskEngine` sets `accepting = false` and rejects `Submit` with `ErrEngineQuiescing` (`internal/taskengine/engine_admission.go:88-90`), while cancelling `rootCtx` to drain active download (`10m` max) and delivery (`30m` max) tasks cleanly (`TestYTZLifecycleCancellationDoesNotEmitPostDisableFailureUI`).

### 8.3 P2-B3 — Progress Reporter Lifecycle
- `downloadProgressReporter` (`plugins/downloader/progress.go:76-118`) binds `stopParent = context.AfterFunc(parent, cancel)` and runs 1 ticker goroutine (`loop()`), joined synchronously via `<-r.done` in `Close()`.
- **Confirmed Defect (`P2B-F4`):** In `plugins/downloader/url_pipeline.go:108-110`, `reporter.Close()` is called sequentially after `p.registry.Download(...)` rather than via `defer`. If `Download` panics, `reporter.Close()` is skipped (goroutine still exits when `TaskEngine` cancels `taskCtx`, but is not joined synchronously before worker unwind and `stopParent` is not unregistered).

### 8.4 P2-B4 — Download & Media Processing Failure Matrix
| Failure Mode | Enforcement / Handling | Evidence |
|---|---|---|
| Invalid URL / scheme | `ValidateURL` rejects non-HTTP(S) & empty host | `internal/services/download/client.go:70-91` |
| Redirect / SSRF / DNS rebinding | `CheckRedirect` (max 5 + `ValidateURL`) + `Dialer.Control` (`validateDialAddress`), `Proxy: nil` | `client.go:108-147` |
| HTTP non-200 / oversized `Content-Length` | Early reject before `os.OpenFile` | `client.go:207-214` |
| Streaming overflow / transfer error | `io.LimitReader(resp.Body, maxBytes+1)` + `os.Remove(dstPath)` | `client.go:227-237` |
| Extractor timeout / output flood | `process.NewOSRunner(2, 5m, 4MiB)` + temp dir cleanup | `provider_extractor.go:66` |
| Disk space / size limit (Telegram media) | `ValidateMediaSize(500MiB)` + `CheckDiskSpace` | `plugins/downloader/downloader.go:270-281` |

### 8.5 P2-B5 — Telegram Delivery Failure Matrix
- `telegram.Service.SendMedia` (`internal/telegram/service.go:1357-1409`) enforces `DefaultMaxUploadSize` (`500 MiB`), uploads chunks via `uploader.FromPath` (`managedUploadRPCClient` → `RPCExecutor`), and sends via `execNonIdempotentPeerVal("messages.sendMedia")` (`RPCExecutor`).
- **Ambiguity / No Exactly-Once:** `execNonIdempotentPeerVal` does **not** blindly retry `messages.sendMedia` on ambiguous network/timeout errors (preventing duplicate sends on ambiguous RPCs), and `TestYTZZDeliveryFailureKeepsRetainedAsset` proves retained assets remain intact on delivery failure. If Telegram accepts `messages.sendMedia` right before a connection drop, the task reports failure while the message may already exist on Telegram.

### 8.6 P2-B6 — Cross-Component Ownership Consistency
| Failure Point | File State | Storage State | Registry State | Task State | Expected Recovery |
|---|---|---|---|---|---|
| Temp workspace created, `Submit` rejected | Cleaned by `defer cleanup()` or never created | None | None | Admission error | Immediate cleanup |
| `Submit` accepted, cancelled before start | None | None | None | `OutcomeCancelled` | Session cancelled in `OnComplete` |
| Download succeeds, `storage.Put` fails | Temp cleaned on `defer cleanup()` | `.part` rolled back (`os.RemoveAll`) | None | `OutcomeFailed` | Terminal failure edit |
| `storage.Put` succeeds, `RegisterAsset` fails | Rolled back via `store.Delete` | Deleted (`5s` timeout) | None | `OutcomeFailed` | Atomic rollback (`media_registry_test.go`) |
| `RegisterAsset` succeeds, delivery `Submit` fails | Retained on disk | `Asset` persisted | `LifecycleRetained` | Delivery not admitted | Retained asset intact; user notified |
| Delivery admitted, `materializeDeliveryAsset` fails | Delivery temp cleaned on error | `Asset` retained | `LifecycleRetained` | `OutcomeFailed` | Retained asset intact (`delivery_test.go`) |
| Materialization succeeds, upload/`SendMedia` fails | Delivery temp cleaned by `defer cleanup()` | `Asset` retained | `LifecycleRetained` | `OutcomeFailed` | Retained asset intact; FloodWait via `RPCExecutor` |
| `SendMedia` ambiguous (accepted, response lost) | Delivery temp cleaned by `defer cleanup()` | `Asset` retained | `LifecycleRetained` | `OutcomeFailed` | Not retried automatically (avoids duplicate send) |
| Crash during `.part` write | `.part` left in `data/downloads` | Uncommitted | None | Interrupted | `NewFileStorage` cleans `.part` at startup |

### 8.7 P2-B Findings Register
| ID | Severity | File & Symbol | Finding & Evidence | Status |
|---|---|---|---|---|
| `P2B-F1` | Medium / Risk | `internal/services/storage/fs.go` (`FileStorage.Put`), `internal/app/modules.go:131-146` | `LifecycleRetained` downloader assets have no age/LRU reclamation policy; `FileStorage` fails closed with `ErrQuotaExceeded` at `5 GiB`. | Observed (P2-B) |
| `P2B-F2` | Low / Observation | `internal/services/storage/fs.go:212-300` (`FileStorage.Put`) | `f.mu.Lock()` serializes concurrent `Put` streams to guarantee exact quota accounting. | Observed (P2-B) |
| `P2B-F3` | Low / Observation | `plugins/downloader/url_pipeline.go:66`, `delivery.go:236, 241` | `continuationCtx := context.Background()` is used only for `TaskEngine.Submit` admission and is bounded by `ErrEngineQuiescing` and `TaskEngine` `rootCtx` shutdown. | Verified (P2-B) |
| `P2B-F4` | Medium / Confirmed Defect | `plugins/downloader/url_pipeline.go:108-110` (`submitURLPipeline`) | `reporter.Close()` is not `defer`red; a panic in `registry.Download` skips synchronous `<-r.done` join and `stopParent()` unregister. | Confirmed (read-only; defer fix for P2-D if approved) |
| `P2B-F5` | Low / Test Gap | `plugins/downloader/progress.go:109`, `internal/services/download/client.go` | No dedicated test for `reporter.Close()` idempotency / panic cleanup or multi-hop redirect-to-SSRF / mismatched `Content-Length`. | Test Gap (P2-B) |
| `P2B-F6` | Low / Observation | `internal/telegram/service.go:1382` (`Service.SendMedia`) | `execNonIdempotentPeerVal` avoids automatic retry on ambiguous `messages.sendMedia` outcomes; retained asset stays intact, no exactly-once guarantee across crash/timeout. | Observed (P2-B) |

### 8.8 P2-B Verification & Completion Record
- **Tests executed locally (`-count=1 -race`):**
  - `go test -count=1 -race ./plugins/downloader/... ./internal/services/download/... ./internal/services/mediaregistry/... ./internal/services/storage/...`: **PASS** (`plugins/downloader` `2.173s`, `download` `1.028s`, `mediaregistry` `6.107s`, `storage` `1.034s`).
- **Status:** **P2-B Complete (Read-Only)**. Awaiting review before P2-C or any P2-D implementation.

---

## 9. P2-D1 & P2-D2 — Limited Hardening & Test Coverage (Pre-P2-C)

### 9.1 P2-D1 — Progress Reporter Cleanup Ordering (`P2B-F4`)
- **Fix (`plugins/downloader/url_pipeline.go:96`):** Added `defer reporter.Close()` immediately after `newDownloadProgressReporter(...)` in `submitURLPipeline`. Because `downloadProgressReporter.Close()` is idempotent (`stopParent()`, `cancel()`, `<-r.done`), normal completion still stops the reporter before `registerRetainedAsset`, and a panic inside `p.registry.Download` still guarantees `stopParent()` unregister and synchronous `<-r.done` join before the worker unwinds.

### 9.2 P2-D2 — Targeted Hardening Tests (`P2B-F5`)
- `plugins/downloader/progress_test.go`:
  - `TestDownloadProgressReporter_CloseIsIdempotentAndJoins`: verifies calling `Close()` multiple times is deadlock-free and closes `done`.
  - `TestURLPipeline_ClosesReporterSynchronouslyOnDownloadPanic`: verifies `submitURLPipeline` under a real `taskengine.Engine` joins the progress reporter cleanly when the download provider panics, with no goroutine leak.
- `internal/services/download/client_test.go`:
  - `TestSafeDownloader_MultiHopRedirectToPrivateIPBlocked`: verifies a 2-hop redirect from `public.example` to `127.0.0.1` is blocked by `NewSafeTransport()`'s `Dialer.Control` (`ErrBlockedSSRF`).
  - `TestSafeDownloader_MismatchedContentLengthStillEnforcesMaxBytes`: verifies a response claiming `Content-Length: 10` with a 250-byte body is still rejected by `io.LimitReader` (`core.ErrResourceLimit`).

### 9.3 Status
- **P2-D1 & P2-D2 Complete.** Awaiting review before **P2-C (Performance & Resource Audit)**.

---

## 10. P2-C — Performance & Resource Audit + P2 Formal Closure

### 10.1 C1 — Measured Baseline Benchmarks
Host: AMD Ryzen 7 5700U with Radeon Graphics, `linux/amd64`, `-benchmem -benchtime=200ms` (Telegram: `-benchtime=100ms`).

| Package / Benchmark | ns/op | Throughput / Notes | B/op | allocs/op |
|---|---|---|---|---|
| `storage.BenchmarkP2C_FileStorage/put_open_delete_10240B` | `127818` | `80.11 MB/s` | `41677` | `89` |
| `storage.BenchmarkP2C_FileStorage/put_open_delete_1048576B` | `871548` | `1203.12 MB/s` | `42022` | `89` |
| `storage.BenchmarkP2C_FileStorage/diagnostics_snapshot` | `0.3700` | lock-free `atomic.Int64` | `0` | `0` |
| `download.BenchmarkP2C_SafeDownloader/download_10240B` | `28652` | `357.39 MB/s` | `36404` | `40` |
| `download.BenchmarkP2C_SafeDownloader/download_1048576B` | `572329` | `1832.12 MB/s` | `36382` | `40` |
| `downloader.BenchmarkP2C_ProgressReporter/callback_burst` | `12.51` | lock-free atomics | `0` | `0` |
| `downloader.BenchmarkP2C_ProgressReporter/format_progress_text` | `2842` | `2s` edit interval | `1112` | `30` |
| `downloader.BenchmarkP2C_URLPipelineAndDelivery` | `143791` | end-to-end `TaskEngine` + `FileStorage` | `46819` | `149` |
| `telegram.BenchmarkResolverMemoryHit` | `189.2` | cache hit | `69` | `3` |
| `telegram.BenchmarkServiceSinglePeerWrapperFastPath` | `1089` | `RPCExecutor` fast path | `624` | `6` |
| `telegram.BenchmarkRPCExecutorSamePeerFloodWaitOccupancy/durable/concurrency_1` | `3249` | `0` inline sleeps | `760` | `15` |

### 10.2 C2 & C3 — Bottleneck Analysis & Remediation Decision
- **Streaming Memory Constant:** `SafeDownloader` (`~36.4 KB/op`) and `FileStorage.Put` (`~42 KB/op`) use constant allocation independent of file size (`10 KB` vs `1 MB`).
- **Hot-Path Lock-Freedom:** `downloadProgressReporter.Callback` (`12.51 ns/op, 0 allocs`) and `FileStorage.Diagnostics` (`0.37 ns/op, 0 allocs`) are lock-free.
- **No Production Bottleneck Requiring Code Changes:** No unbounded queue/cache/goroutine growth or material contention bottleneck was observed; no speculative optimization was applied. Added 3 reproducible benchmark suites (`p2c_benchmark_test.go` in `storage`, `download`, and `downloader`).

### 10.3 C4 — P2 Formal Closure
- **Final Status:** **CLOSED** (`P2-A`, `P2-B`, `P2-D1/D2`, `P2-C` Complete)
- **Accepted Residual Risks:** `P2B-F1` (`5 GiB` `FileStorage` quota fails closed without automatic eviction), `P2B-F6` (non-idempotent `messages.sendMedia` ambiguity).
- **Handoff:** Ready for review before starting **P3 — Runtime Lifecycle → Resource Accounting**.
