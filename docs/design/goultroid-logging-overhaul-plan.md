# Goultroid Logging Architecture Overhaul — Master Plan

- **Status:** Active (L0 Inventory Complete; Ready for L1 Design)
- **Baseline Branch:** `test-next` (`2b09316d18f92403f95bdbb020011d64d6174a4b`)
- **Companion Inventory:** [goultroid-logging-l0-inventory.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l0-inventory.md)

---

## 1. Core Principle & Goals

> **Logging must be part of Goultroid's core infrastructure, without becoming a burden on the primary execution path.**

Logging must never slow down command execution, message dispatching, Telegram RPCs, job execution, or plugins. At the same time, it must provide consistent operational observability, strict secret/PII protection, and reliable user activity auditing.

### Non-Negotiable Architectural Constraints

1. **No second logging runtime:** Retain Zap for operational logging and `UserLog` for Telegram userbot activity notifications.
2. **No logging-dedicated EventBus:** Reuse existing `core.EventBus` for domain events that UserLog already subscribes to; do not route operational logs through an event bus.
3. **No extra TaskEngine or worker pools for operational logging:** Operational logging writes synchronously/buffered via Zap cores without spawning new worker subsystems.
4. **No unbounded queues:** Any async buffering (such as UserLog delivery) must remain strictly bounded with explicit drop/backpressure accounting.
5. **No Telegram dependency for operational logging:** Operational logging must function identically when Telegram is disconnected, degraded, or unconfigured.
6. **Logging failures never fail commands or jobs:** Logger/UserLog errors or panics must never propagate to command/job callers.
7. **Pre-sink redaction:** Secrets, tokens, credentials, OTPs, MSISDNs, and raw message bodies must be redacted/classified *before* reaching any output sink (terminal, structured log, resource metadata, or UserLog).

---

## 2. Target Conceptual Architecture

```text
+-------------------------------------------------------------------------+
|                         Goultroid Application                           |
|   Core · Runtime · TaskEngine · Jobs · Scheduler · Telegram · Plugins   |
+------------------------------------+------------------------------------+
                                     |
                                     v
+-------------------------------------------------------------------------+
|               Logging Facade / Structured Event Contract                |
|  Consistent fields · Event classification · Context · Correlation ID    |
+------------------------------------+------------------------------------+
                                     |
                                     v
+-------------------------------------------------------------------------+
|                          Shared Logging Policy                          |
|  Redaction (secrets, tokens, PII, URLs) · Classification · Cardinality  |
+------------------+-----------------------------------+------------------+
                   |                                   |
                   v                                   v
+----------------------------------+  +-----------------------------------+
|       Operational Logger         |  |         User Activity Log         |
|              (Zap)               |  |             (UserLog)             |
| Terminal, structured JSON/console|  | Telegram notifications, audit,    |
| diagnostics, lifecycle, panics   |  | media metadata, delivery metrics  |
+----------------------------------+  +-----------------------------------+
```

### Standard Event Schema & Error Classification

Every structured operational log event should carry consistent, low-cardinality context where applicable:
- `component` (e.g., `dispatcher`, `executor`, `scheduler`, `jobs`, `downloader`, `myxl`)
- `operation` (e.g., `dispatch`, `execute`, `recover`, `download`, `notify`)
- `event` (stable snake_case event identifier, e.g., `command_failed`, `download_failed`)
- `correlation_id` (bounded request/job correlation ID)
- `error_category` (classified category rather than relying solely on raw error strings)
- `retryable` (boolean when relevant)

Standard error categories:
`validation`, `authorization`, `telegram_rpc`, `network`, `persistence`, `resource`, `internal`, `cancellation`.

Event classes (to avoid logging noise):
- Operational
- Security
- User Activity
- Lifecycle
- Performance
- Diagnostic

---

## 3. Work Breakdown (L0 – L8)

| ID | Phase / Area | Focus & Deliverables | Gate |
| --- | --- | --- | --- |
| **L0** | **Logging Inventory** | Full codebase audit of Zap, `rt.Logger`, `log`, `fmt.Print*`, UserLog, ResourceManager, panic/error handling, and overhead. | [goultroid-logging-l0-inventory.md](file:///home/dhimas/any/random/ultroid-go/docs/design/goultroid-logging-l0-inventory.md) |
| **L1** | **Logging Core (Design & Contract)** | Define logging facade/package boundary (respecting `internal/architecture` tiers), standard field schema, event types, error classification, and ownership. | Design ADR + core package interfaces & unit tests |
| **L2** | **Security & Redaction** | Centralized sanitization/redaction policy (args, URLs, tokens, OTPs, MSISDNs, inline queries, panic values/stacks, ResourceManager metadata). | Policy implementation + security regression tests |
| **L3** | **Runtime Integration** | Startup/shutdown logger config (prod vs dev), `Sync()` semantics, panic reporters (`core.PanicReport`, `runtime.PanicReport`), `internal/runtime` & `internal/supervisor` observability. | Lifecycle + panic recovery tests |
| **L4** | **Execution Integration** | `TaskEngine`, `JobsManager`, `Scheduler`, `RPCExecutor` (gotd logger & RPC diagnostics without per-RPC noise). | Race suite + execution diagnostics tests |
| **L5** | **Telegram Integration** | `Dispatcher`, `core.CommandExecutor`, `core.LoggingMiddleware` (eliminate double-logging & raw `ctx.Args`), `Assistant` client/router, `Inline` engine (remove raw `query`). | Dispatcher & middleware tests |
| **L6** | **Plugin Integration** | Migrate plugins (`myxl`, `downloader`, `afk`, `settings`, `admin`, etc.) from raw `*zap.Logger` / `rt.Logger` to the new facade/contract. | Plugin tests + `featuregen` check |
| **L7** | **UserLog Rewrite** | Unified UserLog event model, pre-delivery sanitization, media metadata support, accurate delivery/drop/skip metrics, clean bounded shutdown. | UserLog service & plugin race tests |
| **L8** | **Diagnostics & Performance** | Zap sampling/level policy, cardinality bounds, allocation & lock contention benchmarks, failure injection, `sysinfo` diagnostics integration. | `go test -race ./...` + benchmarks |

---

## 4. Success Criteria

1. **Single logging standard** across `cmd/`, `internal/`, and `plugins/`.
2. **Zero credential or PII leaks** in operational logs, diagnostics, `ResourceManager`, or `UserLog`.
3. **End-to-end traceability** via `correlation_id`, `component`, `operation`, and `error_category`.
4. **Decoupled plugins:** Plugins depend on the logging contract/facade, not raw Zap configuration or internal logger plumbing.
5. **Failure isolation:** Logging or UserLog failures never fail commands, jobs, or message dispatch.
6. **Non-blocking UserLog:** Bounded queue, non-blocking enqueue, accurate delivery/drop/skip metrics, and fast cancellation on shutdown.
7. **Actionable diagnostics:** Runtime health explainable from bounded structured metrics without grepping thousands of raw log lines.
8. **Low overhead:** Minimal allocations and lock contention both when idle and under burst load.

---

## 5. Working Rules

- Refresh exact GitHub HEAD `test-next` before starting each phase.
- Do not check CI unless explicitly requested.
- Always run `gofmt` on modified Go files and run `go test -race ./...` when logic or APIs change.
- Update tests whenever behavior or contracts change.
- Keep `internal/architecture/imports_test.go` tier boundaries intact (`database`/`core` -> Tier 1, `services` -> Tier 2, `plugins` -> Tier 3, `app` -> Tier 4).
