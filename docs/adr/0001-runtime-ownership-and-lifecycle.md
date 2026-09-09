# ADR 0001: Runtime Ownership and Lifecycle

- Status: Accepted
- Date: 2026-09-09
- Source: Blueprint §8–11; roadmap Phase 1

## Context

`internal/app.App` already coordinates lifecycle, but long-lived components
(DB, Telegram, event bus, scheduler, workers, addons, plugins) remain separate
objects. Manual lifecycle ordering makes rollback, extension, health, and
shutdown correctness difficult. Plugins must never own global lifecycle.

## Decision

Introduce `internal/runtime.Runtime` as the sole lifecycle owner. `App` remains
the composition root and a compatibility façade during migration. Runtime owns
a root context, observable state, component registry, dependency graph, and
health aggregate.

```go
type Component interface {
    Name() string
    Dependencies() []string
    Start(context.Context) error
    Stop(context.Context) error
    Health(context.Context) ComponentHealth
}
```

Runtime validates a DAG before startup, starts deterministically in dependency
order, and stops successful components in reverse order. States are `created`,
`initializing`, `starting`, `running`, `stopping`, `stopped`, and `failed`.
`Start` is single-use; `Stop` is idempotent and concurrent callers wait for the
first shutdown subject to their context. Startup failure rolls back only started
components in reverse dependency order.

Ingress stops before consumers and infrastructure: plugin intake -> scheduled
triggers -> queues/workers -> plugin scopes -> Telegram/network/process ->
storage -> logger/diagnostics. Exact order comes from component dependencies,
not scattered manual lists.

## Consequences

- One state and one rollback/shutdown implementation are observable and testable.
- Components must state dependencies and critical/optional failure policy.
- Existing `App.Run`/`Shutdown`, SQLite schema, commands, and plugin API remain
  compatible while adapters are introduced.
- Component adapter work is required; global singleton Runtime is forbidden.

## Alternatives Rejected

- Keep `App` as unstructured lifecycle owner: does not scale to new runtime
  subsystems.
- Global Runtime singleton: prevents isolated tests and safe ownership.
- Big-bang rewrite: unacceptable Telegram/database regression risk.

## Rollout and Acceptance

Add state and graph tests, wrap existing components without behavioural change,
delegate App lifecycle, then remove legacy paths only after migration. Tests
must reject cycles/missing dependencies, prove concurrent start/stop semantics,
rollback on injected failure, and prove DB never closes before consumers stop.
