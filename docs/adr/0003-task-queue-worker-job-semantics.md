# ADR 0003: Task, Queue, Worker, and Job Semantics

- Status: Accepted
- Date: 2026-09-09
- Source: Blueprint §28–37; roadmap Phases 4–5

## Context

Service-local goroutines and the current scheduler do not supply a unified
model for bounded pressure, ownership, cancellation, quota, retry, or
diagnostics. Heavy media/download work must not starve commands. Scheduled work
must not bypass execution safeguards.

## Decision

Use four separate concepts:

| Concept | Meaning | State |
|---|---|---|
| Job | declarative work and schedule | optionally persistent |
| Scheduler | when a job triggers | schedule/runtime |
| Task | one concrete execution | runtime/execution record |
| Queue/Worker | buffer pressure/concurrency | runtime |

Scheduler never invokes a feature handler directly. Job Manager creates a
Task; Task Manager applies owner, timeout, retry, idempotency, and quota; a
named bounded queue feeds a worker pool. Initial pools: `event`, `general`,
`download`, and `media-process`. Unlimited queues and a single global worker
pool are prohibited.

Each Task has ID, owner, name, priority, timeout, retry policy, idempotency
key, correlation ID, timestamps, state, and terminal error. Queue policy is
explicit (`reject`, context-bound block, drop, coalesce, or priority) and
observable. Plugin scope cancellation removes queued work and cancels owned
running contexts. Initial quota covers queued and concurrent tasks.

Persistent Jobs store data, schedule, recovery policy, and execution identity;
never a function pointer. Restart policy is `run_immediately`, `skip`, or
`recalculate`, protected by execution idempotency/lease.

## Consequences

- Backpressure and heavy-work isolation become measurable.
- Task/job metadata adds ceremony but replaces ad-hoc goroutines.
- Short bounded work may stay synchronous; the threshold is documented per API.
- Persistent scheduler migration needs fresh/upgraded/restart coverage.

## Alternatives Rejected

- One global worker pool: media can exhaust command capacity.
- Feature-owned channels/workers: no shared lifecycle, quota, or diagnostics.
- Scheduler callback execution: bypasses queue, timeout, retry, ownership.

## Rollout and Acceptance

Build primitives and a fake-clock harness; pilot downloader/media; migrate OCR,
process, and heavy work; then place Job Manager over existing scheduler and
migrate persistent jobs. Tests must prove bounded depth, recorded overload,
command availability during download saturation, owned task cancellation, and
restart without duplicate visible job effects.
