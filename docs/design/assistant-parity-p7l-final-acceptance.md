# Assistant Parity P7-L — final group-plane acceptance

## Status

**P7-L is CLOSED for implementation/source acceptance. P7-A through P7-L are therefore CLOSED as the Assistant group-plane implementation phase.**

The final re-audit closed every source residual identified against `docs/bug/4.md`:

- delayed media-filter persistence now revalidates Telegram admin authority immediately before commit;
- bot-right downgrade after admission has an explicit acceptance regression;
- high-cardinality cold traffic has process-level goroutine/heap/RSS acceptance coverage;
- TaskEngine runtime pool bounds in P7-K documentation match the actual runtime configuration;
- repository Go sources were normalized with `gofmt`;
- focused P7 acceptance and benchmark commands are wired into the standard CI workflow as continuing verification.

Runtime CI results are intentionally not used as the phase-closure blocker in this pass. The operator requested source fixes and closure first. The repository still retains both `tools/accept-p7l.sh` and the CI P7 acceptance job for post-closure verification.

This document does not invent PASS results for a CI run that was not inspected as part of this closure.

## Scope

P7-L validates the complete Assistant group plane assembled by P7-A through P7-K:

```text
Telegram group update
    ↓
surface / feature-interest classification
    ↓
canonical group context
    ↓
global invocation policy + contextual Telegram role
    ↓
shared TaskEngine admission
    ↓
fresh authorization / target / bot-rights validation
    ↓
shared managed Telegram RPC
    ↓
topic-preserving response / mutation
```

No separate manager runtime, mutation scheduler, per-topic worker, or Assistant-owned RPC executor is accepted.

## Acceptance finding fixed during P7-L

P7-L found one real scheduler gap that was not visible from the earlier topic unit tests.

After P7-K consolidated group tasks onto:

```text
QuotaOwner = telegram:chat:<chat_id>
```

all topics in a chat correctly shared one bounded quota, but the admission controller selected only the FIFO head of each owner queue.

The problematic sequence was:

```text
topic A / task A1 -> running, ordering key A locked
topic A / task A2 -> queue head, blocked by key A
topic B / task B1 -> queued behind A2
```

Even though topic B had an independent ordering key, B1 could not pass A2. This created cross-topic head-of-line blocking.

P7-L changes owner-queue selection to choose the **earliest eligible entry** from the already bounded owner queue.

Important properties remain:

- one chat quota owner;
- one shared TaskEngine;
- same ordering key still serializes strictly;
- different topic ordering keys can make progress independently;
- no per-topic queue or goroutine is created;
- scanning is bounded by the owner waiting quota;
- TaskID removal and deadline accounting remain exact.

Regression coverage:

`TestP7LAdmissionIndependentTopicsDoNotHeadOfLineBlock`.

## Final behavior matrix

### Surface classification

Executable acceptance:

`TestP7LAssistantGroupSurfaceMatrix`

Expected contract:

| Surface | Group-only manager feature |
| --- | --- |
| private user dialog | reject |
| basic group | accept |
| supergroup | accept |
| broadcast channel | reject |

A bare/known broadcast `PeerChannel` must never inherit manager-group authority.

### Global privilege vs Telegram contextual authority

Executable acceptance:

`TestP7LGlobalPrivilegeDoesNotReplaceTelegramGroupRole`

Matrix:

| Global identity | Telegram role | Contextual admin command |
| --- | --- | --- |
| Owner | Member | deny |
| Sudo | Member | deny |
| ordinary user | Administrator + required right | allow |
| ordinary user | Member | deny |

Owner/Sudo remains global Goultroid policy. It does not manufacture Telegram group administrator rights.

### Role changed while queued

Existing executable acceptance:

- `TestP7CContextualAuthorizationCachedPreflightThenFreshAfterAdmission`
- `TestP7CFreshRevalidationRejectsRoleDowngradeAfterAdmission`

Cached role may be used only as preflight optimization. Fresh role validation happens after TaskEngine admission and can veto execution.

### Bot rights / actor rights / hierarchy at mutation time

Executable acceptance now includes the exact post-admission bot-right downgrade race:

- `TestP7LBotRightsChangedAfterAdmissionPreventPhysicalMutation`

Existing P7-G acceptance:

- `TestP7GSupergroupBanRevalidatesTargetBotActorBeforeRPC`
- `TestP7GBotRightsFailurePreventsPhysicalMutation`
- `TestP7GActorRightsFailurePreventsPhysicalMutation`
- `TestP7GProtectsCreatorAndHigherAdminTargets`
- `TestP7GPurgeIsTopicAwareBoundedAndRevalidatesDeleteRPC`

