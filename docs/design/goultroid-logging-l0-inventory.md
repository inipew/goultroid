# Goultroid Logging Architecture Overhaul — L0 Complete Logging Inventory

- **Phase:** L0 (Read-only Codebase Audit — No Production Code Modified)
- **Baseline Commit:** `2b09316d18f92403f95bdbb020011d64d6174a4b` (`origin/test-next`)
- **Master Plan:** [goultroid-logging-overhaul-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-overhaul-plan.md)

---

## 1. Logging Mechanisms & Package Inventory

### 1.1 Operational Logging (`go.uber.org/zap`)

Zap is used directly across **48 non-test Go files** (`internal/` and `plugins/`). There is no abstraction or facade over `*zap.Logger`; every layer imports `go.uber.org/zap` directly.

| Layer | Files Using `zap` | Key Packages / Files |
| --- | --- | --- |
| **App & Wiring** | 9 | `internal/app/app.go`, `lifecycle.go`, `shutdown.go`, `dependencies.go`, `settings_live.go`, `wiring_core.go`, `wiring_services.go`, `wiring_telegram.go` |
| **Core Execution** | 2 | `internal/core/executor.go`, `internal/core/middleware.go` |
| **Jobs & Scheduler** | 4 | `internal/jobs/manager.go`, `manager_recovery.go`, `internal/scheduler/engine.go`, `periodic_timer.go` |
| **Platform & Addons** | 3 | `internal/platform/audit/audit.go`, `internal/addon/manager.go`, `internal/addon/ipc.go` |
| **Telegram Userbot** | 8 | `internal/telegram/client.go`, `dispatcher.go`, `dispatcher_dispatch.go`, `dispatcher_callback.go`, `dispatcher_handlers.go`, `dispatcher_command_claim.go`, `dispatcher_callback_claim.go`, `dispatcher_peer.go`, `resolver.go` |
| **Assistant Bot** | 7 | `internal/assistant/app.go`, `client/client.go`, `client/updates.go`, `client/audience.go`, `client/force_sub.go`, `client/shell_interaction.go`, `command/router.go`, `interaction/message.go` |
| **Domain Services** | 6 | `internal/services/userlog/service.go`, `pmpermit/service.go`, `pmpermit/approval.go`, `moderation/service.go`, `broadcast/service.go`, `inline/engine.go` |
| **Voice & Module** | 2 | `internal/voice/player.go`, `internal/module/module.go` |
| **Plugins** | 8 | `plugins/myxl/maintenance_observability.go`, `myxl/myxl.go`, `downloader/downloader.go`, `downloader/interactive.go`, `downloader/youtube_cookies.go`, `downloader/youtube_cookies_snapshot.go`, `afk/afk.go`, `settings/settings.go` |

**Notable absences (zero logger integration):**
- `internal/runtime` (`runtime.go`, `supervisor.go`) — relies on `PanicReporter` callbacks wired in `internal/app/app.go` (`zapRuntimePanicReporter`), with no structured lifecycle/transition logging.
- `internal/taskengine` — zero Zap/logger usage; exposes `RuntimeStats` only.
- `internal/resource` (`manager.go`) — zero logger usage; stores diagnostic metadata in-memory.
- `internal/telegram/rpc_executor.go` — `NewRPCExecutor` in `internal/telegram/client.go` takes no logger; `gotd`'s `telegram.Options` also omits `Logger`.

### 1.2 Standard Library `log` and `fmt.Print*`

| Location | Mechanism | Purpose | Assessment |
| --- | --- | --- | --- |
| `cmd/goultroid/main.go:28` | `log.Printf("Error: %v", err)` | Fatal CLI error output before `os.Exit(1)` | Acceptable for CLI entrypoint, though inconsistent with structured errors. |
| `cmd/goultroid/main.go` (various) | `fmt.Fprintln`, `fmt.Fprintf` | Interactive CLI (`init`, `doctor`, `whoami`, `version`, `help`) | Intentional CLI stdout/stderr UX; `whoami` & `doctor` use `maskPhone`. |
| `internal/telegram/client.go:100,118` | `fmt.Print("Enter 2FA Password: ")`, `fmt.Print("Enter Telegram Login Code: ")` | Interactive terminal login prompt | Intentional CLI auth prompt (reads from `os.Stdin`). |

