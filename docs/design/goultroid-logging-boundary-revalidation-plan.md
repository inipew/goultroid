# Goultroid Logging Overhaul — Boundary Revalidation Plan (Pre-L4 Gate)

- **Status:** Active (Read-Only Audit — No Production Code Modified)
- **Verified HEAD:** `a13966320826bc8cdb5ecb99f37c97838fc66fc7` (`origin/test-next`)
- **Predecessor:** [goultroid-logging-l1-l3-corrective-audit-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l1-l3-corrective-audit-plan.md) (R1–R6 Closed)
- **Next Gate:** [goultroid-logging-l4-execution-integration-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l4-execution-integration-plan.md)

---

## 1. Executive Baseline

At commit `a13966320826bc8cdb5ecb99f37c97838fc66fc7`:
- **Total non-test Go files importing `go.uber.org/zap`:** **51 files**
  - **2 infrastructure-owned Zap files** (`internal/logging/zaplog/adapter.go`, `internal/app/logger.go`) — permanent Zap boundary.
  - **1 live-reload binding** (`internal/app/settings_live.go`) — permanent `zap.AtomicLevel` boundary.
  - **48 consumer/wiring files** still importing `go.uber.org/zap` directly (pending staged migration in L4–L6).
- **`zap.SugaredLogger` (`Sugar()`) usages:** **0** across `cmd/`, `internal/`, `plugins/`, and `tools/`.
- **`zap.ByteString` usages:** **0** across all non-test files.
- **`zap.Any` usages:** **1** (`internal/platform/audit/audit.go:124` — `event.Details`, already strictly allowlisted by `auditDetails()` to 4 scalar keys: `found`, `secret_present`, `owner_present`, `arg_count`).

---

## 2. Complete Inventory & Classification of Direct Zap Usages (51 Files)

| Category | Count | Files | Migration Target |
| --- | --- | --- | --- |
| **Infrastructure Logging (Keep Zap)** | 3 | `internal/logging/zaplog/adapter.go`, `internal/app/logger.go`, `internal/app/settings_live.go` | **Keep Zap** (backend & `AtomicLevel` owner) |
| **App Composition Root (Transitional)** | 6 | `internal/app/app.go`, `dependencies.go`, `lifecycle.go`, `wiring_core.go`, `wiring_services.go`, `wiring_telegram.go` | Remove raw `*zap.Logger` after L4–L6 complete |
| **Execution Pipeline (L4 Scope)** | 4 | `internal/jobs/manager.go`, `manager_recovery.go`, `internal/scheduler/engine.go`, `periodic_timer.go` | **Migrate in L4** to `logging.Logger` |
| **Core & Telegram Ingress (L5 Scope)** | 10 | `internal/core/executor.go`, `middleware.go`, `internal/telegram/client.go`, `dispatcher.go`, `dispatcher_dispatch.go`, `dispatcher_callback.go`, `dispatcher_handlers.go`, `dispatcher_command_claim.go`, `dispatcher_callback_claim.go`, `dispatcher_peer.go`, `resolver.go` *(11 files)* | **Migrate in L5** to `logging.Logger` (P0 leaks already closed in R2) |
| **Assistant Bot (L5 Scope)** | 8 | `internal/assistant/app.go`, `client/audience.go`, `client/client.go`, `client/force_sub.go`, `client/shell_interaction.go`, `client/updates.go`, `command/router.go`, `interaction/message.go` | **Migrate in L5** to `logging.Logger` |
| **Domain Services (L5/L7 Scope)** | 6 | `internal/services/broadcast/service.go`, `inline/engine.go`, `moderation/service.go`, `pmpermit/approval.go`, `pmpermit/service.go`, `userlog/service.go` | **Migrate in L5/L7** (`userlog` in L7; P0 leaks closed in R2) |
| **Platform, Addons, Voice, Module (L5/L6 Scope)** | 5 | `internal/addon/ipc.go`, `addon/manager.go`, `internal/module/module.go`, `internal/platform/audit/audit.go`, `internal/voice/player.go` | **Migrate in L5/L6** (`audit` already allowlisted) |
| **Plugins (L6 Scope)** | 8 | `plugins/afk/afk.go`, `downloader/downloader.go`, `downloader/interactive.go`, `downloader/youtube_cookies.go`, `downloader/youtube_cookies_snapshot.go`, `myxl/maintenance_observability.go`, `myxl/myxl.go`, `settings/settings.go` | **Migrate in L6** once `module.Runtime.Logger` switches to `logging.Logger` |

