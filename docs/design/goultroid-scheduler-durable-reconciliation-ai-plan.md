# P1 — Scheduler & Durable Reconciliation
## AI implementation plan: recovery correctness, bounded catch-up, lease lifecycle, and observability

**Status:** READY FOR IMPLEMENTATION  
**Target branch:** `test-next`  
**Audit baseline HEAD:** `c54d2f590f3cf4d7877a75db8b3a067bd72dd458`  
**Audit date:** 2026-11-24  
**Scope:** Scheduler timing, durable schedule materialization, JobManager occurrence/attempt recovery, legacy scheduled-job leases, and recovery diagnostics.

> This document is the implementation contract for a future AI coding session. It records findings from the audit and defines a staged fix plan. It is not evidence that any fix has already been implemented. Before each implementation phase, refresh the remote branch, fetch/pull the latest `test-next`, record the exact HEAD, inspect the current source and tests, and reconcile this plan with intervening changes. The baseline SHA is a review anchor, never permission to overwrite newer work.

---

## 1. Executive summary

The current architecture has the right high-level ownership boundaries:

- **Scheduler** owns time-based orchestration, due-time selection, misfire policy, legacy claim lifecycle, and schedule reconciliation.
- **JobManager** owns durable logical jobs, occurrences, attempts, retry decisions, recovery, and outbox convergence.
- **TaskEngine** owns physical execution admission, fairness, resource accounting, worker lifecycle, cancellation, and task result delivery.
- **Telegram RPCExecutor** remains the shared authority for Telegram RPC rate limiting, FloodWait handling, and retry semantics.

The audit did not justify replacing these components. It found four targeted issues in the existing Scheduler/JobManager boundary:

| ID | Finding | Priority | Main risk |
|---|---|---:|---|
| P1-A | A materialized `ready` occurrence without an attempt is not included in startup/safety recovery | High | Durable work can remain stuck forever after a crash |
| P1-B | Legacy recurring catch-up advances one interval per loop iteration | Medium–High | CPU and SQLite transaction time grow with downtime |
| P1-C | Legacy scheduler claim lease can expire while the associated JobManager attempt remains valid; active reconciliation does not renew it | Medium | Reclaim churn, rejected duplicate admissions, misleading lease-expiry history |
| P1-D | JobManager recovery pass discards its returned error | Medium | Recovery failures become invisible until another pass succeeds |

The redesigned schedule path already has useful protections that must be retained: atomic occurrence materialization plus schedule advancement, occurrence-key idempotency, bounded due batches, constant-time interval advancement, and delegation of every physical attempt to TaskEngine.

The work should therefore be a small sequence of correctness fixes and regression tests, not an architectural rewrite.

---

## 2. Goals and measurable acceptance criteria

### 2.1 Goals

1. Every durable occurrence that is safe to execute again eventually converges after process restart, including the crash gap between materialization and first attempt creation.
2. Recovery never replays an operation whose external effect is unknown. Such attempts remain fenced/quarantined for explicit reconciliation.
3. Legacy recurring catch-up is constant-time with respect to the number of missed intervals.
4. A legacy scheduled-job claim remains owned while its associated occurrence is actively executing, without allowing an abandoned owner to hold a claim forever.
5. Recovery errors are observable, bounded, and distinguishable from a successful scan that found no work.
6. All scans, batches, retries, state, and diagnostics remain bounded.
7. Existing schedule timing, misfire, overlap, idempotency, cancellation, retry-budget, and compatibility semantics remain intact.
8. The final implementation has no new execution runtime or idle goroutine.

### 2.2 Acceptance criteria

- A `ready` occurrence with no attempt is recovered after restart and is submitted at most once for that occurrence at a time.
- The same occurrence ID/key is reused; recovery does not materialize a second occurrence for the same schedule slot or advance the schedule a second time.
- Repeated recovery passes are idempotent and converge.
- A ready occurrence with a missing/disabled definition or missing handler is reported as orphaned/held with a diagnosable reason; it is not silently discarded or executed through an unknown handler.
- A dispatched occurrence with an expired leased/running attempt and unknown external effect remains blocked/fenced. The ready-occurrence fix must not weaken this quarantine.
- A long legacy downtime (including a one-second interval over many days) completes catch-up with a bounded number of arithmetic operations, and preserves the intended anchored phase/SkipMissed semantics.
- Legacy lease renewal uses the existing claim token as a fencing condition. A stale owner cannot renew, complete, or fail a reclaimed claim.
- Active-claim renewal is bounded and failure-aware. If the process stops renewing, the claim expires and can be reclaimed.
- Recovery failures produce a structured log and/or bounded diagnostics counter/last-error state. A successful empty pass remains distinguishable from failure.
- No unbounded queue, goroutine, map, cache, retry loop, or scan is introduced.
- Targeted unit/integration tests, race tests, and relevant benchmarks pass locally. CI is not inspected or polled unless explicitly requested.

