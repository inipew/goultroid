# Goultroid Logging Architecture Overhaul — L1 Architecture & Contract Design

- **Phase:** L1 (Architecture & Contract Design — No Production Code Modified)
- **Baseline Commit:** `2b09316d18f92403f95bdbb020011d64d6174a4b` (`origin/test-next`)
- **Master Plan:** [goultroid-logging-overhaul-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-overhaul-plan.md)
- **L0 Baseline:** [goultroid-logging-l0-inventory.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l0-inventory.md)

---

## 1. Current Architecture & Core Problems

Based on the [L0 Inventory](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l0-inventory.md), Goultroid currently has two logging paths—Zap operational logging and `UserLog` Telegram notifications—with the following structural defects:

| Priority | Problem | Root Cause in Codebase |
| --- | --- | --- |
| **P0** | **48 files depend directly on `go.uber.org/zap`** | No logging facade or shared field contract; `module.Runtime.Logger` exposes `*zap.Logger` directly. |
| **P0** | **No centralized redaction boundary** | `LoggingMiddleware` logs `ctx.Args`, `network.Service` stores raw `req.URL.String()` in `ResourceManager`, `inline.Engine` logs raw `query`, panic handlers log `zap.Any` + raw stack, and `UserLog` forwards raw `message.Text`. |
| **P1** | **Fragmented runtime & execution observability** | `internal/runtime`, `internal/taskengine`, `internal/resource`, `RPCExecutor`, and `gotd` have zero operational logging integration; `CorrelationID` is not propagated end-to-end across layers. |
| **P1** | **Double-logging of command failures** | `core.LoggingMiddleware` logs `Warn` and `core.CommandExecutor.Execute` immediately logs `Error` for the same error. |
| **P1** | **Inaccurate `UserLog` delivery metrics** | `plugins/userlog` worker increments `deliveredCount` even when `userlog.Service` returns an error or skips due to disabled/unconfigured state; closure queue (`chan func()`) lacks typed metadata. |
| **P2** | **Development config hardcoded in production** | `initLogger` in `internal/app/app.go` unconditionally calls `zap.NewDevelopmentConfig()` with no sampling, JSON mode, or caller/stack policy. |

---

## 2. Target Logging Architecture (L1-A)

```text
+-----------------------------------------------------------------------------+
|                            Goultroid Application                            |
|  Core · Runtime · TaskEngine · Jobs · Scheduler · Telegram · Services · Plugins
+--------------------------------------+--------------------------------------+
                                       |
                                       v
+-----------------------------------------------------------------------------+
|                     internal/logging (Logging Facade)                       |
|  Logger interface · Typed Field value struct · Context helpers · Classifier |
+--------------------------------------+--------------------------------------+
                                       |
                                       v
+-----------------------------------------------------------------------------+
|                        Shared Policy & Redaction Layer                      |
|  Pre-sink Redactor · Allowlisted Metadata · Cardinality Caps · Safe Panics  |
+--------------------+-------------------------------------+------------------+
                     |                                     |
                     v                                     v
+------------------------------------+  +-------------------------------------+
|    Operational Backend (Zap)       |  |    User Activity Audit (UserLog)    |
|  Console / JSON · AtomicLevel      |  |  Typed UserLogEvent · Bounded Queue |
|  Sampling · Sync() on Shutdown     |  |  Media Metadata · Delivery Metrics  |
+------------------------------------+  +-------------------------------------+
```

### Architectural Decision (L1-A): Package Ownership & Backend
- **Decision:** Introduce a lightweight Tier-0/shared package `internal/logging` containing the `Logger` interface, value-typed `Field` struct, error classifier, correlation/context helpers, and centralized `Redactor`. Keep `go.uber.org/zap` as the operational backend encapsulated behind `internal/logging` (or `internal/logging/zapbackend` constructed in `internal/app`), so `core`, `services`, and `plugins` do not import `go.uber.org/zap`.
- **Rejected Alternatives:**
  1. *Using `log/slog` directly as the facade:* `slog.Logger` is a concrete struct rather than a minimal interface, and wrapping Zap via `slog.Handler` adds allocation overhead for `slog.Record` attribute iteration compared to a direct `Field` -> `zap.Field` adapter.
  2. *Exposing `*zap.Logger` everywhere with helper functions:* Perpetuates tight coupling of 48+ files and plugins to Zap types, making centralized pre-sink policy enforcement and plugin isolation harder.
  3. *Building a custom async logging EventBus/worker pool:* Explicitly forbidden; Zap already provides fast, low-allocation buffered/locked writes and sampling without a second runtime.
