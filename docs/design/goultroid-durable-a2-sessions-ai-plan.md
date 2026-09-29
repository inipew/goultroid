# Goultroid Durable A2 Session Hardening — AI Session Plan

Status: **OPEN — D1–D3 implemented; D4 implementation present but local acceptance is still pending**

Audit baseline:

- Branch: `test-next`
- Baseline HEAD: `d0b6659eeb1f613482c2018a199da93d3be92447`
- Baseline commit: `feat(interaction): persist opted-in A2 sessions`
- Audit date: 29 September 2026
- Primary ADRs:
  - `docs/adr/0007-durable-a2-sessions.md`
  - `docs/adr/0005-storage-migration-and-namespace-compatibility.md`

This document is the implementation handoff for hardening the durable A2 interaction-session work introduced at `d0b6659e...`.

The implementation already moves A2 sessions beyond purely in-memory lifetime for opted-in features. The remaining work is **not** to redesign the interaction stack. It is to make restart durability, lifecycle deletion, schema ownership, bounded cleanup, feature-level acceptance, and performance evidence match the guarantees already claimed by ADR 0007.

The target remains:

> one canonical A2 interaction runtime, one callback protocol, one TaskEngine execution authority, and one persistent session representation for opted-in features.

This plan deliberately does **not** authorize active-active multi-instance support. Multi-instance remains a separately scoped architecture problem unless the user explicitly requests it.

---

## 1. User rules and hard constraints

Every AI session continuing this plan must preserve all of the following rules.

1. **Always refresh `test-next` HEAD before starting a new phase.**
   - Record the exact SHA and commit message before coding.
   - Do not assume the baseline in this document is still current.
   - Re-read current source if HEAD advanced.

2. Run `gofmt` **before every commit that changes Go code**.
   - Format every changed `.go` file, including test files.
   - Prefer an explicit changed-file list rather than formatting unrelated packages.

3. **Do not inspect, poll, wait for, or report CI unless the user explicitly asks.**
   - Local tests are allowed and expected.
   - CI state must not be used as implicit acceptance evidence.

4. **If a production Go API, function, method, type, field, constant, or constructor is renamed or reshaped, update its tests in the same phase.**
   - Search test files for old names/signatures.
   - Do not leave stale tests compiling against removed APIs.
   - Format changed test files with `gofmt`.
   - A phase is not complete merely because production code compiles.

5. Do not create:
   - a second interaction runtime;
   - a second callback protocol;
   - a second feature registry;
   - a second TaskEngine;
   - a second Telegram RPC executor;
   - a second downloader/retry engine;
   - a background persistence worker merely to avoid synchronous writes.

6. The existing A2 runtime remains the only owner of live interaction sessions.

7. The existing `a2:<action>:<session>.<revision>` callback format remains canonical unless a concrete correctness defect proves a protocol change is unavoidable.

8. TaskEngine remains the execution authority for finite work. Durable interaction persistence must not become another executor or replay engine.

9. Keep retained state bounded.
   - No unbounded retry lists.
   - No unbounded restore accumulation.
   - No permanent retry goroutine solely for failed interaction-session cleanup.
   - No background ticker unless a concrete invariant cannot be maintained lazily.

10. Persistence failures must fail closed with explicit propagation where correctness depends on durable state.

11. Normal process shutdown and normal feature disable are different lifecycle events:
   - process shutdown may preserve compatible durable rows;
   - feature disable/unregister must not allow its sessions to resurrect after restart.

12. Do not broaden this work into active-active multi-instance coordination unless the user explicitly requests that architecture.

13. Do not silently change feature UX, callback labels, state formats, MyXL purchase semantics, or Assistant navigation semantics just to satisfy persistence tests.

14. Before every commit:
   - inspect the diff;
   - ensure no unrelated files are included;
   - run the relevant focused tests;
   - run `gofmt` first for every Go-changing commit;
   - do not check CI.

### 1.1 Mandatory execution discipline — no unvalidated Go pushes

The rules above are gates, not recommendations.

For every phase that changes Go code, use one continuous loop:

```text
refresh HEAD
→ read only the phase + directly affected source
→ implement scoped production/test changes
→ gofmt every changed Go file
→ verify gofmt -l is empty
→ run focused tests
→ run race/vet acceptance required by the phase
→ git diff --check
→ inspect status/diff for unrelated changes
→ commit
→ push
→ refresh HEAD
→ update phase documentation from actual results
```

Hard requirements:

- **Do not push Go changes if the required local validation cannot be executed.**
- Static review, GitHub source inspection, or reasoning about compilation **must not substitute** for `go test`, `go test -race`, `go vet`, or `gofmt` when the phase requires those checks.
- If the AI environment has no usable repository checkout, it may audit source, prepare a patch, or document the exact next commands, but it must **stop before commit/push** of Go changes.
- A user request to "push" does not waive these plan rules unless the user explicitly says to bypass a particular gate.
- A phase must not be marked `CLOSED` from source inspection alone.
- Do not rely on CI to discover compile, formatting, lint, race, stale-test, or mock/interface regressions. CI remains uninspected unless explicitly requested.
- A separate follow-up `fmt` commit is evidence that the previous Go-changing commit did not follow this plan. Fix formatting before the original commit instead.
- A compile/test correction immediately after a phase commit should be treated as a failed pre-push gate and the workflow should be corrected before starting the next phase.

### 1.2 Fast execution path — avoid repeated full-system re-audits

This plan should be executed faster than the initial audit without weakening validation.

At the start of a continuation session:

1. read this document's **Fast-start context** and the current phase only;
2. refresh `test-next` HEAD once;
3. if HEAD has advanced, inspect only the commits/diffs since the documented snapshot and re-read directly affected files;
4. do **not** re-audit the entire interaction/Telegram/plugin stack unless a new failure crosses those boundaries;
5. keep one phase in one worktree loop until its local gate is green;
6. do not start the next phase while the current phase has unresolved compile, format, focused-test, race, or vet failures.

Prefer concrete execution over repeated planning once the current phase contract is already frozen.

---

## 2. Current architecture and implemented baseline

At baseline `d0b6659e...`, Goultroid has already implemented durable A2 sessions for opted-in features.

### 2.1 Canonical runtime remains singular

The durable implementation extends the existing:

- `internal/interaction.Runtime`;
- feature catalog;
- action dispatcher;
- orchestration engine;
- A2 callback token format.

It does **not** introduce a parallel interaction engine.

This invariant must be preserved.

### 2.2 Durable store contract

`internal/interaction/durable.go` currently defines:

```go
type DurableSession struct {
    Session      Session
    Version      string
    InputExpires time.Time
}

type DurableStore interface {
    Save(context.Context, DurableSession) error
    Delete(context.Context, string) error
    Load(context.Context) ([]DurableSession, error)
}
```

The persisted state includes:

- session ID;
- feature ID;
- durability version;
- actor/chat/message/inline binding;
- opaque feature state;
- revision;
- creation deadline;
- expiry deadline;
- pending input deadline.

### 2.3 Feature opt-in

Features opt into restart durability through:

```go
Spec.DurabilityVersion
```

At the audit baseline:

- Assistant Shell: `"3"`;
- Calculator: `"1"`;
- Settings: `"1"`;
- MyXL: `"1"`.

A version change invalidates persisted state that the new feature code does not promise to understand.

### 2.4 Startup restore

Application startup currently:

1. creates the interaction runtime;
2. attaches the SQLite durable store;
3. registers feature contracts;
4. registers Assistant Shell;
5. calls `RestoreDurable`;
6. then exposes the interaction foundation to Assistant/runtime ingress.

Restore:

- rejects incompatible versions;
- rejects expired rows;
- rejects invalid generic session metadata;
- rebinds the restored session to the **current** feature scope generation;
- restores a still-live pending input claim;
- reindexes the session into the runtime's normal bounded state.

This generation rebinding is correct. Persisting an old process generation would be incorrect.

### 2.5 State mutation ordering

Current mutations generally follow:

```text
validate
→ compute next session snapshot
→ durable Save
→ publish in-memory mutation
```

That gives an important invariant:

> if a required durable write fails, the corresponding in-memory state transition must not advance.

Existing tests already cover this for state update and pending-input arming.

### 2.6 Shutdown preservation

`Manager.ShutdownWithContext` calls:

```go
InteractionRuntime().PreserveDurableOnShutdown()
```

before feature cleanup.

That intentionally distinguishes full process shutdown from normal plugin disable.

The target behavior is:

- normal plugin disable: delete its durable sessions;
- process shutdown: release memory/context but preserve compatible rows for the next process.

---

## 3. Confirmed audit findings