---

## 3. Architecture rules and hard constraints

### 3.1 Ownership rules

- Scheduler decides **when** to process schedules. It must not implement job execution, retries, or task admission itself.
- JobManager decides **what durable occurrence/attempt** exists and whether it is eligible for another attempt.
- TaskEngine remains the only physical execution/admission/fairness/resource engine.
- JobManager must continue submitting attempts through the existing TaskEngine client and existing task contracts.
- Telegram FloodWait and RPC retries stay in the shared Telegram RPCExecutor. Do not add scheduler-specific FloodWait logic.
- Outbox replay and durable maintenance remain under the existing JobManager durable coordinator.

### 3.2 Prohibited changes

Do not:

- add a second TaskEngine, JobManager, Scheduler runtime, worker pool, executor, retry engine, timer service, or goroutine-per-occurrence path;
- bypass TaskEngine by invoking a job handler directly during recovery;
- create a second durable occurrence for an already materialized schedule slot;
- weaken lease-epoch, claim-token, cancel-epoch, occurrence-key, or transaction fencing;
- blindly replay an expired attempt with an unknown external effect;
- turn a database error into a tight retry loop;
- add unbounded recovery scans or load all unresolved occurrences into memory;
- change user-visible schedule syntax, command behavior, misfire policy, overlap policy, or plugin behavior unless a separate issue is approved;
- remove legacy compatibility/cutover paths as part of these fixes;
- perform unrelated refactors or formatting churn.

### 3.3 Resource constraints

- Preserve bounded recovery scan limits (current recovery candidate fast path caps at 500).
- Preserve bounded Scheduler claim batches (legacy path caps at 10).
- Keep one durable coordinator/timing owner; do not add a dedicated recovery goroutine.
- Any new counters must be atomic or protected by existing locks and must not retain unbounded error strings/history.
- Keep SQLite transactions short. Do not perform TaskEngine submission, handler execution, or network calls inside a SQLite transaction.
- Any new query should be index-aware and avoid N+1 lookups.
- Recovery must be safe under repeated wakeups and the periodic safety scan.

---

## 4. Audit baseline and source evidence

The branch was freshly queried on 2026-11-24. Current remote `test-next` HEAD at audit time:

```
c54d2f590f3cf4d7877a75db8b3a067bd72dd458
```

The previous audit note referenced an older branch head. The branch has since advanced; this plan is anchored to the newer SHA above.

### 4.1 Relevant current source files

| File | Observed blob SHA | Audit relevance |
|---|---|---|
| `internal/jobs/manager_recovery.go` | `52b1112d3d5825e5e8c75116a5c609b8f15f80f0` | `Recover` currently converges dispatched occurrences; candidate summary handling and attempt state rules live here |
| `internal/jobs/manager_schedule.go` | `6b95dfd7e30bb0d895a873bea8fa0181dddc0424` | `ProcessDueSchedules`, atomic materialization caller, and existing `NextIntervalDueAfter` arithmetic |
| `internal/jobs/manager_attempt.go` | `936f72c41e0c1ba699c51668ab84fde7a8fe9146` | `driveAttempt` prepares a durable attempt lease and submits through TaskEngine |
| `internal/jobs/store_ports.go` | `9e7f5172f9459215b5b16eafd07243b016fa75ad` | Store interfaces, including recovery candidate port |
| `internal/jobs/sqlite/store.go` | `2f9ceea7739a8c421c5f3f249ce8c8e49b36b216` | SQLite recovery candidate query currently selects dispatched occurrences and joins existing attempts |
| `internal/scheduler/repository.go` | `d3f271066dec9baab1ffd83f713eae24c133d5f0` | Legacy recurring completion loop and claim/lease persistence |
| `internal/scheduler/engine.go` | `051212890c911d430e9e77e6f08cf1d61241ed6f` | Scheduler loop, claim reconciliation, active legacy claims, timing and lease constants |

### 4.2 Relevant observed implementation facts

