# P3-C — Safe Global Media Reclamation

P3-C turns the P3-B ownership/reference registry into a deletion coordinator without making physical storage itself authoritative. The central rule is deliberately strict:

> A zero durable-reference count is a safety precondition, not deletion authorization.

Physical deletion requires a durable owner-authorized reclamation intent whose expected owner and lifecycle still match immediately before deletion.

## Durable intent model

`media_reclamation_intents` stores one intent per managed asset with:

- expected owner and lifecycle;
- reason/audit context;
- `prepared`, `pending`, or `deleting` state;
- grace / next-attempt timestamps;
- attempt count and bounded last error;
- short-lived claim token and claim timestamp.

`prepared` is a producer crash guard. Newly-created Clone snapshots and FFmpeg outputs install this before their normal durable-reference/cleanup boundary. Normal reconciliation never deletes a prepared asset. A durable reference insertion automatically cancels the guard. On quiescent startup, leftover prepared guards can be activated after reference backfill has completed.

`pending` means the owner or an explicit owner/lifecycle policy authorized deletion. `deleting` is a short claim lease around the physical delete.

## Re-reference protection

Claiming is an atomic SQL update which succeeds only when all of the following are still true:

1. the intent is due and `pending`;
2. the asset is still globally registered;
3. current owner/lifecycle exactly match the intent snapshot;
4. lifecycle is not `legacy`;
5. no durable reference exists.

After the claim commits, database triggers reject any new reference targeting that asset while the intent is `deleting`. Reference insertion into a `prepared` or `pending` asset instead cancels the intent. Ownership/lifecycle mutation is also blocked while deletion is claimed.

This closes the unsafe `count references -> Storage.Delete` race: a reference cannot become durable between the final DB claim and physical deletion.

## Grace and startup recovery

Runtime producer guards use `DefaultReclamationGrace`. Explicit owner cleanup may activate a prepared guard immediately because the owner already made the deletion decision.

Startup runs in this order:

1. P3-A SavedResponse reconciliation where durable storage is available;
2. SavedResponse global registry compatibility/backfill;
3. Clone reference backfill;
4. explicit startup orphan policies for known owners (`clone/persistent`, `media/transient`);
5. activate leftover prepared intents from the prior process;
6. bounded global reclamation pass.

The startup orphan policies are explicit owner/lifecycle authorization. They are not a generic `ref_count == 0` scan. `downloader/retained`, `legacy`, unknown owners, malformed entries, and unregistered physical entries are not eligible.

When startup falls back to `MemoryStorage`, physical reclamation is skipped entirely because absence in an ephemeral backend says nothing about durable bytes.

## Retry, idempotency, and crash recovery

A physical delete failure returns the claim to `pending`, increments attempts, stores a bounded error string, and schedules exponential retry capped at one hour. Batch reconciliation is bounded and does not busy-loop on failures.

`storage.ErrNotFound` is success: metadata and the durable intent are finalized idempotently.

A process crash while an intent is `deleting` leaves a claim lease behind. A later reconciliation recovers expired claims back to `pending`; retrying a delete that actually completed before the crash is safe because `ErrNotFound` finalizes it.

There is intentionally no polling goroutine in P3-C. Reconciliation is event-driven by owner cleanup and startup. Durable `next_attempt_at` preserves retry/backoff state across process lifetime and future passes without adding idle CPU wakeups.

## Cancellation / shutdown semantics

The physical delete respects the caller context and a bounded delete timeout. If cancellation/shutdown interrupts it, a short detached finalization context attempts to persist the failed claim back to `pending` with backoff. If that final bookkeeping cannot complete because the process/database is already gone, the `deleting` lease is recoverable on the next startup.

No unbounded shutdown wait or background reclaimer drain is introduced.

## Owner integration

### Clone

Clone snapshot registration installs a prepared P3-C guard. Saving `clone_state` writes its durable reference transactionally, which cancels the guard. Production Clone storage is wrapped by an owner-scoped `OwnerStorage`; managed snapshot `Delete` therefore requires registered `clone/persistent` ownership and passes through the global claim/revalidation path. Unregistered managed assets fail closed.

Legacy raw Clone filesystem paths remain under the pre-existing Clone-specific cleanup path and are never adopted by global inference.

### Media

FFmpeg outputs register as `media/transient` and install a prepared guard. `DeleteTransientAsset` activates/reclaims through P3-C. A crash before normal post-send cleanup is recovered at startup.

### Downloader

`downloader/retained` assets intentionally receive no automatic reclamation policy. Zero references are normal for retained downloads and never make them disposable.

### SavedResponse

The P3-A cleanup journal remains the physical authority during this compatibility stage. Global references still participate in the P3-C interlock. A later handoff can migrate SavedResponse cleanup intents without forcing two independent physical deleters to race during P3-C rollout.

## Fail-closed boundary

P3-C never grants delete authority to:

- `legacy_untracked` physical entries;
- malformed/unmanaged storage entries;
- registered `legacy` lifecycle assets;
- retained downloads without an explicit owner action;
- unknown owner/lifecycle combinations;
- registered assets with no durable intent/policy merely because they have zero references.

Physical enumeration remains observation only.