### 3.1 P1 — durable delete failure is lost during feature disable

Normal feature cleanup eventually calls:

```go
registry.interactions.CancelScope(scope)
```

`CancelScope` returns only an integer removal count.

Its internal `removeLocked` currently tries:

```go
r.durable.Delete(context.Background(), id)
```

and on failure:

- increments `persistenceErrorCount`;
- returns `false`;
- does not expose the error to the plugin manager.

This means a lifecycle operation can appear successful while the durable row remains.

Failure sequence:

```text
durable session exists
→ plugin disable begins
→ feature registration is removed
→ CancelScope tries durable Delete
→ SQLite Delete fails
→ error is reduced to false/count
→ disable can still complete
→ row remains in SQLite
→ process restarts later
→ feature registers same durability version
→ RestoreDurable sees a compatible row
→ previously disabled session can resurrect
```

That violates the intended ADR guarantee:

> normal feature disable deletes its sessions.

This is a release-blocking correctness issue for durable-session closure.

### 3.2 P1 — durable cleanup ignores lifecycle cancellation

`removeLocked` calls durable delete with:

```go
context.Background()
```

even when the caller has a real lifecycle context.

Consequences:

- plugin disable/shutdown cancellation cannot interrupt the SQLite delete;
- the database `busy_timeout(5000)` may keep one delete blocked for seconds;
- scope cleanup can perform many per-session deletes;
- lifecycle responsiveness is therefore not bounded by the supplied disable/shutdown context.

This must be corrected together with error propagation.

Do **not** fix this by adding a background deletion goroutine.

### 3.3 P1 — session schema bypasses the repository migration contract

`internal/interaction/sqlite/store.go` currently creates its table using:

```go
Store.InitSchema(...)
```

with direct:

```sql
CREATE TABLE IF NOT EXISTS interaction_sessions (...)
```

called from application construction.

This bypasses the architecture established by ADR 0005 and `database.RunFeatureMigrations`.

ADR 0005 requires feature/service schema ownership through namespaced migration history with:

- deterministic IDs;
- checksums;
- upgrade accounting;
- additive evolution;
- rollback/interruption-aware tests.

A runtime `CREATE TABLE IF NOT EXISTS` does not provide those guarantees.

The durable interaction schema therefore needs a proper feature-owned migration provider and must join `migrateBuiltinFeatures`.

### 3.4 P1/P2 — failed expiry delete can orphan an expired session outside the expiry heap

Current lazy expiry logic roughly does:

```text
heap.Pop(expiry)
→ entry.expiry = nil
→ removeLocked(session)
→ durable Delete may fail
```

If durable deletion fails:

- the session stays in `r.sessions`;
- the session remains expired;
- its expiry heap item is already gone;
- `entry.expiry` is nil;
- later heap-based prune passes no longer naturally retry it.

The stale expired entry can continue consuming:

- global session capacity;
- per-scope capacity;
- per-actor capacity;
- state byte budget.

This conflicts with the runtime's bounded-retention contract.

A durable-delete failure must not silently detach an expired session from its future cleanup path.

### 3.5 P2 — durable DB I/O occurs while holding the global runtime mutex

The current implementation performs SQLite `Save` / `Delete` operations while holding `Runtime.mu`.

Advantages:

- simple ordering;
- no split-brain between memory and persistence;
- straightforward fail-closed semantics.

Risk:

- one slow SQLite writer can stall unrelated A2 mutations;
- database busy timeout can amplify lock hold time;
- callback bursts across unrelated actors become serialized behind storage latency.

This is a performance/latency concern, **not** permission to weaken durability ordering.

The correct next step is measurement.

Do not introduce asynchronous write-behind or a second state owner without benchmark evidence and a separately reviewed design.

### 3.6 P2 — generic runtime restart tests do not prove real feature restart compatibility

The baseline has good generic restart coverage, including:

`TestSQLiteRuntimeRestartKeepsCallback`.

That proves:

- persisted session state survives runtime reconstruction;
- old callback data can resolve after restart;
- current feature generation can replace the old generation.

It does **not** yet prove that every opted-in feature can interpret its own real persisted state after reconstruction.

Required feature-level evidence is still missing for at least:

- Assistant Shell / Help;
- Settings;
- Calculator;
- MyXL.

Each feature that publishes a nonempty `DurabilityVersion` is making a compatibility promise and should have restart acceptance that exercises its real state/action path.

### 3.7 P2 — durability opt-in inventory needs a regression fence

The baseline manually assigns durability versions to several features.

There is no acceptance rule yet proving that:

- every durable feature has restart tests;
- every version bump is deliberate;
- newly durable features do not opt in without state-compatibility coverage.

This should be fenced without inventing central feature semantics inside the runtime.

### 3.8 Boundary — multi-instance is explicitly unsupported

ADR 0007 correctly says the current design supports one active bot process.

The current SQLite store is **not** a distributed session authority.

Two active instances can independently restore the same:

```text
session X
revision N
```

and both may accept the same callback revision.

Current `Save` uses upsert semantics rather than distributed compare-and-swap ownership.

There is no:

- distributed callback claim;
- shared admission lease;
- cross-instance revision CAS;
- cross-instance feature-side-effect fencing.

Do not misrepresent current persistence as multi-instance support.

This plan does not implement that capability.

---

## 4. Target invariants

After this plan is complete, the following must hold.

### 4.1 Single-authority invariant

There is still exactly one:

- A2 interaction runtime;
- A2 callback token protocol;
- feature catalog;
- A2 action dispatcher;
- TaskEngine execution authority.

### 4.2 Durable state transition invariant

For opted-in features:

> a state mutation that requires persistence is not visible in memory unless its durable write succeeds.

This already exists and must not regress.

### 4.3 Disable invariant

For normal plugin disable/unregister:

> if the operation reports success, no durable session owned by the disabled feature generation may remain eligible for future restore.

If durable cleanup fails, the lifecycle operation must surface failure rather than claiming successful teardown.

### 4.4 Shutdown invariant

For full process shutdown:

> compatible durable session rows remain intact, while in-memory contexts/resources are released and in-flight work is not replayed.

### 4.5 Expiry invariant

For an expired durable session:

> either durable deletion succeeds and the session is fully removed, or the runtime retains enough cleanup metadata to retry later without leaking bounded capacity.

### 4.6 Migration invariant

The `interaction_sessions` schema is represented in the namespaced feature migration history.

Fresh installs and supported upgrades must converge on the same schema.

### 4.7 Feature compatibility invariant

A feature may declare nonempty `DurabilityVersion` only when tests prove that its persisted state and action IDs remain valid across runtime reconstruction for that version.

### 4.8 Resource invariant

Durability must not add:

- permanent workers;
- unbounded queues;
- unbounded retry retention;
- duplicate state owners.

---

## 5. Target lifecycle API shape

Exact names may follow repository style, but the resulting semantics should be equivalent.

### 5.1 Context-aware removal

The runtime needs an internal removal path that can propagate persistence errors and accept caller context.

Conceptually:

```go
func (r *Runtime) removeLocked(ctx context.Context, id string, cause error) (bool, error)
```

or equivalent responsibility-specific helpers.

Do not mechanically change every public API before auditing its callers.

The important distinction is:

- best-effort/stateless removal is not acceptable for lifecycle teardown that promises durable deletion;
- process-shutdown preservation intentionally bypasses durable delete.

### 5.2 Scope cancellation

Normal feature teardown needs an error-bearing operation.

A possible target shape is:

```go
func (r *Runtime) CancelScopeContext(
    ctx context.Context,
    scope tasks.ScopeIdentity,
) (int, error)
```

while retaining `CancelScope` only if there are valid compatibility callers that do not need durable guarantees.

If a compatibility wrapper remains, define exactly whether it:

- uses `context.Background()`;
- discards errors;
- is allowed in production lifecycle code.

Production plugin lifecycle must use the error-bearing variant.

### 5.3 Single-session cancel

Audit whether public `Cancel(id) bool` is used in correctness-sensitive durable paths.

If a caller must know whether durable delete failed, introduce a context/error-bearing internal or public variant and migrate that caller.

Do not break unrelated call sites merely for signature purity.

### 5.4 Preserve-on-shutdown

Keep shutdown preservation explicit.

Do not infer shutdown preservation from arbitrary `Close()` calls.

The code should make it obvious which lifecycle path:

- deletes durable rows;
- preserves durable rows.

---

## 6. Target migration ownership

The interaction SQLite package should own a namespaced migration provider.

A target layout may be:

```text
internal/interaction/sqlite/
  store.go
  migrations.go
  migrations_test.go
```