- **Technical Rationale:** Placing `internal/logging` at the bottom of the dependency graph (importing only stdlib, plus `go.uber.org/zap` only in the backend adapter) satisfies `internal/architecture/imports_test.go` across all 4 tiers.
- **Risks & Mitigations:** Over-abstraction risk -> keep the `Logger` interface minimal (6 methods) and `Field` as a flat value struct.

---

## 3. Logging Facade & Interface Contract (L1-A)

```go
package logging

type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)
    With(fields ...Field) Logger
    Named(name string) Logger
}
```

### Design Decisions on the Interface
- **Why `Named(name string)` is included:** Components and plugins can derive scoped loggers (`logger.Named("jobs")`, `logger.Named("myxl")`) which automatically set/prefix the `component` context without manual string prefixes in every log message.
- **Why `Fatal` is NOT on `Logger`:** Calling `Fatal` (`os.Exit(1)`) inside services, dispatchers, or plugins bypasses `App.Shutdown()`, `runtime.StopWithin()`, `ResourceManager` verification, and `logger.Sync()`. Startup/CLI fatal errors belong exclusively to `cmd/goultroid/main.go`.
- **Why `Sync()` is NOT on `Logger`:** Only the composition root (`internal/app`) owns the concrete backend lifecycle and calls `Sync() error` during `App.performShutdown()`. Plugins and services must never flush/sync the global sink.
- **Context-aware logging (`FromContext` / `WithLogger` vs explicit `Logger`):**
  - Services and plugins receive an explicit `logging.Logger` via constructor or `module.Runtime.Logger` (never `nil`; fallback to `logging.Nop()`).
  - Request-scoped correlation metadata (`correlation_id`, `causation_id`) is read from `context.Context` via `logging.ContextFields(ctx)` or `logger.WithContext(ctx)` helper functions, avoiding storing a logger inside `context.Context` as the primary injection mechanism.
- **Zero-allocation `Field` representation:**
  `Field` is a flat value struct (`Key string`, `Kind FieldKind`, `Int64 int64`, `Str string`, `Any any`) mirroring `zapcore.Field`'s layout. Primitives (`String`, `Int`, `Int64`, `Bool`, `Duration`, `Time`) do not box into `any`.

---

## 4. Structured Event Schema (L1-B)

### Standard Field Keys (`snake_case` everywhere)

| Field Key | Type | Requirement | Description |
| --- | --- | --- | --- |
| `timestamp` | RFC3339 / ISO8601 | Automatic (Zap) | Event emission time in UTC. |
| `level` | `debug\|info\|warn\|error` | Automatic (Zap) | Severity level. |
| `component` | `string` | Recommended | Owning subsystem (`dispatcher`, `executor`, `scheduler`, `jobs`, `rpc`, `myxl`, `downloader`). |
| `operation` | `string` | Recommended | Logical action (`dispatch`, `execute`, `recover`, `download`, `notify`, `shutdown`). |
| `event` | `string` | Recommended | Stable, low-cardinality snake_case identifier (`command_failed`, `probe_complete`, `flood_wait`). |
| `correlation_id` | `string` | When in request/job scope | Bounded ID linking `Update -> Dispatcher -> Command -> TaskEngine -> Job -> RPC`. |
| `error_category` | `string` | On error/warn | Classified category from Section 7 (`validation`, `telegram_rpc`, `network`, etc.). |
| `retryable` | `bool` | On error when known | Whether the operation is safe/expected to retry. |
| `duration_ms` | `float64` / `duration` | On completed ops | Elapsed execution duration. |
| `result` | `string` | Optional | Bounded outcome (`success`, `rejected`, `failed`, `cancelled`, `skipped`). |

