# ADR 0005: Storage, Migration, and Namespace Compatibility

- Status: Accepted
- Date: 2026-09-09
- Source: Blueprint §38–40; roadmap Phases 5, 6, and 10

## Context

SQLite currently has historical integer migrations and namespaced feature
migrations. Most plugin persistence is feature-owned, but `addon.go`,
`scheduler.go`, `settings.go`, and `userlog.go` remain feature persistence in
`internal/database`. Cosmetic moves risk duplicate schema execution or broken
upgrades. Blueprint requires generic storage, feature-owned repositories,
namespaces, and explicit transaction ownership.

## Decision

Generic `internal/database` (eventually `internal/platform/storage`) owns DB
connection, transaction primitives, migration execution, health, and generic
queries only. A feature/service owns its repository interface, implementation,
schema declaration, and persistence policy. Plugin/service namespaces (for
example `plugins.afk`, `services.settings`) are ownership/API boundaries, not
separate databases; cross-namespace access requires a service contract.

Historic integer migration IDs are immutable. New feature migrations use
namespaced IDs such as `settings.001` and declare legacy versions they adopt.
Adoption is recorded as metadata; equivalent SQL is never run twice.

Transaction boundary belongs to application/service use cases. Repositories do
not begin unrelated nested transactions. A mutation plus durable event/job/outbox
record commits atomically whenever consistency requires it.

Schema evolution is additive: add -> dual-read/backfill -> dual-write if needed
-> verify upgrades -> remove obsolete representation in a later release. No
historic rewrite or destructive removal occurs in the release introducing a
replacement path without explicit incompatible-release approval and rollback.

## Consequences

- Fresh and upgraded installations follow one verifiable migration history.
- Generic database API stops accumulating feature policy.
- Every migration requires clean, upgraded, and interruption/rollback tests.
- Repository migration needs temporary compatibility adapters and deliberate
  transaction design.

## Alternatives Rejected

- Keep all feature repositories on `database.DB`: preserves boundary debt.
- Rewrite historic migration history: breaks installed databases.
- One DB per plugin: unnecessary operational and transactional complexity.
- Move files but retain direct SQL concrete access: no actual abstraction.

## Rollout and Acceptance

Build migration adoption metadata and a fresh/upgrade/interruption test harness.
Move the four remaining legacy surfaces one at a time, then add generic job,
idempotency, audit, and resource tables only through platform storage. Tighten
the legacy allowlist to zero only after callers migrate. Tests must prove fresh
and supported old databases reach identical behaviour, migrations cannot apply
twice, transactions have a named owner/rollback test, and generic database no
longer contains feature persistence at completion.