- `JobManager.durableCoordinatorLoop` already owns startup/restart recovery, deferred deadlines, outbox replay, and low-frequency safety scans. Do not add another coordinator.
- `runRecoveryPass` currently calls `m.Recover(...)` and discards the returned error (`_, _ = m.Recover(...)`).
- `Recover` consumes `RecoveryCandidateStore.ListRecoveryCandidates` when configured. It currently expects `OccurrenceDispatched`; other states are marked stale.
- SQLite `ListRecoveryCandidates` currently selects only `state = 'dispatched'` and inner-joins attempt statistics/latest attempt. A ready occurrence with no attempt cannot appear in this result.
- `ProcessDueSchedules` materializes an occurrence, increments its processed count, then calls `driveAttempt`. If attempt preparation/submission fails, it signals recovery. If the process exits between materialization commit and `driveAttempt`, the ready occurrence remains durable while the schedule's next due time has already advanced.
- `driveAttempt` uses the existing occurrence lock, prepares an attempt lease, and submits a `tasks.WorkSpec` through the TaskEngine client. Preserve this route.
- The redesigned path already has `NextIntervalDueAfter`, an arithmetic O(1) catch-up helper with overflow checks.
- Legacy `CompleteScheduledJob` still advances `next_run_at` with a loop that adds one interval until it is after `now`, inside a SQLite transaction.
- Legacy Scheduler uses a three-minute claim lease and a 90-second scheduler action timeout constant. Managed job definitions can have their own timeout/attempt lease duration.
- `retainLegacyClaim` renews the legacy claim when retaining an unresolved occurrence. Subsequent `reconcileClaim` reads the row and occurrence but does not renew the claim while the occurrence remains active. This is the lease-lifecycle gap to address.
- Legacy claim tokens and redesigned attempt lease epochs are distinct fencing domains. Do not collapse them into one token or assume one replaces the other.

---

## 5. P1-A — Recover materialized ready occurrences without attempts

**Priority:** High — correctness and durability  
**Primary files:** `internal/jobs/manager_recovery.go`, `internal/jobs/store_ports.go`, `internal/jobs/sqlite/store.go`, `internal/jobs/manager_schedule.go`, recovery tests.

### 5.1 Failure scenario

The redesigned schedule path has this sequence:

```text
Scheduler timing loop
  -> JobManager.ProcessDueSchedules
      -> SQLite transaction:
           insert occurrence(state=ready)
           advance schedule.next_due_at
           commit
      -> JobManager.driveAttempt(occurrence.ID)
           prepare attempt lease
           TaskEngine.Submit
```

A process crash after the SQLite commit but before attempt lease creation leaves:

- one durable occurrence in `ready`;
- no row in `job_attempts`;
- the schedule already advanced to its next due time;
- no TaskEngine task to complete the occurrence.

On restart, the recovery candidate query only selects dispatched occurrences with an attempt. The ready occurrence is invisible to recovery. For a one-shot schedule it can remain stuck indefinitely. For a recurring schedule with `OverlapForbid`, it can remain an active occurrence and prevent later slots from running.

### 5.2 Required design

Extend the existing JobManager recovery path to recognize two separate safe recovery classes:

1. **Ready/no-attempt:** a logical occurrence has been materialized but has not acquired an attempt lease. It is eligible for first admission, subject to definition/handler validity and the durable store's atomic state transition/fencing.
2. **Dispatched/with-attempt:** existing retry/recovery behavior. Apply all current retry budget, deferred deadline, lease epoch, unknown-effect quarantine, typed failure semantics, and attempt history rules unchanged.

Do not make a ready occurrence pretend to have a previous failed attempt. Do not fabricate an attempt row just to fit the existing dispatched query.

### 5.3 Store/query strategy

Preferred direction: extend the existing bounded recovery candidate fast path so one query returns:

- ready occurrences with no attempt row; and
- dispatched occurrences with their latest attempt and aggregate attempt counters.

Use a CTE/UNION or a carefully structured LEFT JOIN. Preserve deterministic ordering (oldest eligible `ready_at`, then stable ID) and the existing scan limit. Ensure a ready occurrence cannot be duplicated by joining multiple attempt rows. The query must not accidentally include ready occurrences that already have attempts due to a partially migrated or corrupted state; define and test the exact invariant.

An alternative separate `ListReadyOccurrences` port is acceptable only if it preserves global boundedness and fairness. Do not scan 500 dispatched plus an unbounded ready set. If two queries are used, define how the shared scan budget is split and how repeated scans avoid starving either class.

### 5.4 Atomic admission and concurrency

Before implementing the ready path, inspect the current SQLite `PrepareNextAttemptLease` transaction and its accepted occurrence states. Confirm whether it can safely transition `ready` to `dispatched` and create the first attempt in one writer-fenced transaction.

Required invariant:

- at most one first attempt lease can be created for a ready occurrence, even if multiple recovery triggers or multiple manager instances race;
- the occurrence state transition and attempt insert are atomic;
- the existing occurrence key and occurrence ID remain unchanged;
- stale recovery snapshots cannot create a later attempt after cancellation, terminal settlement, or a newer revision/lease epoch;
- in-process `lockOccurrence` remains an optimization, not the only correctness fence.

If the existing transaction does not support ready state, add a narrowly scoped store operation or extend the existing lease-preparation transaction. Do not add a new execution layer. The DB transaction must end before calling TaskEngine.

### 5.5 Definition and handler handling

