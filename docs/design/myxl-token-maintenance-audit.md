# Phase M1 — Architecture Audit: MyXL Scheduled Token Maintenance

**Repository:** `inipew/goultroid`  
**Branch:** `test-next`  
**Audited HEAD:** `dd86d28f211bf5a981121dd6066b9238c8c3cae7`  
**Date:** 2026-11-25  
**Status:** Phase M1 Complete (No production code modified)

---

## 1. Git & Workspace Baseline

- Remote `origin/test-next` HEAD: `dd86d28f211bf5a981121dd6066b9238c8c3cae7`
- `git pull --ff-only`: Already up to date.
- Local `HEAD`: `dd86d28f211bf5a981121dd6066b9238c8c3cae7`
- Working tree: Clean tracked files; two pre-existing untracked acceptance test files in `internal/app/` (`u6_health_resource_acceptance_test.go`, `u6_lifecycle_acceptance_test.go`), untouched.

---

## 2. Existing Components & APIs

### 2.1 Scheduler (`internal/scheduler/`)
- **Implementation:** `scheduler.Engine` (`internal/scheduler/engine.go`) implements `scheduler.Service`.
- **Timing Ownership:** `Engine` runs a single unified timing loop (`wakeChan`, `dueTimerWakes`, `safetyTimerWakes`). When `e.jobsMgr.ScheduleCutoverActive(ctx)` is active, `Engine` queries `e.jobsMgr.EarliestScheduleDue(ctx)` for next wake-up and delegates due schedule processing to `e.jobsMgr.ProcessDueSchedules(ctx, now, limit)`.
- **Periodic Tasks vs Durable Schedules:** `RegisterPeriodicTask` registers in-memory `periodicCoordinator` tasks (ephemeral, lost on restart). Durable schedules are owned by `jobs.Manager` (`job_schedules` SQLite table) and woken via `jobsMgr.SetScheduleWake(notifyWake)`.

### 2.2 Jobs Manager (`internal/jobs/`, `internal/jobs/sqlite/`)
- **Core API (`internal/jobs/manager.go`, `manager_schedule.go`):**
  - `RegisterHandler(handlerType string, handler Handler) error`
  - `Register(def JobDefinition) error`
  - `Trigger(ctx context.Context, jobID string) error` (admission-only; creates a `JobOccurrence` and drives physical execution via `TaskEngine`)
  - `SaveSchedule(ctx context.Context, schedule JobSchedule) error` (validates policy, persists to `job_schedules`, signals `scheduler.Engine`)
  - `DisableSchedule(ctx context.Context, scheduleID string) error` (sets `enabled = 0` atomically while preserving history, signals `scheduler.Engine`)
- **Schedule Policy (`validateSchedulePolicy`, `internal/jobs/manager_schedule.go:44-87` & `internal/jobs/sqlite/store.go:1154-1183`):**
  - `Recurrence`: `"once"` or `"interval"` (`Interval >= time.Second`).
  - `Timezone`: IANA timezone string validated with `time.LoadLocation`.
  - `MisfirePolicy`: `MisfireRunOnce` (`"run_once"`) or `MisfireSkip` (`"skip"`). (`MisfireCatchUpBounded` is explicitly rejected.)
  - `OverlapPolicy`: `OverlapForbid` (`"forbid"`) only. (`OverlapReplace` and `OverlapAllowBounded` are explicitly rejected.)
- **Due Processing & Overlap/Misfire Semantics (`ProcessDueSchedules` & `MaterializeDueSchedule`):**
  - `OverlapForbid` checks `SELECT COUNT(*) FROM job_occurrences WHERE job_id = ? AND state IN ('ready', 'dispatched', 'blocked')`. If `activeCount > 0`, it skips materialization and advances `next_due_at`, preventing overlapping cycles.
  - `NextIntervalDueAfter(nextDue, now, interval)` advances `next_due_at` in O(1) strictly past `now`, so missed schedules during downtime produce **at most one** catch-up occurrence (`MisfireRunOnce`) without burst catch-up loops.

### 2.3 TaskEngine (`internal/taskengine/`, `internal/tasks/`)
- **Pools (`internal/taskengine/engine_config.go:64-70`):**
  - `"general"` (concurrency 8), `"interactive"` (32), `"download"` (3), `"media-process"` (2), `"scheduler"` (concurrency 4, `ZeroIdle: true`, `IdleTimeout: 30s`, `BacklogLimit: 100`).
