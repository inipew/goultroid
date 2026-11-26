# Goultroid Logging Overhaul — L1–L3 Corrective Audit & Hardening Plan

- **Status:** Active (Design Only — No Production Code Modified Yet)
- **Baseline HEAD:** `76ec56702f8e0a0db20b43634deab26664b7c2a3` (`origin/test-next`)
- **Reviewed Commits:**
  - `f6a1daa7a390618c199185b56ad2e902435ec4cb` (L1–L2: Logging core, redaction, ResourceManager URL fix)
  - `9b5894dca41818ca1d453fec65ce050cbe622168` (L3 Design)
  - `dd585a7f5a947cca9e6c3f6735d5944a5972b201` (L3 Implementation)
- **Phase Gate Status:**
  - **L1 Architecture Design:** Accepted
  - **L2 Logging Foundation:** Revisions Required
  - **L3 Runtime & Lifecycle Integration:** Revisions Required
  - **L4 Execution Integration:** **On Hold** until all **P0** and **P1** findings below are resolved.

---

## 1. Findings Summary

| ID | Priority | Title | Affected Files |
| --- | --- | --- | --- |
| **P0-1** | **P0** | Redaction bypass via unknown `FieldKind` | `internal/logging/redactor.go`, `logger.go`, `zaplog/adapter.go` |
| **P0-2** | **P0** | `RedactString` misses camelCase, hyphenated, quoted, and uppercase URL secrets | `internal/logging/redactor.go` |
| **P0-3** | **P0** | Redaction is only partially enforced (legacy Zap & `UserLog` bypass adapter) | `internal/core/*`, `internal/services/inline/*`, `internal/services/userlog/*`, `plugins/*` |
| **P1-1** | **P1** | Error classification relies on substring matching instead of typed/sentinel errors | `internal/logging/errors.go`, `internal/core/errors.go`, `internal/execution/result.go` |
| **P1-2** | **P1** | Duplicate Supervisor panic events (`s.logger.Error` + `PanicReporter`) | `internal/runtime/supervisor.go`, `internal/app/app.go` |
| **P1-3** | **P1** | Shutdown calls `Sync()` even after transport/worker join timeout | `internal/app/shutdown.go`, `internal/runtime/supervisor.go` |
| **P1-4** | **P1** | `zaplog.Adapter.With()` lacks panic recovery when formatting `KindError` | `internal/logging/zaplog/adapter.go` |
| **P1-5** | **P1** | `WithCorrelationID` lacks input validation; `crypto/rand.Read` error ignored | `internal/logging/context.go` |
| **P2-1** | **P2** | `App` retains two logger access paths (`opLogger` + raw `logger`) | `internal/app/app.go`, `logger.go`, `wiring_core.go` |
| **P2-2** | **P2** | Fallback `stderr` diagnostics write raw `err` without `RedactString` | `internal/app/logger.go` |
| **P2-3** | **P2** | No allocation/latency benchmarks for `Redactor` or `zaplog.Adapter` | `internal/logging/*`, `internal/logging/zaplog/*` |
| **P2-4** | **P2** | `RedactPanic` trusts regex for raw panic string/error summaries | `internal/logging/redactor.go` |
| **P2-5** | **P2** | Test gaps (IPv6 zone, encoded paths, `With()` panic, duplicate panic, producer timeout, benchmarks) | `*_test.go` |

---

## 2. Detailed Findings & Required Fixes

### P0-1: Redaction Bypass via Unknown `FieldKind`
- **Locations:** `internal/logging/redactor.go` (`RedactField`), `internal/logging/zaplog/adapter.go` (`toZapFields`)
- **Reproduction:**
  ```go
  field := logging.Field{Key: "diagnostic", Kind: logging.KindUnknown, Str: "token=super-secret"}
  // RedactField hits `default: return f`; toZapFields hits `default: zap.String(f.Key, f.Str)`
  ```
- **Required Fix:**
  - Fail-closed in `RedactField`: any unknown/unrecognized `FieldKind` must be replaced with `Field{Key: f.Key, Kind: KindString, Str: "[redacted:unknown-kind]"}` (never return `f.Str` raw).
  - Fail-closed in `toZapFields`: explicit `switch` cases for every valid `FieldKind`; `default` drops or emits `zap.String(f.Key, "[redacted:invalid-kind]")`.
  - Add adversarial struct-literal tests (not just constructors).

