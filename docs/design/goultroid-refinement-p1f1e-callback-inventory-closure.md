# P1-F1-E — Legacy callback inventory closure + freeze

Date: 2026-09-26
Branch: test-next
Audited baseline: 4fb5bb77d9a0a1186eae358699b9402f7eb74db3

## Scope

P1-F1-E closes the P1-F1 inventory program by consolidating P1-F1-A through P1-F1-D. This phase freezes the exact remaining production surface; it does not delete or migrate production callback behavior.

## Authoritative closure result

The repository has no identified production feature namespace remaining on the legacy callback stack.

- production feature v1 producers: 0
- production feature callback.Handler implementations: 0
- production feature StateStore writers: 0 identified
- Settings legacy callback surface: 0
- MyXL legacy callback surface: 0

What remains is infrastructure compatibility only: application composition, module callback-state compatibility capability, plugin.Manager legacy Handler registration, native/userbot non-a2 Router fallback, Assistant non-a2 Router fallback, legacy CallbackContext/error mapping, and the legacy callback package itself.

There is therefore no unknown feature namespace to migrate before state/protocol reclamation.

## Final production API allowlist

P1-F1-E strengthens the freeze from file-level imports to exact callback-package symbols per production file.

| Production file | Exact permitted symbols | Classification | Removal |
|---|---|---|---|
| internal/app/app.go | StateStore | composition compatibility | P1-F3 |
| internal/app/dependencies.go | Router, StateStore | composition compatibility | P1-F3/F4 |
| internal/app/wiring_core.go | NewRouter, NewStateStore | construction compatibility | P1-F3/F4 |
| internal/assistant/client/servicer.go | PreparedCallback | Assistant legacy bridge | P1-F4 |
| internal/module/module.go | StateWriter, NewScopedStateWriter | state compatibility capability | P1-F3 |
| internal/plugin/manager.go | Handler, Registration | legacy plugin registration | P1-F4 |
| internal/telegram/dispatcher.go | Router | native fallback wiring | P1-F4 |
| internal/telegram/dispatcher_accessors.go | Router | native fallback wiring | P1-F4 |
| internal/ui/toast.go | CallbackContext, ErrUnauthorized, ErrStateExpired, ErrStateNotFound, ErrInvalidCallbackData, ErrHandlerNotFound | UX/error compatibility | P1-F3/F4 |

No plugins production file is allowlisted. The allowlist is exact and stale-sensitive: adding a symbol fails, and removing a symbol requires intentionally shrinking the fence.

## Producer and namespace closure

There is no production feature producer allowlist. The v1 encoder implementation remains only inside internal/services/callback/types.go until P1-F3 removes the protocol.

Canonical callback token production remains:

presentation.Compiler -> interaction.Runtime.CallbackData -> interaction.EncodeCallbackToken -> a2 token

Final namespace matrix:

| Domain | Legacy producer | Legacy Handler | Legacy state writer | Current authority | F2 migration |
|---|---:|---:|---:|---|---:|
| Settings | none | none | none | native + Assistant a2 | no |
| MyXL | none | none | none | native + Assistant a2 | no |
| SavedResponse callback | none | none | none | application-owned a2 feature | no |
| other built-in plugins | none identified | none identified | none identified | declared command/feature/a2/inline authorities | no |
| infrastructure residual | n/a | registration API only | composition API only | compatibility only | not a namespace migration |

## Exact P1-F2 worklist

EMPTY.

F1-E found no remaining production legacy namespace for a P1-F2 namespace migration subphase. Do not invent a namespace migration merely to satisfy the original roadmap heading.

The requested risk ordering collapses to:

- small/read-only namespace: none
- bounded stateful namespace: none
- mutation namespace: none
- high-risk purchase/admin namespace: none
- bootstrap-only residual: handled by P1-F3/P1-F4, not F2

The next executable phase after explicit user confirmation is P1-F3 — remove legacy StateStore + legacy v1 protocol.

## Zero-unknown-caller fence

internal/architecture/legacy_callback_f1_closure_test.go performs a repo-wide production Go scan and enforces:

1. every import of internal/services/callback is in the exact infrastructure allowlist;
2. every selector used through that import is in the exact per-file symbol allowlist;
3. dot and blank imports of the legacy package are forbidden;
4. raw production v1 callback literals outside the legacy package are forbidden;
5. Settings and MyXL are independently fenced against legacy state/protocol/Handler surfaces.

This closes the gap left by file-only inventory: an allowlisted compatibility file cannot silently start using another legacy API.

The existing F1-A/B/C/D fences remain intentionally overlapping because they provide narrower importer, producer, topology, and resource regression failures.

## Remaining compatibility graph

Native/userbot:

Telegram callback -> transport idempotency/EventBus -> native a2 ownership -> non-a2 legacy Router fallback -> TaskEngine -> legacy PreparedCallback

Assistant:

Assistant callback -> a2 ownership/InteractionIngress -> non-a2 CoreCallbackDispatcher fallback -> TaskEngine -> legacy PreparedCallback

Legacy state:

buildCore -> StateStore -> Router state resolution -> module.TelegramRuntime.CallbackStore compatibility capability

No built-in feature namespace was found at the end of these compatibility paths.

## Required next order

P1-F3:
- remove module callback-state capability;
- remove StateStore;
- remove v1 encoder/parser/protocol;
- remove legacy state/error mappings;
- atomically narrow any temporarily retained Router so it no longer depends on state/v1;
- preserve explicit residual noop/unknown callback ACK semantics.

P1-F4:
- remove Handler/registration lifecycle;
- remove plugin.Manager callback cleanup branch;
- remove native legacy Router fallback;
- remove Assistant CoreCallbackDispatcher fallback;
- remove Router/CallbackContext/middleware;
- leave one explicit unknown/non-a2 callback policy at ingress.

P1-F5:
- repo-wide zero-legacy acceptance;
- lifecycle/resource acceptance.

P1-F3 must not recreate a state store, v1-compatible adapter, or second interaction runtime.

## Resource baseline

Carried from F1-D:

- StateStore capacity: 5,000 entries
- StateStore retained-byte ceiling: 8 MiB
- per-item ceiling: 64 KiB
- default TTL: 15 minutes
- callback-owned background goroutines: 0
- callback-owned tickers: 0
- private callback limiter: none

Shared interLimiter, TaskEngine, metrics, EventBus, idempotency/dedupe, Inline engine, RPCExecutor, plugin generation scopes, and a2 runtime remain outside the reclamation target.

## Freeze behavior during reclamation

Because F2 has no feature namespace work, the feature allowlist is already empty.

During P1-F3/P1-F4, the direct import/symbol allowlist and callback package file inventory may only shrink. No new v1 compatibility, plugin legacy dependency, or namespace/state/protocol authority may be introduced. Settings and MyXL must remain zero-legacy permanently.

## P1-F1 closure

P1-F1-A: CLOSED
P1-F1-B: CLOSED
P1-F1-C: CLOSED
P1-F1-D: CLOSED
P1-F1-E: CLOSED

P1-F1 is CLOSED.

P1-F2 has no namespace migration work identified by the authoritative F1 matrix.

Next executable phase, only after explicit user confirmation:

P1-F3 — remove legacy StateStore + legacy v1 protocol.
