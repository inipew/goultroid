# Goultroid — P2-C Response/Error Modernization Closure

Date: 2026-09-27  
Branch: `test-next`  
Code acceptance baseline: `89790e517001ee1432a0e91927ac468d36a58a56` — `test(errors): fence indirect diagnostic leaks`

## Status

**P2-C — CLOSED.**

P2-C modernizes user-facing failure presentation without introducing a second sanitizer, response engine, RPC path, or execution runtime. The existing P0-B typed boundary remains canonical:

- `core.Context.Fail(cause, safeMessage)`
- `core.WithUserMessage(...)`
- `core.UserMessage(...)`
- semantic helpers such as `Status`, `Progress`, `Success`, and `Result`

Internal causes remain available to logging/metrics/errors.Is/errors.As while user-visible Telegram output receives only explicitly safe text.

## Build/vet drift repaired before continuation

At the start of this continuation the user reported stale build/vet failures involving:

- unused `target` in `internal/assistant/client/updates.go`
- retired callback package import in `internal/assistant/testing/matrix_test.go`
- syntax drift in `plugins/settings/native_interaction_test.go`
- stale `:=` in `p5_acceptance_test.go`
- `spec.Input` type assertion in downloader E2E acceptance
- stale Help import
- retired `AnswerToast`
- retired MyXL `RequiresCallbackState`

Current HEAD had already advanced through `c214a3c9efceee2df904dc6ab879563c1c6bb156` (`fix(test): repair post-reclamation build drift`) plus later P2-C commits. Source refresh confirmed all reported constructs are gone/current in the branch before this continuation changed P2-C behavior.

No claim is made that `go build` or `go vet` was executed in this session because the container still cannot resolve github.com for a complete checkout.

## Existing P2-C migration already present on refresh

The branch had already accumulated the main migration chain from `38ba25c8...` through `63be01b5...`, covering Settings, Admin, AFK, Blacklist, Broadcast, Calculator, Downloader, Filters, Info, Locks, MyXL Assistant, OCR, Pin, PMPermit, Quote, Scheduler validation, Sticker, Sudo, UserLog, Voice, Wikipedia, and Assistant saved-response administration.

The architecture fence `internal/architecture/user_error_p2c_test.go` already rejected direct raw-error presentation in production `plugins/` and `internal/assistant/`, and preserved the one intentional raw diagnostic exception: owner-only/userbot-only bounded+escaped `.exec`.

## Residuals closed by this continuation

Fresh audit against current source found indirect/raw residuals that were not fully represented by the historical list:

1. Broadcast rich-media capture exposed `captureErr` and TaskEngine `Failure.Message` directly.
   - replaced with `ctx.Fail(..., "Could not capture replied broadcast.")`
   - internal task outcome remains in the returned cause

2. Filters media capture exposed `captureErr` through `EditOrReply(fmt.Sprintf(...))`.
   - replaced with `taskCore.Fail(..., "Could not capture replied response.")`

3. Clone async state re-check exposed `stateErr` in two paths.
   - replaced with `taskCore.Fail(stateErr, "Failed to re-check clone state.")`

4. Scheduler exposed persisted `ScheduledJob.LastError` and history `ErrorMsg` to any Sudo caller.
   - list/history still show failure state, attempts, timestamps, and duration
   - raw persisted diagnostic strings are no longer rendered to Telegram

5. Regression fence expanded with exact markers for Broadcast, Filters, Clone, and Scheduler indirect diagnostic leaks.

Continuation commits:

- `6221ef97617fd4a147d3e4216be1c41d31e8af89` — sanitize broadcast capture failures
- `84a995e5d3b2bb51dc64053133722919dfe29b53` — sanitize filter capture failures
- `9faa38fba22a9942718b66bf6b6f059590ead9cb` — fence capture error presentation
- `bc6ed55efac915659cc44fef7dabb6a04d65e8bb` — sanitize clone state re-check failures
- `064c39938f3d31623be5e1637126dc9e5a49cc7e` — hide scheduler internal diagnostics
- `89790e517001ee1432a0e91927ac468d36a58a56` — fence indirect diagnostic leaks

## Accepted exceptions and safe classifiers

Not every `err.Error()` occurrence is user-facing.

Accepted examples include:

- Admin/Locks/Pin error-string inspection used only to classify Telegram error semantics before returning localized safe copy.
- MyXL purchase persistence stores the internal error for transaction/audit state without directly presenting it.
- OCR uses an explicit bounded, normalized, HTML-escaped sanitizer for OCR tool diagnostics.
- Downloader `failedView(err,...)` intentionally ignores the raw error and returns a fixed localized failure view.
- `.exec` remains the explicit owner-only diagnostic exception, bounded and escaped by architecture fence.

## Architecture/resource invariants

P2-C does not alter:

- TaskEngine ownership/admission
- Telegram RPCExecutor or FloodWait policy
- a2 interaction/session runtime
- plugin generation/lifecycle semantics
- mutation authorization/revalidation
- resource/caching cardinality
- localization authority introduced by P2-B

No new worker, registry, sanitizer service, or background loop was introduced.

## Verification limitations

CI was not inspected.

Direct container access to github.com remains unavailable, so a complete executable checkout could not be obtained. Consequently this session does not claim execution of `go build`, `go vet`, `go test`, race tests, benchmarks, or repository-wide `gofmt`.

The connector-written changes preserve the existing gofmt-shaped formatting, but this document intentionally does not claim that the `gofmt` command was run.

## Next

**NEXT = P2-D — remove dead legacy UI helpers.**

Do not reopen P2-C unless current source or executable verification shows a concrete regression.