- If definition exists, is enabled, and handler is registered: call the same `driveAttempt` path with the existing occurrence ID.
- If the definition is missing/disabled or handler is unavailable: keep the occurrence durable and surface it as orphaned/held with an observable reason. Do not silently mark it complete or repeatedly spin on it.
- Do not create an ad-hoc handler registry or persist executable closures.
- Re-check current definition/handler under the existing Manager synchronization rules before submission.

### 5.6 Unknown external effects

This change applies only to `ready` occurrences that have no attempt. It must not relax the existing safety policy for dispatched attempts whose external effect may have occurred.

Preserve:
- expired leased/running attempt fencing;
- `OccurrenceBlocked` and blocked-review/quarantine behavior;
- lease-epoch checks;
- non-retryable typed semantics;
- retry budget and deferral limits.

Never infer “safe to replay” merely because a process restarted or a lease expired.

### 5.7 Required tests

- `TestManagerRecoversReadyOccurrenceWithoutAttempt`
- `TestRedesignedScheduleRecoversAfterMaterializationCrash`
- `TestReadyOccurrenceRecoveryIsIdempotentAcrossRepeatedPasses`
- `TestConcurrentRecoveryCreatesAtMostOneFirstAttempt` (use store-level concurrency/race coverage)
- `TestReadyOccurrenceWithMissingDefinitionIsObservable`
- `TestReadyOccurrenceWithDisabledDefinitionDoesNotExecute`
- `TestReadyOccurrenceRecoveryDoesNotReplayBlockedUnknownEffect`
- Regression: existing dispatched retry, deferred retry, retry-budget, cancellation, and expired-lease quarantine tests remain unchanged and pass.

The existing automatic recovery test must be reviewed: it currently prepares/commits an attempt before Manager.Start, so it does not prove the ready/no-attempt crash gap.

---

## 6. P1-B — Make legacy recurring catch-up O(1)

**Priority:** Medium–High — performance and transaction duration  
**Primary file:** `internal/scheduler/repository.go`; tests in scheduler repository package.

### 6.1 Current issue

Legacy `CompleteScheduledJob` currently calculates the next run with a loop:

```go
nextRun := nextRunAt.Add(time.Duration(intervalSeconds) * time.Second)
for !nextRun.After(now) {
    nextRun = nextRun.Add(time.Duration(intervalSeconds) * time.Second)
}
```

The loop is proportional to missed intervals and executes inside the SQLite completion transaction. A one-second schedule delayed for 30 days can require roughly 2.59 million loop iterations before the transaction commits.

### 6.2 Required fix

Reuse the arithmetic approach already implemented by `jobs.NextIntervalDueAfter`, or introduce a small scheduler-local equivalent if package boundaries make importing jobs inappropriate.

Requirements:
- preserve anchored cadence/phase from the prior `next_run_at`;
- return the first scheduled time strictly after `now`;
- preserve the legacy SkipMissed behavior;
- reject invalid/non-positive intervals and arithmetic overflow explicitly;
- avoid duration multiplication overflow and time wraparound;
- do not silently change recurrence semantics or switch to `now + interval`;
- calculate before or within the transaction without a work-proportional loop; keep the transaction short.

If a shared helper is extracted, avoid creating a dependency cycle or exposing a generic scheduling framework for one function.

### 6.3 Required tests

- `TestLegacyRecurringCatchUpIsBoundedForLongDowntime`
- `TestLegacyRecurringCatchUpPreservesAnchoredPhase`
- `TestLegacyRecurringCatchUpHandlesExactBoundary`
- `TestLegacyRecurringCatchUpRejectsOverflow`
- Existing one-shot completion, recurring completion, claim-token fencing, and history atomicity tests.

Add a benchmark or bounded-operation test for representative intervals/downtime. Do not make wall-clock microbenchmarks the only correctness assertion.

---

## 7. P1-C — Align legacy claim renewal with active occurrence lifetime

**Priority:** Medium — lease lifecycle and database churn  
**Primary files:** `internal/scheduler/engine.go`, `internal/scheduler/repository.go`, scheduler lease/reconciliation tests.

### 7.1 Current issue

The legacy scheduled-job row has a three-minute claim lease. The JobManager attempt has its own lease based on the managed definition timeout (currently timeout plus one minute). These leases protect different layers.

The scheduler renews a claim when retaining an unresolved occurrence, but its later `reconcileClaim` pass does not renew the claim while that occurrence remains active. If the JobManager attempt is valid beyond the scheduler claim deadline:

1. Scheduler claims the legacy scheduled row.
2. JobManager creates an occurrence and attempt.
3. Scheduler retains/renews the claim and tracks it in memory.
4. The claim lease expires while the attempt remains valid.
5. Another scheduler pass/process can reclaim the row and record lease-expiration history.
6. The occurrence gate/attempt lease should prevent a duplicate physical execution, but repeated submissions can be rejected while the original attempt remains active; the scheduler may churn claims and SQLite writes.

