# Assistant vNext P0 — Feature Surface / Interaction Contract

## Status

Implemented as the first foundation phase for the Assistant/Inline rework. This phase intentionally does **not** reimplement `/start`, settings screens, inline features, callback workflows, PM relay, or the render bridge.

## Goal

Define one lifecycle-safe contract describing how a Goultroid feature is discoverable across userbot, assistant, inline, callback/action, screen, and deep-link surfaces without creating a second command registry or letting plugins register raw Telegram handlers.

The contract is a **catalog and ownership boundary**, not an execution engine.

## Core invariants

1. `core.Command` and `core.Router` remain the only canonical command execution authority.
2. Feature catalog command entries are projections of `Plugin.Commands()`; a `FeatureSpecProvider` is rejected if it attempts to declare command entries directly.
3. Non-command interactions use typed kinds: `inline`, `action`, `screen`, and `deep_link`.
4. Every non-command interaction must declare an explicit invocation policy for every enabled transport surface. `InvocationDefault` is rejected for these entries.
5. `Permission` and invocation remain separate concepts, matching the existing command model.
6. Every published feature spec is owned by a `tasks.ScopeIdentity` generation.
7. Disable, shutdown, or failed registration removes the feature contract before the plugin scope is discarded.
8. Re-enable publishes a new generation; stale cleanup handles cannot remove a newer registration.
9. Consumers receive a read-only `feature.Catalog`. Registry mutation stays inside plugin lifecycle transactions.
10. P0 stores metadata only. No interaction handler is invoked from the feature registry.

## Contract model

```text
Plugin
  │
  ├── Commands() []core.Command ──────────────┐
  │                                            │ canonical execution metadata
  └── FeatureSpec() feature.Spec (optional)    │
            │                                  │
            └── non-command interactions       │
                                               ▼
                                  feature.BindCanonicalCommands
                                               │
                                               ▼
                                      feature.Registry
                                               │
                                     read-only Catalog
                                               │
                 ┌─────────────────────────────┼────────────────────────────┐
                 │                             │                            │
           Assistant vNext                Inline vNext               Help / discovery
            later phase                    later phase               later phase
```

`FeatureSpec()` is optional during migration. Every existing plugin still receives a catalog entry generated from its canonical commands. A plugin implements `FeatureSpecProvider` only when it needs to declare non-command interaction surfaces.

## Interaction policy

A non-command interaction contains:

```go
feature.Interaction{
    ID:       "home",
    Kind:     feature.InteractionScreen,
    Surfaces: execution.SurfaceAssistant,
    Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
}
```

The policy keeps authorization tier and invocation audience separate:

```text
Permission
  - Everyone
  - Sudo
  - Owner

Invocation per surface
  - SelfOnly
  - SelfOrSudo
  - Anyone
```

Convenience constructors `OwnerPolicy`, `SudoPolicy`, and `PublicPolicy` populate explicit rules for the selected surfaces.

P0 deliberately rejects unspecified interaction invocation policy. This is the fail-closed default for new Assistant/Inline work.

## Typed interaction kinds

### `inline`

Declares an inline-query entry point. It must expose `SurfaceInline`.

P0 does not yet define matcher, cache, pagination, or result handler bindings. Those remain in the current inline engine until Inline vNext binds them to this contract.

### `action`

Declares an interactive action, normally reached from a bot callback or inline callback. It must expose Assistant and/or Inline.

P0 does not yet define callback payload/session binding. P1 will bind action IDs to interaction sessions and the next callback protocol.

### `screen`

Declares a renderable/navigation surface. It is intentionally transport-neutral so later presentation code can render the same conceptual screen through Assistant or a userbot render bridge.

### `deep_link`

Declares an Assistant `/start` deep-link entry point. It must expose the Assistant surface.

P0 only reserves identity and policy. Token routing/expiry belongs to a later phase.

## Command compatibility bridge

Existing plugins continue to implement:

```go
Commands() []core.Command
```

During plugin registration, the manager projects every command into `feature.CommandSurface`, preserving:

- canonical name and aliases;
- description, usage, and category;
- `execution.SurfaceMask`;
- `Permission`;
- effective userbot and assistant invocation policy;
- private/group restrictions.

A provider cannot publish its own `Spec.Commands`. `BindCanonicalCommands` rejects that attempt. This prevents drift between help/discovery metadata and actual command execution.

## Lifecycle and generation ownership

Each registry entry is owned by:

```go
feature.Owner{
    ID: pluginID,
    Scope: tasks.ScopeIdentity{
        Owner:      "plugin:" + pluginID,
        Generation: generation,
    },
}
```

Registration returns a tokenized cleanup handle. Cleanup removes an entry only if its token still matches the current registry entry.

This protects the sequence:

```text
old generation disable
        │
        ├── detach catalog entry
        └── close scope

new generation enable
        │
        └── publish new catalog entry
```

A stale cleanup from the previous generation cannot delete the new entry.

## Plugin manager transaction behavior

Initial registration is now conceptually:

```text
validate manifest/commands
        ↓
create plugin scope generation
        ↓
initialize plugin
        ↓
register canonical commands
        ↓
register message hooks
        ↓
register legacy callback service hook
        ↓
register feature contract
        ↓
commit plugin manager state
```

Any failure after feature publication rolls the feature registration back together with callbacks, hooks, commands, and plugin scope.

Disable/shutdown detaches feature contracts before plugin shutdown. Enable re-projects canonical commands with the new scope generation and republishes the contract.

## Read path

External consumers receive `feature.Catalog`, not `*feature.Registry`:

```go
catalog := pluginManager.FeatureCatalog()
```

Available operations are discovery-only:

- `Get(featureID)`
- `All()`
- `ForSurface(source)`
- `FindCommand(featureID, commandOrAlias)`
- `FindInteraction(featureID, kind, id)`

This is intentionally enough for later Assistant shell/help/inline routing without granting those components lifecycle mutation authority.

## What P0 intentionally does not do

P0 does not:

- replace `internal/assistant` yet;
- bind interaction IDs to handlers;
- create the new callback/session protocol;
- implement `/start` routing;
- migrate settings UI;
- migrate current inline handlers;
- implement render bridge/self-inline delivery;
- implement PM relay;
- change TaskEngine, RPC execution, or command execution semantics.

Those changes depend on this contract rather than being mixed into it.

## Migration rule for features

Existing features require no immediate changes. A feature only adds `FeatureSpec()` when it gains a non-command surface.

Example:

```go
func (p *Plugin) FeatureSpec() feature.Spec {
    return feature.Spec{
        ID:   p.Name(),
        Name: "Example",
        Interactions: []feature.Interaction{
            {
                ID:       "search",
                Kind:     feature.InteractionInline,
                Surfaces: execution.SurfaceInline,
                Policy:   feature.OwnerPolicy(execution.SurfaceInline),
            },
        },
    }
}
```

The feature must not register a Telegram event handler from this method and must not populate `Spec.Commands`.

## P0 acceptance criteria

P0 is complete when:

- a registry entry is automatically created for normal plugins from canonical commands;
- an optional `FeatureSpecProvider` can add typed non-command surfaces;
- invalid/implicit interaction policy fails registration;
- command duplication through `FeatureSpec` fails registration;
- catalog entries carry plugin scope generation;
- disable removes the entry;
- enable republishes it under a new generation;
- shutdown detaches registered feature surfaces;
- stale registrations cannot remove newer entries;
- no `/start`, settings, or inline feature-specific implementation is added in this phase.

## Next phase

P1 should build the **Interaction Session Runtime** on top of this catalog: bounded state, TTL, actor/chat/message binding, generation binding, cancellation/shutdown behavior, and the versioned callback token protocol. No feature-specific UI should be migrated before that runtime exists.