### P0-2: `RedactString` Misses Common Secret Variations
- **Location:** `internal/logging/redactor.go`
- **Gaps Identified:**
  - Case-sensitive `https?://` misses `HTTPS://example.com/?token=abc`.
  - `kvSecretPattern` misses camelCase (`accessToken=...`, `refreshToken=...`), hyphenated (`x-api-key=...`), compound (`auth_token=...`), and quoted values with spaces (`password="my secret"` leaves `secret"` behind).
- **Required Fix:**
  - Split sanitization into dedicated, format-aware helpers: case-insensitive URL scanner + `SanitizeURL` (including IPv6 zones & percent-encoded paths), authorization header parser, format-aware key-value parser (handling quoted values, camelCase, snake_case, and hyphenated keys), and path sanitizer.
  - Enforce fail-closed behavior when format is ambiguous.

### P0-3: Global Redaction Coverage Gap
- **Locations:** `internal/core/middleware.go` (`ctx.Args`), `internal/core/executor.go`, `internal/services/inline/engine.go` (`query`, `panic`), `internal/services/userlog/service.go`, `plugins/userlog/userlog.go`, and remaining direct Zap callers.
- **Required Fix:**
  - Do **not** claim global redaction until all sensitive sinks pass through the policy.
  - Complete a boundary audit (R2) documenting every remaining direct Zap / `UserLog` sink, and immediately close high-risk P0 leaks (`ctx.Args` in `LoggingMiddleware`, raw `query` in `inline.Engine`, and raw content/error in `UserLog` or explicitly scope/gate them).

### P1-1: Error Classification Relies on Substring Matching
- **Location:** `internal/logging/errors.go`
- **Issue:** `ClassifyError` inspects `strings.ToLower(err.Error())` for `"telegram"`, `"timeout"`, `"sqlite"`, etc., and always marks `context.DeadlineExceeded` as `retryable=true`.
- **Required Fix:**
  - Prioritize typed/sentinel errors (`errors.Is` / `errors.As`) and a clean classifier interface (`interface{ ErrorCategory() ... }` or `execution.Semantics` / `core.ErrorCategory` adapters) without violating `internal/architecture` tiers.
  - Keep `CategoryInternal` as fallback; distinguish `retryable` (error property) from `retry_decision` (component action).
  - Do not assume `context.DeadlineExceeded` is universally retryable.

### P1-2: Duplicate Supervisor Panic Events
- **Locations:** `internal/runtime/supervisor.go`, `internal/app/app.go`
- **Issue:** `Supervisor.invokeWorker` logs `s.logger.Error("supervisor worker panic recovered", ...)` AND calls `s.reporter.ReportPanic(...)`, while `App` configures both `WithSupervisorLogger` and `WithPanicReporter(safeRuntimePanicReporter{...})`, emitting two near-identical operational log events per panic.
- **Required Fix:**
  - Assign a single owner for operational panic logging (`Supervisor` logs via `s.logger.Error`), and reserve `PanicReporter` for distinct non-logger consumers (or omit `safeRuntimePanicReporter` when `Supervisor` already has `opLogger`).
  - Add a test asserting **exactly one** operational log event per panic.

### P1-3: Shutdown `Sync()` Runs Even After Producer Join Timeout
- **Locations:** `internal/app/shutdown.go`, `internal/runtime/supervisor.go`
- **Issue:** If `transportDone` hits `timer.C` (`transportJoinTimeout`) or `Supervisor.ForceStop` times out while a worker is still running, `performShutdown()` still calls `syncAppLogger()` and proceeds as if producers stopped.
- **Required Fix:**
  - Track explicit producer join state (`stoppedCleanly` vs `timedOutActiveProducers`).
  - If any producer failed to stop within its deadline, record the state clearly, avoid claiming clean producer quiescence, report via sanitized stderr fallback if needed, and ensure `Sync()` behavior is consistent with active-producer risk.
  - Add regression tests for transport join timeout and uncooperative `Supervisor` workers.

### P1-4: `zaplog.Adapter.With()` Lacks Panic Recovery
- **Location:** `internal/logging/zaplog/adapter.go`
- **Issue:** `log()` has `defer func() { _ = recover() }()`, but `With()` calls `toZapFields(fields)` (which invokes `err.Error()` for `KindError`) without panic recovery.
- **Required Fix:**
  - Guard `With()` (and `RedactError`) with panic recovery so an error whose `Error()` panics never crashes callers.
  - Add a unit test with a panicking `error` implementation passed to `With()` and `Error()`.