The mutation executor checks bot rights first and actor/target contextual state immediately before the physical mutation RPC. Therefore rights missing at execution time suppress the mutation even if earlier command admission succeeded.

### Task rejection / backpressure

New P7-L acceptance:

- `TestP7LTaskAdmissionRejectionNeverExecutesGroupHandler`
- `TestP7LGroupRuleAdmissionRejectionStopsBeforeExecution`

An owner/pool admission rejection does not run:

- command handler;
- group-rule peer resolution;
- group classifier;
- rule handler;
- mutation path.

Backpressure remains a TaskEngine concern instead of spawning fallback goroutines.

### Verification outage

Existing acceptance:

- `TestTelegramRoleResolverVerificationFailureIsNotCached`
- `TestTelegramRoleResolverSaturationFailsWithoutRPC`
- P7-I verification-failure safe-bypass tests.

A Telegram verification outage is not cached as authority. Local verification saturation fails fast without issuing additional RPCs.

### Restart / durable state

Existing acceptance:

- `TestSQLiteStoreCASRestartAndChatIsolation`
- `TestP7HRestartPreloadRestoresInterestAndSubscription`

Group configuration survives process/service recreation. Chat coordinates remain isolated. Active event interest is reconstructed from durable state.

### Stale cached role

Existing acceptance:

`TestTelegramRoleResolverFreshBypassesCachedRole`

Fresh resolution bypasses stale cached role observations and refreshes the bounded cache.

## Media-filter delayed persistence

P7-L re-audit found and closed a cross-task authorization window in P7-I media filters.

The original sequence was:

```text
fresh admin authorization in command task
    ↓
media-capture continuation queued
    ↓
actor can lose admin role
    ↓
captured response persisted
```

The continuation now carries a lightweight fresh-auth guard captured from the admitted Assistant group context. Immediately before durable `SaveFilter` replacement it calls the shared `GroupRoleResolver.ResolveGroupRoleFresh` and re-applies the Administrator requirement. Verification failure or role downgrade aborts the write; captured media is reclaimed.

Regression:

`TestP7LMediaFilterContinuationRevalidatesAuthorityBeforePersistence`

## Topic/thread isolation

P7-L accepts four separate properties.

### 1. Context correctness

Existing P7-J tests reject:

- linked-chat replies before reply RPC;
- cross-topic reply targets;
- topic response fallback without contextual transport;
- topic media fallback without contextual transport.

### 2. Ordering identity

Group work uses:

```text
chat:<chat_id>
chat:<chat_id>:topic:<topic_id>
```

No historical topic registry is retained.

### 3. Aggregate quota

New acceptance:

`TestP7LHighCardinalityTopicsShareChatQuotaButKeepOrderingIdentity`

512 distinct topics/senders in one group retain distinct transient ordering identities while sharing:

```text
telegram:chat:<chat_id>
```

as the quota owner.

### 4. Scheduler independence

New acceptance:

`TestP7LAdmissionIndependentTopicsDoNotHeadOfLineBlock`

A blocked second task in topic A no longer prevents an eligible topic B task behind it from dispatching.

## High-cardinality acceptance

### Chat/topic TaskEngine quota

New acceptance:

`TestP7LChatQuotaBoundsHighCardinalityTopics`

With the default owner policy, many topic IDs cannot multiply capacity:

```text
MaxWaiting = 50 per chat owner
MaxActive  = 10 per chat owner
```

The 51st waiting topic is rejected even though its ordering key is unique.

### Role cache

New acceptance:

`TestP7LHighCardinalityRoleCacheRemainsBounded`

The test pushes 1024 distinct verified users through a role resolver configured to 128 hot entries and verifies:

- cache remains exactly bounded;
- verification slots return to zero;
- no hidden waiter state accumulates.

Production capacity remains 4096.

### Irrelevant group traffic

New acceptance:

`TestP7LHighCardinalityIrrelevantGroupsStayCold`

The test sends 2048 different irrelevant group IDs through the actual Assistant update dispatcher and verifies zero:

- entity-cache work;
- peer resolver calls;
- TaskEngine submissions;
- rule handler calls.

Only the cheap feature-interest check executes.

This is the key protection against historical group/topic cardinality becoming resident runtime state.

## Idle footprint

Executable evidence already exists in the shared runtimes:

EventBus:

- `TestEventBusStartsWithZeroDispatchWorkers`
- `TestEventBusWorkersSpawnOnDemandAndRetire`
- `TestContextSubscriptionUsesCancellationWithoutWatcherWorker`

TaskEngine:

- `TestDefaultPoolsStartWithZeroPhysicalWorkers`
- `TestZeroIdlePoolSpawnsOnDemandAndRetiresToZero`
- `TestCompletionDeliveryWorkersAreLazyAndRetire`
- `TestDurabilityLaneWorkersAreLazyAndRetire`

