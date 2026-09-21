# Assistant vNext P5-D — Settings mutation contract

## Status

P5-D adds typed settings mutations to the a2 Assistant shell while deliberately keeping free-form string input on the legacy a1 workflow.

The migrated mutation surface supports:

- bool toggle;
- enum next-value rotation;
- int decrease/increase;
- duration decrease/increase;
- user-override reset.

String settings remain read-only in a2 until a dedicated input-session phase.

## Stable setting identity

P5-C used category and setting indexes only as read-only navigation cursors.

P5-D adds a stable 128-bit SHA-256-derived mutation fingerprint of the normalized `namespace:key` identity. The fingerprint is separated from category/setting cursors and exists only to bind the detail session to that stable schema key.

When a detail screen opens, the selected schema identity fingerprint is bound into the P1 session state. Mutation handlers never authorize writes from the current index alone.

Before any write:

1. the detail screen must still be active;
2. the current registry item at the cursor must match the bound identity;
3. the registry is looked up again by the stable namespace/key;
4. the stable identity must still match;
5. the optimistic P1 revision is consumed;
6. the current per-definition schema revision is captured;
7. persistence revalidates that exact revision under the registry lock;
8. persistence uses the registered-mutation service boundary.

A registry reorder therefore makes the old detail fail closed rather than writing whichever setting moved into the old index.

## Optimistic UI revision

Mutation callbacks reserve the current session revision before persistence:

```text
callback revision N
      ↓
binding validation
      ↓
UpdateState(same state)
      ↓
session revision N+1
      ↓
revalidate stable identity
      ↓
persist
      ↓
render revision N+1
```

The state bytes do not need to change for the revision to advance.

This means a retry of the old Telegram button cannot replay the mutation after the handler has crossed the write boundary.

## Registered persistence boundary

P5-D adds:

- `settings.Service.SetRegistered`
- `settings.Service.ResetRegistered`

These are intentionally separate from existing `Set` and `Reset`.

The registry now also exposes a per-definition revision token. That token changes only when the same `namespace:key` is replaced through `Register` or `SetDefault`; unrelated schema updates do not invalidate the mutation.

The registered variants require the expected per-definition revision, verify it under the registry read lock, and keep that read lock held from definition lookup/canonicalization until repository commit/delete finishes. A concurrent replacement of that same schema therefore cannot slip between mutation planning and persistence.

Existing legacy callers keep the original APIs and semantics.

## Typed mutation planning

`assistant/shell` now exposes a pure typed planner:

```text
PlanMutation(definition, current, explicitUserOverride, operation)
    -> MutationPlan
```

Operations:

- `change`
- `increase`
- `decrease`
- `reset`

Outcomes:

- `changed`
- `noop`

Type rules match the established legacy behavior:

- bool: toggle;
- enum: rotate to the next allowed value;
- int: UI step, default 1, with existing min/max wrapping behavior;
- duration: UI step seconds, default 5 seconds, clamped to min/max;
- string: unsupported in a2 and routed to Classic menu for input.

Reset is a typed no-op when no explicit user override exists.

## Typed result and errors

Mutation execution uses:

```go
type MutationResult struct {
    Namespace string
    Key       string
    Operation MutationOperation
    Outcome   MutationOutcome
    Previous  string
    Persisted string
    Effective string
    Source    string
}
```

Failures use `MutationError` with a stage:

- `binding`
- `reserve_revision`
- `persist`
- `render`

`MutationError.Committed` distinguishes a presentation failure after a successful persistent commit from a failure where no write committed.

This distinction is required for correct recovery UX and for avoiding unsafe automatic retries.

## Recovery semantics

### Persistence failure

If persistence fails after revision reservation:

- the old button is already stale;
- no committed result is reported;
- the handler attempts to render a fresh detail with an explicit failure notice;
- the callback answer says the operation can be retried safely.

A second press of the original button returns `ErrStaleToken` and cannot duplicate work.

### Persistence success, render failure

If persistence commits but Telegram edit fails:

- the persisted value remains authoritative;
- the session revision has already advanced;
- the original callback token is stale;
- `MutationError{Stage: render, Committed: true}` is returned;
- the callback answer explicitly says the value was saved but the view could not refresh.

The transport must not retry the mutation automatically.

### No-op

No-op mutations still consume the revision and render fresh markup. This keeps replay semantics identical to changed mutations without issuing a repository write.

## Presentation

Bound detail screens expose only typed controls that are valid for the schema type:

- bool/enum: **Change**;
- int/duration: **Decrease / Increase**;
- reset: only when a user override currently exists;
- string: no a2 mutation control.

Sensitive values remain masked before presentation.

## Resource behavior

P5-D adds no:

- background goroutine;
- timer;
- polling loop;
- mutation cache;
- callback payload registry;
- feature-global conversation state.

The binding is retained inside the bounded P1 session state.

## Tests

P5-D covers:

- typed mutation planner behavior;
- bool/enum/int/duration operation constraints;
- string rejection;
- stable normalized identity binding;
- registry reorder failing closed before persistence;
- optimistic revision making the old mutation token stale;
- successful mutation and reset;
- persistence failure with recovery rendering;
- successful commit followed by Telegram edit failure with `Committed=true`;
- schema replacement being blocked until a registered repository commit finishes;
- stale per-definition revisions being rejected before the repository is touched.

## Deferred input phase

Free-form string mutation remains intentionally deferred.

A later phase must define:

- message/input ownership;
- actor/chat binding;
- TTL and cancellation;
- plugin-generation cancellation;
- stable namespace:key revalidation after user input arrives;
- delete/cleanup of prompt state;
- persistence-success/render-failure recovery for delayed input.

P5-D does not create a second conversation subsystem to solve that prematurely.