The exact migration ID should follow current repository conventions, for example an interaction-owned namespace equivalent to:

```text
interaction.001
```

Do not copy this ID blindly if the current migration namespace rules require another prefix.

### Migration requirements

The first migration must create the current `interaction_sessions` shape.

Acceptance must cover:

- fresh database;
- upgrade from a database created before durable A2 sessions;
- database already containing the table from baseline `InitSchema`;
- migration idempotence through recorded migration metadata;
- schema verification where legacy adoption requires it.

Because `d0b6659e...` may already have been run by users, migration work must account for databases where:

- the table exists;
- no feature migration record exists.

Do not destroy or recreate the table just to bring it under migration ownership.

Prefer safe adoption/schema verification.

### Application wiring target

After migration closure:

- `migrateBuiltinFeatures` includes the interaction migration provider;
- application startup no longer depends on ad-hoc `Store.InitSchema`;
- `NewStore` remains a persistence adapter, not a schema bootstrapper.

---

## 7. Implementation phases

### D0 — Refresh, reproduce, and freeze the current contract

Status: **CLOSED — source/caller audit frozen at `caf676269119b7ca50641816c84c53605238856b`; no production code changed**

D0 refresh:

- Branch: `test-next`
- Audited HEAD: `caf676269119b7ca50641816c84c53605238856b`
- Commit: `docs(design): plan durable A2 session hardening`
- Audit date: 29 September 2026
- CI: **not inspected**
- Local checkout/test execution: **unavailable in the current AI environment because GitHub hostname resolution is unavailable**. D0 therefore records source-level reproductions and existing test coverage only; it does not claim a local `go test` result.

No Go production behavior was changed in D0. A red regression-test commit was intentionally not published without a local execution path. D1/D2 must add the named regression tests together with the fixes, so `test-next` is not knowingly left with an unverified permanently-red test commit.

#### D0.1 — Authoritative removal caller inventory

The current interaction runtime has one internal removal primitive:

```go
func (r *Runtime) removeLocked(id string, cause error) bool
```

Current direct callers found by source audit:

- `internal/interaction/runtime_internal.go`
  - `loadCurrentSession`: removes an expired session encountered by resolve/load;
  - `cancelIfScope`: removes a stale-generation session;
  - `pruneExpiredLocked`: removes heap-expired sessions.
- `internal/interaction/session.go`
  - `UpdateState`: expired-session check;
  - `BindTarget`: expired-session check;
  - `Touch`: expired-session check;
  - `Cancel`: explicit single-session cancellation;
  - `CancelScope`: feature-generation teardown.
- `internal/interaction/input.go`
  - `ArmInput`: expired-session check;
  - `TakeInput`: expired-session check.

This inventory is important for D1: changing `removeLocked` to carry context/error semantics cannot be done only at `CancelScope`. Every caller above must be classified so tests and signatures do not drift.

#### D0.2 — Feature lifecycle caller inventory

`internal/plugin/features.go` has two interaction scope-cleanup sites:

1. registration rollback;
2. normal returned feature cleanup.

Both currently call:

```go
registry.interactions.CancelScope(scope)
```

and both discard all durable-delete detail because `CancelScope` returns only a count.

Normal `Manager.Disable` runs feature cleanup through the existing lifecycle callback executor and already has an `errs` / `teardownErrors` path. D1 should reuse that lifecycle failure model rather than creating another teardown-error registry.

Full manager shutdown is intentionally different:

- `Manager.ShutdownWithContext` first calls `PreserveDurableOnShutdown`;
- feature cleanup then cancels in-memory interaction scopes;
- durable rows are intentionally retained for restart.

D1 must preserve that distinction.

#### D0.3 — Durable delete failure reproduction from current source

The current removal path is:

```text
removeLocked
→ durable.Delete(context.Background(), id)
→ on error increment PersistenceErrors
→ return false
```

The returned `false` is consumed by `CancelScope` only as a removal count decision. No error reaches `featureCleanup`, `Manager.Disable`, or `teardownErrors`.

Therefore the following failure is reproducible directly from the current control flow:

```text
durable row exists
→ normal feature Disable
→ registration cleanup closes feature catalog entry
→ CancelScope attempts durable Delete
→ Delete fails
→ session remains in runtime / durable row remains in DB
→ cleanup callback itself returns nil
→ Manager.Disable has no durable-cleanup error to record
```

This is the concrete D1 target.

Regression tests to add with D1:

- `TestDurableCancelScopeDeleteFailureIsObservable`;
- `TestManagerDisableFailsWhenDurableCleanupFails`;
- `TestManagerDisableDurableFailureBlocksReenableAsIncompleteTeardown`;
- `TestDurableScopeDeleteUsesLifecycleContext`.

Exact names may be adjusted to repository style, but all four semantics must be covered.

#### D0.4 — Lifecycle context loss is confirmed

The durable delete call is hard-coded to:

```go
context.Background()
```

inside `removeLocked`.

No caller context can currently reach the store deletion operation. This is true even when `Manager.Disable(ctx, ...)` is using a deadline/canceled lifecycle context.

D1 must make correctness-sensitive scope cleanup context-aware. Do not add a deletion worker or detached goroutine.

#### D0.5 — Expiry bookkeeping failure is confirmed

`pruneExpiredLocked` currently performs the critical sequence:

```text
heap.Pop(expiry item)
→ entry.expiry = nil
→ removeLocked(id, ErrExpired)
```

If `removeLocked` cannot delete the durable row:

- it returns `false`;
- the session remains in `r.sessions`;
- normal accounting remains retained;
- the expiry item is already gone;
- `entry.expiry` is nil.

The session is therefore expired but no longer represented in the expiry heap for later lazy retry.

D2 regression tests must include:

- `TestDurableExpiryDeleteFailureRemainsTracked`;
- `TestDurableExpiryDeleteRetryRemovesExactlyOnce`;
- `TestDurableExpiryDeleteFailurePreservesCapacityAccounting`;
- an input-claim variant if the implementation can retain an input claim at expiry.

The test should explicitly inspect heap/session/accounting invariants from the `interaction` package rather than testing only the public error result.

#### D0.6 — Schema ownership gap is confirmed

Current application startup in `internal/app/app.go` performs:

```go
interactionStore := interactionsqlite.NewStore(coreDeps.db.DB)
interactionStore.InitSchema(context.Background())
```

before the later built-in feature migration call.

`internal/app/modules.go:migrateBuiltinFeatures` currently aggregates migration providers for existing built-in services/modules but does **not** include an interaction-session migration provider.

Therefore `interaction_sessions` is outside the namespaced migration/checksum/adoption history required by ADR 0005.

D3 must cover both database populations:

- databases created before durable sessions existed;
- databases already touched by `d0b6659e...`, where `interaction_sessions` exists but there is no interaction feature-migration record.

No destructive recreate/drop is acceptable merely to acquire migration ownership.

#### D0.7 — Existing tests that already protect correct durability semantics

The current suite already contains useful success/fail-closed coverage, including:

- `TestDurableWriteFailureDoesNotAdvanceState`;
- `TestDurableInputWriteFailureDoesNotReserveClaim`;
- `TestDurableSessionSurvivesRuntimeRestart`;
- `TestDurableSessionRejectsChangedVersion`;
- `TestDurableSessionUpdateSurvivesRestart`;
- `TestDurableSessionCancelRemovesStoredRow`;
- `TestDurableSessionCancelAfterFeatureUnregisterRemovesStoredRow`;
- `TestDurableShutdownPreservesStoredRow`;
- `TestDurableInputClaimSurvivesRestart`;
- `TestDurableRestoreRejectsExcessSessions`;
- `TestManagerInteractionRuntimeFollowsPluginLifecycle`.

The missing coverage is specifically the **delete-failure lifecycle path**, **context propagation**, and **failed expiry cleanup bookkeeping**. D1/D2 should extend the existing test doubles instead of introducing an unrelated testing framework.

#### D0.8 — Test/API drift checklist frozen for D1/D2

If D1 changes any of:

- `removeLocked`;
- `Cancel`;
- `CancelScope`;
- feature cleanup callback shape;
- durable store cleanup helpers;

the same phase must search and update:

- `internal/interaction/*_test.go`;
- `internal/plugin/*_test.go`;
- architecture tests referencing those APIs;
- mocks/fakes implementing the affected interface;
- benchmarks, if any reference the old signature.

Run `gofmt` on every changed Go/test file before commit.

#### D0 gate

D0 is closed because:

