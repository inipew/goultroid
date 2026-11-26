# Goultroid Logging Architecture Overhaul — L3 Runtime & Lifecycle Integration Plan

- **Phase:** L3 (Design Document — No Production Code Modified Yet)
- **Exact Baseline HEAD:** `f6a1daa7a390618c199185b56ad2e902435ec4cb` (`origin/test-next`)
- **Master Plan:** [goultroid-logging-overhaul-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-overhaul-plan.md)
- **L1 Contract:** [goultroid-logging-l1-architecture-contract.md](file:///home/dhimas/any/random/ultroid-logging-l1-architecture-contract.md)

---

## 1. Exact Baseline HEAD
- Repository: `https://github.com/inipew/goultroid`
- Branch: `test-next`
- Verified Commit: `f6a1daa7a390618c199185b56ad2e902435ec4cb` (includes L0, L1, and L2 `internal/logging`, `internal/logging/zaplog`, and `ResourceManager` URL redaction).

---

## 2. Audit of Current Logger Initialization

In [internal/app/app.go](file:///home/dhimas/any/random/ultroid-go/internal/app/app.go#L561-L581):
- `initLogger(logLevel string) (*zap.Logger, zap.AtomicLevel, error)` unconditionally calls `zap.NewDevelopmentConfig()`.
- There is no `development` vs `production` mode switch, no JSON encoder option, no `zapcore.NewSamplerWithOptions` protection for high-frequency events, and stacktraces are emitted on `Warn` in Zap development mode.
- If `initLogger` fails in `App.New(cfg)`, `App.New` returns an error immediately without any fallback logger.
- `cmd/goultroid/main.go` calls `log.Printf("Error: %v", err)` on fatal exit and invokes `app.New(cfg)` for `run`.

---

## 3. Audit of App Dependency Wiring

In [internal/app/app.go](file:///home/dhimas/any/random/ultroid-go/internal/app/app.go#L96-L160):
- `App` stores `logger *zap.Logger` (not `logging.Logger` / `*zaplog.Adapter`).
- `buildCore(cfg, logger)`, `buildTelegramRuntime(cfg, coreDeps, logger)`, and `buildDomainServices(cfg, coreDeps, tgRuntime, logger)` all accept raw `*zap.Logger`.
- `pluginManager.SetPanicReporter(zapCorePanicReporter{logger: logger.Named("plugin.panic")})` and `runtime.WithPanicReporter(zapRuntimePanicReporter{logger: logger.Named("supervisor")})` log `zap.Any("value", report.Value)` and `zap.ByteString("stack", report.Stack)` directly without `logging.Panic` redaction.
- `buildLiveSettingsBinder` in [internal/app/settings_live.go](file:///home/dhimas/any/random/ultroid-go/internal/app/settings_live.go#L32-L45) binds `debug.log_level` directly to `zap.AtomicLevel`.

---

## 4. Audit of Runtime and Supervisor

- **`internal/runtime/runtime.go`:**
  - `Runtime` has zero logger dependency. Lifecycle transitions (`Start`, `StopWithin`, component start/stop errors, `ShutdownReport` phases) are only returned as Go errors or stored in `lastShutdown`.
- **`internal/runtime/supervisor.go`:**
  - `Supervisor` only accepts `PanicReporter` (`WithPanicReporter`). Worker starts, transient restarts, backoff, and exhausted restart budgets (`WorkerStateFailed` / `WorkerStateDegraded`) emit no operational logs.

---

## 5. Audit of Shutdown Sequence

In [internal/app/shutdown.go](file:///home/dhimas/any/random/ultroid-go/internal/app/shutdown.go#L34-L106):
1. `a.markStopping()`
2. `a.savedDeepLinkRegistration.Close()`
3. `a.runtime.StopWithin(shutdownCtx)` (stops components & supervisor workers in reverse dependency order)
4. `transportCancel()` + bounded join on `transportDone` (`transportJoinTimeout = 5s`)
5. `appCancel()`
6. **`a.logger.Sync()`**  <-- **Order defect:** runs *before* `resources.ForceCleanupAllContext`!
7. `a.resources.ForceCleanupAllContext(shutdownCtx)` + `a.resources.VerifyShutdownClean()`
8. `a.markStopped(shutdownErr)`

Any log emitted during `ForceCleanupAllContext` or `VerifyShutdownClean` occurs **after** `a.logger.Sync()`, risking lost/unflushed shutdown diagnostics.

---

## 6. Dependency Injection Design (L3-A & L3-B)

- `App` owns:
  - `opLogger *zaplog.Adapter` (implements `logging.Logger`, exposes `Sync() error`)
  - `logLevel zap.AtomicLevel` (for live reload via `settings_live.go`)
  - `rawLogger *zap.Logger` (transitional field ONLY for unmigrated L4/L5/L6 consumers; will be removed once L4–L6 complete)
- **Startup sequence:**
  1. `config.Load` / `cfg` validation
  2. `initLogger(cfg)` -> returns `(*zaplog.Adapter, *zap.Logger, zap.AtomicLevel)` with safe stderr fallback if Zap config build fails (never drops startup errors).
  3. `buildCore`, `buildTelegramRuntime`, `buildDomainServices` (continue receiving `rawLogger` for L4/L5/L6 compatibility while `App`, `Runtime`, `Supervisor`, and `PanicReporter`s use `opLogger` / `logging.Logger`).
  4. `runtime.New(runtime.WithLogger(opLogger.Named("runtime")))`
  5. `runtime.NewSupervisor(..., runtime.WithSupervisorLogger(opLogger.Named("supervisor")), runtime.WithPanicReporter(...))`

---

## 7. Logger Ownership & Runtime Integration Design (L3-A & L3-C)

- **Single Owner:** `App` is the sole owner of `opLogger` and the sole caller of `opLogger.Sync()`.
- **No global mutable logger:** No package-level `var globalLogger`.
- **No per-request/per-update logger:** `Runtime` and `Supervisor` receive one `logging.Logger` at construction and use `logging.Nop()` if nil.
- **Runtime & Supervisor logging scope (transitions & abnormalities only, never per-loop):**
  - `Runtime`: `runtime_starting`, `runtime_started`, `runtime_stopping`, `runtime_stopped`, `component_start_failed`, `component_stop_failed`.
  - `Supervisor`: `worker_restarting` (with `worker`, `attempt`, `error_category`), `worker_exhausted_restarts` (Error), `worker_panic_recovered` (via safe `logging.Panic`).
- **Panic Reporters in `internal/app/app.go`:**
  - Replace `zapCorePanicReporter` and `zapRuntimePanicReporter` raw `zap.Any` / `zap.ByteString` with `logging.Logger.Error("recovered panic", logging.Component(...), logging.String("owner", ...), logging.Panic(report.Value, report.Stack))`.

---

## 8. Configuration & Shutdown Design (L3-D & L3-E)

### Configuration (`internal/config/config.go` + `internal/app/logger.go`)
- Add optional fields to `config.Config`:
  - `LogLevel` (`LOG_LEVEL`): `debug | info | warn | error` (default `info`).
  - `LogEnv` (`LOG_ENV`): `production | development` (default `production`).
  - `LogFormat` (`LOG_FORMAT`): `json | console` (default `json` when `production`, `console` when `development`).
- **Validation:** Invalid `LOG_LEVEL`, `LOG_ENV`, or `LOG_FORMAT` fails config validation clearly (`LoadFrom`), with safe defaults when unset.
- **Zap Production vs Development:**
  - `production`: JSON (or console if `LOG_FORMAT=console`), ISO8601 UTC timestamp, stacktrace on `Error` only, conservative `zapcore.NewSamplerWithOptions(core, time.Second, 100, 10)` so high-frequency identical `Debug`/`Info`/`Warn` bursts are bounded without hiding initial occurrences or `Error` events.
  - `development`: Console encoder, colored/readable level, stacktrace on `Error` only.
- **Live Reload:** `settings_live.go` continues updating `zap.AtomicLevel` via `debug.log_level`.

### Shutdown Sequence (L3-D)
Reorder `App.performShutdown()` in `internal/app/shutdown.go` so `opLogger.Sync()` runs **exactly once** after all producers and resource cleanups have stopped:
1. `a.markStopping()` (quiesce)
2. `a.savedDeepLinkRegistration.Close()`
3. `a.runtime.StopWithin(shutdownCtx)` (stops Runtime components & Supervisor workers)
4. `transportCancel()` + bounded join on `transportDone`
5. `appCancel()`
6. `a.resources.ForceCleanupAllContext(shutdownCtx)` + `a.resources.VerifyShutdownClean()`
7. **`a.opLogger.Sync()`** (exactly once, after all producers & cleanups)
8. `a.markStopped(shutdownErr)`

---

## 9. Planned File Changes (Implementation Phase)

| File | Change |
| --- | --- |
| `internal/config/config.go` (& `config_test.go`) | Add `LogEnv` (`LOG_ENV`) and `LogFormat` (`LOG_FORMAT`) with validation and safe defaults. |
| `internal/app/logger.go` (new) | Encapsulate `initLogger(cfg)` with dev/prod config, conservative sampling, stderr fallback, and `zaplog.Adapter`. |
| `internal/app/app.go` | Store `opLogger *zaplog.Adapter`; wire `opLogger` to `Runtime`, `Supervisor`, and safe `PanicReporter`s; remove `initLogger` from `app.go`. |
| `internal/app/shutdown.go` | Move `opLogger.Sync()` after `resources.ForceCleanupAllContext` and `VerifyShutdownClean`. |
| `internal/runtime/runtime.go` | Add `Option` / `WithLogger(logging.Logger)` (default `logging.Nop()`); log lifecycle transitions and component errors. |
| `internal/runtime/supervisor.go` | Add `WithSupervisorLogger(logging.Logger)` (default `logging.Nop()`); log worker restart/exhaustion transitions. |
| `internal/app/*_test.go`, `internal/runtime/*_test.go` | Lifecycle, shutdown order, fallback logger, live reload, and race tests. |

*Explicitly excluded from L3:* migrating the 48 Zap files, changing `TaskEngine`, rewriting `UserLog`, or altering Telegram RPC contracts.

---

## 10. Test Plan & Acceptance Criteria

- `internal/runtime` has zero Zap imports (`go list -f '{{.Imports}}' ./internal/runtime`).
- `App` owns one `opLogger` (`*zaplog.Adapter`), calls `Sync()` exactly once at the end of shutdown after all producers and resource cleanups stop.
- `initLogger` supports `production` (JSON + conservative sampling) and `development` (console), plus live reload via `debug.log_level` and safe fallback on error.
- `PanicReporter`s use `logging.Panic` (redacted, bounded) instead of `zap.Any` / raw `zap.ByteString`.
- `go test -race ./internal/config/... ./internal/runtime/... ./internal/app/... ./internal/architecture/...` and `go build -v ./cmd/goultroid` pass.

---

## 11. Backward Compatibility Risks

- **Existing `.env` files without `LOG_ENV` / `LOG_FORMAT`:** Default to `production` + `json` (or `LOG_FORMAT` override to `console`), which may change local console appearance if developers expect colored console logs without setting `LOG_ENV=development`. Mitigation: document in `.env.example` or default `LOG_FORMAT=console` unless `LOG_FORMAT=json` is set, while still using production Zap encoder/sampling rules when `LOG_ENV=production` (let's evaluate during L3-E implementation, or keep `LOG_ENV=production` with `LOG_FORMAT=json` as standard and `LOG_FORMAT=console` configurable).
- **`runtime.New()` signature:** Existing callers call `runtime.New()` with 0 arguments. Using `runtime.New(opts ...Option)` preserves 100% backward compatibility.

---

## 12. Rollback Strategy

- Because `App` temporarily retains `rawLogger *zap.Logger` for L4–L6 consumers and `runtime.New(opts...)` is additive, L3 changes are localized to `internal/config`, `internal/app`, and `internal/runtime` and can be reverted cleanly without affecting plugins, Telegram, or `TaskEngine`.