Do not describe this as confirmed duplicate external execution. The existing occurrence and attempt fencing are expected to prevent that. The demonstrated risk is lease churn, noisy history, repeated admission rejection, and unnecessary persistence work.

### 7.2 Required behavior

While a tracked legacy claim still maps to a nonterminal, non-quarantined occurrence owned by the same claim token:

- renew the scheduler claim for a bounded extension using the existing `RenewJobLease` operation;
- perform renewal from the existing reconciliation loop, not a new goroutine/timer;
- keep the renewal cadence comfortably shorter than the lease duration;
- renew only after confirming the stored claim token still matches the tracked token;
- on `ErrJobLeaseLost`, stop treating the local claim as authoritative and untrack/reconcile safely;
- on transient DB errors, log/count the failure and retry on the next bounded reconciliation pass; do not tight-loop;
- when the occurrence is terminal, complete/fail the legacy row and untrack as today;
- when the occurrence is blocked due to unknown effect, retain current quarantine/failure semantics and do not renew indefinitely.

### 7.3 Design cautions

- Claim token and JobManager attempt lease epoch are separate fences.
- Do not set the scheduler lease to an arbitrarily large duration as a substitute for renewal.
- Do not renew a claim merely because it exists in the in-memory map; verify the durable row/token and occurrence state.
- If multiple processes can run the same Scheduler, renewal must be a conditional DB update on ID + claim token + running state. In-memory ownership is not sufficient.
- Consider whether a dedicated `renew-if-current` repository method is needed. Prefer existing `RenewJobLease` if it already enforces the required conditions.
- Keep renewal and settlement serialized consistently with existing `claimOperationMu` and claim lifecycle locks. Avoid introducing lock-order inversions.

### 7.4 Required tests

- `TestLegacyClaimLeaseRenewedWhileAttemptRunning`
- `TestLegacyClaimRenewalStopsAfterOccurrenceSettles`
- `TestLegacyClaimRenewalStopsAfterClaimTokenChanges`
- `TestLegacyClaimRenewalDoesNotRenewBlockedOccurrence`
- `TestLegacyClaimCanBeReclaimedAfterOwnerStopsRenewing`
- Multi-engine/store-level test proving stale owner cannot renew or settle a reclaimed claim.

Use a controllable clock or deterministic lease timestamps where possible; avoid sleep-heavy tests.

---

## 8. P1-D — Make recovery failures observable

**Priority:** Medium — operational visibility  
**Primary file:** `internal/jobs/manager_recovery.go`; tests for durable coordinator diagnostics.

### 8.1 Current issue

The existing durable coordinator owns recovery timing correctly, but `runRecoveryPass` discards the error returned by `Recover`. SQLite lock/query failures or other store errors can therefore be invisible. The periodic safety scan is a fallback, not a substitute for observability.

### 8.2 Required fix

Keep the current single coordinator and make the outcome visible through the existing logger/diagnostics conventions.

Minimum:
- log a warning/error when `Recover` returns an error, including bounded context such as operation name and scan limit;
- do not log payloads, credentials, full job payload bytes, or sensitive user data;
- distinguish a failed pass from a successful pass with zero candidates;
- preserve context-cancellation/shutdown behavior (expected cancellation should not be reported as an operational recovery failure).

Preferred, if an existing diagnostics surface can be extended without adding state-heavy infrastructure:
- cumulative recovery pass count;
- cumulative recovery failure count;
- last successful recovery timestamp;
- last failure timestamp and bounded error code/message;
- last successful `RecoverReport` summary (scanned, redriven, finalized, blocked, orphaned, stale).

Do not store an unbounded history of failures. Keep diagnostics snapshots immutable/value-based and concurrency-safe.

### 8.3 Required tests

- `TestRecoveryFailureIsObservable`
- `TestRecoveryCancellationIsNotReportedAsFailure`
- `TestSuccessfulEmptyRecoveryPassIsDistinctFromFailure`
- `TestRecoveryDiagnosticsRemainBounded`

Prefer injecting a failing store/logger or testing the diagnostics snapshot over asserting fragile log formatting.

---

## 9. Implementation phases and sequencing

Each phase is a separate reviewable unit. Do not combine all fixes into one large commit.

### Phase 0 — Refresh and implementation preflight

1. Fetch remote `test-next` and record its exact HEAD.
2. Pull with fast-forward only; stop if local work conflicts or branch history diverges.
3. Inspect the latest versions of all files listed in Section 4 and relevant tests.
4. Inspect SQLite `PrepareNextAttemptLease` transaction and current occurrence-state constraints before designing P1-A.
5. Inspect scheduler repository lease renewal conditions and all call sites before designing P1-C.
6. Check whether a diagnostics surface already exists before adding fields.
7. Update this plan's baseline/source hashes if the branch advanced.
8. Do not inspect or poll CI.