*(Note: `internal/logging`, `internal/runtime`, `internal/taskengine`, `internal/resource`, `internal/config`, and `internal/platform/network` have **0** direct `go.uber.org/zap` imports.)*

---

## 3. Sensitive Field & Redaction Verification

| Check Target | Current State at `a1396632` | Status |
| --- | --- | --- |
| **Command `ctx.Args`** | Removed in R2 (`arg_count` only in `internal/core/middleware.go`) | **Closed** |
| **Inline raw `query`** | Removed in R2 (`query_len` only in `internal/services/inline/engine.go`) | **Closed** |
| **ResourceManager URLs** | Sanitized via `logging.SanitizeURL` in L2 (`internal/platform/network/service.go`) | **Closed** |
| **Panic payloads & stacks** | Routed through `logging.RedactPanic` in `middleware.go`, `inline/engine.go`, `app.go`, `supervisor.go` | **Closed** |
| **UserLog PM/Mention/Action** | Routed through `logging.RedactString` before `core.EscapeHTML` in `internal/services/userlog/service.go` | **Closed (secret redaction);** full event/privacy model in L7 |
| **`logging.RedactError` / `RedactString` in direct-Zap files** | Used in `core/middleware.go`, `inline/engine.go`, `app/logger.go`; **not yet** used in unmigrated `zap.Error(err)` sites (`core/executor.go`, `assistant/*`, `scheduler/*`, `pmpermit/*`, `plugins/*`), nor `youtube_cookies.go:165` (`valErr.Error()`) or `shell_interaction.go:157` (`result.Failure.Message`) | **Tracked for L4–L6** (automatically resolved when migrated to `logging.Logger` / `zaplog.Adapter`) |
| **Headers / Cookies / Tokens / Metadata** | `auditDetails` allowlists 4 scalars; `plugins/myxl` & `plugins/downloader` log only bounded enum categories/status | **Verified safe** |

---

## 4. Files Intentionally Kept on Zap (Not Migrated to Consumer Facade)

1. `internal/logging/zaplog/adapter.go` — Implements `logging.Logger` over `*zap.Logger` and enforces `RedactField` / `RedactString` pre-sink.
2. `internal/app/logger.go` — Owns Zap configuration (`production` JSON + sampling vs `development` console), stderr fallback, and `Sync()`.
3. `internal/app/settings_live.go` — Owns `zap.AtomicLevel` live reload binding for `debug.log_level`.

---

## 5. Pre-L4 Gate Conclusion

- **Baseline direct-Zap count before L4:** **51 files** total (**3 permanent Zap infrastructure**, **48 transitional/consumer files**).
- **No P0 leaks remain** on any audited hot path (`args`, `query`, `url`, `panic`, `UserLog`, `UnknownFieldKind`).
- **Recommendation:** Boundary revalidation is **clean**. Proceed directly to **L4 — Execution Pipeline Integration** (`internal/taskengine`, `internal/jobs`, `internal/scheduler`, `internal/telegram/rpc_executor.go`) per [goultroid-logging-l4-execution-integration-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l4-execution-integration-plan.md), which will migrate 4 of the 48 files (`jobs/*`, `scheduler/*`) and integrate `TaskEngine` + `RPCExecutor` directly onto `logging.Logger`.