No stray `fmt.Println`/`log.Println` debug statements were found in `internal/` or `plugins/`.

---

## 2. Logger Initialization, Configuration & Lifecycle

- **Initialization (`internal/app/app.go:561-581`):**
  - `initLogger(logLevel string)` **always** uses `zap.NewDevelopmentConfig()` regardless of environment or mode. There is no production JSON encoder, no log sampling, and no stacktrace/caller level differentiation.
  - Returns `*zap.Logger` and `zap.AtomicLevel`.
- **Live Level Reloading (`internal/app/settings_live.go:32-45`):**
  - Bound to setting `debug.log_level` via `settings.LiveBinder`; updates `zap.AtomicLevel` dynamically.
- **Propagation:**
  - Passed as raw `*zap.Logger` to `buildCore`, `buildTelegramRuntime`, `buildDomainServices`, and `module.Runtime.Logger`.
  - `jobsManager.SetLogger(logger.Named("jobs"))` is the only subsystem using `.Named(...)`; most other components log with the root logger and ad-hoc message prefixes (`"dispatcher: ..."`, `"assistant: ..."`, `"downloader: ..."`).
  - Many plugins/services implement custom `SetLogger(*zap.Logger)` + `getLogger()` with `zap.NewNop()` fallback (`plugins/myxl`, `plugins/downloader`, `plugins/afk`, `plugins/settings`, `internal/services/userlog`, `internal/scheduler`, `internal/addon`).
- **Shutdown & Flushing (`internal/app/shutdown.go:88-92`, `internal/app/app.go:110`):**
  - Calls `a.logger.Sync()` ignoring `syscall.EINVAL` (for stdout/stderr TTYs), after `runtime.StopWithin` and `transportDone` join.

---

## 3. Core, Runtime & Telegram Logging Patterns

- **Inconsistent Context & Field Naming:**
  - `internal/core/middleware.go` uses `correlation_id`, `command`, `user_id`, `chat_id`, `duration`.
  - `internal/telegram/dispatcher_dispatch.go` mixes `camelCase` (`peerType`, `chatType`, `chatID`, `msgOut`) and `snake_case` (`chat_id`, `error_type`, `error_code`, `handler_id`, `fail_closed`).
  - `plugins/myxl/maintenance_observability.go` uses `plugin`, `job_id`, `trigger_source`, `outcome`, `error_class`.
  - `plugins/downloader/` uses `category`, `provider`, `phase`.
  - Most other call sites lack `component`, `operation`, `event`, or `error_category`.
- **Error Classification:**
  - `internal/core/errors.go` defines `ErrorCategory` (`CategoryNone`, `CategoryTransient`, `CategoryPermanent`, `CategoryRateLimited`, `CategorySecurity`, `CategoryInvalidInput`, `CategoryResourceLimit`, `CategoryInternal`), and `internal/services/userlog/service.go` has `deliveryErrorCode`, while `plugins/downloader` has `classifyDownloaderError`. However, there is **no unified logging error classifier** used across core, scheduler, jobs, RPC, and plugins.

---

## 4. UserLog Pipeline (Producer -> Queue -> Telegram RPC)

### 4.1 Architecture & Producers
- **Service (`internal/services/userlog/service.go`):**
  - `Sender` interface (`SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error)`).
  - Synchronous `sendToLogChat(ctx, text)` calls `svc.SendMessage` directly; relies on `TelegramServicer` / `RPCExecutor` for FloodWait/retry.
  - Supports `LogMention`, `LogPM`, `LogAction`, `LogActionDetailed`, `SendTestMessage`.
  - `plugins/myxl` also holds a `*userlog.Service` reference (`p.userlogSvc`) for notification destination resolution/delivery.
- **Plugin Worker & Queue (`plugins/userlog/userlog.go`):**
  - Subscribes to `core.EventBus` for incoming messages (`HandleMessageEvent`), moderation actions, and PMPermit actions.
  - Enqueues closures (`chan func()`) into a bounded queue with an on-demand idle-timeout worker (`startWorkerLocked` / `worker`).
  - `ShutdownContext` closes the queue, cancels `p.ctx` immediately, closes subscriptions, and discards queued old-generation items so plugin unload is never blocked by Telegram RPCs.