**Exit:** implementation design matches actual HEAD; no code changed yet.

### Phase 1 — P1-A ready occurrence recovery

1. Add regression test reproducing a ready occurrence with no attempt before Manager.Start.
2. Extend recovery candidate model/query with bounded ready/no-attempt candidates.
3. Add/adjust atomic store transition so first attempt creation is fenced and idempotent.
4. Route eligible ready occurrences through the existing `driveAttempt` → TaskEngine path.
5. Preserve dispatched recovery, retry budget, deferred behavior, and unknown-effect quarantine.
6. Add restart, repeated-pass, concurrent-recovery, missing-definition, and disabled-definition tests.
7. Run focused tests and race tests for jobs/SQLite.

**Exit:** crash gap is closed without direct handler execution or duplicate logical occurrence creation.

### Phase 2 — P1-B constant-time legacy catch-up

1. Add tests for long downtime and anchored phase before changing implementation.
2. Replace interval loop with O(1) arithmetic and overflow checks.
3. Preserve transactional update/history/claim-token behavior.
4. Run scheduler repository tests and a focused benchmark/operation-count check.

**Exit:** catch-up work no longer scales with missed intervals.

### Phase 3 — P1-C legacy lease renewal

1. Add deterministic tests for active attempt longer than scheduler lease.
2. Renew tracked active claims during the existing reconciliation pass.
3. Ensure renewal checks the durable claim token and running state.
4. Stop renewal on terminal, blocked, deleted, or reclaimed claims.
5. Verify lock ordering and multi-engine fencing.
6. Run scheduler/job integration tests and race tests.

**Exit:** active work retains its claim only while the owner keeps renewing it; stale owners cannot settle or renew.

### Phase 4 — P1-D recovery observability

1. Add structured logging for unexpected recovery errors.
2. Extend existing diagnostics only if there is a natural bounded surface.
3. Distinguish cancellation, failure, and successful-empty outcomes.
4. Add observability tests.

**Exit:** a failed recovery pass is visible without adding a new worker or unbounded history.

### Phase 5 — Integrated verification and handoff

1. Run targeted package tests for `internal/jobs`, `internal/jobs/sqlite`, and `internal/scheduler`.
2. Run the relevant broader test suite.
3. Run `go test -race` for the affected packages; run repository-wide race tests if practical and within the agreed phase.
4. Run focused benchmarks for recovery candidate query, long catch-up, and reconciliation with representative claim counts.
5. Run `go test ./...` and `go vet ./...` as appropriate.
6. Format all Go code changed in the phase with `gofmt`; check `gofmt -l` on changed Go files.
7. Review diff for accidental API changes, unbounded state, goroutine growth, SQL transaction expansion, and duplicated retry/execution logic.
8. Update this plan with implementation commit SHAs, tests actually run, benchmark results, and any deferred work.
9. Do not inspect/poll CI unless explicitly requested.

**Exit:** every acceptance criterion is either proven or explicitly marked deferred with rationale.

---

## 10. Test matrix and failure-injection scenarios

| Scenario | Expected durable result | Expected runtime behavior |
|---|---|---|
| Crash after occurrence materialization, before attempt lease | One `ready` occurrence, zero attempts | Startup recovery creates one first attempt through TaskEngine |
| Two recovery triggers race on same ready occurrence | One occurrence, one first attempt lease | Loser observes fenced/current state and exits safely |
| Crash after attempt lease, before TaskEngine admission | Dispatched occurrence + leased attempt | Existing recovery rules classify safe abort/retry; no fabricated second occurrence |
| Crash after external effect may have started | Dispatched occurrence + expired active lease | Occurrence is blocked/fenced; no automatic replay |
| Missing/disabled handler for ready occurrence | Occurrence retained and diagnosable | No busy loop; surfaced as orphaned/held |
| Recovery store query fails | Durable rows unchanged | Error observable; next safety pass remains bounded |
| Legacy schedule misses many intervals | Next due is first anchored interval after now | Constant-time arithmetic, short transaction |
| Legacy claim active beyond 3 minutes | Claim token remains current through renewal | No repeated reclaim churn |
| Old Scheduler resumes after another owner reclaimed claim | New claim token remains authoritative | Old renewal/settlement rejected |
| Occurrence becomes terminal | Scheduler row settles according to current semantics | Claim untracked; renewal stops |
| Occurrence becomes blocked | Quarantine/failure semantics preserved | No indefinite lease renewal |

---

## 11. Performance and resource guardrails

Measure before/after where relevant; do not invent arbitrary performance targets without a baseline.