- exact HEAD was refreshed and recorded;
- no production behavior changed;
- the durable-delete error-loss path is identified end-to-end;
- the context-loss path is identified;
- the expiry orphaning sequence is identified;
- schema bootstrap outside feature migrations is identified;
- direct removal and lifecycle callers are inventoried;
- existing and missing test coverage are explicitly separated;
- exact D1/D2 regression targets are recorded.

Execution caveat: this D0 closure is based on current-source audit, not a claimed local test run. The next phase must refresh HEAD again before coding.

### D1 — Make durable deletion error-bearing and lifecycle-aware

Status: **IMPLEMENTED — source/test changes complete at `0db750308d5151a7583cd963787bda9814964325`; local package execution still pending because the current AI environment has no repository checkout/network path**

D1 implementation baseline:

- Phase start HEAD: `d545bb3cd385da4b9315474eb394799920f26ee9`
- Source/test implementation HEAD: `0db750308d5151a7583cd963787bda9814964325`
- CI: **not inspected**
- No second runtime, callback protocol, TaskEngine, cleanup worker, or persistence queue was introduced.

Implementation commits:

- `80205ae3372eb101c65e8ab5b96db221eea42ab4` — context-aware internal durable removal;
- `9534f19b745b917d7398ff2897f8c53d74607e05` — error-bearing `CancelScopeContext` plus compatibility `CancelScope`;
- `66459b401b2540bc44720113bbddce146bd33ae4` — context/error-bearing feature cleanup;
- `862f3a88a6a72e13d410e0c755934e4c7abfdd22` — plugin lifecycle propagation into existing `teardownErrors`;
- `eeb5fd8b5bcd8d00231f385770c8216fea380fdd` — durable scope-cleanup regression coverage;
- `f92243a5d53fceae4221d52bc4b44fde95fc3b90` — failed durable disable + blocked re-enable coverage;
- `31619b0cfa831ce2d29bb702c9dd873bd1a2246e` — full-shutdown durable preservation coverage;
- `0db750308d5151a7583cd963787bda9814964325` — direct lifecycle-context cancellation coverage.

#### D1.1 — Context-aware durable removal

The runtime now has an error-bearing internal path:

```go
removeLockedContext(ctx context.Context, id string, cause error) (bool, error)
```

The existing `removeLocked` remains as an internal compatibility wrapper using `context.Background()` for callers whose API has not yet been changed.

This keeps D1 scoped: lifecycle teardown can propagate durable failures now, while D2 can separately repair lazy expiry semantics without forcing an unrelated public API break.

#### D1.2 — Error-bearing scope cancellation

The runtime now exposes:

```go
CancelScopeContext(ctx context.Context, scope tasks.ScopeIdentity) (int, error)
```

It:

- passes the supplied context to `DurableStore.Delete`;
- attempts every session in the scope;
- counts successful removals;
- joins durable deletion errors;
- leaves sessions whose durable delete failed retained in memory;
- increments existing persistence-error diagnostics through the canonical removal path.

The existing:

```go
CancelScope(scope tasks.ScopeIdentity) int
```

remains as a compatibility wrapper. Production plugin lifecycle no longer depends on that compatibility-only path.

#### D1.3 — Feature cleanup is now context/error-bearing

`registerFeatureContract` now receives the lifecycle context and returns a cleanup function of shape:

```go
func(context.Context) error
```

Feature cleanup still preserves the original ordering:

1. native cleanup;
2. saved-response cleanup;
3. inline registrations;
4. feature catalog registration;
5. action scope;
6. interaction sessions.

The interaction step now calls `CancelScopeContext` and returns any durable deletion error instead of discarding it.

#### D1.4 — Manager lifecycle uses existing incomplete-teardown semantics

`Manager.featureCleanups` now stores context/error-bearing cleanup callbacks.

Normal `Manager.Disable` executes feature cleanup through the existing bounded lifecycle callback executor and appends any error to its ordinary teardown error list.

Therefore a durable DELETE failure now results in:

```text
Disable
→ feature cleanup
→ CancelScopeContext
→ DurableStore.Delete fails
→ error returns through runLifecycleCallback
→ Disable returns error
→ teardownErrors[plugin] records incomplete teardown
→ Enable refuses the plugin generation
```

No second teardown-error registry was added.

This closes the original resurrection-risk contract: a disable operation cannot report success while a durable interaction row failed to delete.

#### D1.5 — Full shutdown semantics remain distinct

`ShutdownWithContext` still calls:

```go
PreserveDurableOnShutdown()
```

before feature cleanup.

The new error-bearing feature cleanup therefore removes in-memory sessions on shutdown without issuing durable DELETE operations.

A plugin-level regression test now checks that:

- shutdown performs zero durable deletes;
- the durable row remains present;
- runtime in-memory sessions/state settle to zero.

This preserves restart recovery while keeping normal disable fail-closed.

#### D1.6 — Context propagation and cancellation coverage

The interaction tests now cover both:

- context identity/value propagation into `DurableStore.Delete`;
- an actual cancellation path where a test store blocks on `ctx.Done()` and `CancelScopeContext` returns `context.Canceled`.

This proves the old unconditional `context.Background()` lifecycle behavior is no longer used for scope teardown.

#### D1.7 — Regression coverage added

D1 adds coverage equivalent to the planned targets:

- `TestDurableCancelScopeDeleteFailureIsObservable`;
- `TestDurableScopeDeleteUsesLifecycleContext`;
- `TestDurableScopeDeleteHonorsContextCancellation`;
- `TestManagerDisableFailsWhenDurableCleanupFails`;
- `TestManagerShutdownPreservesDurableSessions`.

The manager disable test also proves:

- the plugin is marked disabled after failed teardown;
- the durable row remains, rather than being falsely reported removed;
- the runtime records the persistence failure;
- the old session is stale after feature registration is detached;
- re-enable is rejected through the existing incomplete-teardown error.

#### D1.8 — Compatibility/caller classification

D1 intentionally does not change every removal caller.

Current classification:

- **normal plugin lifecycle:** migrated to `CancelScopeContext`;
- **full shutdown:** migrated through the same context/error-bearing feature cleanup, but `PreserveDurableOnShutdown` suppresses durable DELETE by design;
- **legacy/general `CancelScope` callers:** compatibility wrapper retained;
- **single-session `Cancel`:** unchanged because D0 found no production lifecycle path requiring its boolean API to become the D1 teardown authority;
- **expiry, stale-resolution, and expired input/state checks:** still use the existing internal compatibility removal path and belong to D2's retry/accounting repair.

This prevents D1 from accidentally solving D2 by changing lazy-expiry behavior without its dedicated tests.

#### D1.9 — Formatting and validation status

The changed Go blocks and new tests were run through `gofmt` during implementation, including the new runtime, lifecycle, and test blocks. Source was re-read after publication to check imports/signatures and stale-call-site drift in the affected interaction/plugin packages.

A full local repository checkout is still unavailable in this AI environment because direct GitHub hostname resolution is unavailable. Therefore D1 does **not** claim successful local `go test`, `go test -race`, or `go vet` execution. CI was not inspected.

Required execution gate before changing D1 to **CLOSED**:

```text
gofmt -w internal/interaction/runtime_internal.go \
          internal/interaction/session.go \
          internal/interaction/durable_test.go \
          internal/plugin/features.go \
          internal/plugin/manager.go \
          internal/plugin/interaction_runtime_test.go

gofmt -l internal/interaction/runtime_internal.go \
          internal/interaction/session.go \
          internal/interaction/durable_test.go \
          internal/plugin/features.go \
          internal/plugin/manager.go \
          internal/plugin/interaction_runtime_test.go

go test ./internal/interaction ./internal/plugin
go test -race ./internal/interaction ./internal/plugin
go vet ./internal/interaction ./internal/plugin
git diff --check
```

`gofmt -l` must print nothing. If any API/test compile drift is found, fix both production and test callers in the same D1 follow-up before closure.

D1 implementation is complete, but its final status remains **IMPLEMENTED** rather than **CLOSED** until the local execution gate above is run.

### D2 — Repair expiry/remove invariants under persistence failure

Status: **IMPLEMENTED — source/test changes complete at `66d620ddf64cc060cad1d591bdb9b69c18d1ce93`; focused package execution still pending because the current AI environment cannot clone the repository**

D2 refresh:

- Phase start HEAD: `fa717df61ffc75697d17133a72aa46163b6cf2fa`
- Phase start commit: `fmt`
- Source fix: `3ca562a1804ede9ea76d664dc7cb512ad1285c01` — `fix(interaction): retain failed expiry cleanup for retry`
- Regression tests: `66d620ddf64cc060cad1d591bdb9b69c18d1ce93` — `test(interaction): cover durable expiry retry invariants`
- CI: **not inspected**

