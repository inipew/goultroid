# ADR 0002: Plugin Scope and Resource Ownership

- Status: Accepted
- Date: 2026-09-09
- Source: Blueprint §13–22; roadmap Phase 2

## Context

Current plugin lifecycle manages commands and hooks but cannot prove that every
subscription, job, task, goroutine, temporary file, connection, or process is
stopped on plugin disable. Blueprint requires owner, identity, cancellation,
cleanup, state, and diagnostics for all long-lived resources.

## Decision

Each enabled plugin receives one `resource.Scope`, owned by Plugin Manager and
identified by stable owner `plugin:<id>`. Plugins receive a scoped plugin
context, never global Runtime.

```go
type Scope interface {
    Context() context.Context
    Owner() Owner
    Go(name string, fn func(context.Context)) error
    Defer(name string, cleanup func(context.Context) error)
    Track(Resource) (ReleaseFunc, error)
    Close(context.Context) error
}
```

Resource registry records ID, owner, type, time, state, sanitized metadata,
cleanup/cancellation callback, and terminal error. Scope close is idempotent
and bounded: stop intake; unregister command/hook; cancel scope; cancel
tasks/jobs; unsubscribe; close process/network work; run LIFO cleanup; wait;
verify residual resources. Leaks remain in diagnostics and trigger configured
degraded/failed policy; they are never silently discarded.

## Consequences

- Plugin enable/disable becomes a real resource boundary and supports quota.
- New long-lived feature work must use scope `Go`, `Track`, or managed services.
- Legacy plugins need adapters and can expose hidden leaks during migration.
- Cleanup must be cancellation-aware; Runtime must retain dependencies until
  scopes finish or shutdown deadline is reached.

## Alternatives Rejected

- `Plugin.Shutdown()` alone: lacks visibility and owner-enforced cleanup.
- A global runtime passed to plugins: permits cross-plugin interference.
- Scope per command only: misses background and persistent plugin resources.

## Rollout and Acceptance

Track commands/hooks first, then events, callbacks, scheduler jobs, tasks,
goroutines, processes, connections, and temp files. A disabled test plugin must
leave zero owned registrations/resources; a hanging cleanup must not hang
shutdown; diagnostics must identify owner/type/age/state; Plugin A cannot
cancel Plugin B resources.
