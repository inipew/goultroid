# ADR 0007: Durable A2 sessions

## Status

Accepted

## Context

A2 callback buttons refer to in-memory sessions. Restarting the bot loses those sessions even when Telegram still displays the buttons. Plugin scope generations also change on each process start, so persisting the old scope cannot make callbacks valid again.

## Decision

Features opt in by declaring `Spec.DurabilityVersion`. A nonempty version promises that the feature can interpret its saved A2 state and action IDs after restart. Calculator, Settings, and MyXL opt in with version `1`; Assistant Shell, which owns interactive Help, opts in with version `3` matching its state format. SQLite stores the session ID, feature and version, binding, state, revision, deadlines, and pending input deadline. Writes complete before the runtime publishes a durable state change. The existing callback token format is unchanged.

After feature registration, startup loads live rows whose versions match and binds them to the current plugin generation. Expired, incompatible, invalid, and over-capacity rows are removed. Normal feature disable deletes its sessions. Process shutdown preserves durable rows while releasing in-memory resources. Session contexts and in-flight work are never resumed; a callback after restart starts a new task against the restored state.

This design supports one active bot process using the existing SQLite database. Multiple active processes require shared admission and callback coordination before they can safely use the same session table.

## Consequences

Opted-in interactions add a SQLite write to every state change. A database write failure leaves the in-memory state unchanged and fails the operation. Synchronous state is recoverable after restart, while already-running asynchronous work must be handled by each feature's own recovery design. Operators should keep the configured database path persistent; an in-memory database cannot provide restart recovery.

MyXL purchase confirmation remains bounded by its existing deadline. After confirmation begins, its processing view has no actionable buttons, and restart never replays the purchase task. The normal MyXL purchase flow still rechecks quote and uses its existing idempotency guard when a live confirmation callback is dispatched.