The phase started from the refreshed HEAD rather than the earlier D1 document HEAD. The intervening `fmt` commit was treated as authoritative current source.

#### D2.1 — Expiry removal now retries without losing heap ownership

The old sequence was:

```text
heap.Pop(expired item)
→ entry.expiry = nil
→ durable Delete
→ Delete failure leaves session retained but no heap item
```

The new prune algorithm processes expired items at most once per prune pass:

```text
pop expired item temporarily
→ validate session ↔ expiry-item ownership
→ attempt canonical removeLockedContext
    → success: session/accounting/context are removed normally
    → durable failure: keep the same expiry item in a pass-local retry list
→ continue processing other already-expired items
→ reinsert failed items after the pass
```

This preserves the invariant:

> a durable-delete failure may retain the expired session, but it cannot permanently detach that session from future lazy cleanup.

No retry worker, timer, ticker, or persistent retry queue was added.

#### D2.2 — No busy loop and no permanent head-of-line blocking

A failed expired row is attempted only once during one call to `pruneExpiredLocked`.

Failed items are reinserted only after the current expired-item pass completes. Therefore:

- the same failed row is not immediately popped and retried in a tight loop;
- another expired session behind it can still be reclaimed in the same pass;
- retry happens naturally on a later `PruneExpired`, `Stats`, `Create`, or another runtime operation that triggers lazy pruning.

The temporary retry slice is bounded by the runtime's configured maximum number of retained sessions and exists only for the duration of one prune call.

#### D2.3 — Accounting remains fail-closed while persistence is unavailable

On durable delete failure, the runtime intentionally retains:

- the session map entry;
- scope ownership;
- actor count;
- state-byte accounting;
- durable database row;
- expiry retry item.

That means failed durable cleanup still consumes configured capacity.

This is deliberate: capacity must not be released until the runtime has actually completed the authoritative removal.

Once the durable store recovers and retry succeeds, the canonical removal path releases:

- session;
- scope entry;
- actor count;
- state bytes;
- expiry item;
- session context.

#### D2.4 — Expired session remains unusable while retained for cleanup

A failed durable DELETE does not make an expired interaction usable again.

Public resolution still returns:

```text
ErrExpired
```

for the retained expired session.

Its heap ownership remains intact after that failed resolve-triggered cleanup attempt, so a later lazy prune can still retry deletion.

#### D2.5 — Input claim semantics remain bounded

Pending input expiry is clamped to the parent session deadline.

`PruneExpired` continues to prune expired input claims before attempting parent-session removal.

Therefore, when parent durable deletion fails:

- the expired input claim is removed from the in-memory input index;
- the expired parent session remains retained only for durable cleanup retry;
- state/accounting remains retained until parent removal succeeds;
- the persisted input deadline is already expired and therefore is not eligible for restore as a live claim.

No duplicate input reservation is introduced.

#### D2.6 — Regression coverage added

A dedicated `internal/interaction/durable_expiry_test.go` now covers:

- `TestDurableExpiryDeleteFailureRemainsTracked`
  - durable delete failure leaves the same session represented in the expiry heap;
  - scope/actor/state accounting remains intact;
  - `Expired` is not incremented before successful removal;
  - public resolve still returns `ErrExpired`.

- `TestDurableExpiryDeleteRetryRemovesExactlyOnce`
  - repeated failed prune passes keep exactly one heap entry;
  - one delete attempt occurs per prune pass;
  - recovery removes the session once;
  - later prune does not delete the same session again.

- `TestDurableExpiryDeleteFailurePreservesCapacityAccounting`
  - failed cleanup keeps capacity occupied;
  - after durable recovery and successful prune, capacity becomes available again.

- `TestDurableExpiryDeleteFailureCleansExpiredInputClaim`
  - expired pending input is cleared even when parent durable deletion fails;
  - parent session remains expiry-tracked;
  - later successful cleanup settles session/input/state accounting to zero.

- `TestDurableExpiryDeleteFailureDoesNotBlockOtherExpiredSessions`
  - one selected durable row can fail deletion;
  - a separate expired session behind it is still reclaimed in the same prune pass;
  - only the failed row is reinserted.

#### D2.7 — Go formatting rule

Before each D2 Go commit, the changed Go source was materialized from the refreshed repository source and run through `gofmt`.

The final D2 files were checked with:

```text
gofmt -w internal/interaction/runtime_internal.go
gofmt -w internal/interaction/durable_expiry_test.go
gofmt -l internal/interaction/runtime_internal.go internal/interaction/durable_expiry_test.go
```

The equivalent materialized files produced no output from `gofmt -l`.

The repository could not be cloned in the current execution environment because `github.com` DNS resolution fails. D2 therefore does **not** claim a successful `go test`, `go test -race`, or `go vet` run.

Required local execution gate before changing D2 from **IMPLEMENTED** to **CLOSED**:

```text
gofmt -w internal/interaction/runtime_internal.go \
          internal/interaction/durable_expiry_test.go

gofmt -l internal/interaction/runtime_internal.go \
          internal/interaction/durable_expiry_test.go

go test ./internal/interaction
go test -race ./internal/interaction
go vet ./internal/interaction
git diff --check
```

`gofmt -l` must print nothing.

#### D2 gate status

Implemented invariants:

- failed durable expiry deletion remains retryable;
- no duplicate heap item is created across failed prune passes;
- no tight retry loop was introduced;
- another expired session is not permanently blocked behind one failed row;
- retained scope/actor/state capacity remains accurate;
- expired input claims do not remain reserved;
- successful retry increments expiry/removal accounting exactly once;
- no new worker/goroutine or unbounded retained retry state exists.

D2 remains **IMPLEMENTED** rather than **CLOSED** until the focused local execution gate is run on a full repository checkout.

### D3 — Move interaction schema into namespaced migrations

Status: **IMPLEMENTED — namespaced migration ownership landed at `cd167d64a28dd6c9e76feb1bb909c00d97f98a54`; focused local package execution is still pending because the current AI environment cannot clone the repository**

D3 refresh:

- Phase start HEAD: `c6e7c6a97b26b740f4824eef44b33568a69ddd1d`
- Phase start commit: `test(architecture): update context-aware feature cleanup invariants`
- Implementation commit: `cd167d64a28dd6c9e76feb1bb909c00d97f98a54`
- Commit message: `refactor(interaction): migrate durable session schema ownership`
- CI: **not inspected**

D3 was committed atomically from the refreshed phase-start HEAD. Production schema ownership, application wiring, store tests, migration tests, and the app-level migration regression were included in the same Go-changing commit so the branch was not intentionally left in an intermediate state with stale callers.

#### D3.1 — Interaction-owned namespaced migration

`internal/interaction/sqlite` now owns:

```go
type MigrationProvider struct{}
```

with migration:

```text
interaction.001
```

The migration owns the canonical `interaction_sessions` schema and declares no legacy integer migration because durable A2 sessions were originally introduced through direct runtime schema initialization rather than through historical `schema_migrations`.

Its checksum is fixed and namespaced migration metadata is recorded through the existing `database.RunFeatureMigrations` contract.

#### D3.2 — Physical schema verification

`migration001` implements:

```go
database.SchemaInvariantMigration
```

and validates the physical SQLite table through:

```sql
PRAGMA table_info('interaction_sessions')
```

The verifier checks the current baseline contract for all 12 columns:

- `id`;
- `feature_id`;
- `version`;
- `actor_id`;
- `chat_id`;
- `message_id`;
- `inline_message_id`;
- `state`;
- `revision`;
- `created_at`;
- `expires_at`;
- `input_expires_at`.

It also checks the expected SQLite type/nullability/primary-key/default characteristics.

The baseline SQLite shape was validated directly before publication, including the SQLite-specific facts that:

- `id TEXT PRIMARY KEY` is reported by `PRAGMA table_info` with `notnull=0` and `pk=1`;
- `input_expires_at` reports default `0`.

#### D3.3 — Baseline direct-created databases are adopted without destructive recreation

Databases already touched by the original durable-session implementation may contain:

```text
interaction_sessions
```

but no:

```text
feature_schema_migrations.id = interaction.001
```

D3 handles that population through the normal namespaced migration transaction:

```text
RunFeatureMigrations
→ interaction.001 Up
→ CREATE TABLE IF NOT EXISTS interaction_sessions
→ physical schema verification
→ record interaction.001
```

Because table creation is idempotent, a compatible baseline table is not dropped or recreated.

The migration explicitly verifies the table **before** returning from `Up`; therefore a malformed preexisting table cannot be silently recorded as migrated merely because `CREATE TABLE IF NOT EXISTS` was a no-op.

