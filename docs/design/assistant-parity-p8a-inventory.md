# Assistant Parity P8-A — behavioral inventory and target freeze

## Status

**P8-A is CLOSED.**

This phase freezes the remaining Assistant/Inline parity target before any new P8 implementation work.

Baseline audited:

- Goultroid `test-next`: `8a43ebd8b43a5445d0e688a87b3d3688ec6de80a`
- Ultroid `main`: `edd18d31eae982dff468bd8ce785fbb319e370c3`

P8-A does not add a new runtime, worker pool, callback protocol, command registry, or Telegram dispatcher.

Its purpose is to prevent P8 from drifting into either of these failure modes:

1. cloning every Ultroid plugin/provider one-by-one; or
2. declaring parity complete while advanced interaction capabilities are still unproven in production.

## Scope reconciliation

The original roadmap described:

```text
P7 — group/manager + advanced workflows
P8 — migration/parity + delete old stack
```

The implementation evolved differently.

P7-A through P7-L became the complete **Group/Manager Plane**:

- contextual Telegram authority;
- manager routing;
- group state;
- moderation;
- event-driven group features;
- blacklist/filter/warnings;
- reply/media/topic semantics;
- lifecycle/resource hardening;
- final group acceptance.

The advanced workflow canaries originally mentioned beside P7 — especially self-inline rendering, calculator, rich lookup, and interactive downloader — were not implemented as part of that group-plane sequence.

P8 therefore owns those remaining advanced parity proofs before final migration/reclamation acceptance.

This is a scope reconciliation, not a new architecture direction.

## Target classes

Every parity item is frozen into one of four classes.

### MUST

Behavior/capability required before P8 can close.

A MUST item can be satisfied by a Goultroid-native implementation with different architecture and UI details, but the user-visible capability and lifecycle guarantees must exist.

### REPRESENTATIVE

The platform capability must be proven by at least one production implementation.

Provider-for-provider cloning is explicitly not required.

Example:

```text
Ultroid:
  Google
  F-Droid
  Play Store
  OrangeFox
  Saavn
  Twitter
  TL search

Goultroid P8:
  one bounded network lookup workflow
  + reusable FeatureSpec / Inline vNext path
```

Once the common capability is proven, more providers are ordinary feature work rather than parity blockers.

### BASELINE-CLOSED

Already implemented and source-accepted before P8-A.

P8 may add regression coverage, but must not redesign these areas without a concrete defect.

### EXCLUDED

Explicitly not a P8 parity requirement.

The exclusion may be because the behavior is provider-specific, unsafe, redundant with a stronger Goultroid capability, or unrelated to the Assistant interaction migration.

## Frozen architecture invariants

P8 MUST preserve all of the following.

```text
core.Command / core.Router
    = only canonical command execution registry

feature.Registry
    = canonical cross-surface discovery metadata

interaction.Runtime
    = bounded session / a2 token authority

interaction/orchestration.Engine
    = feature-facing state/presentation transaction boundary

presentation.Port
    = transport abstraction

TaskEngine
    = execution/backpressure/resource authority

telegram.RPCExecutor
    = Telegram retry/FloodWait authority
```

P8 must not introduce:

- an Assistant-only command registry;
- a second callback protocol;
- per-feature Telegram update handlers;
- per-user conversation goroutines;
- unbounded global workflow maps;
- an Inline-specific downloader engine;
- an Assistant-specific downloader engine;
- a feature-owned retry/FloodWait scheduler;
- plugin-local periodic cleanup workers for interaction state.

## Current foundation inventory

### Feature / interaction contract

Status: **BASELINE-CLOSED**

Evidence:

- `internal/feature/spec.go`
- `internal/feature/registry.go`
- `internal/feature/admission.go`
- `internal/plugin/features.go`

Current behavior:

- canonical command metadata comes from `Plugin.Commands()`;
- non-command surfaces use typed `inline/action/screen/deep_link` declarations;
- interaction policy is explicit;
- feature registration is lifecycle-generation scoped;
- owned inline bindings are removed with feature lifecycle;
- stale cleanup cannot remove a newer generation.

### Interaction sessions / a2 callbacks

Status: **BASELINE-CLOSED**

Evidence:

- `internal/interaction/runtime.go`
- `internal/interaction/orchestration`
- `internal/presentation`

Current P1 defaults remain bounded:

| Limit | Default |
| --- | ---: |
| live sessions | 4096 |
| per feature generation | 512 |
| per actor | 64 |
| state per session | 64 KiB |
| total retained state | 8 MiB |
| default TTL | 15 min |
| maximum TTL | 24 h |

There is no session cleanup ticker or waiting conversation goroutine.

### Inline vNext infrastructure

Status: **BASELINE-CLOSED infrastructure / incomplete product breadth**

Evidence:

- `internal/services/inline/engine.go`
- `internal/plugin/inline_runtime_test.go`

Existing guarantees include:

- lifecycle-owned handlers;
- generation-sensitive handler versioning;
- explicit access/cache policy;
- max 50 results;
- bounded title/description/text/id sizes;
- interactive results forced to `CacheNone`;
- local media never enters persistent/shared inline cache;
- prepared query work enters shared TaskEngine.

### Legacy a1 Assistant stack

Status: **BASELINE-CLOSED / already removed**

Evidence:

- `internal/assistant/legacy_stack_test.go`

The production tree must continue to reject:

- `LegacyAssistantMenu`;
- `CompatibilityHost`;
- `MenuInstanceStore`;
- old Assistant menu/presentation/callback packages;
- `a1:` callback envelopes.

P8-G must not recreate this stack under a different package name.

## Behavioral parity matrix

### Control / public plane

| Behavior | Ultroid contract | Goultroid current state | Class | P8 owner |
| --- | --- | --- | --- | --- |
| Separate Assistant bot runtime | Assistant client separate from user account | implemented | BASELINE-CLOSED | regression only |
| Public `/start` | visitor/public entry | implemented through typed deep-link/start flow | BASELINE-CLOSED | regression only |
| Owner home/control shell | owner callback menu | a2 shell | BASELINE-CLOSED | regression only |
| Status | owner status surface | a2 status screen | BASELINE-CLOSED | regression only |
| Help/module navigation | generated/help maps in Ultroid | generated from canonical command/catalog data | BASELINE-CLOSED | regression only |
| Settings navigation | callback settings | a2 typed settings | BASELINE-CLOSED | regression only |
| Free-form settings input | conversation | bounded input/session state machine | BASELINE-CLOSED | regression only |
| Update/restart/plugin control | control-plane actions | canonical Assistant commands already exposed where allowed | BASELINE-CLOSED | regression only |
| Addon management | owner Assistant control | `addon` command supports Assistant surface | BASELINE-CLOSED | regression only |
| Presentation localization / language selection | Assistant localization menu | localization service exists, but Assistant shell presentation is not locale-driven and no Assistant language selector is present | MUST | P8-F residual parity |

### Canonical command surface

| Behavior | Current state | Class |
| --- | --- | --- |
| userbot-only command stays userbot-only | implemented by surfaces | BASELINE-CLOSED |
| dual userbot + Assistant command | implemented through one `core.Command` | BASELINE-CLOSED |
| owner/sudo/public policy separation | implemented | BASELINE-CLOSED |
| resource-bearing Assistant command reaches TaskEngine | implemented | BASELINE-CLOSED |
| no Assistant command registry #2 | architecture invariant | BASELINE-CLOSED |

### SavedResponse / custom Assistant response parity

| Behavior | Goultroid state | Class |
| --- | --- | --- |
| Assistant command binding | implemented | BASELINE-CLOSED |
| Inline binding | implemented by `savedresponse.InlineSource` | BASELINE-CLOSED |
| a2 callback binding | implemented | BASELINE-CLOSED |
| Deep-link binding | implemented | BASELINE-CLOSED |
| media-aware delivery | implemented | BASELINE-CLOSED |

This supersedes Ultroid-style `.addcmd` storage duplication. P8 must not create another custom Assistant response repository.

### PM relay / public messaging

| Behavior | Goultroid state | Class |
| --- | --- | --- |
| visitor → owner relay | durable P6 service | BASELINE-CLOSED |
| owner reply → visitor | durable P6 service | BASELINE-CLOSED |
| relay mapping survives restart | accepted in P6-H | BASELINE-CLOSED |
| visitor registry | durable bounded audience registry | BASELINE-CLOSED |
| block/ban visitor policy | implemented | BASELINE-CLOSED |
| force-sub | implemented | BASELINE-CLOSED |
| audience broadcast | shared bounded broadcast path | BASELINE-CLOSED |

### Group / manager plane