### P1-5: `WithCorrelationID` Input Validation & Entropy Failure Handling
- **Location:** `internal/logging/context.go`
- **Issue:** `WithCorrelationID(ctx, id)` accepts arbitrary strings (including newlines/control characters or unbounded length), and `NewCorrelationID()` ignores `crypto/rand.Read` errors.
- **Required Fix:**
  - Validate caller-supplied IDs (max length e.g. 64 chars, allowed charset `[A-Za-z0-9._-]`, reject newlines/control chars); if invalid, replace with a freshly generated ID.
  - Handle `crypto/rand.Read` error explicitly (e.g., atomic counter + monotonic timestamp fallback rather than silent zero bytes).

### P2-1 – P2-5: Maintainability, Panic Summary Policy, Stderr Redaction & Benchmarks
- **P2-1:** Keep raw `*zap.Logger` strictly as a temporary L4–L6 bridge; target removal from `App`.
- **P2-2:** Sanitize error messages in `initAppLoggerWith` and `syncAppLogger` stderr fallback via `logging.RedactString`.
- **P2-3:** Add `testing.B` benchmarks for `RedactString` (plain & with URLs), `RedactField`, and `zaplog.Adapter.Info`/`Error` (`ns/op`, `B/op`, `allocs/op`).
- **P2-4:** Harden `RedactPanic` so raw panic `string`/`error` payloads are **not** emitted by default (record `panic_type` + bounded/redacted stack trace; emit summary only for explicitly safe/classified metadata).
- **P2-5:** Close all test gaps (IPv6 zone IDs, percent-encoded paths, uppercase schemes, `With()` panic, duplicate panic count, producer timeout shutdown, encoder/sampling/live-reload verification).

---

## 3. Remediation Roadmap (R1 – R6)

| Step | Priority | Scope | Verification |
| --- | --- | --- | --- |
| **R1** | **P0** | `internal/logging/redactor.go`, `logger.go`, `zaplog/adapter.go` (fail-closed `FieldKind`, format-aware `RedactString`, IPv6/encoded URLs, `RedactPanic` hardening) | Adversarial struct-literal, URL, and credential tests |
| **R2** | **P0** | Audit & close remaining direct-Zap / `UserLog` leaks (`ctx.Args` in `core/middleware.go`, `inline` query, `UserLog` sanitization policy) | Middleware, inline, and UserLog security tests |
| **R3** | **P1** | Typed `ClassifyError`, single-owner Supervisor panic logging, `With()` & `RedactError` panic isolation | Error matrix, duplicate-panic count, and panicking-error tests |
| **R4** | **P1** | `CorrelationID` validation & `rand.Read` fallback in `internal/logging/context.go` | Control-char, length, and entropy fallback tests |
| **R5** | **P1** | Shutdown producer-join state tracking (`transportDone` & `Supervisor` timeout) + sanitized `stderr` fallback | Transport timeout & uncooperative worker shutdown tests |
| **R6** | **P2** | Benchmarks (`Redactor`, `zaplog.Adapter`), encoder/sampling/live-reload tests, full `go test -race`, `architecture`, `vet`, `build` | Benchmarks + full local suite |

---

## 4. Closure Criteria (Gate to Unhold L4)

- [ ] Zero `FieldKind` bypasses; unknown kinds fail-closed.
- [ ] Format-aware `RedactString` handles camelCase, hyphenated, quoted, uppercase URL, IPv6 zone, and encoded inputs.
- [ ] All P0 direct-Zap / `UserLog` data leaks documented and closed or explicitly policy-enforced.
- [ ] `ClassifyError` uses typed/sentinel classification as primary truth.
- [ ] Exactly one operational log event per Supervisor panic.
- [ ] `Adapter.With()` and all public logger methods are panic-isolated.
- [ ] `CorrelationID` rejects invalid/control characters and bounds length.
- [ ] Shutdown accurately distinguishes clean producer stop from timed-out active producers before `Sync()`.
- [ ] Benchmarks, `go test -race`, `internal/architecture`, `go vet`, `gofmt`, and `go build ./cmd/goultroid` all pass.
