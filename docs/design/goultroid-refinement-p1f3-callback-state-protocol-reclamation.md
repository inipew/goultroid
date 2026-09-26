# P1-F3 — Legacy StateStore + v1 protocol reclamation

Date: 2026-09-26
Branch: `test-next`
Baseline before implementation: `31a78a8fbe240c807d85357b44e6d2208bc845d8`

## Scope

P1-F3 removes the legacy callback state authority and the retired v1 callback protocol while deliberately leaving P1-F4 ownership intact.

P1-F4 is **not** part of this change. The following compatibility surfaces remain temporarily:

- `callback.Router`;
- `callback.Handler` / `HandlerWithOptions`;
- plugin-manager callback registration/cleanup;
- native Dispatcher callback Router wiring;
- Assistant `CoreCallbackDispatcher` bridge;
- legacy `CallbackContext` transport conveniences.

The purpose of the temporary shell is compile/lifecycle separation only. It is no longer a production namespace execution path.

## State reclamation

Removed from production:

```text
callback.StateStore
callback.StateWriter
callback.StateScope
callback.NewStateStore
callback.NewScopedStateWriter
module.TelegramRuntime.CallbackStore
module.Runtime.ScopedCallbackStore
App.callbackStore
coreDependencies.callbackStore
runtime registration of callback_store
```

Deleted callback production files:

```text
internal/services/callback/lifecycle.go
internal/services/callback/scope.go
internal/services/callback/scoped_writer.go
internal/services/callback/state.go
internal/services/callback/store.go
```

The callback package now has exactly three production files:

```text
middleware.go
router.go
types.go
```

No replacement StateStore or compatibility state adapter was introduced. Interactive state remains owned by the canonical a2 `interaction.Runtime`.

## v1 protocol reclamation

Removed:

```text
CallbackVersion1
EncodeCallbackData
EncodeCallbackDataChecked
ParseCallbackData
legacy namespace/action/opaque validation
HandlerWithStatePolicy
RequiresState
ErrStateExpired
ErrStateNotFound
ErrStateConsumed
ErrStateScopeStale
ErrUnauthorized
```

No production `v1:` producer/parser remains.

The generic UI error presenter no longer maps legacy state/authorization errors. It retains only compatibility mappings needed by the temporary Router shell until P1-F4.

## Temporary Router compatibility policy

The existing Router object remains because P1-F4 owns transport/bootstrap/plugin removal.

Its P1-F3 admission policy is intentionally narrow:

```text
raw "noop"
  -> prepare terminal no-op lease
  -> existing TaskEngine bridge may dispatch it
  -> AnswerCallbackQuery("", false)

every other non-a2 callback
  -> do not parse a namespace
  -> do not resolve plugin scope
  -> do not enter StateStore
  -> do not submit callback work to TaskEngine
  -> AnswerCallbackQuery("⌛ Interaction expired. Please reopen it.", false)
  -> return ErrHandlerNotFound
```

This intentionally ends the already-issued v1 compatibility window at P1-F3.

A registered legacy Handler may still exist structurally until P1-F4, but no production ingress can route a namespace payload to it. Regression coverage explicitly registers a legacy handler and proves that a retired payload is rejected before handler execution.

## Limiter and execution ownership

The shared `interLimiter` remains wired into Router until P1-F4 because its composition removal belongs to Router reclamation.

P1-F3 no longer calls callback rate-limit admission for retired non-a2 payloads. Therefore it creates no new `callback:<user>` buckets.

The limiter itself remains shared infrastructure and is not removed.

TaskEngine remains canonical. Raw `noop` can still pass through the existing bridge during the compatibility shell. Retired namespace payloads are rejected before TaskEngine submission.

## Resource effect

The legacy callback state budget is removed:

- maximum 5,000 StateStore entries;
- maximum 8 MiB retained callback state;
- maximum 64 KiB per item;
- opaque state cloning/eviction/pruning paths.

This is reclaimed *capacity*, not a claim that the running process previously held 8 MiB.

Callback-owned background workers before P1-F3: 0.
Callback-owned background workers after P1-F3: 0.
Callback-owned tickers before/after: 0.

a2 session bounds/resources are unchanged.

## Tests and architecture fences

Updated:

- callback Router tests: state/v1 scenarios retired; raw noop and fail-closed retired-payload behavior covered;
- callback contract tests: residual callback policy + `expired_legacy` metrics;
- Assistant callback bridge tests: no v1 parser/encoder/StateStore dependency; registered legacy handler cannot execute through canonical Router after F3;
- UI error tests: state-only callback errors removed;
- F1-A importer allowlist shrunk;
- F1-C consumer topology drops StateStore/ScopedCallbackStore edges;
- F1-D production file inventory shrunk to three files;
- callback API surface fence forbids reintroducing retired F3 constructors/encoders/parsers;
- F1-E exact symbol allowlist shrunk and its prior malformed quote literal corrected;
- new `legacy_callback_p1f3_test.go` rejects callback state/v1 symbols, raw production `v1:` payloads, module callback-state capability, and callback-package file regrowth.

Deleted test:

`internal/services/callback/store_hardening_test.go`

because the implementation it exclusively tested is removed.

## Formatting / verification

Rewritten/new callback and architecture Go sources were formatted with `gofmt` before commit. Simple source removals preserve existing formatting.

No CI was inspected.

A full repository build/test run is not claimed because this session has no executable repository checkout: direct GitHub access from the container remains unavailable, while repository mutation/read access is through the GitHub connector.

## P1-F3 closure

P1-F3 removes the state/v1 authority without performing P1-F4 Router/bootstrap reclamation.

P1-F3: **CLOSED** after this change is committed.

Next phase, only after explicit user confirmation:

**P1-F4 — remove legacy Router + bootstrap/plugin wiring.**