P7 group services themselves remain free of per-chat/topic workers and periodic polling as fenced by P7-K architecture tests.

## High-cardinality process footprint

P7-L now contains a process-level cold-path stress acceptance:

`TestP7LHighCardinalityColdTrafficDoesNotAmplifyIdleProcessState`

It sends 8,192 unique irrelevant group IDs through the actual Assistant dispatcher, forces GC/OS-memory release before and after, and records:

- goroutine count;
- Go heap allocation;
- Linux RSS when `/proc/self/statm` is available.

The test also asserts the traffic never escapes into entity cache, peer resolver, TaskEngine, or group-rule execution. Goroutine/heap/RSS deltas use deliberately broad regression bounds so the test catches cardinality-driven retention without encoding machine-specific performance expectations.

The focused acceptance harness runs with `-v`, so these observations are preserved in CI logs.

## FloodWait / RPC acceptance

Assistant managed API remains an adapter over the application-owned shared `telegram.RPCExecutor`.

P7-L manifest requires existing executable cases covering:

- short inline FloodWait;
- long/deferred FloodWait;
- durable short FloodWait yield;
- cancellation during limiter wait;
- limiter hard bounds;
- bucket saturation fail-closed;
- penalty overflow fail-closed;
- high-cardinality peer bucket reclamation.

Relevant tests include:

- `TestRPCExecutor_Case9_FloodWaitBelowThreshold`
- `TestRPCExecutor_Case10_FloodWaitAboveThreshold`
- `TestRPCExecutor_DurableContextYieldsShortFloodWait`
- `TestRPCExecutor_Case6_CanceledDuringLimiterWait`
- `TestHierarchicalRPCLimiter_HardBounds`
- `TestHierarchicalRPCLimiter_BucketSaturationFailsClosedWithoutResettingLiveState`
- `TestHierarchicalRPCLimiter_PenaltyOverflowFailsClosedWithoutDroppingFloodWait`
- `TestHierarchicalRPCLimiter_HighCardinalityPeersCollapseAfterSafeRefill`

## Cancellation / shutdown

P7-L keeps the existing lifecycle acceptance:

- Assistant quiesce closes update admission;
- App owns one global shutdown deadline;
- TaskEngine stops accepting before forced finalization;
- completion/durability lanes are bounded;
- transport remains available during runtime drain and is cancelled afterwards.

TaskEngine lifecycle evidence required by the P7-L manifest:

- `TestStopDeadlineNotBlockedByCompletionCallback`
- `TestForceStopFencesCommitPending`
- `TestForceStopMarksDurableInFlightRecoveryRequired`

This matters for group mutations because shutdown must not convert an uncertain external Telegram side effect into a false “clean cancellation”.

## Hot-path benchmark

P7-L adds:

`BenchmarkP7LIrrelevantGroupMessageHotPath`

It benchmarks the actual Assistant update-dispatch path for a normal group text message when no group rule is active.

The benchmark additionally asserts that the measured path does not escape into cache, resolver, TaskEngine, or rule execution.

Run with:

```bash
go test ./internal/assistant/client   -run '^$'   -bench '^BenchmarkP7LIrrelevantGroupMessageHotPath$'   -benchmem   -count=5
```

P7-L intentionally does **not** hard-code an ns/op threshold across machines. The required evidence is the recorded `ns/op`, `B/op`, and `allocs/op`, which can be compared across future revisions on the same environment.

## Architecture manifest

`internal/architecture/assistant_p7l_test.go` acts as the final matrix manifest.

It fences:

- presence of executable acceptance tests for every P7-L matrix category;
- one shared TaskEngine execution authority;
- one application-owned Telegram RPC executor;
- topic-aware eligible scheduling;
- absence of Assistant-local TaskEngine/admission/RPC construction;
- existence of the execution harness.

## Execution harness

Run:

```bash
bash tools/accept-p7l.sh
```

The harness executes, in order:

1. `go vet ./...`
2. production binary build
3. `go test ./...`
4. race tests for group plane + TaskEngine/App lifecycle
5. focused P7 acceptance tests
6. P7-L irrelevant-group benchmark with `-benchmem -count=5`

The script prints the exact git HEAD and Go version first so evidence is attributable.

## Closure

P7-A through P7-L are closed for implementation/source acceptance.

The continuing operational verification set is:

```text
go vet
production build
full tests
race suite
focused P7 matrix
cold-path benchmark
```

Those checks remain executable through the standard CI P7 acceptance job and `tools/accept-p7l.sh`. They are post-closure verification evidence, not a reason to reopen P7 unless they reveal a concrete regression.

No unobserved CI result is claimed by this document.