Status for the complete domain: **BASELINE-CLOSED by P7-A→L**.

Frozen P8 assumption:

- group contextual principal is separate from global Owner/Sudo identity;
- Telegram role is freshly revalidated where required;
- bot/actor/target hierarchy is checked before physical mutation;
- group rules are interest-indexed and bounded;
- group/topic traffic does not create permanent workers;
- topic ordering does not multiply chat quota;
- reply/topic/media boundaries fail closed.

P8 must not build a second manager-bot subsystem for advanced workflows.

### Existing complex interactive feature

MyXL status: **BASELINE-CLOSED as a2 proof surface**.

Evidence:

- `plugins/myxl/assistant_interaction.go`

It already proves:

- FeatureSpec-owned screens/actions;
- bounded free-form input;
- typed a2 action slots;
- lifecycle generation;
- multi-step transactional workflow.

Therefore P8 does not need another stateful business workflow merely to prove a2 can handle multi-step state.

## Remaining MUST parity capabilities

### 1. Production self-inline / render bridge

Current status: **GAP**

Class: **MUST**

Target owner: **P8-B**

The generic presentation abstraction exists, but no production capability currently provides the full Ultroid semantic:

```text
userbot feature
    ↓
transport-neutral interactive View
    ↓
Assistant inline rendering
    ↓
inline result selected into target chat
    ↓
a2 callback continuity
```

P8-B must provide this without letting feature code manually:

- query its own Assistant;
- click a result;
- allocate ad-hoc temporary global presentation IDs;
- invent callback bytes.

The render bridge must remain bounded and generation-aware.

### 2. Callback-heavy calculator canary

Current status: **MISSING**

Class: **MUST**

Target owner: **P8-C**

Parity objective is not to clone Ultroid's `CALC` map or Python `eval`.

Required proof:

- command/userbot entry can render the same interactive calculator through the canonical presentation path;
- inline entry is feature-owned;
- state lives in bounded P1 session state;
- no `eval`;
- stale revision rejected;
- cross-user callback rejected;
- plugin disable/reload invalidates old state;
- no permanent worker/ticker.

### 3. Locale-aware Assistant presentation

Current status: **PARTIAL**

Class: **MUST**

Target owner: **P8-F**

The repository already has `internal/services/localization` with English/Indonesian catalogs, but the Assistant shell currently presents hard-coded text and Settings has no locale registration.

P8 parity requires one canonical locale selection/configuration path and locale-aware Assistant presentation.

This does **not** require reproducing every Ultroid translation pack.

### 4. Unload/reload cross-surface acceptance

Current status: **PARTIAL foundation, missing final cross-surface matrix**

Class: **MUST**

Target owner: **P8-H**

Required final proof:

```text
load
  command + inline + actions + screens visible

disable
  commands removed
  inline removed
  actions removed
  sessions cancelled
  TaskEngine scope cancelled
  old a2 token invalid

enable
  new generation
  old token remains invalid
  new surfaces operational
```

### 5. Resource / idle / high-load acceptance

Current status: **PARTIAL per subsystem**

Class: **MUST**

Target owner: **P8-I**

P8 must validate combined cross-surface load, not only isolated subsystem bounds.

## REPRESENTATIVE parity capabilities

### Rich network lookup

Current status: **PARTIAL**

Class: **REPRESENTATIVE**

Target owner: **P8-D**

Current reusable backend candidate:

- `plugins/wikipedia`
- canonical userbot + Assistant command already exists.

P8-D should expose at least one production FeatureSpec-owned inline lookup using existing network/runtime boundaries.

Required behavior:

- bounded results;
- explicit cache scope;
- cancellation;
- no unbounded per-query/user cache;
- no one-handler-per-query registration;
- lifecycle cleanup.

Once this succeeds, provider-specific copies such as Google/F-Droid/OrangeFox/Twitter/Saavn are not P8 blockers.

### Interactive downloader / YouTube-style flow

Current status: **backend present, interactive parity missing**

Class: **REPRESENTATIVE, but required as the heavy-workflow proof**

Target owner: **P8-E**

Existing downloader backend already provides:

- shared TaskEngine continuation;
- bounded download size;
- `download` resource;
- `process` only for extractor providers;
- scoped temp workspace;
- persistent media ownership.

P8-E must prove one interaction of the form:

```text
query / select source
    ↓
choose audio/video or equivalent variant
    ↓
choose format
    ↓
bounded TaskEngine download/process work
    ↓
deliver/edit result
```

It does not require cloning all Ultroid YouTube UI details.

## Explicit EXCLUDED parity items

The following are not P8 closure blockers unless separately requested later.

### Provider-for-provider inline catalog cloning

Excluded as individual parity blockers:

- Google;
- Play Store;
- F-Droid;
- OrangeFox;
- Twitter/X lookup;
- Saavn;
- Telegram TL object search;
- arbitrary third-party app/mod indexes.

Their common interaction capability is covered by P8-D.

### Remote arbitrary code execution through public/inline workflow

Ultroid's Piston-style code execution is **EXCLUDED** from P8 parity.

Goultroid already has an owner-only system execution capability with explicit platform/process boundaries. P8 does not need a new remote-code product surface merely to match Ultroid's inline catalog.

### Games / novelty inline features

Games and novelty assistant features are **EXCLUDED** from P8 closure.

They are ordinary product features, not interaction architecture acceptance requirements.

### Exact visual/text parity

Button labels, emojis, text wording, screen ordering, and provider-specific thumbnails do not require byte-for-byte Ultroid parity.

The target is behavioral capability plus Goultroid lifecycle/resource invariants.

## Reclamation classification

P8-G must use reference-driven reclamation.

### DELETE when proven dead

- transitional Assistant migration adapters that have no production/test ownership reason;
- obsolete comments/docs that claim a1 is still required;
- dead compatibility shims whose callers are fully gone.

### KEEP as architecture fences

- `internal/assistant/legacy_stack_test.go`;
- generation/reload regression tests;
- stale callback/session tests.

### DO NOT classify as Assistant legacy automatically

These belong to different migrations and require their own exit criteria:

- `internal/services/savedresponse/ledger_compat.go`;
- `internal/services/savedresponse/registry_compat.go`;
- media ownership compatibility/reconciliation code.

### Audit separately

Generic raw callback/message-hook compatibility must only be removed if current production references are gone and capability migration is complete.

P8 must not delete it merely because the type/comment contains the word `legacy`.

## P8 phase ownership after freeze

The frozen sequence is:

```text
P8-A  behavioral inventory + target freeze                 CLOSED
P8-B  production self-inline / RenderBridge
P8-C  calculator callback-heavy canary
P8-D  representative rich search/lookup inline
P8-E  representative interactive downloader workflow
P8-F  complete behavioral matrix + remaining MUST UX
      residuals, including locale-aware Assistant UI
P8-G  reference-driven compatibility/dead-stack reclamation
P8-H  unload/reload/generation cross-surface acceptance
P8-I  resource/idle/high-load acceptance
P8-J  final cleanup, parity freeze, and closure
```

No future P8 phase may silently convert a REPRESENTATIVE provider into a MUST provider without updating this inventory and recording the reason.

## Acceptance baseline by P8-J

P8 can close only when all MUST items are implemented and accepted, and every REPRESENTATIVE capability has at least one production proof.

Expected final matrix:

| Domain | Expected P8-J state |
| --- | --- |
| Assistant shell/control | closed |
| public `/start` | closed |
| canonical dual commands | closed |
| generated Help | closed |
| Settings/input | closed |
| locale-aware Assistant presentation | closed |
| SavedResponse surfaces | closed |
| deep-link | closed |
| typed callbacks | closed |
| PM relay/audience/force-sub | closed |
| group manager/rules/moderation | closed |
| reply/topic/media | closed |
| MyXL a2 workflow | closed |
| production self-inline bridge | closed |
| calculator canary | closed |
| representative rich lookup inline | closed |
| representative interactive downloader | closed |
| plugin unload/reload cross-surface lifecycle | closed |
| idle/load/resource matrix | closed |
| legacy a1 stack | absent + regression-fenced |

## Non-goals

P8-A intentionally does not:

- implement RenderBridge;
- add calculator;
- add a search provider;
- add YouTube integration;
- delete compatibility code;
- change TaskEngine;
- change RPC policy;
- change P7 group behavior;
- claim CI/runtime results.

Those belong to their frozen downstream phases.

## Next phase

Proceed to **P8-B — production self-inline / RenderBridge**.

P8-B is the correct next dependency because calculator and heavy interactive downloader must consume a shared rendering capability rather than each reimplementing the userbot→Assistant inline trick independently.