### Cardinality Rules
- `component`, `operation`, `event`, `error_category`, `result` **must** be bounded enums/constants—never dynamic user input, URLs, or formatted error strings.
- Remove inconsistent `camelCase` keys observed in L0 (`peerType`, `chatType`, `chatID`, `msgOut` in `dispatcher_dispatch.go`) and standardize on `snake_case` (`peer_type`, `chat_type`, `chat_id`, `msg_out`).

---

## 5. Context Propagation & Correlation (L1-B)

### End-to-End Flow

```text
Telegram Update (Ingress)
  └─> EnsureCorrelation(ctx)  [generates cid-... if absent; never uses msg.ID or user.ID]
        ├─> Dispatcher (attaches correlation_id to MessageEnvelope & core.Context)
        │     ├─> Command Handler / Middleware (reads & propagates ctx)
        │     ├─> TaskEngine.Submit(ctx, ...) (preserves correlation_id across worker hop)
        │     └─> JobsManager (persists/restores correlation_id & causation_id on job)
        └─> Telegram RPCExecutor (reads correlation_id from ctx for RPC diagnostics)
```

### Decisions (L1-B)
- **Single Correlation Format:** Unify `core.NewCorrelationID()` (`cid-<unixNano>-<hex>`) and `core.CorrelationMiddleware` (`req-...`) into one generator in `internal/logging` (or `core` delegating to `logging`).
- **Context Contract:** `logging.WithCorrelationID(ctx, id)` and `logging.CorrelationID(ctx)` (with `core.WithCorrelationID` / `core.GetCorrelationID` aliasing or delegating during migration) ensure `ctx.Ctx` in `core.Context` **always** carries `ctx.CorrelationID`.
- **No raw Telegram IDs as correlation IDs:** `msg.ID` (which collides across chats) and `user.ID` are never used as `correlation_id`.
- **Rejected Alternative:** OpenTelemetry span/trace runtime -> explicitly rejected to avoid a second tracing runtime and per-span heap allocations.

---

## 6. Redaction Architecture & Security Contract (L1-C)

### Non-Negotiable Rules
1. Never log passwords, tokens, OTPs, cookies, API keys, or session credentials.
2. Never log raw URLs with query parameters or userinfo (`scheme://host/path` only, or redacted query).
3. Never log `ctx.Args` automatically (`LoggingMiddleware` must log `arg_count` only, never `args`).
4. Never log raw inline queries (`inline.Engine` must log `query_len` / `matched_pattern`, never raw `query`).
5. Never log raw `zap.Any("recover", r)` or unbounded stack traces from panics.
6. Never rely on developer memory alone—redaction is enforced at the typed field & sink boundary.

