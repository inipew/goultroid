# P1-F1-B — Legacy callback producer inventory

Date: 2026-09-26  
Branch: `test-next`  
Audited baseline: `53a198fb984fe544ec2f4d2fe73b1ef94544e9a3`

## Scope

P1-F1-B inventories every production site that can create or emit legacy callback payloads for the retired v1 protocol:

```text
v1:<namespace>:<action>:<opaque-id>
```

This phase is producer inventory/freeze only. It does **not** remove the parser, encoder, Router, StateStore, or callback fallback path.

The local container still cannot resolve `github.com`, so a direct checkout/grep was not available. The inventory therefore combines:

1. P1-F1-A's exact production import allowlist;
2. source inspection of every direct `internal/services/callback` production importer;
3. source inspection of the callback protocol implementation;
4. source inspection of all generic UI callback builders and the canonical a2 presentation compiler;
5. explicit inspection of the MyXL semantic-intent adapter, which is a known false-positive class;
6. an architecture fence that scans all production Go source when tests run and rejects any new v1 producer outside the legacy callback package.

GitHub code search was not used as proof because it is not authoritative for this branch.

## Producer classification

### 1. Canonical legacy encoder

| Producer | File / symbol | Namespace | Consumer | State requirement | Target surface | Classification | Planned replacement |
|---|---|---|---|---|---|---|---|
| v1 encoder implementation | `internal/services/callback/types.go::EncodeCallbackData` | generic | callers would emit bytes consumed by legacy Router | opaque ID mandatory | Telegram callback data | `GENERIC_UTILITY` inside legacy subsystem | delete with v1 protocol in P1-F3 after producer/consumer proof |
| checked v1 encoder | `internal/services/callback/types.go::EncodeCallbackDataChecked` | generic | same | opaque ID mandatory | Telegram callback data | `GENERIC_UTILITY` inside legacy subsystem | delete with v1 protocol in P1-F3 |

The encoder constructs:

```text
v1:<namespace>:<action>:<opaque-id>
```

No production caller outside `internal/services/callback` was found.

Because any external call to either encoder must import the callback package, P1-F1-A's exact nine-file importer set was re-inspected. None invokes `EncodeCallbackData` or `EncodeCallbackDataChecked`.

**Result: zero production feature v1 encoder callers.**

### 2. Raw `v1:` payload literals

No inspected production callback/UI/presentation/plugin surface was found emitting raw `v1:` callback data.

The P1-F1-B architecture fence now scans all non-test production Go files and fails if a string literal beginning with `v1:` appears outside `internal/services/callback`.

This is deliberately scoped to a leading `v1:` literal rather than banning arbitrary occurrences of the text `v1`, because unrelated version labels are not callback protocol producers.

### 3. Generic UI callback builders — not protocol producers

The following functions create Telegram callback-button metadata but **do not define callback protocol bytes**:

```text
internal/ui.NewCallbackButton
internal/ui.NewRoleCallbackButton
internal/ui.BuildToggleSwitch
internal/ui.BuildStateToggle
internal/ui.BuildStepper
internal/ui.BuildMultiStepStepper
internal/ui.BuildSelector
internal/ui.BuildMultiSelector
internal/ui.BuildSegmentedSlider
internal/ui.BuildDurationPicker
internal/ui.BuildPaginationRow
internal/ui.BuildNavRow
internal/ui.BuildUserActionBar
internal/ui.BuildChatActionBar
internal/ui.BuildMessageActionBar
internal/ui.BuildRetryRow
internal/ui.NewPaginationRow
internal/ui.NewConfirmCancelRow
internal/ui.NewCloseRow
internal/ui.NewBackRow
internal/ui.NewStandardActionRow
internal/ui wizard/navigation helpers
```

They accept caller-provided `[]byte` and therefore are **transport containers**, not v1 producers by themselves.

Classification: `GENERIC_UTILITY`.

They must not be treated as evidence that a legacy namespace still exists. P2-D may later reclaim dead UI helpers after callback removal, but P1-F1-B does not delete them.

### 4. Canonical a2 producer — current replacement authority