- **Priority Classes (`internal/tasks/types.go:17-22`):**
  - `PriorityInteractive`, `PriorityNormal`, `PriorityBackground`, `PriorityMaintenance` (`"maintenance"`).
- **Admission & Execution:** `jobs.Manager` submits `tasks.WorkSpec` to `TaskEngine` with the `JobDefinition`'s `Pool`, `Class`, `Timeout`, `Resources`, and `ScopeIdentity`. Workers retire to zero when idle (`ZeroIdle: true`).

### 2.4 Plugin Capability & Scoped Access (`internal/plugin/`)
- **Capabilities (`internal/plugin/manifest.go`):**
  - `CapJobs` (`"jobs"`) gates `pctx.Jobs() (plugin.JobClient, error)`.
  - `CapScheduler` (`"scheduler"`) gates `pctx.Schedules() (plugin.ScheduleClient, error)`.
  - `CapStorageRead` / `CapStorageWrite` gate `pctx.Storage() (storage.KVStore, error)`.
  - `CapHTTP` gates `pctx.HTTP() (*network.Client, error)`.
- **Scoped Clients (`internal/plugin/context_jobs.go`):**
  - `scopedJobClient` namespaces `ID`, `JobID`, `HandlerType`, `ScopeOwner`, `QuotaOwner` to `plugin:<owner>:<name>`.
  - `scopedScheduleClient` namespaces `ID` and `JobID` to `plugin:<owner>:<name>`.
  - `plugin.ScheduleClient` exposes only `SaveSchedule` and `DisableSchedule` (no read/list API). The scoped schedule client rewrites both schedule ID and JobID into the plugin namespace; the schedule's JobID therefore must refer to the scoped ID of the registered MyXL job.
- **Lifecycle Cleanup (`internal/plugin/manager.go:928`):**
  - On `Disable` or `ShutdownWithContext`, `plugin.Manager` calls `jobsMgr.CancelByOwner(owner)` and `schedCl.UnregisterPeriodicTasksByOwner`.
  - MyXL currently does not declare `CapJobs` or `CapScheduler`; these capabilities must be added to the module manifest before `InitPlugin` can obtain the scoped clients. `Module.Register` itself receives `module.Runtime`, while `Plugin.InitPlugin(pctx)` is the capability-gated integration point.

### 2.5 MyXL Plugin & Token Refresh (`plugins/myxl/`)
- **Module (`plugins/myxl/module.go`):**
  - Declares `CapTelegramSendMessage`, `CapStorageRead`, `CapStorageWrite`, `CapHTTP`, `CapSecretRead`, `CapFilesystemTemp`, `CapTasks`. Does **not** yet declare `CapJobs` or `CapScheduler`.
  - `Module.Register(ctx, rt)` has access to `rt *module.Runtime` (`rt.OwnerID`, `rt.Logger`, `rt.Telegram.MessageService`, `rt.Service.UserlogService`).
- **Plugin Lifecycle (`plugins/myxl/myxl.go`):**
  - `InitPlugin(pctx)` initializes `HTTP`, `Files`, `TaskClient`, and `Secrets`.
  - Does not currently implement `ShutdownContext` (no background goroutines owned by MyXL).
- **Token Expiry & Refresh (`plugins/myxl/client.go`):**
  - Constants: `DefaultTokenRefreshSkew = 2 * time.Minute`, `DefaultTokenExpiryFallback = 60 * time.Minute`.
  - `applyTokensToAccount` sets `TokenExpiresAt = time.Now().Add(ExpiresIn)` if `ExpiresIn > 0`, else `time.Now().Add(DefaultTokenExpiryFallback)`. Therefore the 60-minute fallback is a locally synthesized estimate, not an unknown/CIAM-confirmed expiry; legacy or absent persisted expiry must be treated separately.
  - `EnsureFreshToken(ctx, acc)` skips if `!acc.TokenExpiresAt.IsZero() && time.Now().Add(DefaultTokenRefreshSkew).Before(acc.TokenExpiresAt)`; otherwise calls `refreshSingleflight(ctx, acc, false, "")`.
  - `refreshSingleflight(ctx, acc, force, failedIDToken)` uses `c.refreshGroup.DoChan(msisdn, ...)` (`golang.org/x/sync/singleflight`), re-reads `c.repo.GetByMSISDN`, re-checks freshness, calls `c.doRefreshToken` (which attempts `grant_type=refresh_token` when `RefreshToken != ""`, and falls back to `extendSession` -> `submitDeviceIDToken` when `SubscriberID != ""`), applies tokens, and persists via `c.repo.Save`.
  - `ForceRefreshToken(ctx, identifier)` calls `RefreshToken(ctx, acc)` -> `refreshSingleflight(ctx, acc, true, "")`.
  - `refreshAllTokens(ctx)` (`plugins/myxl/refresh_all.go`) loops all accounts and calls `ForceRefreshToken` unconditionally.
