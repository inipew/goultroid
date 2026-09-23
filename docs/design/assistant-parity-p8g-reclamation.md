# Assistant Parity P8-G — reference-driven compatibility/dead-stack reclamation

## Status

**P8-G is CLOSED for source reclamation.**

Baseline:

`fb32d215b00045f0c38a4aff4c7f083808f00d44` — P8-F locale-aware behavioral matrix.

P8-G does not perform name-based deletion. Every candidate is classified from current production call paths first.

## Reclaimed production compatibility

### 1. `assistant.NewBotClient`

Before P8-G:

```go
func NewBotClient(...) *AssistantApp {
    return NewApp(...)
}
```

Current production wiring already constructs the Assistant with:

```go
assistant.NewApp(...)
```

The only remaining owner of `NewBotClient` was a unit test whose sole purpose was verifying the alias.

P8-G removes both the alias and that alias-only test.

Canonical constructor:

```text
assistant.NewApp
```

### 2. command-router compatibility ingress

Before P8-G the Assistant command router exposed three entry points:

```text
Dispatch
DispatchMessage
DispatchMessageContext
```

`Dispatch` and `DispatchMessage` synthesized a `core.Chat` from `tg.InputPeerClass` through `legacyChatForPeer`.

That inference cannot distinguish a broadcast channel from a megagroup when only `InputPeerChannel` is available.

Current production update ingress does not use either wrapper.

Production already performs:

```text
Telegram update + tg.Entities
        ↓
assistantCommandMessageContext
        ↓
authoritative chat kind/message/reply/topic/media/self metadata
        ↓
Router.DispatchMessageContext
```

P8-G therefore removes:

- `Router.Dispatch`;
- `Router.DispatchMessage`;
- `legacyChatForPeer`;
- the production fallback that rebuilt a missing chat identity from `InputPeer`.

`DispatchMessageContext` now rejects a zero chat identity with `core.ErrInvalidArguments`.

This converts missing context from silent inference into a fail-closed contract.

## Test migration

Tests that intentionally exercise command semantics without constructing Telegram entities still need a compact fixture.

P8-G moves peer-to-chat inference into:

```text
internal/assistant/command/dispatch_test.go
```

The helper exists only in the test binary.

Production code no longer carries convenience compatibility merely for tests.

## Reference-driven KEEP classification

The following names may look transitional but are production-live and are **not** reclaimed.

### `internal/assistant/interaction`

KEEP.

It is the current Telegram transport implementation used by:

- a2 presentation;
- callback acknowledgement/edit/delete;
- inline message editing;
- inline media upload;
- feature-driver runtime.

It is not the deleted a1 interaction stack.

### `internal/assistant/client/interaction_ingress.go`

KEEP.

This is the canonical a2 callback/input ingress:

```text
a2 token
  ↓
PrepareCallback
  ↓
TaskEngine
  ↓
prepared Dispatch
```

### `internal/assistant/client/callback_dispatch.go`

KEEP.

This is the canonical generic callback TaskEngine adapter for non-a2 callback surfaces. It does not create a second callback protocol.

### `internal/assistant/command/telegram_menu.go`

KEEP.

This synchronizes Telegram's native command list from the canonical `core.Router`. It is transport presentation, not an Assistant command registry.

### migration files

KEEP.

Database/data migration files under Assistant/deep-link/group/service packages remain required for upgrade compatibility. A migration filename is not evidence of runtime legacy.

### lightweight taskless command execution

KEEP for now.

`executeCanonicalTask` still permits non-resource, non-contextual commands to execute directly when no TaskEngine is configured.

Production Assistant wiring always supplies TaskEngine. The fallback remains useful to lightweight embeddings/tests and is not tied to a1/menu compatibility.

P8-G does not expand scope into changing this execution contract.

## Explicit non-Assistant compatibility exclusions

P8-G intentionally does not remove:

- `internal/services/savedresponse/ledger_compat.go`;
- `internal/services/savedresponse/registry_compat.go`;
- media ownership/reconciliation compatibility;
- module/media registry compatibility tests.

Those components have independent storage/migration exit criteria.

Deleting them merely because their names contain `compat` would violate the reference-driven rule.

## Legacy a1 fence

`internal/assistant/legacy_stack_test.go` remains unchanged as the historical architecture regression gate.

It continues to reject:

- `LegacyAssistantMenu`;
- `CompatibilityHost`;
- `MenuInstanceStore`;
- retired Assistant menu/presentation/callback packages;
- `a1:` callback envelopes;
- transitional `v2*.go` interaction source names.

P8-G adds a separate architecture fence for the shims reclaimed in this phase:

- `NewBotClient`;
- `legacyChatForPeer`;
- `Router.Dispatch`;
- `Router.DispatchMessage`.

It also asserts that production update ingress still uses `DispatchMessageContext` plus `assistantCommandMessageContext`.

## Resource effect

P8-G adds no runtime service and removes only compatibility surface.

```text
new goroutines       = 0
new timers           = 0
new caches           = 0
new persistence      = 0
new callback protocol= 0
```

The main correctness improvement is removal of peer-only chat-kind inference from production command dispatch.

## P8 status after G

```text
P8-A CLOSED
P8-B CLOSED
P8-C CLOSED
P8-D CLOSED
P8-E CLOSED
P8-F CLOSED
P8-G CLOSED

P8-H NEXT  unload/reload/generation cross-surface acceptance
P8-I TODO  resource/idle/high-load acceptance
P8-J TODO  final cleanup/parity freeze/closure
```

## Formatting and CI

All changed Go files for P8-G must pass `gofmt` before commit.

P8-G does not inspect CI unless explicitly requested.