- Recovery candidate query: retain one bounded query path where possible; avoid N+1 regressions.
- Recovery pass: cost must be proportional to the bounded scan limit, not total database history.
- Ready candidate selection: deterministic ordering and no starvation between ready and dispatched candidates.
- Legacy catch-up: constant-time arithmetic independent of missed interval count.
- Lease renewal: one bounded DB operation per tracked claim per reconciliation cadence, not per TaskEngine poll or per message.
- Idle behavior: no new periodic goroutine, no extra timer owner, no work when no recovery/claim/schedule deadline exists beyond existing safety cadence.
- Memory: no unbounded per-occurrence retention; use current bounded maps/queues and existing pruning rules.
- SQLite: keep transactions short, preserve writer-intent/fencing, and avoid query-per-candidate patterns.
- TaskEngine: no changes to worker count, admission queue, fairness, pools, resource accounting, or lifecycle are part of this plan.

Benchmark reference from prior project work may be used for comparison, but rerun measurements on the current code and machine before making claims. Do not treat historical numbers as current results.

---

## 12. Required AI work rules

The implementing AI must follow these rules exactly:

1. **Refresh first:** fetch the latest remote `test-next`, record exact HEAD, then `git pull --ff-only` before each implementation phase. If pull cannot fast-forward, stop and report; do not reset, rebase destructively, or overwrite user changes.
2. **Inspect actual source:** baseline hashes in this document are historical anchors. Re-read source, tests, interfaces, schema, and call sites at the refreshed HEAD.
3. **Plan before nontrivial changes:** if the current code requires a materially different design, update this document and present the revised plan before implementation.
4. **One phase at a time:** keep P1-A, P1-B, P1-C, and P1-D independently reviewable and commit each coherent phase separately.
5. **Tests with logic:** update/add tests whenever production logic, store contracts, state transitions, or APIs change.
6. **Format Go:** run `gofmt` on all changed Go files before commit and verify with `gofmt -l`.
7. **No CI polling:** do not inspect, poll, or wait on GitHub Actions/CI unless the user explicitly requests it.
8. **No silent scope expansion:** do not refactor adjacent Scheduler/JobManager/TaskEngine code unless required by a proven failure and documented first.
9. **No false completion claims:** report exact commands run, results, skipped checks, commit SHAs, and unresolved risks.
10. **Preserve user changes:** never force-push, reset, clean, or overwrite user work. Use fast-forward-only synchronization.
11. **Keep the branch target:** implementation and plan updates target `test-next` unless the user explicitly changes it.
12. **Do not implement voice:** voice feature remains out of scope.

---

## 13. Completion checklist

- [x] Latest `test-next` fetched and fast-forward pulled before implementation.
- [x] P1-A ready/no-attempt crash gap reproduced by a failing regression test before fix.
- [x] Ready occurrence recovery is bounded, idempotent, and transaction-fenced.
- [x] Dispatched unknown-effect quarantine remains unchanged and tested.
- [x] P1-B legacy catch-up is O(1), phase-preserving, and overflow-safe.
- [x] P1-C active legacy claim renews conditionally and stops on terminal/blocked/reclaimed states.
- [x] P1-D recovery errors are observable; cancellation is not misreported.
- [x] No new runtime, executor, worker pool, retry engine, or periodic goroutine.
- [x] Tests and race checks run and recorded.
- [x] Relevant benchmarks run and recorded where applicable.
- [x] All changed Go files formatted and checked.
- [x] No CI inspection/polling performed unless requested.
- [x] Plan updated with actual implementation commits and verification evidence.

---

## 14. Final handoff format and implementation evidence

### 14.1 Implementation commit records

| Phase | Commit SHA | Conventional commit message | Scope / Description |
|---|---|---|---|
| P1-A | `a7a2f07b` | `feat(jobs): recover ready occurrences without attempts after crash` | Recover materialized `ready` occurrences without attempts after restart/crash; keep bounded fast-path query and attempt idempotency |
| P1-B | `5eb09edd` | `perf(scheduler): make legacy recurring catch-up constant time` | Constant-time $O(1)$ legacy recurring schedule catch-up arithmetic with overflow protection and anchored phase preservation |
| P1-C | `5924d7df` | `fix(scheduler): align legacy claim lease renewal with active occurrence` | Conditionally renew legacy scheduler claim lease in reconciliation loop while mapped occurrence is active and unblocked; stop on terminal, blocked, or lost claim |
| P1-D | `1ce46ccf` | `feat(jobs): make recovery pass failures observable` | Record bounded recovery diagnostics (pass/failure counts, timestamps, error message, report) and emit structured warning logs on unexpected recovery errors |
| P1-D (fix) | `d3b18f30` | `fix(jobs): record internal recovery timeout while parent context is active` | Distinguish internal `recoveryTimeout` expiry from parent context cancellation, ensuring internal timeout is tracked as an operational failure, with payload leak protections |