- **HTTP Transport:**
  - `network.Service` (`internal/platform/network/service.go`) provides `*network.Client` with a 30s default HTTP client timeout and connection pooling (`MaxIdleConnsPerHost: 10`), tracked by `resource.Manager`. Telegram `RPCExecutor` is for MTProto only and does not govern CIAM HTTP requests.
- **Database & Migrations (`plugins/myxl/migration.go`, `repository.go`):**
  - Uses `database.SchemaInvariantMigration` (`myxl.001` through `myxl.007`: `myxl_accounts`, `myxl_saved_packages`, `myxl_decoy_configs`, `token_expires_at` column on `myxl_accounts` with `DEFAULT '1970-01-01 00:00:00'`, `myxl_purchase_requests`, `idx_myxl_accounts_single_active`, `myxl_pending_qris`). Any new maintenance tables will start at `myxl.008`.
  - `myxl_accounts` stores `msisdn`, `alias`, `is_active`, `access_token`, `id_token`, `refresh_token`, `subscriber_id`, `subscription_type`, `token_expires_at`, `created_at`, `updated_at`.
- **Telegram UI, Commands & Architecture Guardrails (`plugins/myxl/myxl.go`, `menu.go`, `assistant_interaction.go`):**
  - `.myxl` command supports `SurfaceUserbot | SurfaceAssistant` with `PermissionOwner`.
  - Interactive menu uses `feature.Spec` + `assistantActionSlotCount` (32 session-bound slots) in `assistant_interaction.go` and `MenuManager` screen builders in `menu.go` (canonical a2 interaction architecture).
  - Enforced by `internal/architecture/` tests: `assistant_optional_userbot_p5_test.go` / `p6_test.go` (userbot surface must not require assistant), `myxl_zero_legacy_callback_p1e4_test.go` (zero legacy `v1:myxl` callbacks), `user_error_boundary_p0b_test.go` / `p2c_test.go` (safe user-facing error boundaries), and `durable_feature_inventory_test.go`.

### 2.6 Observability (`internal/app/diagnostics.go`, `internal/core/metrics.go`)
- Goultroid uses in-memory `DiagnosticsSnapshot` structs (`jobs.Diagnostics`, `scheduler.Diagnostics`, `taskengine.RuntimeStats`, `core.MetricsCollector`, `database.DBMetrics`) and structured `zap.Logger`. There is no Prometheus registry.

---

## 3. Identified Gaps & Conflicts with Plan

1. **Configurable Refresh Window vs Hardcoded `DefaultTokenRefreshSkew`:**
   - `EnsureFreshToken` and `refreshSingleflight` (`plugins/myxl/client.go:400,447`) hardcode `DefaultTokenRefreshSkew` (2m).
   - Scheduled maintenance requires a configurable `RefreshWindow` (e.g. `30m`). Calling `EnsureFreshToken` directly when a token expires in 15m would skip refresh.
   - **Resolution:** Add a shared window-aware method on `Client` (e.g. `EnsureFreshTokenWithin(ctx, acc, window)` or `refreshSingleflightWithSkew`) that shares the exact same `refreshGroup` singleflight and `repo.Save` path, while `EnsureFreshToken` delegates to it with `DefaultTokenRefreshSkew`.

2. **Error Classification in `doRefreshToken`:**
   - `doRefreshToken` (`plugins/myxl/client.go:334-365`) discards HTTP status codes (400/401/403/429/5xx) and transport errors, returning a generic error `"refresh token and extend session both failed"`.
   - Maintenance requires distinct per-account outcomes (`Authentication Rejected`, `Rate Limited`, `Network Error`, `Persistence Error`, etc.).
   - **Resolution:** Return structured/wrapped error types from `doRefreshToken` and `refreshSingleflight` so callers can classify outcomes via `errors.Is`/`errors.As` without breaking existing error callers.