### 4.2 Identified UserLog Gaps
1. **No pre-delivery content sanitization:** `LogPM` and `LogMention` take raw `message.Text`, truncate to 250 runes (`truncateRunes`), and HTML-escape it. Secrets, OTPs, tokens, or sensitive URLs in PMs/mentions are forwarded verbatim to the log chat. `LogActionDetailed` also forwards raw `errDetail`.
2. **Missing media metadata:** `HandleMessageEvent` only extracts `message.Text`; messages with photos, documents, stickers, voice notes, or video without captions produce empty/uninformative log entries.
3. **Inaccurate / incomplete metrics:**
   - `userlog.Service.Stats()` tracks `deliveredCount`, `failedCount`, `consecutiveFailures`, `lastSuccessAt`, `lastErrorAt`, `lastErrorMsg`.
   - `plugins/userlog` tracks `enqueuedCount`, `deliveredCount`, `droppedCount` separately—and `p.deliveredCount.Add(1)` in `worker()` increments even if `job()` returned an error or skipped because the feature/destination was disabled!
   - `IsFeatureEnabled` lookup failures in `LogMention`/`LogPM`/`LogActionDetailed` return errors without incrementing `failedCount`.
4. **Closure-based queue:** `chan func()` allocates a closure per event instead of a typed, bounded `UserLogEvent` struct.

---

## 5. Error Handling & Panic Recovery

- **Command Panics (`internal/core/middleware.go:40-60`):**
  - `RecoveryMiddleware` recovers panics, converts `debug.Stack()` to `string`, and logs `zap.Any("recover", r)` + `zap.String("stack", stack)` without redaction.
- **Core & Runtime Panics (`internal/app/app.go:686-718`):**
  - `zapCorePanicReporter` and `zapRuntimePanicReporter` log `zap.Any("value", report.Value)` and `zap.ByteString("stack", report.Stack)` without redaction or size bounds.
- **Inline Engine Panics (`internal/services/inline/engine.go:950`):**
  - Logs `zap.Any("panic", rec)` without redaction.
- **Dispatcher Message Hook Panics (`internal/telegram/dispatcher_dispatch.go:640`):**
  - Safely logs `zap.String("panic_type", fmt.Sprintf("%T", r))` (avoids leaking panic value). Good pattern to standardize.

---

## 6. Event Duplication & Log Noise

1. **Command Execution Double-Logging:**
   - `internal/core/middleware.go:108-128` (`LoggingMiddleware`) logs `Warn` on command errors (`command hit telegram rate limit` or `command executed with error`).
   - `internal/core/executor.go:158-163` (`CommandExecutor.Execute`) immediately logs `Error("command failed", ...)` for the exact same error!
2. **Debug Hot-Path Noise:**
   - `LoggingMiddleware` logs both `"executing command"` (`Debug`) and `"command executed successfully"` (`Debug`) for every command, plus `dispatcher_dispatch.go` logs `Debug` for idempotency/claim/hook steps.

---

## 7. Security & Redaction Gaps (Verified in Code)