This is intentionally different from legacy-integer adoption: there is no historical integer migration to claim.

#### D3.4 — Store is no longer a schema bootstrapper

`internal/interaction/sqlite/store.go` no longer exposes:

```go
Store.InitSchema
```

and no longer contains the interaction-session `CREATE TABLE` statement.

The store is again only the durable-session persistence adapter:

- `Save`;
- `Delete`;
- `Load`.

Schema declaration and migration policy now live in the migration provider owned by the same package.

#### D3.5 — Application startup uses the canonical migration runner

`internal/app/modules.go:migrateBuiltinFeatures` now includes:

```go
interactionsqlite.MigrationProvider{}
```

in the built-in provider list.

The provider capacity was updated accordingly.

`internal/app/app.go` no longer performs the old interaction-specific:

```text
interactionStore.InitSchema(...)
```

bootstrap.

The interaction store can still be attached to the runtime before built-in migrations execute because no durable load occurs at that point. `RestoreDurable` remains later in startup, after `migrateBuiltinFeatures` has completed.

#### D3.6 — Tests were migrated with the production API

The old store tests that called `InitSchema` were updated in the same D3 commit.

`internal/interaction/sqlite/store_test.go` now uses:

```go
database.RunFeatureMigrations(ctx, db, MigrationProvider{})
```

before exercising the store.

This applies to:

- runtime restart callback persistence;
- store round-trip;
- empty-state persistence.

No current interaction SQLite test is intentionally left on the removed schema-bootstrap API.

#### D3.7 — Migration regression matrix

`internal/interaction/sqlite/migration_test.go` adds explicit coverage for:

- fresh database creation through `interaction.001`;
- idempotent repeated `RunFeatureMigrations`;
- compatible baseline table with no migration record;
- preservation of an existing durable session row during baseline adoption;
- malformed preexisting table rejection;
- ensuring malformed schema is **not** recorded as successfully migrated.

An additional app-level test:

```text
TestBuiltinFeatureMigrationsIncludeInteractionSessions
```

proves the application migration aggregation creates both:

- the `interaction_sessions` table;
- the `interaction.001` migration record.

#### D3.8 — Stale-caller audit

Before implementation, the current `test-next` branch was audited for interaction `InitSchema` callers.

The interaction-specific occurrences were limited to:

- `internal/app/app.go`;
- `internal/interaction/sqlite/store.go`;
- `internal/interaction/sqlite/store_test.go`.

All of those interaction-specific uses were removed or migrated in D3.

Other unrelated `InitSchema` APIs in other subsystems, such as jobs/idempotency/storage, are outside D3 and were not changed.

#### D3.9 — Formatting and validation discipline

Before the D3 Go commit, the newly created/replaced D3 Go files were run through `gofmt` and the local formatting gate produced no output from `gofmt -l`.

The final D3 changed-file set is:

```text
internal/app/app.go
internal/app/interaction_migration_test.go
internal/app/modules.go
internal/interaction/sqlite/migration.go
internal/interaction/sqlite/migration_test.go
internal/interaction/sqlite/store.go
internal/interaction/sqlite/store_test.go
```

`app.go` changed only by removing the three-line `InitSchema` error block; no replacement syntax was introduced there. Because that file had not been passed through `gofmt` before the atomic commit, the exact committed file was reconstructed after the commit and validated explicitly:

```text
sha256 before gofmt: c17cc74c46c3b6af948e3e650cb435119bc63c31a0b4c0d0ad7d76c6daa20969
gofmt -w internal/app/app.go
gofmt -l internal/app/app.go   # no output
sha256 after gofmt:  c17cc74c46c3b6af948e3e650cb435119bc63c31a0b4c0d0ad7d76c6daa20969
cmp before after              # identical
```

This confirms the committed bytes were already canonical Go formatting, while preserving the audit fact that this particular validation happened post-commit. Future Go-changing commits must run `gofmt` on **every** changed Go file before commit, with no format-neutral exception.

The source branch was re-read after the atomic commit to verify:

- no interaction `InitSchema` remains in `app.go`;
- `store.go` contains no schema bootstrapper;
- `store_test.go` uses the migration runner;
- `modules.go` includes the interaction provider;
- migration and app regression tests reference `interaction.001`.

The current AI environment still cannot clone `github.com/inipew/goultroid` because DNS resolution for `github.com` fails. Therefore D3 does **not** claim successful execution of `go test`, `go test -race`, or `go vet`.

Required local execution gate before changing D3 from **IMPLEMENTED** to **CLOSED**:

```text
gofmt -w internal/app/app.go \
          internal/app/interaction_migration_test.go \
          internal/app/modules.go \
          internal/interaction/sqlite/migration.go \
          internal/interaction/sqlite/migration_test.go \
          internal/interaction/sqlite/store.go \
          internal/interaction/sqlite/store_test.go

gofmt -l internal/app/app.go \
          internal/app/interaction_migration_test.go \
          internal/app/modules.go \
          internal/interaction/sqlite/migration.go \
          internal/interaction/sqlite/migration_test.go \
          internal/interaction/sqlite/store.go \
          internal/interaction/sqlite/store_test.go

go test ./internal/interaction/sqlite ./internal/app
go test -race ./internal/interaction/sqlite ./internal/app
go vet ./internal/interaction/sqlite ./internal/app
git diff --check
```

`gofmt -l` must print nothing.

#### D3 gate status

Implemented invariants:

- `interaction_sessions` is owned by `interaction.001`;
- fresh databases create the schema through the namespaced migration runner;
- baseline durable databases can acquire migration ownership without row loss;
- malformed baseline schema fails closed before a migration record is written;
- application startup no longer calls interaction `Store.InitSchema`;
- interaction store tests use the same migration path as production;
- application migration aggregation has direct regression coverage;
- no second migration runner or schema authority was introduced;
- CI was not inspected.

D3 remains **IMPLEMENTED** rather than **CLOSED** until the focused local execution gate is run on a full repository checkout.

### D4 — Add real feature restart acceptance

Status: **CLOSED** — local acceptance gate passed on `e19d270cd28a7b87c97b5cafb601a8169544f6c3` (`docs(interaction): harden durable session execution discipline`).

D4 implementation commits: `63e8c29a` (`test(interaction): add durable feature restart acceptance`), `e870471e` (`test(interaction): fix durable restart acceptance tests`), and `0d2308be` (`test(myxl): complete D4 durable restart acceptance`). Validation on this checkout ran `gofmt -w` and `gofmt -l` on all four D4 Go files (empty `gofmt -l` output); `go test ./internal/interaction ./internal/interaction/sqlite ./internal/plugin ./internal/app ./internal/assistant/client ./plugins/calculator ./plugins/settings ./plugins/myxl ./internal/architecture` (all nine packages passed); `go test -race ./...` (passed); `go vet ./...` (passed); and `git diff --check` (passed). The working tree had no Go changes after formatting. CI was not inspected.

Current implementation snapshot before this documentation update:

- Source HEAD: `0d2308be258ac7059d4efd3d2dcf4c0a04ab9290` — `test(myxl): complete D4 durable restart acceptance`.
- Assistant Shell / Help restart acceptance exists.
- Settings restart acceptance exists.
- Calculator restart acceptance exists.
- MyXL restart coverage now exercises real summary/navigation, saved-package navigation, still-valid confirmation, quote revalidation, no processing replay, and pending-input restore semantics.
- This snapshot is **not** acceptance evidence. D4 remains blocked until the required local commands pass on a full checkout.
- Do not start D5 merely because these tests are present in source.
- CI must remain uninspected unless the user explicitly asks.

Goal:

> every current nonempty `DurabilityVersion` is backed by a real feature compatibility test.

Required feature coverage:

#### Assistant Shell / Help

Prove a real persisted shell/help session can:

- survive runtime reconstruction;
- rebind to the new generation;
- resolve an old Telegram callback;
- decode its actual state;
- execute the intended navigation action;
- produce the next valid state/view.

Include a stale-revision negative case.

#### Settings

Prove a real Settings A2 session can survive restart and continue a representative navigation/mutation flow.

Do not merely assert `DurabilityVersion == "1"`.

#### Calculator

Prove a representative callback-heavy calculator state survives restart and the old callback resolves against restored state.

#### MyXL

Cover at least:

- summary/navigation state;
- saved-package navigation;
- a still-valid confirmation state where the existing semantics permit continuation.

Also assert:

- in-flight purchase work is **not** replayed by restore;
- already-running asynchronous work is not reconstructed;
- existing quote refresh/idempotency protections remain authoritative.

#### Pending input

Where a current durable feature arms an input session, prove:

- a live input deadline survives restart;
- expired input is not re-armed;
- actor/chat exclusivity remains correct.

Gate:

- each current durability opt-in has semantic restart coverage;
- no test relies only on the generic `demo` catalog;
- all D4 changed Go/test files are `gofmt`-clean;
- focused D4 packages compile and pass;
- repository-wide `go test -race ./...` passes, or any pre-existing unrelated failure is explicitly isolated and proven unrelated before D4 closure;
- `go vet ./...` passes;
- `git diff --check` passes;
- no D4 commit is followed by a formatting/compile/test repair that should have been caught by the pre-push gate.

Minimum D4 validation before closure:

```text
gofmt -w <all D4 changed Go files>
gofmt -l <all D4 changed Go files>   # must print nothing

go test ./internal/interaction
go test ./internal/interaction/sqlite
go test ./internal/plugin
go test ./internal/app
go test ./internal/assistant/client
go test ./plugins/calculator
go test ./plugins/settings
go test ./plugins/myxl
go test ./internal/architecture

go test -race ./...
go vet ./...
git diff --check
```

Do not weaken this gate to source inspection because the current workstream has already demonstrated that unused imports, misspelled symbols, stale expectations, missing test helpers, formatting drift, and race/test regressions can otherwise reach the branch.

### D5 — Fence durability declarations and compatibility changes

Status: **OPEN**

Goal:

> future durability opt-ins or version changes cannot silently bypass restart compatibility evidence.

Tasks:

- add an architecture or package-level test that inventories current nonempty durability declarations;
- keep the fence narrow enough not to encode all feature business logic centrally;
- document where a feature must add/update restart tests when changing `DurabilityVersion`;
- ensure a version bump intentionally invalidates old rows in generic restore tests.

Possible acceptance model:

- a feature-specific test file owns its compatibility proof;
- a central architecture fence owns only the declared durable-feature inventory/version expectation.

Do not create a global decoder registry just for testing.

Gate:

- adding a new durable feature requires an explicit test/update;
- changing a version cannot be accidental.

### D6 — Measure durable mutation latency and lock contention

Status: **OPEN**

Goal:

> determine whether SQLite-under-`Runtime.mu` is acceptable before considering a more complex concurrency design.

Do not optimize before measurement.

Benchmarks should include representative operations such as:

- durable `Create`;
- durable `UpdateState`;
- callback transition/update;
- `BindTarget`;
- `ArmInput` / `TakeInput`;
- scope cleanup with multiple durable sessions;
- mixed actors under concurrent callback pressure.

Measure where practical:

- ns/op or ms/op;
- allocations/op;
- p50/p95/p99 for a bounded concurrent workload;
- throughput;
- behavior during SQLite writer contention;
- whether unrelated actors suffer unacceptable serialization;
- cleanup latency for a scope near the configured per-scope limit.

Use the repository's existing SQLite settings and default cache unless there is a reason to document another configuration.

Do **not** respond to a slow benchmark by automatically adding:

- a write-behind queue;
- persistence worker pool;
- second runtime;
- optimistic in-memory commit before durable commit.

If results are acceptable, record the synchronous design as deliberate.

If results are not acceptable, stop and produce a separate design note for a safe transactional/CAS approach before changing concurrency ownership.

Gate:

- performance characteristics are documented;
- no speculative architecture rewrite.

### D7 — Final local acceptance and document closure

Status: **OPEN**

Refresh HEAD again before final acceptance.

Required checks should include the relevant current packages. Adjust exact package list to current source layout, but at minimum cover:

```text
internal/interaction
internal/interaction/sqlite
internal/feature
internal/plugin
internal/app
plugins/calculator
plugins/settings
plugins/myxl
internal/assistant/shell
internal/architecture
```

Required validation:

```text
gofmt -w <all changed Go files>
gofmt -l <all changed Go files>   # must print nothing
git diff --check

go test ./internal/interaction ./internal/interaction/sqlite
go test ./internal/feature ./internal/plugin ./internal/app
go test ./internal/assistant/shell ./plugins/calculator ./plugins/settings ./plugins/myxl
go test ./internal/architecture
go vet ./...
```

Run selected `-race` tests for the changed runtime/lifecycle packages when the environment permits.

If the broader repository suite is run and has failures:

- record them accurately;
- do not describe the suite as green;
- classify whether they are introduced by this work;
- do not hide unrelated failures.

**Do not inspect CI unless the user explicitly asks.**

Final document update must record:

- final HEAD;
- implementation commits;
- exact commands run;
- pass/fail outcomes;
- any remaining non-blocking debt;
- whether D0–D7 are closed.

---

## 8. Test matrix

The final test matrix should cover at least the following.

| Area | Required behavior |
| --- | --- |
| Create persistence | durable row exists before state is published |
| Update persistence | write failure leaves revision/state unchanged |
| Bind target | target persistence survives restart |
| Touch | expiry persistence survives restart |
| Arm input | failed write does not reserve input |
| Take input | revision/input clearing persists before publication |
| Release input | durable failure does not falsely report successful release |
| Cancel session | success deletes row |
| Cancel scope | failure is returned to lifecycle caller |
| Disable feature | successful disable cannot leave restorable rows |
| Process shutdown | durable rows intentionally survive |
| Expiry | failed delete remains retryable and bounded |
| Restore version | mismatched version is rejected/deleted |
| Restore capacity | excess rows are rejected deterministically |
| Binding | actor/target mismatch still fails closed |
| Revision | old callback after state advance stays stale |
| Shell | real Help/navigation state survives restart |
| Settings | representative state survives restart |
| Calculator | callback state survives restart |
| MyXL | representative navigation/confirmation state survives restart |
| Input | live claim survives; expired claim does not |
| Migration fresh | schema is created by namespaced migration |
| Migration upgrade | old DB upgrades without data loss |
| Migration adoption | baseline direct-created table is safely adopted |
| Resource | no new worker/goroutine is introduced |
| Performance | synchronous durable critical section is measured |

---

## 9. Failure semantics to preserve

### 9.1 Persistence Save failure

For state transitions:

```text
Save fails
→ do not mutate in-memory state
→ return the persistence error
→ count diagnostic failure
```

Do not convert a correctness failure into a warning-only metric.

### 9.2 Persistence Delete failure during normal feature teardown

Target:

```text
Delete fails
→ session is not falsely declared fully removed
→ lifecycle receives error
→ feature teardown is marked incomplete
→ restart must not silently resurrect after a supposedly successful disable
```

### 9.3 Persistence Delete failure during lazy expiry

Target:

```text
Delete fails
→ expired session remains cleanup-tracked
→ no action dispatch is permitted from it
→ later prune may retry
→ no tight retry loop
```

### 9.4 Process shutdown

Target:

```text
quiesce TaskEngine ownership
→ mark durable rows for preservation
→ detach feature surfaces
→ release in-memory sessions/contexts
→ close runtime
→ preserve durable rows
```

Do not replay in-flight work on the next startup.

---

## 10. Multi-instance boundary

This plan intentionally stops at reliable single-active-process restart durability.

A future active-active design would need a separate architecture review for at least:

- shared callback admission;
- atomic revision compare-and-swap;
- distributed action claims;
- side-effect idempotency ownership;
- lease expiry/recovery;
- cross-instance cleanup;
- database topology appropriate for multiple writers.

The existing `DurableStore.Save` upsert is not enough.

Do not add partial multi-instance behavior inside D0–D7.

If multi-instance is requested later, the preferred direction is to extend the existing canonical runtime/store contract with shared transactional fencing rather than creating another interaction engine.

---

## 11. Non-goals

Do not use this work to:

- redesign A2 token syntax;
- redesign Assistant UI;
- change Help navigation structure;
- change Settings UX;
- change MyXL package/purchase UX;
- replay in-flight purchase tasks;
- add distributed HA;
- move callback execution out of TaskEngine;
- add a persistence polling loop;
- add an interaction cleanup worker pool;
- rewrite unrelated SQLite repositories;
- refactor every plugin lifecycle API;
- fix unrelated repository-wide test debt;
- inspect CI without explicit user instruction.

---

## 12. Risks to watch during implementation

### Lifecycle partial-failure risk

Feature registration may already be detached when durable deletion fails.

The manager must not pretend teardown is complete if retry/recovery is still required.

Reuse existing incomplete-teardown semantics where possible.

### Lock-order risk

Changing removal/error propagation may introduce new lock ordering between:

- interaction runtime mutex;
- feature registry lock;
- plugin manager lock;
- SQLite calls.

Audit lock boundaries deliberately.

Do not call back into plugin lifecycle while holding `Runtime.mu`.