The canonical interactive producer is:

```text
internal/presentation.Compiler
 -> interaction.Runtime.CallbackData
 -> interaction.EncodeCallbackToken
 -> a2:<action>:<session>.<revision>
```

This is the replacement authority for interactive callback payloads.

It is not legacy and is explicitly excluded from the legacy producer allowlist.

### 5. MyXL semantic intents — false positive, not Telegram callback protocol

`plugins/myxl/menu.go::newMenuButton` stores plugin-internal semantic intent strings such as:

```text
myxl:home
myxl:refresh
myxl:checkout
myxl:method:qris:<option-code>
assistant:close
```

as `ui.Button.Data`.

This does **not** emit those bytes as the final Assistant Telegram callback token.

The Assistant adapter:

```text
assistantScreen(...)
 -> state.Slots = append(..., string(button.Data))
 -> presentation.Button{ActionID: assistantSlotID(slot)}
 -> presentation.Compiler
 -> a2 callback token
```

Therefore these `myxl:...` strings are internal semantic intents stored in a2 session state.

Classification: `FALSE_POSITIVE` for legacy producer inventory.

The same rule applies generally: a colon-delimited internal intent is not a legacy callback payload unless it is actually serialized to Telegram as the v1 callback protocol.

## Authoritative producer matrix

| Namespace | Producer file/function | Consumer | Legacy state requirement | Target | Planned a2 replacement | Migration risk |
|---|---|---|---|---|---|---|
| none active | none outside callback subsystem | none | none | none | already a2 | none |
| generic v1 implementation only | `internal/services/callback/types.go::EncodeCallbackData*` | legacy `Router.Parse/Prepare` path if called | opaque ID required | Telegram callback bytes | `interaction.Runtime.CallbackData` / presentation compiler | low once parser/state consumers are proven zero |

**There is no remaining production feature namespace to schedule for P1-F2 based on producer evidence.**

That is a stronger result than the original handoff expected. It does **not** mean P1-F2 can be skipped yet: F1-C and F1-D still need to prove whether residual consumers/state wiring exist solely as compatibility infrastructure or whether an indirect producer/consumer edge remains.

## Consumer side observed during producer audit

The legacy parser remains in `internal/services/callback/types.go::ParseCallbackData`, and the legacy Router still prepares/dispatches callback data after native a2 declines ownership.

That means the current repository can still **consume** legacy v1 payloads even though no current production feature producer was identified.

This distinction is critical:

```text
producer count = zero
consumer/fallback count != proven zero yet
```

P1-F1-C owns the consumer/router/bootstrap proof.

## P1-F1-B freeze fence

`internal/architecture/legacy_callback_producers_p1f1b_test.go` enforces:

1. no production file outside `internal/services/callback` may call `EncodeCallbackData` or `EncodeCallbackDataChecked`;
2. no production file outside `internal/services/callback` may contain a raw string literal beginning with `v1:`;
3. the canonical legacy encoder remains confined to the legacy subsystem until P1-F3 deletes it.

This fence is intentionally producer-focused. It does not yet ban parser/router/state references.

## Migration-order implication

F1-B does not produce a list of feature namespaces for F2 because the current producer list is empty.

The likely next decision after F1-C/D is one of:

```text
A. consumer/state audit finds hidden feature ownership
   -> migrate that exact namespace in P1-F2

B. consumer/state audit confirms infrastructure-only compatibility
   -> P1-F2 has no namespace migration work
   -> proceed, after explicit closure, to state/protocol reclamation in P1-F3
```

Do not choose between A and B before F1-C/D finish.

## Closure

P1-F1-B conclusion:

- zero production feature v1 producers found;
- zero external production callers of `EncodeCallbackData*` found;
- generic UI callback builders are protocol-neutral and not counted as v1 producers;
- MyXL `myxl:...` button data is confirmed semantic/session intent, not Telegram v1 protocol;
- a2 is the canonical current callback-token producer;
- legacy consumer compatibility remains and is intentionally deferred.

Next phase, only after explicit user confirmation:

**P1-F1-C — consumer/router/bootstrap inventory.**