| Area | Location | Exact Issue Found | Risk |
| --- | --- | --- | --- |
| **Command Args Leak** | `internal/core/middleware.go:97` | `zap.Strings("args", ctx.Args)` logged at `Debug` level for every command. | High — leaks OTPs, MSISDNs, passwords, API keys, tokens passed to commands (e.g. `.myxl`, `.set`, `.ytcookies`). |
| **Inline Query Leak** | `internal/services/inline/engine.go:819,846` | `zap.String("query", trimmed)` logged at `Debug` level on rate-limit and no-match. | Medium — leaks raw user inline queries. |
| **ResourceManager URL Leak** | `internal/platform/network/service.go:150-153` | `Metadata: map[string]string{"url": req.URL.String(), "method": req.Method}` stores full URLs (including query params/tokens) in `ResourceManager`. | High — exposes query tokens/secrets in resource diagnostics/snapshots. |
| **Panic Value / Stack Leak** | `internal/app/app.go:697-698,714-715`, `internal/core/middleware.go:50-51`, `internal/services/inline/engine.go:950` | `zap.Any("value", ...)`, `zap.Any("recover", r)`, `zap.Any("panic", rec)`, and full stack traces logged raw. | Medium — panic payloads can contain secrets or message text. |
| **UserLog Content Leak** | `internal/services/userlog/service.go:469,503,544` | Raw `messageText` (250-rune snippet) and `errDetail` forwarded to Telegram log chat without secret/OTP/token redaction. | Medium — forwards sensitive PMs/mentions/errors to log group. |
| **Audit Service (Good Reference)** | `internal/platform/audit/audit.go:30-57` | `auditDetails` strictly allowlists scalar keys (`found`, `secret_present`, `owner_present`, `arg_count`). | Safe pattern to emulate for structured log metadata. |
| **Dispatcher Parse Error (Good Reference)** | `internal/telegram/dispatcher_dispatch.go:85-94` | Explicitly avoids logging `msg.Message` or `err.Error()`, logging only `reason` enum (`unclosed_quote`, `trailing_escape`, `invalid_syntax`). | Safe pattern to preserve and generalize. |

---

## 8. Diagnostics & Runtime Metrics Integration

- `plugins/sysinfo/diagnostics_snapshot.go` aggregates bounded numeric counters (`ProcessMemory`, `TaskEngine`, `EventBus`, `Persistence`, `Jobs`, `Interaction`, `Inline`, `ResourceActive`, `ResourceLeaked`, `DB`, `ResolverCache`, `PeerCache`, `RPC`).
- **Gap:** Operational logging has **no counters** in `ResourceSnapshot` (e.g., dropped logs, sampled logs, panic count, error counts by category), and `UserLog` `ServiceStats` is isolated from `sysinfo` diagnostics.

---

## 9. Performance, Allocations & Overhead

- `initLogger` uses `zap.NewDevelopmentConfig()` (stacktraces on `Warn+`, console encoder, no `zapcore.NewSamplerWithOptions`), increasing CPU and allocation overhead under burst errors/warnings.
- `internal/core/middleware.go:97` allocates and serializes `ctx.Args` on every command when `Debug` is enabled.
- `plugins/userlog/userlog.go` allocates a `func()` closure per event rather than passing a value-typed `UserLogEvent` struct through the bounded channel.
- `internal/platform/audit/audit.go` takes `s.mu.Lock()` on every `Record()` and copies the ring buffer slice on shift (`s.buffer[1:]`).

---

## 10. Ownership & Keep / Merge / Replace / Remove Summary

| Component | Current Owner | Recommendation | Rationale |
| --- | --- | --- | --- |
| **Zap Core & `AtomicLevel`** | `internal/app/app.go`, `settings_live.go` | **Keep & Harden** | Solid foundation; replace `NewDevelopmentConfig()` with explicit prod/dev config + sampling + pre-sink redaction core. |
| **Logging Contract / Facade** | *(Missing)* | **Introduce (L1)** | Decouple `core`, `services`, and `plugins` from raw `*zap.Logger` conventions; enforce `component`, `operation`, `event`, `correlation_id`, `error_category`. |
| **Redaction Policy** | Ad-hoc (`auditDetails`, `maskPhone`, `dispatcher_dispatch`) | **Centralize (L2)** | Single shared redaction/sanitization policy applied to log fields, `ResourceManager` URLs, panic values, and `UserLog`. |
| **`LoggingMiddleware` + `CommandExecutor`** | `internal/core/` | **Merge / Deduplicate (L5)** | Remove `ctx.Args` logging; eliminate double-logging between middleware and executor. |
| **`UserLog` Service & Plugin** | `internal/services/userlog`, `plugins/userlog` | **Rewrite Event Model & Metrics (L7)** | Keep bounded worker + `ShutdownContext` cancellation; replace `chan func()` with typed events, add media metadata, redaction, and accurate drop/skip/fail metrics. |
| **`Audit` Service** | `internal/platform/audit` | **Keep & Align** | Allowlist pattern is strong; align its event schema with the new logging contract. |