### Context propagation risk

A canceled lifecycle context can cause a durable delete to fail immediately.

That failure must be classified correctly rather than silently converted into successful cleanup.

### Heap consistency risk

Expiry heap mutation and session-map removal must remain exactly synchronized.

Tests should assert both successful and failed-delete paths.

### Migration adoption risk

Some users may already have `interaction_sessions` created by the baseline direct `InitSchema`.

A new migration must adopt compatible existing schema without data loss and reject incompatible schema clearly.

### Feature state compatibility risk

A feature can keep the same `DurabilityVersion` while accidentally changing state interpretation.

Feature-level restart tests are the primary regression guard.

### Performance risk

Removing the runtime lock around durable I/O without a complete transactional design can create memory/DB divergence.

Do not trade correctness for benchmark numbers.

### Test drift risk

Renaming a method in production while leaving tests on the old signature has repeatedly caused regressions in this repository.

Every phase that changes Go APIs must explicitly search and update:

- `*_test.go`;
- architecture fences;
- test fixtures;
- mocks;
- compile-time interface assertions;
- benchmark files.

Then run `gofmt` on all changed Go files before commit.

---

## 13. Suggested commit strategy

Keep commits narrow and attributable.

Suggested sequence:

1. `test(interaction): reproduce durable cleanup failure paths`
2. `fix(interaction): propagate durable scope cleanup errors`
3. `fix(interaction): preserve expiry cleanup on delete failure`
4. `refactor(interaction): migrate durable schema ownership`
5. `test(interaction): prove durable feature restart compatibility`
6. `test(architecture): fence durable interaction declarations`
7. `perf(interaction): benchmark durable session mutations`
8. `docs(interaction): close durable A2 hardening plan`

These are suggestions, not mandatory exact commit names.

For **every** commit that changes Go:

1. refresh HEAD if beginning a new phase;
2. make the scoped code + test changes in one worktree loop;
3. update stale tests/mocks/signatures in the same phase;
4. run `gofmt` on every changed Go file;
5. verify `gofmt -l` is empty for those files;
6. run the phase's focused tests until green;
7. run the phase-required race and vet gates **before** commit;
8. run `git diff --check`;
9. inspect status/diff for unrelated changes;
10. commit and push only after the gate is green;
11. refresh HEAD after push and record the actual commit;
12. **do not check CI unless the user asks**.

If steps 4–9 cannot be executed because there is no usable local checkout, stop before committing/pushing Go changes. Prepare the patch/handoff instead. Do not replace executable validation with static reasoning.

---

## 14. Definition of done

This hardening item is closed only when all of the following are true:

- normal feature disable cannot report success if durable session deletion failed;
- lifecycle context reaches correctness-sensitive durable deletion;
- a successful feature disable cannot leave a compatible session row that can resurrect on restart;
- failed expiry deletion does not orphan a session outside cleanup tracking;
- bounded session/scope/actor/state accounting stays correct after persistence failure;
- `interaction_sessions` is owned by the repository's namespaced migration system;
- an existing baseline database with the direct-created table upgrades/adopts safely;
- ad-hoc runtime schema initialization is removed from ordinary startup;
- Assistant Shell has real restart compatibility coverage;
- Settings has real restart compatibility coverage;
- Calculator has real restart compatibility coverage;
- MyXL has real restart compatibility coverage;
- live/expired pending-input restart semantics are tested where applicable;
- every current nonempty `DurabilityVersion` is covered by an explicit regression contract;
- durability performance/lock contention has been measured before any concurrency redesign;
- no second interaction runtime, callback protocol, TaskEngine, executor, or persistence worker was introduced;
- no in-flight feature task is replayed merely because its session was restored;
- all Go-changing commits had matching test updates where APIs changed;
- all changed Go/test files were formatted with `gofmt` before commit;
- focused local tests and relevant race tests pass, or any failure is explicitly documented and classified;
- CI was not inspected unless the user explicitly requested it;
- this document is updated with exact final HEAD, commits, validation commands, and closure status.

---

## 15. Recommended next action

Continue **D4 validation and stabilization**. Do **not** restart from D0 and do **not** begin D5 yet.

The next implementation session should:

1. refresh `test-next` HEAD and record exact SHA/message;
2. read the **Fast-start context** below plus D4 only;
3. inspect only commits that advanced beyond the snapshot and the directly affected D4 test/source files;
4. on a real local checkout, run `gofmt`/focused D4 tests first;
5. fix every compile, stale expectation, fixture/mock, race, or vet failure attributable to D4 in the same phase;
6. run the complete D4 gate, including `go test -race ./...`, `go vet ./...`, and `git diff --check`;
7. only after the gate is green, update D4 to `CLOSED`, record commands/results/exact HEAD, commit the documentation, and push;
8. then refresh HEAD again before starting D5.

Do not inspect CI unless the user explicitly asks.

---

## 16. Fast-start context for the next AI session

Use this section to avoid rebuilding context from the entire repository history.

### 16.1 What this work is

The active plan is:

`docs/design/goultroid-durable-a2-sessions-ai-plan.md`

Purpose:

> harden the existing canonical A2 durable-session implementation for lifecycle correctness, migration ownership, restart compatibility, bounded retention, and measured performance without introducing a second runtime/executor/persistence worker.

Canonical boundaries remain:

- one A2 interaction runtime;
- one `a2:<action>:<session>.<revision>` callback protocol;
- one feature registry;
- TaskEngine as finite-work execution authority;
- one durable session representation for opted-in features;
- single-active-process restart durability only; active-active is out of scope.

### 16.2 Progress already made

Do not redo these phases from scratch:

- **D0**: contract/source audit completed.
- **D1**: durable deletion was made error-bearing/lifecycle-aware.
- **D2**: expiry/remove retry invariants were hardened under persistence failure.
- **D3**: interaction schema ownership was moved to namespaced migrations; application startup no longer relies on ad-hoc interaction schema bootstrap.
- **D4**: feature-level restart tests have been implemented, but acceptance is **not yet closed** because local validation has not been proven green.

Relevant recent commits before this documentation update include:

```text
cd167d64  refactor(interaction): migrate durable session schema ownership
39f1ddbf  docs(interaction): record D3 migration ownership
3ed26b98  docs(interaction): record exact D3 gofmt validation
63e8c29a  test(interaction): add durable feature restart acceptance
e870471e  test(interaction): fix durable restart acceptance tests
0d2308be  test(myxl): complete D4 durable restart acceptance
```

The presence of repair commits is the reason the stronger no-unvalidated-push rule now exists.

### 16.3 Current durability inventory

Current nonempty durability versions that D4 must cover semantically:

- Assistant Shell: `"3"`
- Calculator: `"1"`
- Settings: `"1"`
- MyXL: `"1"`

### 16.4 D4 semantic coverage expected

Assistant Shell / Help:

- restore real shell/help state;
- rebind current generation;
- dispatch old callback;
- render valid next view/state;
- stale callback must fail.

Settings:

- restore real Settings session;
- continue representative navigation/mutation;
- stale callback protection remains intact.

Calculator:

- restore callback-heavy expression state;
- old callback continues from restored state;
- revision/stale protection remains intact.

MyXL:

- real summary/dashboard navigation;
- real saved-package navigation;
- still-valid purchase confirmation continues after restart;
- fresh quote/idempotency checks remain authoritative;
- quote drift fails safely;
- processing/in-flight purchase is not replayed or reconstructed;
- pending alias input survives only while deadline is live;
- expired input is not rearmed;
- actor/chat input binding remains exclusive.

### 16.5 Immediate task

The next AI should **validate and stabilize D4, not redesign it**.

Start by refreshing HEAD. If it advanced beyond the snapshot, inspect only the new diff first.

Then run the D4 local gate exactly as documented. Any failure is work for D4 until fixed. Do not mark D4 closed and do not move to D5 while:

- `gofmt -l` reports changed Go files;
- focused tests fail;
- `go test -race ./...` fails because of this work;
- `go vet ./...` fails;
- `git diff --check` fails.

If the environment cannot run these commands, stop before any Go push and report/prepare the exact patch instead.

### 16.6 Operational rules that must not be forgotten

- refresh HEAD before each new phase;
- `gofmt` before every Go-changing commit;
- update tests/mocks/fixtures in the same phase as API changes;
- no commit/push of Go work before local gates pass;
- no CI inspection unless explicitly requested;
- no second runtime/registry/TaskEngine/RPC executor/downloader/retry engine/persistence worker;
- keep state/retry/resource retention bounded;
- do not silently change Help/Settings/MyXL UX or purchase semantics to make tests pass.
