# Execution runtime baseline and compatibility checkpoint

Date: 14 September 2026.

This document freezes the source/semantic baseline used by the execution-runtime rework. It is an implementation artifact for P0 of `03-implementation-plan.md`; it does not change the target architecture in ADR 0006.

## Source checkpoint

- Rework source branch: `test`.
- Frozen source commit: `7722d56e57d944357de04f8bebff8101d40c0790` (`redesign docs`).
- Merge-base/default-main checkpoint observed when implementation started: `37cfbf45622bc201745552c0deb030f1834008ab`.
- `test` is ahead of that main checkpoint and contains the execution-admission/periodic fixes that ADR 0006 says must be preserved.
- Implementation branch: `rework/execution-runtime`.
- Module toolchain contract: Go version from `go.mod` (currently Go 1.27).
- Repository CI contract: `gofmt`, `go vet ./...`, generated-module freshness, golangci-lint, `go test -v -race ./...`, and `go build -v ./cmd/goultroid`.

The current CI workflow is triggered only for pushes or pull requests targeting `main`/`master`. A push to the rework branch alone therefore is not evidence that the full CI suite ran.

## Baseline worker pools

The legacy `workers.Manager` provisions the following compatibility pools:

| Pool | Workers | Physical queue | Queue policy |
| --- | ---: | ---: | --- |
| `general` | 8 | 200 | block |
| `interactive` | 32 | 128 | reject |
| `download` | 3 | 50 | reject |
| `media-process` | 2 | 20 | reject |
| `scheduler` | 4 | 100 | block |

`PoolInteractive` was referenced by `NewManager` but had no declaration in the source snapshot. That is a pre-existing compile blocker, not a redesign decision. P0 restores the stable identifier as `interactive` and adds a regression test that the pool is provisioned.

These queue sizes are baseline behavior only. ADR 0006 replaces the logical/physical double-backlog model with TaskEngine waiting queues plus one-assignment physical worker slots; the numbers above must not be copied blindly into the new config.

## Execution surface inventory

The following ownership surfaces are in the migration set and must have an explicit cutover or retirement condition:

| Surface | Current execution ownership | Required migration rule |
| --- | --- | --- |
| `workers.Manager.Submit` / non-blocking admission | WorkerManager + TaskManager + physical Pool | Adapter to TaskEngine, then remove logical admission ownership from WorkerManager |
| physical `Pool.Submit` | Pool queue | No feature producer may call it after P2 cutover; physical handoff requires a concrete permit |
| `jobs.Manager` trigger/wait/completion | JobManager plus legacy task/worker coupling | Trigger creates an Occurrence; waits key by occurrence/attempt handle, never only JobID |
| durable scheduler | Scheduler currently claims DB rows, reserves capacity, runs actions/jobs, retries, and completes persistence | Scheduler becomes deadline/reference producer only; JobManager owns occurrence/attempt protocol |
| periodic coordinator | Independent timer/retry state feeding workers | Becomes in-memory JobDefinition/JobSchedule using shared JobManager transitions |
| Telegram command dispatch | Dispatcher/direct handler paths plus `cmdSem` | Keep cheap auth/permission/idempotency gates synchronous; finite feature execution enters TaskEngine |
| callback router / inline engine | Called directly from Telegram update path | Bounded TaskEngine execution with acknowledgement/deadline/stale-result policy |
| plugin `Tasks()` / `Jobs()` / scheduler access | Raw manager pointers | Scoped clients that inject ScopeIdentity, generation, quota identity, allowed pool/class |
| plugin `Scope.Go` | Scope-owned goroutine | Retain for supervised/long-lived service loops only; finite feature work migrates to TaskEngine |
| EventBus workers / transport loops / persistence pumps | Supervised services | Remain outside finite Task workers; lifecycle dependencies are explicit |

This inventory is intentionally ownership-oriented rather than folder-oriented. Every production caller discovered during P2-P6 must be added here before its adapter is removed.

## Compatibility contract to preserve

During migration, each surface must preserve or deliberately version these externally visible semantics:

- authentication and permission decision timing;
- command/callback/inline output and acknowledgement behavior;
- ordering guarantees and ordering-key scope;
- accepted-vs-completed feedback;
- queue/admission lifetime and overload feedback;
- cancellation visibility and no hidden execution after rejected/cancelled admission;
- retry identity (`OccurrenceID` stable, new `AttemptID` and `TaskID` per retry);
- durable history and claim fencing;
- recurring interval/misfire/overlap behavior;
- plugin disable/reload generation fencing;
- shutdown behavior for accepted, running, never-started, commit-pending, and recovery-required work.

## Mandatory regression set

The following tests from the frozen branch protect behavior that must survive the redesign and are mandatory whenever the corresponding subsystem is changed:

- `internal/workers/admission_regression_test.go`
- `internal/workers/workers_test.go`
- `internal/tasks/completion_test.go`
- `internal/tasks/lifecycle_physical_test.go`
- `internal/queue/notification_test.go`
- `internal/jobs/completion_test.go`
- `internal/scheduler/admission_regression_test.go`
- `internal/scheduler/managed_job_async_test.go`
- `internal/scheduler/periodic_timer_test.go`
- `internal/scheduler/periodic_workers_integration_test.go`
- `internal/scheduler/repository_concurrency_test.go`

New model/invariant tests do not replace this set until the related legacy execution path is removed.

## P0 measurement status

No performance number is recorded without a reproducible run. B0-B8 benchmark results are therefore intentionally absent from this checkpoint until they are executed against the exact frozen source/toolchain. The redesign must not claim an idle, latency, throughput, goroutine, allocation, or memory improvement merely from code inspection.

The source/semantic baseline is frozen by this artifact; the quantitative benchmark gate remains open and must close before production cutover/canary decisions.