3. **`JobSchedule` Recurrence Model vs Daily Time (`03:00`):**
   - `jobs.JobSchedule` supports `"once"` and `"interval"` (`Interval >= 1s`), not cron or `"daily"` expressions.
   - **Resolution:** Treat `DailyTime` only as the local-time anchor for the first `NextDueAt`/interval phase, not as an independent daily recurrence. Store one durable interval schedule with the chosen `Interval`, `Timezone`, and an anchor-aligned `NextDueAt`. For example, interval 6h anchored at 03:00 means 03:00, 09:00, 15:00, and 21:00. Fixed 24h intervals shift relative to wall-clock time across DST transitions; either restrict supported timezone behavior or explicitly define that semantics in M2.

4. **Plugin Schedule Readability and Source of Truth:**
   - `plugin.ScheduleClient` only exposes `SaveSchedule` and `DisableSchedule`; it cannot read the persisted schedule or its next due time.
   - **Resolution:** Persist user intent/configuration in one MyXL-owned durable configuration record, but keep the actual due time and schedule lifecycle authoritative in `jobs.JobSchedule`. Do not persist a second `next_run_at` in MyXL config. The current scoped API cannot display/read scheduler-owned next due time from the plugin; M2 must decide whether status can omit that field, obtain it through an existing supported diagnostics path, or justify a narrowly scoped read API. Do not duplicate schedule state merely to fill the UI. Use existing job occurrence/history facilities where possible; add bounded MyXL per-account outcomes only if existing diagnostics cannot represent the required detail.

5. **Security Audit of Existing `.myxl refresh` (`plugins/myxl/myxl.go:561-583`):**
   - `handleRefreshToken` currently outputs `acc.MSISDN` unmasked even in group chats and prints 20-char prefixes of `AccessToken`, `IDToken`, and `RefreshToken`.
   - **Resolution:** Mask MSISDN in group chats and redact token prefixes per §10.

6. **Metrics Convention:**
   - The plan proposes Prometheus-style metric names (`myxl_token_maintenance_runs_total`, etc.), while Goultroid uses in-memory `Diagnostics` structs + `zap.Logger`.
   - **Resolution:** Maintain bounded atomic/DB-backed maintenance diagnostics counters and expose them via `Plugin` diagnostics / `.myxl schedule` / structured `zap` logs without introducing a second metrics registry.

---

## 4. Recommended Integration Approach

1. **Capabilities & lifecycle:** Add `plugin.CapJobs` and `plugin.CapScheduler` to `myxl.ModuleType.Manifest()`. Obtain `pctx.Jobs()` and `pctx.Schedules()` from `Plugin.InitPlugin(pctx)` (not `Module.Register`, which only has `module.Runtime`). Register the handler/definition there using the plugin-scoped clients and preserve the existing plugin initialization/error cleanup contract.
2. **Execution Pipeline:**
   - Register job handler `"token_maintenance"` via `pctx.Jobs().RegisterHandler(...)`.
   - Register durable `jobs.JobDefinition` `"token_maintenance"` with `Pool: "scheduler"`, `Class: string(tasks.PriorityMaintenance)`, `Timeout` (e.g. `3 * time.Minute`), `RetryPolicy: {MaxAttempts: 1}` (partial failures are handled per-account inside the maintenance handler).
   - Register/update `jobs.JobSchedule` `"token_maintenance"` via `pctx.Schedules().SaveSchedule(...)` with `Recurrence: "interval"`, `MisfirePolicy: jobs.MisfireRunOnce`, `OverlapPolicy: jobs.OverlapForbid`. The schedule ID and JobID are plugin-scoped by `ScheduleClient`; use the same logical local ID as the registered job and do not manually pre-scope it.
   - Disable via `pctx.Schedules().DisableSchedule(...)`.
   - Manual run (`.myxl schedule run` or menu button) calls `pctx.Jobs().Trigger(ctx, "token_maintenance")`, using the exact same `Jobs` -> `TaskEngine` -> handler pipeline.
3. **Concurrency & Bounds:**
   - Prefer sequential account processing initially. Only introduce bounded intra-job concurrency if measurements show a real need and the shared HTTP transport, repository, singleflight, and rate-limit behavior are proven safe. Do not assume a worker pool is necessary. Derive the job timeout from the maximum supported account count and HTTP timeout budget; `3 * time.Minute` is an example, not a validated default.
   - Zero MyXL-owned goroutines, zero tickers, zero `time.Sleep` polling.
4. **Telegram & Notifications:**
   - Single configuration service (`MaintenanceService`) shared by `.myxl schedule ...` commands and `MenuManager` / `assistant_interaction.go` menu screens.
   - Owner notifications via `rt.Telegram.MessageService` (to `rt.OwnerID`) or `UserlogService`, non-blocking/best-effort so notification failures never mark a token refresh as failed.
