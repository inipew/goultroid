# P3 Audit — Runtime Lifecycle & Resource Accounting

## Status

- Status: **CLOSED**
- Workstream: P3 of the Goultroid Architecture Audit Roadmap P1–P3
- Branch: `test-next`
- Baseline HEAD (post-P2 closure): `0d30a1943877b0a1b083e0058d0089f2d6f40480`
- Scope: end-to-end architecture, lifecycle, resource accounting, and verification audit. No production defects requiring code changes were found.

---

## 1. Audit Scope

Packages audited from source and verified via tests:
- `internal/app/` (`app.go`, `lifecycle.go`, `shutdown.go`, `runtime_resources.go`, `wiring_core.go`, `wiring_services.go`, `modules.go`, `diagnostics.go`, `repeated_lifecycle_test.go`, `u6_lifecycle_acceptance_test.go`, `u6_health_resource_acceptance_test.go`, `shutdown_test.go`)
- `internal/runtime/` (`runtime.go`)
- `internal/resource/` (`manager.go`, `types.go`)
- `internal/core/` (`events.go`)
- `internal/interaction/` (`runtime.go`, `config.go`, `runtime_internal.go`, `dispatcher.go`)
- `internal/telegram/` (`client.go`, `dispatcher_ingress_dedupe.go`, `rpc_executor.go`, `rpc_limiter.go`)

---

## 2. Lifecycle Map: Startup, Shutdown & Reload

1. **Startup & Partial-Failure Rollback (`internal/runtime/runtime.go:167-251`, `internal/app/lifecycle.go:37-97`):**
   - `App.runLifecycle` calls `beginStart()` → `startBackgroundServices` → `runtime.Start(ctx)`.
   - `Runtime.Start` validates the DAG (`r.graph.StartupOrder()`), transitions `Initializing → Starting`, starts components in topological order, and rolls back all already-started components in reverse order (`failStartAndRollback`) if any critical component fails or `ctx` is cancelled.
   - `App.runLifecycle` publishes `transportCancel`/`transportDone` under `lifecycleMu` before launching `a.client.Run(transportCtx)`, preventing transport launch if shutdown raced with startup.
2. **Graceful & Bounded Shutdown (`internal/app/shutdown.go:13-106`, `internal/runtime/runtime.go:441-520`):**
   - `App.Shutdown` is idempotent (`beginQuiesce()`), bounded by `appShutdownTimeout = 30s` (`performShutdown`).
   - Order: close `savedDeepLinkRegistration` → `runtime.StopWithin(shutdownCtx)` (reserves `forcedStopReserve = 250ms` for `ForcedStopper` if graceful budget expires, stops in reverse dependency order) → cancel `transportCancel` and join `transportDone` within `transportJoinTimeout = 5s` → `appCancel()` → `logger.Sync()` → `resources.ForceCleanupAllContext(shutdownCtx)` + `resources.VerifyShutdownClean()`.
   - Transport/RPC remains available *during* `runtime.StopWithin` so in-flight handlers/notifications can finish cleanly before transport teardown.
3. **Reload & Feature Lifecycle (`internal/app/repeated_lifecycle_test.go`, `internal/core/events.go`, `internal/interaction/`):**
   - Repeated `App` composition start/shutdown rounds (`TestRepeatedLifecycle_RealAppCompositionStartShutdownRounds`, `TestU6_S11_RepeatedAppLifecycleNoLeakOrGrowth`) verify no goroutine, session, or resource accumulation across cycles.
   - `EventBus` subscriptions (`SubscribeOwned`, `SubscribeContextScoped`) and `interaction.HandlerRegistration.Close()` unbind handlers deterministically on reload/disable.

---

## 3. Resource Ownership & Accounting Matrix

