# Assistant Parity P8-H — unload/reload/generation cross-surface acceptance

## Status

**P8-H is CLOSED for implementation/source acceptance.**

Baseline:

`c0ef995d2dc238bfdb2006378e1e49c94ff743ce` — P8-G reference-driven compatibility reclamation.

P8-H does not add a lifecycle engine. It exercises the lifecycle authorities that already own plugin generations:

- `plugin.Manager`;
- `feature.Registry`;
- Inline vNext registrations;
- `interaction.Runtime`;
- `interaction.Dispatcher`;
- SavedResponse provider registrations;
- shared TaskEngine scoped ownership;
- P8-B self-inline authorization.

## Acceptance canary

P8-H adds one integrated plugin canary with all generation-sensitive surfaces:

```text
canonical command
FeatureSpec screen
FeatureSpec action
FeatureSpec inline
FeatureSpec deep-link declaration
Inline vNext implementation
SavedResponse resolver used by typed deep-link delivery
a2 session + callback
pending free-form input
scoped TaskEngine client
download:1 resource ownership
```

The test intentionally uses the real registries/runtime/TaskEngine rather than a second test lifecycle implementation.

## Load contract

On the first plugin generation, P8-H verifies that:

- the canonical command is registered in `core.Router`;
- command scope equals the plugin generation;
- screen/action/inline/deep-link declarations are visible in `feature.Registry`;
- the Inline vNext handler is visible with the same scope;
- inline handler version contains the generation;
- a2 session scope equals the plugin generation;
- a SavedResponse-backed deep-link prepares into the same plugin generation;
- a scoped TaskEngine task with `download:1` is admitted under the same scope.

## Revision-stale callback contract

Before testing unload, the acceptance canary mints an a2 callback and then arms free-form input.

`ArmInput` advances the interaction revision.

The callback minted before that transition must then fail with:

`interaction.ErrStaleToken`

This is independent of generation invalidation and verifies the ordinary optimistic UI revision fence remains intact.

## Disable contract

`plugin.Manager.Disable` remains the sole lifecycle authority.

The existing ordering is preserved:

```text
mark generation disabled / detach manager scope
        ↓
TaskEngine.CancelScope(old generation, CauseScopeClosed)
        ↓
feature cleanup
  ├ SavedResponse registration Close
  ├ Inline registration Close
  ├ FeatureSpec registration Close
  ├ action dispatcher UnregisterScope
  └ interaction Runtime CancelScope
        ↓
generic callback/message-hook cleanup
        ↓
canonical command unregister
        ↓
plugin shutdown + Scope.Close
```

P8-H verifies after disable:

| Surface / state | Required result |
| --- | --- |
| command | absent |
| FeatureSpec | absent |
| inline | absent |
| a2 session | cancelled with `ErrScopeStale` |
| pending input | removed |
| old a2 callback | invalid |
| prepared old action | cannot execute |
| old action handler | never invoked |
| SavedResponse deep-link prepare | unavailable while provider disabled |
| running scoped TaskEngine work | cancelled with `CauseScopeClosed` |
| late submit through old scoped client | rejected with `ErrScopeClosed` |

No worker, ticker, retry engine, callback protocol, or lifecycle registry is added for this behavior.

## Re-enable contract

Re-enable creates a new plugin scope generation.

P8-H verifies:

- the new scope is not equal to the old scope;
- command, screen, action, inline and deep-link declaration are visible again;
- all restored surfaces point at the new generation;
- Inline vNext handler version changes;
- an old a2 token remains invalid;
- an old prepared action remains invalid;
- a new a2 session and typed action work;
- a new pending input claim can be armed and consumed;
- the plugin receives a new scoped TaskEngine client;
- new `download:1` work executes under the new generation.

TaskEngine's old scope tombstone prevents a retained old scoped client from becoming usable after the new generation exists.

## Deep-link lifecycle semantics

P8-H distinguishes **durable routing identity** from an **execution lease**.

A reusable opaque `d1_...` deep-link token is allowed to survive a normal plugin reload. That token contains durable routing state, not an authorization to execute an old generation.

The rule is:

```text
raw durable token
  may survive reload
        ↓
Prepare after reload
        ↓
resolve current SavedResponse provider
        ↓
new generation scope
```

But a prepared target from the old generation must never cross the generation boundary.

P8-H verifies:

- old prepared deep-link execution after reload fails with `savedresponse.ErrBindingStale`;
- re-preparing the same reusable token resolves the new generation;
- fresh execution then succeeds.

This preserves durable links across operational reloads without allowing queued/prepared old feature code to execute.

## Self-inline lifecycle hardening

P8-H found one real lifecycle gap in P8-B authorization.

Before P8-H, the authorized self-inline renderer checked Telegram capabilities on every render, but normal plugin disable does not remove its manifest. A feature retaining its injected renderer could therefore still pass the capability check while disabled.

P8-H extends the existing stateless per-call authorizer with:

```text
manager.IsEnabled(pluginID)
        +
telegram.read
        +
telegram.send_message
```

Behavior:

- enabled generation: render may proceed;
- disabled plugin: `selfinline.ErrUnavailable` before transport;
- re-enabled generation: the same stateless wrapper may proceed again after current checks.

No renderer cache or generation map is introduced.

## Architecture fences

P8-H adds source regression coverage requiring the canonical cleanup boundaries to remain present:

- `registry.actions.UnregisterScope(scope)`;
- `registry.interactions.CancelScope(scope)`;
- lifecycle-owned Inline registration close;
- lifecycle-owned SavedResponse registration close;
- TaskEngine `CancelScope(... CauseScopeClosed)`;
- canonical command unregister;
- self-inline current enable-state check.

The fences protect ownership boundaries; they do not prescribe a second runtime.

## Resource ownership acceptance

The cross-surface canary runs real TaskEngine work with:

```text
scope     = plugin generation
resource  = download:1
```

Disable must cancel the running old-generation task and release its resource permit through normal TaskEngine cancellation.

A new-generation task then successfully acquires `download:1`, proving the old generation does not retain the resource across reload.

P8-I still owns broad mixed-load, goroutine/RSS/heap and saturation acceptance.

## Resource cost of P8-H

Production change is one extra current-state check in the existing per-render self-inline authorization path.

```text
new lifecycle engine   = 0
new goroutines         = 0
new timers             = 0
new tickers            = 0
new caches/maps        = 0
new callback protocol  = 0
new task engine        = 0
```

## P8 status after H

```text
P8-A CLOSED
P8-B CLOSED
P8-C CLOSED
P8-D CLOSED
P8-E CLOSED
P8-F CLOSED
P8-G CLOSED
P8-H CLOSED

P8-I NEXT  resource / idle / high-load acceptance
P8-J TODO  final cleanup, parity freeze, closure
```

## Formatting and runtime verification

All changed Go files for P8-H must be processed with `gofmt` before commit.

The execution environment used for this phase cannot resolve github.com from the shell, so it cannot materialize the complete repository checkout needed to run the focused `go test` commands locally. P8-H therefore records implementation/source acceptance and adds executable regression tests, without claiming those package tests were executed in this session.

CI is not inspected unless explicitly requested.
