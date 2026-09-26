# P2-A — Plugin message-hook registration simplification

Date: 2026-09-27  
Branch: `test-next`  
Baseline entering P2-A: `f61ec2847f9d9212368634dad03c788c827223e2`  
Final code acceptance baseline before documentation: `0b3dac3b45e8b1117657c9fafd8a96a03ffe4fa7`

## Scope

P2-A simplifies the plugin-to-dispatcher message-hook registration boundary. It does not redesign message execution, TaskEngine admission, feature routing, or plugin lifecycle.

The old manager negotiated registrar capabilities through one base interface plus nine optional registrar interfaces for combinations of raw/canonical, scoped/unscoped, routed/unrouted, and state-aware hooks.

## Final contract

The shared authority is now `core.MessageHookRegistration`:

```text
Scope
Priority
Routing
StateGate
Handler
RawHandler
LegacyRouting
```

`plugin.HookRegistrar` exposes one operation:

```text
RegisterMessageHook(core.MessageHookRegistration) (cleanup, error)
```

The application still wires `plugin.Manager` directly to the Telegram Dispatcher. The Dispatcher implements the single registration method and forwards accepted registrations to its existing `addMessageHandler` storage/index path.

No second registry, router, dispatcher, queue, worker, or state store was introduced.

## Canonical and raw behavior

Canonical message hooks remain the preferred production contract. They receive `core.MessageEnvelope`, require explicit structural routing, optionally carry a dynamic chat-state gate, and continue to require `telegram.read` when the capability gate is enabled.

Raw hooks remain a privileged compatibility contract and continue to require `telegram.raw`. Explicitly routed/stateful raw hooks carry routing/state in the common registration. Unrouted raw compatibility hooks set `LegacyRouting`, allowing the Dispatcher to derive the existing historical lane from priority and plugin scope.

This preserves the important legacy compatibility rule for scoped feature hooks: a priority-50 scoped raw hook remains in the event lane rather than silently moving to the decision lane.

## Production inventory

Current production message hooks found during the audit:

| Plugin | Contract | Routing/state |
|---|---|---|
| AFK | canonical | routed |
| Blacklist | canonical | routed + state gate |
| Filters | canonical | routed + state gate |
| PMPermit | canonical | routed + state gate |
| UserLog | canonical | routed |

No active production implementation of the raw `HandleIncomingMessage` hook was found. The raw contract is retained only as privileged compatibility/API surface.

## Lifecycle/resource invariants

Registration during initial plugin load and enable/reload still converges on the same `registerMessageHook` helper. The registration carries the generation-bound `tasks.ScopeIdentity`; cleanup is still retained in `plugin.Manager.hookCleanups` and executed by the existing bounded lifecycle callback executor.

P2-A adds no idle goroutine, ticker, polling loop, cache, map, queue, or permanent worker. The Dispatcher continues to rebuild the immutable indexed message-route table only on registration/removal; ingress still performs its existing atomic indexed lookup.

## Regression fences

P2-A adds/updates tests proving:

- `HookRegistrar` exposes exactly one registration method;
- the nine retired registrar capability interfaces stay absent;
- `registerMessageHook` does not type-assert registrar capabilities;
- raw and canonical manager paths converge on the same registrar method;
- the shared registration field set remains explicit;
- scope/routing/state gate metadata survives registration;
- canonical/raw handler exclusivity is enforced;
- cleanup removes the registered handler;
- legacy raw scoped lane semantics are preserved;
- capability-gate denial still prevents registrar admission.

## Commits

```text
ec55296b8971ed3c3cebe697dfa849e932fb69b3  refactor(plugin): unify message hook registration
bb7de208c409a9a05ce5bdad6c724e654a54012d  fix(plugin): repair p2-a registration patch markers
cb95dfd1a032ce8102d2e1b7d46afc0afef17361  test(architecture): harden p2-a hook fence
0b3dac3b45e8b1117657c9fafd8a96a03ffe4fa7  test(architecture): make p2-a contract fence format-stable
```

## Verification constraints

No CI was inspected.

The executable container could not resolve GitHub, so a complete checkout was unavailable. Therefore this phase does **not** claim that `gofmt`, `go build`, `go test`, `go vet`, race tests, or benchmarks executed. Current GitHub source inspection, production hook inventory, and the committed regression/architecture fences are the acceptance evidence available in this environment.

## Closure

**P2-A: CLOSED.**

Next phase, only after explicit user confirmation:

**P2-B — expand localization into common userbot UX.**

Do not start P2-C until P2-B is independently completed and accepted.