| Resource | Owner | Lifecycle Start | Configured Bound | Cancellation / Cleanup | Released When | Verification |
|---|---|---|---|---|---|---|
| Runtime components & workers | `runtime.Runtime` | `Runtime.Start` | DAG-ordered components | `StopWithin` + `ForcedStopper` (`250ms` reserve) | `StopWithin` completes | `TestU6_S01`, `S02`, `S09`, `S10A/B` |
| Tracked resources | `resource.Manager` | `RegisterWithCleanupContext` | Explicit registered handles | `ForceCleanupAllContext` + `VerifyShutdownClean` | `Release` or `App.Shutdown` | `TestAppShutdown_DetectsResourceLeak` |
| `TaskEngine` pools & inbox | `taskengine.Engine` | `Start` | `Inbox=2048`, `Result=1000`, `MaxTerminal=1000`, `TTL=5m`, `Tombstones=4096`, `ZeroIdle=true` | `rootCancel()` + drain | `Stop` / idle timeout | `internal/taskengine` suite |
| `JobManager` retry/recovery | `jobs.Manager` | `Start` | `retryQueueCap=256`, `retryWorkers=4`, `recoveryScanLimit=256` | `baseCancel()` + `wg.Wait()` | `Stop` | `internal/jobs` suite |
| `PersistencePump` | `jobs.PersistencePump` | `Start` | `concurrency=2`, `queueCap=256`, `maxRetainedBytes=64 MiB` | `cancel()` + `wg.Wait()` | `Stop` | `persistence_pump_test.go` |
| `interaction.Runtime` sessions | `interaction.Runtime` | `NewRuntime` | `MaxSessions=4096`, `PerScope=512`, `PerActor=64` | Reaper + `Close()` | TTL expiry / `Close()` | `internal/interaction` suite |
| `ingressMessageDedupe` | `internal/telegram` | `newIngressMessageDedupe` | `maxEntries` fixed ring buffer (`O(1)` replacement) | Bound to dispatcher | Dispatcher stop | `dispatcher_ingress_dedupe_test.go` |
| `FileStorage` (`data/downloads`) | `storage.FileStorage` | `NewFileStorage` | `maxQuota = 5 GiB` | `.part` cleanup at startup; `Delete` | `Delete` / `RegisterAsset` rollback | `internal/services/storage` suite |

---

## 4. Findings Register

| ID | Severity | Status | Evidence | Impact & Decision |
|---|---|---|---|---|
| `P3-F1` | Observation | Valid Design | `internal/app/shutdown.go:37, 64` (`appShutdownTimeout = 30s`, `transportJoinTimeout = 5s`) + `internal/runtime/runtime.go:472` (`forcedStopReserve = 250ms`) | Bounded shutdown budget guarantees `App.Shutdown` cannot hang indefinitely even if a component blocks; `VerifyShutdownClean` surfaces any unclosed resource. **Accept (no code change).** |
| `P3-F2` | Observation | Valid Design | `internal/interaction/config.go:12-14`, `internal/telegram/dispatcher_ingress_dedupe.go:46-63`, `internal/taskengine/engine_config.go:62-81` | All major in-memory stores/queues (`TaskEngine`, `JobManager`, `PersistencePump`, `interaction.Runtime`, `ingressMessageDedupe`, `PeerCache`, `HierarchicalRPCLimiter`) enforce explicit hard bounds. **Accept (no code change).** |
| `P3-F3` | Observation | Cross-reference P1/P2 | `MyXL` `EventCancelled` (`15s` budget) and `downloader` `LifecycleRetained` (`5 GiB` quota) | Documented in N9 and P2-B/C; both are bounded (`15s` / `5 GiB`) and do not violate P3 lifecycle bounds. **Accept.** |

No Critical, High, or Medium production defects were found during P3.

---

## 5. Verification & Measurements (Executed Locally)

- `go test -count=1 -race ./internal/runtime/... ./internal/resource/... ./internal/app/... ./internal/architecture/...`:
  - `internal/runtime`: **PASS** (`1.530s`)
  - `internal/resource`: **PASS** (`1.142s`)
  - `internal/app`: **PASS** (`74.929s`, includes `TestU6_S01`–`S13`, `TestRepeatedLifecycle_*`, `TestAppShutdown_*`)
  - `internal/architecture`: **PASS** (`12.380s`)
- `go vet ./internal/runtime/... ./internal/resource/... ./internal/app/... ./internal/core/... ./internal/interaction/...`: **PASS**
- `go test -count=1 ./internal/core/... ./internal/interaction/...`: **PASS**

---

## 6. Closure Decision

- **P3 Status:** **CLOSED**
- **Goultroid Architecture Audit Roadmap (P1–P3):** **ALL PHASES CLOSED** (`P1`, `P2`, `P3`).
- **Production Code Changes:** None required.
