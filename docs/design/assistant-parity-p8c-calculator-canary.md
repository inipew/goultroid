# Assistant Parity P8-C — calculator callback-heavy canary

## Status

**P8-C is CLOSED for implementation/source acceptance.**

Baseline:

`3ae37805080134f571c936293528eeb46b823cf6` — P8-B self-inline RenderBridge.

P8-C is the first production feature proving that the P0→P3 interaction foundation and P8-B RenderBridge work together for a callback-heavy userbot workflow without recreating Ultroid's global `CALC` state.

## User-visible flow

```text
.calc [expression]
    ↓
canonical userbot command
    ↓
P8-B selfinline.Renderer
    ↓
own Assistant inline query: calc <expression>
    ↓
FeatureSpec-owned Inline vNext result
    ↓
P1 bounded interaction session
    ↓
typed a2 calculator buttons
    ↓
Assistant callback ingress / TaskEngine
    ↓
calculator FeatureDriver action
    ↓
session Transition
    ↓
inline message edit
```

The same calculator inline surface may also be invoked directly through Telegram inline mode.

## Canonical ownership

The calculator is one normal plugin:

`plugins/calculator`

It owns:

- one canonical `.calc` / `.calculator` command;
- one `InteractionInline` declaration;
- fixed typed `InteractionAction` declarations;
- one FeatureSpec-owned inline binding;
- one FeatureDriver binding for typed actions.

There is no:

- `assistant/calculator` subsystem;
- calculator callback router;
- calculator TaskEngine;
- calculator session registry;
- calculator worker.

## Self-inline capability injection

P8-B exposed the production renderer at App level. P8-C adds explicit feature injection:

`internal/app/selfinline_features.go`

Only plugins implementing:

`SetSelfInlineRenderer(selfinline.Renderer)`

receive the renderer.

The injected renderer is wrapped with:

`selfinline.Authorized`

and performs:

`CapabilityGate.Check(pluginID, plugin.CapTelegramSendMessage)`

**on every Render call**.

Capability authorization is therefore not cached inside the plugin across disable/reload or manifest changes.

The calculator manifest explicitly declares `telegram.send_message`.

## Command surface

The command is:

```text
.calc [expression]
.calculator [expression]
```

Properties:

- userbot surface only;
- Owner permission;
- no raw MTProto fallback;
- opens the interactive UI through P8-B;
- preserves reply/topic context;
- best-effort removes the invoking command after the inline result is successfully inserted.

The command does not create its own interaction state.

## Inline surface

The inline handler is FeatureSpec-owned:

```text
InteractionInline: calculator
surface: Inline
policy: Owner
cache: None
private result: true
```

Each response contains exactly one result with:

- stable result ID `calculator`;
- typed `ActionRows`;
- expression bytes as `InteractionState`;
- 10 minute session TTL.

Interactive inline results remain under Inline vNext's existing no-shared-cache rule.

## Session state

Calculator mutable state is only the expression string stored in P1:

```text
max expression state: 128 bytes
plugin global session state: 0
calculator-owned session map: 0
```

P1 remains the retention authority:

- global session capacity;
- per-feature generation capacity;
- per-actor capacity;
- total retained bytes;
- expiry;
- generation invalidation.

## Typed actions

The calculator declares a fixed action vocabulary:

- digits 0–9;
- decimal point;
- +, -, ×, ÷, %, ^;
- left/right parenthesis;
- clear;
- backspace;
- equals.

Every button is represented by a stable action ID.

No raw `callback_data` is embedded by calculator code.

The FeatureDriver registers handlers through:

`orchestration.Engine.RegisterAction`

and re-applies current feature admission for the concrete callback target.

For `presentationtelegram.InlineTarget`, the existing Assistant admission boundary correctly uses:

`execution.SourceInline`

rather than treating the callback as an Assistant message action.

## Revision and actor safety

Each successful button operation calls:

`ctx.Transition(...)`

which advances the P1 revision before rendering the new view.

Consequences:

- old buttons fail with `ErrStaleToken`;
- actor mismatch fails with `ErrBindingMismatch`;
- first concrete inline callback claims the concrete inline-message target;
- copied/different inline targets remain rejected by existing P1 binding rules.

P8-C includes a real orchestration test using:

- feature Registry;
- interaction Runtime;
- action Dispatcher;
- orchestration Engine;
- calculator FeatureDriver;
- synthetic InlineTarget.

It proves successful `=` transition, stale old token rejection, and cross-user rejection.

## Safe expression evaluator

P8-C does **not** use:

- `eval`;
- shell/process execution;
- `go/parser`;
- JavaScript/Python subprocesses.

The calculator uses a small recursive-descent arithmetic evaluator supporting:

- + / -;
- * / / / %;
- ^;
- unary + / -;
- parentheses;
- decimal numbers;
- scientific notation produced by calculator result formatting.

The expression input is capped at 128 bytes.

Division/modulo by zero and non-finite results fail closed.

## Lifecycle

Calculator action registrations are feature-generation scoped.

Existing plugin feature cleanup already performs:

```text
registry.actions.UnregisterScope(scope)
registry.interactions.CancelScope(scope)
```

Therefore disable/reload invalidates:

- action handlers from the old generation;
- live calculator sessions from the old generation;
- stale a2 tokens.

P8-H will later exercise the complete enable/disable/re-enable matrix across all surfaces.

## Idle/resource profile

P8-C adds no permanent worker.

When no calculator session exists:

```text
calculator goroutines = 0
calculator tickers    = 0
calculator timers     = 0
calculator global maps = 0
calculator retained expression bytes = 0
```

During use, mutable state is charged to the existing P1 interaction runtime.

## Generated module registration

`plugins/calculator/module.go` is a normal module and `internal/app/generated_modules.go` is updated exactly as `featuregen` would discover it.

No hand-written special-case startup registration is added.

The only application composition hook is generic renderer injection for any feature that explicitly opts into `SetSelfInlineRenderer`.

## Acceptance coverage

P8-C adds coverage for:

- FeatureSpec inline/action declarations;
- bounded private no-cache inline result;
- userbot command → self-inline renderer;
- reply/topic preservation;
- bounded calculator state;
- unknown action rejection;
- safe arithmetic precedence;
- division-by-zero/unsafe syntax rejection;
- real typed callback transition;
- stale revision rejection;
- actor binding rejection;
- per-render capability authorization;
- generated module presence;
- architecture fences against eval/process/global worker state.

## Non-goals

P8-C does not:

- add general search;
- add YouTube/download interaction;
- add locale-aware Assistant UI;
- solve final live reload acceptance for every feature;
- add another callback protocol;
- check CI.

## Next phase

Proceed to **P8-D — representative rich search/lookup inline**.

The recommended canary is the existing Wikipedia backend because it already uses the capability-gated network service and canonical userbot/Assistant command path. P8-D should add a bounded FeatureSpec-owned inline lookup rather than a second search service.


## Final depth hardening

The final source re-audit adds an explicit parser recursion bound in addition to the existing 128-byte expression limit:

```text
max parser recursion depth = 32
```

The bound applies to nested parentheses, unary recursion, and right-associative exponent recursion. This prevents parser stack depth from depending only on the byte limit.

Regression:

`TestEvaluateExpressionDepthBound`

This hardening was passed through `gofmt` before its commit.