### Two-Layer Defense (Typed Fields + Pre-Sink Redactor)
- **Primary Defense (Structural / Typed Fields):**
  - `logging.URL(key, rawURL)` parses and strips query strings, fragments, and `user:pass@` credentials before emitting (`https://example.com/path?[redacted]`).
  - `logging.Error(err)` extracts `error_category` and `retryable`, and sanitizes `err.Error()` through the `Redactor` (stripping URLs, bearer/token patterns, home paths) before writing.
  - `logging.Panic(r, stack)` records `panic_type` (`fmt.Sprintf("%T", r)`), redacts the stringified value if safe/bounded, and caps/redacts stack traces (first N frames, strips `/home/...` prefixes).
  - `logging.Metadata(key, map[string]string)` uses explicit key allowlisting (following `internal/platform/audit/audit.go`'s `auditDetails` pattern).
- **Secondary Defense (Pre-Sink Redactor):**
  - A fast-path `Redactor` in `internal/logging` inspects sensitive field keys (`token`, `secret`, `password`, `otp`, `cookie`, `authorization`, `api_key`, `msisdn`, `phone`, `session`) and masks their values (`[REDACTED]`), plus scrubs common credential patterns in error strings.
  - Applied **before** Zap encodes or `ResourceManager` stores metadata (fixing `internal/platform/network/service.go:150-153`) or `UserLog` formats Telegram messages.
- **Rejected Alternative:** Regex-only scrubbing on serialized JSON lines -> slow, fragile, and misses structured `ResourceManager` metadata and `UserLog` HTML messages.

---

## 7. Error Classification (L1-B)

Map all operational errors into 8 canonical logging categories, bridging `core.ErrorCategory` and `execution.Disposition`:

| Logging `error_category` | Existing `core.ErrorCategory` / Sentinel | `retryable` | Examples |
| --- | --- | --- | --- |
| `validation` | `CategoryInvalidInput`, `ErrValidation`, `ErrInvalidArgs`, `UsageError` | `false` | Bad command syntax, missing args, invalid setting |
| `authorization` | `CategorySecurity`, `ErrPermissionDenied`, `ErrUnauthorized`, `ErrForbidden`, `ErrInvocationDenied` | `false` | Non-owner command, group role denied |
| `telegram_rpc` | `ErrTelegram`, `ErrRateLimited`, `RateLimitError`, `FLOOD_WAIT` | `true` (if rate-limited/transient) | `FLOOD_WAIT`, `PEER_ID_INVALID`, `MESSAGE_NOT_MODIFIED` |
| `network` | `network.ErrNetworkTimeout`, HTTP/dial errors | `true` | DNS failure, TCP reset, HTTP timeout |
| `persistence` | `ErrStorage`, SQLite errors | depends | `SQLITE_BUSY`, constraint/query failure |
| `resource` | `CategoryResourceLimit`, `ErrResourceLimit`, admission rejection | `true` | Queue full, concurrency budget exhausted |
| `cancellation` | `context.Canceled`, `ErrCancelled`, `context.DeadlineExceeded` | `false` (cancelled) / `true` (deadline) | Shutdown cancellation, timeout |
| `internal` | `CategoryInternal`, `ErrInternal`, panics, unknown | `false` | Nil dependency, recovered panic |

---

## 8. Log Levels & Operational Policy (L1-B)

| Level | Policy | Hot-Path Rules |
| --- | --- | --- |
| **Debug** | Controlled diagnostics (disabled in production by default). | Never log raw `ctx.Args`, raw `msg.Message`, or raw inline `query`. |
| **Info** | Significant lifecycle & operational milestones (startup, shutdown, plugin loaded, job summary, maintenance run). | **Never** log per-Telegram update, per-successful RPC, or per-successful command at `Info`. |
| **Warn** | Recoverable abnormal conditions (rate limit/FloodWait, retryable failure, degraded UserLog delivery, fallback triggered). | Sample or rate-limit repetitive warnings (e.g., repeated retry/admission rejections). |
| **Error** | Operation failures requiring attention (unhandled command failure, persistence failure, panic recovery, failed lifecycle transition). | **Single owner logs each failure** (eliminate `LoggingMiddleware` + `CommandExecutor` double-logging). |
| **Fatal** | **Prohibited** in `internal/` and `plugins/`. | Only `cmd/goultroid` may exit with non-zero status on unrecoverable startup failure. |

---

## 9. Logger Initialization & Configuration (L1-D)

- **Configuration (`internal/config`):**
  - `LOG_LEVEL`: `debug | info | warn | error` (default: `info`).
  - `LOG_FORMAT`: `console | json` (default: `console` for local/TTY, `json` for structured/daemon deployments).
  - `LOG_ENV` (or derived from config): `development | production` (default: `production` for `goultroid run` unless `development` is selected).
- **Production vs Development Zap Config:**
  - **Production:** JSON or clean console encoder, `zapcore.NewSamplerWithOptions` (e.g., initial 100, thereafter 10 per second per level/message) to prevent log storms during FloodWait/network outages, stacktraces only on `Error` (never on `Warn`), ISO8601 UTC timestamps.
  - **Development:** Human-readable console encoder, colored levels, stacktraces on `Error`.
- **Live Reload:** Preserve `zap.AtomicLevel` bound to `debug.log_level` in `internal/app/settings_live.go`.

---

## 10. Lifecycle & Shutdown Contract (L1-D)

1. **Startup:** `logging` backend is initialized **first** in `internal/app` before `buildCore`, `buildTelegramRuntime`, and `buildDomainServices`. `logging.Nop()` is used in tests or when no logger is injected.
2. **Runtime & Supervisor Integration:** `internal/runtime` and `internal/runtime/supervisor.go` accept `logging.Logger` to record component start/stop failures and worker restarts, replacing ad-hoc `PanicReporter` raw Zap structs with `logging.Panic(...)`.
3. **Shutdown:**
   - Operational logger remains active throughout `runtime.StopWithin(shutdownCtx)` and `ResourceManager.ForceCleanupAllContext`.
   - `Sync()` is called **once** at the end of `App.performShutdown()` (ignoring `syscall.EINVAL` / `ENOTTY` on stdout/stderr), ensuring final shutdown diagnostics are flushed.
   - Operational logger has **zero** dependency on Telegram transport.

---

## 11. Plugin Integration Contract (L1-E)

- **Boundary:** `module.Runtime.Logger` changes from `*zap.Logger` to `logging.Logger` (scoped per module via `rt.Logger.Named(manifest.ID)`).
- **Plugin Rules:**
  - Plugins must **never** import `go.uber.org/zap`, `internal/app`, or `os/exec`/`net/http` for logging.
  - Plugins must **never** call `Sync()` or `Fatal`.
  - Plugins receive a pre-scoped, non-nil `logging.Logger` (eliminating boilerplate `getLogger()` + `zap.NewNop()` in `myxl`, `downloader`, `afk`, `settings`).
  - Plugins pass `ctx` (or `logging.WithCorrelationID`) so command/job logs automatically correlate.

---

## 12. UserLog Boundary (L1-E)

`UserLog` remains a **separate** Telegram activity audit service (`internal/services/userlog` + `plugins/userlog`), sharing only the **Redaction Policy** (`L2`) and **Error Classification** (`L1-B`) with operational logging:

- **Typed Event Model:** Replace `chan func()` in `plugins/userlog` with a bounded channel of typed `UserLogEvent` structs (`Kind`: `PM | Mention | Action | System`, `Actor`, `Chat`, `MessageSummary`, `MediaMeta`, `Outcome`, `ErrorCategory`, `CorrelationID`).
- **Pre-Delivery Sanitization:** All text snippets and error details pass through `logging.Redactor` before HTML formatting and Telegram `SendMessage`.
- **Media Metadata:** Extract safe, bounded media descriptors (`photo`, `document`, `video`, `voice`, `sticker`, `mime_type`, `size_bytes`) when `message.Text` is empty or accompanied by media.
- **Accurate Metrics:** Separate `enqueued`, `delivered` (incremented **only** when `SendMessage` succeeds), `failed`, `dropped` (queue full / shutting down), and `skipped` (feature disabled / destination unconfigured). Expose bounded `UserLogStats` to `sysinfo.ResourceSnapshot`.
- **Shutdown:** Preserve non-blocking `ShutdownContext` behavior (immediate context cancellation + discard of queued items so plugin unload never blocks on Telegram RPCs).

---

## 13. Architecture Import Constraints

Per [internal/architecture/imports_test.go](file:///home/dhimas/any/random/ultroid-go/internal/architecture/imports_test.go):
- `internal/logging` (contract, fields, redactor, classifier) **must not** import `internal/core`, `internal/database`, `internal/services/*`, `plugins/*`, `internal/app`, or `cmd/*`.
- `internal/core`, `internal/database`, `internal/runtime`, `internal/taskengine`, `internal/jobs`, `internal/scheduler`, `internal/telegram`, `internal/services/*`, and `plugins/*` **may** import `internal/logging`.
- `internal/app` constructs the Zap backend and injects `logging.Logger` into all subsystems and `module.Runtime`.
- `internal/architecture/imports_test.go` will be updated in L1/L2 implementation to enforce that `plugins/*` and `internal/core` do not directly import `go.uber.org/zap` once migration completes.

---

## 14. Performance & Allocation Constraints

- **No-op / Disabled Level Fast Path:** `if !logger.Enabled(level)` or Zap's `Check(level, msg)` short-circuits before allocating `Field` slices when `Debug` is disabled.
- **Zero-Allocation Primitives:** `logging.Field` uses value-type storage (`int64`, `string`, `FieldKind`) without `interface{}` boxing for strings, ints, bools, and durations.
- **Bounded Buffers & Cardinality:** No unbounded queues; no unbounded map/slice growth in `audit` or `ResourceManager` metadata; `Sampler` protects stdout/disk under burst errors.
- **Zero Lock Contention on Hot Path:** Operational logging uses Zap's lock-free/minimal-lock `zapcore`; `UserLog` uses non-blocking `select` enqueue on a bounded channel.

---

## 15. Migration Strategy (L2 – L8)

Rather than a risky big-bang rewrite of 48 files, migration proceeds in targeted phases:

| Phase | Scope | Strategy |
| --- | --- | --- |
| **L2** | `internal/logging` + `Redactor` + `ResourceManager` URL fix | Build `internal/logging` contract, Zap adapter, and `Redactor`; fix `network.Service` URL leak and `audit` alignment. |
| **L3** | `internal/app` + `internal/runtime` | Switch `initLogger` to prod/dev config + sampler; wire `logging.Logger` into `runtime`/`supervisor` and safe `Panic` reporters. |
| **L4** | `TaskEngine`, `JobsManager`, `Scheduler`, `RPCExecutor` | Propagate `CorrelationID` across task/job boundaries; add bounded RPC/scheduler diagnostics without per-RPC log spam. |
| **L5** | `core` middleware/executor, `telegram` dispatcher, `assistant`, `inline` | Remove `ctx.Args` and raw `query` logs; eliminate double-logging between `LoggingMiddleware` and `CommandExecutor`; normalize `snake_case` fields. |
| **L6** | `plugins/*` & `internal/module` | Switch `module.Runtime.Logger` to `logging.Logger`; migrate the 8 Zap-using plugins cleanly. |
| **L7** | `internal/services/userlog` & `plugins/userlog` | Typed `UserLogEvent`, redaction, media metadata, accurate metrics. |
| **L8** | `sysinfo` diagnostics, benchmarks, race & failure-injection tests | Verify zero leaks, zero races, and overhead targets. |

---

## 16. Acceptance Criteria & Testing Strategy

### L1 Design Acceptance Criteria
- [x] `Logger` interface is minimal (6 methods), `Fatal`-free, and tier-compliant with `internal/architecture/imports_test.go`.
- [x] Standard `snake_case` event schema and 8 `error_category` values bridge cleanly with `core.ErrorCategory` and `execution.Disposition`.
- [x] Correlation propagation covers `Update -> Dispatcher -> Command -> TaskEngine -> JobManager -> RPC` without a second tracing runtime.
- [x] Redaction policy addresses every L0 leak (`ctx.Args`, `ResourceManager` URL, `inline` query, panic payloads, `UserLog` text).
- [x] Migration path avoids big-bang breakage and preserves `UserLog` separation.

### Testing Strategy (for L2–L8 Implementation)
- **Unit & Security Tests:** Test `Redactor` against tokens, OTPs, MSISDNs, URLs with query secrets, and panic payloads; verify `LoggingMiddleware` never emits `args`.
- **Architecture Tests:** Extend `internal/architecture/imports_test.go` to verify tier boundaries and eventual removal of direct `go.uber.org/zap` imports in `plugins/*`.
- **Concurrency & Lifecycle Tests:** Run `go test -race ./...` covering `UserLog` bounded queue saturation, `ShutdownContext` cancellation, `App.Shutdown()` `Sync()`, and `TaskEngine` correlation propagation.
- **Benchmarks:** Add allocation/latency benchmarks comparing disabled `Debug` and active `Info`/`Error` paths against L0 baseline.
