# Goultroid Durable A2 Session Hardening — AI Session Plan

Status: **OPEN — implementation pending**

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

Status: **OPEN**

Before changing code:

1. refresh `test-next` HEAD;
2. record exact SHA/message in this document;
3. inspect:
   - `internal/interaction/durable.go`;
   - `internal/interaction/runtime_internal.go`;
   - `internal/interaction/session.go`;
   - `internal/interaction/input.go`;
   - `internal/interaction/sqlite/store.go`;
   - `internal/plugin/features.go`;
   - `internal/plugin/manager.go`;
   - `internal/app/app.go`;
   - `internal/app/modules.go`;
   - current interaction/runtime/plugin tests.

Add or confirm focused failing tests for the concrete findings **before** broad refactoring where practical.

Required reproductions:

- durable delete failure during scope cleanup is not propagated;
- failed expiry durable delete leaves cleanup state inconsistent;
- lifecycle cancellation cannot reach durable Delete;
- schema is not represented in feature migration history.

Gate:

- no behavior-changing fix yet unless required to write a minimal test seam;
- current failure modes are explicit and reproducible;
- test names and expected invariants are recorded.

### D1 — Make durable deletion error-bearing and lifecycle-aware

Status: **OPEN**

Goal:

> normal feature teardown cannot claim success when durable session deletion fails.

Tasks:

- add context-aware removal semantics;
- add error-bearing scope cancellation;
- route plugin feature cleanup through the error-bearing path;
- preserve existing shutdown semantics;
- ensure durable delete receives the lifecycle context instead of unconditional `context.Background()`;
- audit public `Cancel`, `CancelScope`, expiry, stale-scope, and resolve-triggered removal callers;
- retain compatibility wrappers only where justified by caller evidence.

Important:

- do not move durable delete to a fire-and-forget goroutine;
- do not treat `PersistenceErrors` metrics as a substitute for returning an error;
- do not allow feature registration cleanup to close the catalog and then silently ignore durable cleanup failure without recording incomplete teardown.

Plugin lifecycle must retain a safe state if feature cleanup partially fails.

Audit whether the existing `teardownErrors` mechanism can represent the failure without inventing another lifecycle error registry.

Tests:

- durable delete failure causes normal plugin disable to return an error;
- failed teardown is visible through existing manager teardown state where appropriate;
- successful disable removes durable rows;
- process shutdown still preserves durable rows;
- supplied context cancellation reaches durable Delete;
- no stale old API remains in production callers.

Gate:

- successful disable implies durable sessions for that scope are gone;
- shutdown preservation remains unchanged;
- no new worker/goroutine is introduced.

### D2 — Repair expiry/remove invariants under persistence failure

Status: **OPEN**

Goal:

> an expired session cannot disappear from the expiry retry structure while still consuming runtime capacity.

Tasks:

- redesign the local prune/remove ordering so durable deletion failure does not orphan the session;
- keep expiry cleanup lazy/event-driven;
- avoid a permanent cleanup loop;
- ensure retry is naturally triggered by later runtime operations or explicit prune;
- verify accounting remains correct for:
  - `sessions`;
  - `byScope`;
  - `actorCounts`;
  - `stateBytes`;
  - `inputs`;
  - expiry heap.

Potential implementation patterns include:

- attempt persistence deletion before removing the heap item; or
- reinsert/reschedule the expiry item on failure.

Choose the smallest design that preserves heap correctness and avoids hot retry loops.

Tests:

- first durable delete fails;
- session remains reachable for cleanup bookkeeping but resolves as expired;
- capacity/accounting does not become permanently corrupted;
- later prune after store recovery removes the session exactly once;
- input claim cleanup remains correct;
- stats count expiry only when removal actually completes;
- no duplicate heap item is created.

Gate:

- no expired durable session can become permanently untracked;
- no busy-loop retry;
- bounded memory/accounting invariants remain true.

### D3 — Move interaction schema into namespaced migrations

Status: **OPEN**

Goal:

> durable A2 persistence participates in the same migration contract as other feature-owned schema.

Tasks:

- add interaction SQLite migration provider;
- register it in `migrateBuiltinFeatures`;
- remove direct startup schema creation after migration compatibility is proven;
- add adoption logic for databases where `interaction_sessions` already exists from `d0b6659e...`;
- preserve all existing data;
- verify the physical schema before recording adoption;
- keep migration ownership in the interaction persistence package.

Tests:

- fresh DB gets the table via feature migration;
- existing pre-durable DB upgrades successfully;
- baseline durable DB with table but no namespaced migration record is adopted safely;
- malformed/incompatible preexisting schema fails clearly rather than being silently accepted;
- repeated startup does not rerun destructive SQL;
- current Store round-trip tests still pass.

Gate:

- no `CREATE TABLE IF NOT EXISTS interaction_sessions` remains in ordinary application bootstrap;
- migration history records the schema owner/version;
- fresh and upgraded DBs converge.

### D4 — Add real feature restart acceptance

Status: **OPEN**

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
- no test relies only on the generic `demo` catalog.

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
2. make the scoped code + test changes;
3. update stale tests/mocks/signatures in the same phase;
4. run `gofmt` on every changed Go file;
5. verify `gofmt -l` is empty for those files;
6. run focused tests;
7. inspect `git diff --check`;
8. inspect the diff for unrelated changes;
9. commit and push;
10. **do not check CI unless the user asks**.

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

Start with **D0**, not D3 or D6.

The first implementation session should:

1. refresh `test-next` HEAD;
2. reproduce durable-delete failure through the current interaction/plugin lifecycle;
3. add focused regression tests for error propagation and expiry cleanup;
4. only then implement **D1**.

The highest-value first fix is the lifecycle correctness gap:

> a failed durable delete must not be reduced to a boolean/count that allows plugin disable to look successful.

After D1 and D2 are stable, migrate schema ownership in D3, then prove actual feature restart compatibility in D4–D5, and only then use D6 measurements to decide whether the current synchronous SQLite critical section needs any further design work.
