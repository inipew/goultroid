# Goultroid — P2-D Dead Legacy UI Helper Reclamation Closure

Date: 2026-09-27  
Branch: `test-next`  
Implementation baseline: `6261169c07de9c26f60304b0136c14a1e2a85b30` — `refactor(ui): reclaim dead legacy helpers`

## Status

**P2-D — CLOSED.**

P2-D re-audited `internal/ui` after P1-F callback reclamation and removed only helpers with no current production ownership. It did not redesign interaction, callbacks, presentation, Telegram encoding, or execution.

## Reclaimed compatibility surface

Deleted files:

- `internal/ui/actions.go`
- `internal/ui/confirmation.go`
- `internal/ui/navigator.go`
- `internal/ui/wizard.go`

Removed from the remaining button/menu surface:

- generic action bars, retry/loading buttons;
- toggle/state-toggle, steppers, selectors, duration picker;
- callback pagination/nav builders;
- confirmation/preview callback cards;
- presentation-role-to-legacy-button adapters;
- convenience callback markup rows.

Tests that existed only to exercise that reclaimed compatibility surface were removed or narrowed.

## Production/pure UI retained

Current production still uses or benefits from:

- `Card` and pure HTML/value formatting;
- progress formatting;
- `Screen`;
- `Button`, `ButtonRow`, and `Markup` transport-neutral values used by current MyXL presentation;
- `PaginateSlice` used by Settings;
- `PresentUserError`;
- `internal/ui/render` as the compatibility adapter from UI values to canonical presentation compilation.

The UI adapter does not serialize Telegram keyboards itself. It continues to call `presentationtelegram.EncodeMarkup(rows)`.

## Architecture fence

`internal/architecture/ui_helper_reclamation_p2d_test.go` now:

- requires the reclaimed legacy files to remain absent;
- rejects resurrection of the removed callback-era helper names;
- requires representative pure/production UI helpers to remain;
- requires UI Telegram markup adaptation to delegate to the canonical presentation/telegram encoder.

The older P1-A vocabulary fence was updated so it no longer requires legacy role-button adapters. Common labels remain owned by `internal/presentation`.

## Invariants

P2-D introduced no:

- callback protocol;
- interaction/session runtime;
- TaskEngine;
- Telegram RPC executor;
- Telegram keyboard serializer;
- localization authority;
- worker/ticker/cache.

## Verification limitations

CI was not inspected.

The container still cannot resolve github.com for a complete executable checkout. Therefore this closure does not claim execution of repository-wide `gofmt`, `go build`, `go vet`, `go test`, race tests, or benchmarks. Source/diff inspection plus committed architecture fences are the available acceptance evidence.

## Next

**NEXT = P3-A — benchmark current post-refinement source before any optimization.**

Do not start P3-C from old hotspot assumptions. P3-A measurements are the authority.