### 14.2 Files changed per phase

- **P1-A:**
  - `internal/jobs/manager_attempt.go`: guard `driveAttempt` against concurrent submissions using `m.tracked` under occurrence gate.
  - `internal/jobs/manager_recovery.go`: add branch for `OccurrenceReady` candidate to drive first attempt or report orphaned if definition/handler missing.
  - `internal/jobs/sqlite/store.go`: extend `ListUnresolvedOccurrences` and `ListRecoveryCandidates` with safe `LEFT JOIN` to return `OccurrenceReady` candidates with 0 attempts alongside dispatched candidates.
  - `internal/jobs/ready_occurrence_recovery_test.go`: comprehensive regression test suite reproducing the crash gap and verifying restart, idempotency, concurrency, disabled definitions, and unknown-effect quarantine.
- **P1-B:**
  - `internal/scheduler/repository.go`: add O(1) arithmetic helper `nextIntervalDueAfter` with overflow validation and update `CompleteScheduledJob` to avoid loop proportional to downtime.
  - `internal/scheduler/repository_test.go`: add tests for long downtime (30 days / 2.59M missed intervals), anchored minute/second phase preservation, exact interval boundary cases, and arithmetic overflow rejection.
- **P1-C:**
  - `internal/scheduler/engine.go`: add `ClaimLeaseRenews` to `Diagnostics`, `claimLeaseRenews atomic.Uint64` to `Engine`, and conditionally renew claim lease via `RenewJobLease` in `reconcileClaim` when occurrence is active and remaining lease is within renewal window; handle `ErrJobLeaseLost`.
  - `internal/jobs/manager.go`: defensive check in `LatestAttempt` and `OccurrenceByKey` when store port is not configured.
  - `internal/scheduler/runtime_execution_e0_test.go`: implement mock `RenewJobLease` in test repo.
  - `internal/scheduler/runtime_execution_e3_benchmark_test.go`: implement mock `RenewJobLease` in benchmark repo.
  - `internal/scheduler/legacy_claim_renewal_test.go`: test suite verifying renewal while attempt is running, cessation after settlement, cessation after token change, exclusion of blocked occurrences, reclaimability after expiry, and multi-engine fencing.
- **P1-D:**
  - `internal/jobs/manager.go`: add `RecoveryDiagnostics` struct, `SetLogger` method, and `Recovery` diagnostics exposure in `Diagnostics()`.
  - `internal/jobs/manager_recovery.go`: capture returned `report, err` from `Recover`, record bounded failure/success timestamps and counts, ignore normal shutdown cancellations, and emit structured warning logs.
  - `internal/app/wiring_core.go`: wire `jobsManager.SetLogger(logger.Named("jobs"))`.
  - `internal/jobs/recovery_observability_test.go`: test observability of recovery failures, exclusion of context cancellations, distinction between empty pass and failure, and bounded error message length.

### 14.3 Verification command evidence

1. **Formatting check:**
   ```bash
   gofmt -l internal/jobs internal/scheduler internal/app
   # Output: clean (0 unformatted files)
   ```
2. **Go vet:**
   ```bash
   go vet ./...
   # Output: exit 0 (clean)
   ```
3. **Architecture package rules:**
   ```bash
   go test -v ./internal/architecture/...
   # Output: PASS ok github.com/inipew/goultroid/internal/architecture 1.376s
   ```
4. **Complete repository-wide race test suite:**
   ```bash
   go test -race ./... -count=1
   # Output: PASS ok across all 100+ packages in repository (0 race conditions, 0 failures)
   ```
5. **Benchmarks:**
   ```bash
   go test -bench=. ./internal/scheduler -run="^$"
   # Output:
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaims/claims=1-16         1398816         866.4 ns/op
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaims/claims=100-16        15890         75547 ns/op
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaims/claims=1000-16        1443        821826 ns/op
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite/claims=1-16    15894         76632 ns/op
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite/claims=100-16    152       7553835 ns/op
   # BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite/claims=1000-16    14      76547662 ns/op
   ```
6. **Binary build:**
   ```bash
   go build -v ./cmd/goultroid
   # Output: built cleanly
   ```

### 14.4 Architecture & resource constraints confirmation

- **TaskEngine as sole execution authority:** Untouched and preserved. All attempts are created and submitted through `tasks.Client.Submit`.
- **No new goroutines/runtimes:** Single coordinator loop and existing reconciliation loop reused. No new worker pools or background timers added.
- **Durable boundaries:** All lease epochs, claim tokens, and cancel epochs remain fenced. SQLite transactions kept strictly bounded and short.
- **Voice feature:** Untouched and out of scope.

