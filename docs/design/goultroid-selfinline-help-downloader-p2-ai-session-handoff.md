# Goultroid — AI Session Handoff: self-inline Help/Downloader runtime defects + P2 lifecycle/cutover

Date: 2026-09-25  
Branch: test-next  
Verified HEAD at handoff creation: 6c0cb3bcb174a332284d874d7a77c8adef50d39e — test(selfinline): close P1 lifecycle and end-to-end acceptance

This is the primary continuation document for the next AI session. Read this document before changing code, then refresh test-next and compare HEAD with the verified commit above.

---

## 1. Immediate live defects

### 1.1 Userbot .help renders the first menu but deeper navigation fails

Live behavior:

- .help successfully inserts an Assistant-authored inline result.
- The first Help button/menu list is visible.
- Clicking module/command buttons does not open deeper views.
- The deeper Help views already exist in source: HelpModuleView, HelpCommandView, paging and back actions.
- Runtime callback dispatch fails before those handlers execute.

Representative runtime log:

~~~text
2026-09-25T18:09:18.814+0700 WARN client/updates.go:868
assistant: interaction inline callback dispatch failed
error = interaction: action handler unavailable
~~~

The same error repeated for multiple Help button clicks.

### 1.2 .download <youtube-url> renders UI but buttons are unclickable/non-functional

The user reports that the YouTube-link downloader selection UI is rendered but its buttons do not work.

No exact downloader callback log was captured in this session. The next session must reproduce and record:

- feature ID;
- action ID;
- resolved session Scope owner/generation;
- current FeatureCatalog scope owner/generation;
- Dispatcher handler presence and scope;
- exact callback error.

Do not assume the downloader error is identical to Help until this is reproduced. Source audit nevertheless found a high-confidence lifecycle defect that can produce this exact behavior after plugin disable/enable/reload.

---

## 2. Standing project rules

1. Refresh test-next before work and record exact HEAD.
2. Compare with this handoff HEAD and read every drifted source/test file before patching.
3. Current source plus architecture/integration/regression tests are truth. Old docs can be stale.
4. Run gofmt before every commit that changes Go.
5. Do not inspect/poll/wait for CI unless explicitly requested.
6. Do not create a second interaction runtime, registry, TaskEngine, Telegram RPC executor, callback protocol, downloader engine, or retry engine.
7. Keep state/cache/cardinality bounded and idle footprint near zero.
8. Shared TaskEngine and shared Telegram RPC executor remain canonical.
9. a2 interaction runtime remains canonical for new Assistant/self-inline interactive UX.
10. Voice remains deferred.
11. Userbot-first behavior remains the primary product requirement.
12. Preserve plugin/feature generation fencing through reload.
13. Do not hide lifecycle bugs by weakening admission, generation checks, stale-token checks, or retry semantics.

---

## 3. Recent self-inline chain — do not redo blindly

Already implemented before the current live failure:

- a091aa19ac8cb5ce8fd0965ef73c4ee9236bc9a8 — fix(assistant): resolve self-inline transport lazily
  - current Telegram Service is resolved lazily; app construction no longer captures nil/stale Service.

- 10b3e3567ce0b4790e49258739a2b9c8d36e835a — fix(assistant): gate identity on current readiness
  - no fabricated GoUltroidBot username;
  - identity is current-run/readiness gated.

- 7e11c41059d89f19a3bfc22e80d3bd60bc90a6ac — refactor(assistant): remove stale readiness channel.

- feb7d23fef2a1a237d178e7a716d90d847e62d72 — fix(assistant): preflight inline bot capability
  - current bot identity checks Telegram inline capability;
  - BotFather /setinline failure is fail-closed for self-inline.

- cd85aa795ddc45cd2f1cc08a0c802d1b4357899b — fix(assistant): normalize self-inline Telegram errors.
- c91236b3b80a4b22de191ade819ecdc1fc80e02c — test(assistant): black-box self-inline diagnostics.

- 4120e3e9033f4be12d5513da334467e28fd9ea24 — feat(help): bridge userbot help through Assistant inline.
- eb4eb462e9d6f893723436eadd8d8154664ff621 — fix(help): preserve inline help continuation chrome.

- 6c0cb3bcb174a332284d874d7a77c8adef50d39e — test(selfinline): close P1 lifecycle and end-to-end acceptance.

Important correction: the latest P1 acceptance was incomplete for clickable callbacks. It proved token/session resolution but not real action-handler dispatch.

---

## 4. Confirmed root cause: .help action handler unavailable

This is a confirmed source-level defect.

### 4.1 What succeeds

The Assistant shell declares Help InteractionAction entries in its FeatureSpec, including:

- ActionHelp;
- ActionHelpPrev / ActionHelpNext;
- ActionHelpCmdPrev / ActionHelpCmdNext;
- ActionHelpBack;
- bounded Help module slot action IDs;
- bounded Help command slot action IDs.

The Inline Engine validates those declarations, creates an a2 session, and compiles ActionRows into callback tokens.

Therefore this path succeeds:

~~~text
.help
 -> selfinline.Render
 -> messages.getInlineBotResults
 -> Assistant InlineEngine
 -> assistant_shell inline Help handler
 -> validate declared ActionRows
 -> create a2 session
 -> compile callback tokens
 -> messages.setInlineBotResults
 -> messages.sendInlineBotResult
~~~

That is why the first Help menu renders.

### 4.2 Where concrete handlers come from

Concrete shell action handlers are installed in:

internal/assistant/client/shell_interaction.go

via:

~~~text
AssistantClient.ensureShellActions(engine, catalog)
~~~

This registers handlers in the shared root interaction Dispatcher for the current assistant_shell feature scope.

### 4.3 The lifecycle bug

At current HEAD, production calls ensureShellActions from the normal Assistant /start shell entry path.

Conceptually:

~~~text
/start
 -> dispatchStart
 -> ensureShellActions
 -> handlers registered
 -> begin shell session
~~~

But userbot .help self-inline does not pass through /start.

Therefore a fresh process can reach:

~~~text
Assistant starts
 -> a2 Runtime exists
 -> FeatureCatalog contains assistant_shell action declarations
 -> no shell action handlers registered yet

user runs .help
 -> InlineEngine accepts declared actions
 -> session/token compilation succeeds
 -> buttons appear

user clicks a button
 -> Runtime resolves token/session
 -> Dispatcher.Prepare looks for handler(feature=assistant_shell, action=X)
 -> no handler entry
 -> interaction: action handler unavailable
~~~

This matches the live log exactly.

### 4.4 Why tests missed it

Existing shell tests explicitly call ensureShellActions before dispatch.

The newest test at:

internal/assistant/client/selfinline_p1_e2e_test.go

extracts real callback data but closes with InteractionRuntime.ResolveCallback.

That proves:

- token is syntactically and cryptographically valid;
- session exists;
- actor/session binding is valid;
- generation token can resolve.

It does not prove Dispatcher has a current-generation action handler.

The missing acceptance step is real Dispatcher Prepare/Dispatch through the Assistant interaction ingress.

---

## 5. P0-next: Help action binding lifecycle

This is the first task for the next session because it is a live production breakage.

### P0-A — remove hidden dependency on /start

Shell action registration must not depend on the user first opening /start.

Required invariant:

~~~text
If InlineEngine can emit a callback token for
feature = assistant_shell
action = X
scope = N

then the root Dispatcher must already contain
handler(feature=assistant_shell, action=X, scope=N).
~~~

Likely implementation point: after Assistant Start creates the production orchestration Engine, before accepting callback updates, install current shell bindings when assistant_shell is active.

Do not make Assistant unusable merely because assistant_shell was intentionally disabled. Re-audit behavior for absent/disabled shell before choosing exact startup handling.

### P0-B — shell generation refresh

Current ensureShellActions already detects a changed FeatureScope and can replace old registrations, but it is not invoked automatically on every relevant lifecycle transition.

Acceptance must cover:

1. fresh Assistant start with no prior /start;
2. .help click works;
3. disable assistant_shell;
4. old token fails;
5. enable assistant_shell generation N+1;
6. fresh .help token uses N+1;
7. fresh button works;
8. no hidden /start initialization is required.

Do not weaken ErrScopeStale or ErrHandlerUnavailable.

---

## 6. Downloader source audit: high-confidence generation-binding defect

Downloader has a feature-owned Assistant interaction driver in:

plugins/downloader/interactive.go

It implements the FeatureDriver boundary and binds typed actions such as:

- select_audio;
- select_video;
- format_m4a / format_mp3 / format_opus;
- format_mp4 / format_best;
- video quality actions;
- download_file;
- back;
- retry;
- cancel.

The action declarations and BindAssistant registrations exist.

### 6.1 Current Assistant driver lifecycle

At app composition, internal/app/app.go snapshots plugins that implement Assistant FeatureDriver and calls SetInteractionDrivers.

At Assistant Start, internal/assistant/client/interaction_drivers.go calls bindFeatureDrivers.

Downloader BindAssistant registers action handlers into the shared interaction Dispatcher using the downloader FeatureScope current at bind time.

Those registrations are owned by AssistantClient.featureDriverCleanups and are torn down when the Assistant transport generation exits.

### 6.2 Plugin reload lifecycle gap

Plugin disable/enable is owned by plugin.Manager and creates a new feature Scope generation.

The Assistant feature-driver registrations are not visibly rebound on every plugin generation change.

Dangerous sequence:

~~~text
Assistant starts
 downloader feature scope = N
 BindAssistant registers downloader action handlers at N

plugin downloader disabled
 feature registration/session cleanup runs

plugin downloader enabled
 feature scope = N+1
 inline registry and FeatureSpec now use N+1
 Assistant action handler may still be bound at N

fresh .download URL
 InlineEngine creates session/token at N+1

click button
 Dispatcher entry is still N
 resolved session is N+1
 -> handler unavailable / scope mismatch
~~~

This is a high-confidence explanation for downloader buttons failing after plugin reload or disable/enable.

It is not yet proven to be the exact live trigger because the downloader callback log was not supplied.

### P0-C — reproduce before changing downloader

Perform both cases:

~~~text
A. clean process
   .download URL
   click Audio/Video

B. disable/enable or reload downloader while Assistant stays running
   .download URL
   click Audio/Video
~~~

Capture exact FeatureID, ActionID, session scope, handler scope, current feature scope and returned error.

### P0-D — feature-driver generation ownership

If confirmed, make Assistant feature-driver bindings track plugin feature generation, not only Assistant transport generation.

Requirements:

- one shared root Dispatcher;
- no second orchestration engine;
- no polling;
- no duplicate registration;
- disable detaches/invalidates old generation;
- enable/reload binds new generation;
- Assistant restart binds current generations;
- plugin reload must not require Assistant restart.

Inspect existing plugin lifecycle hooks before adding a new callback mechanism.

---

## 7. Help menu completeness

The deeper Help UI is already implemented in source:

- HelpView root/module list;
- HelpModuleView command page;
- HelpCommandView command detail;
- module pagination;
- command pagination;
- module slot actions;
- command slot actions;
- Back action and corresponding state transitions.

Therefore the symptom “only Help button list is visible” is probably not missing view code. It is primarily the action dispatch failure preventing navigation.

After P0-A/B, verify:

~~~text
.help
 -> root modules
 -> click module
 -> module command page
 -> next/prev
 -> click command
 -> command detail
 -> back
~~~

Also verify direct selectors:

~~~text
.help ping
.help <alias>
.help <exact module>
~~~

Do not build another Help menu implementation.

---

## 8. P1 acceptance gap: ResolveCallback is not enough

The previous P1 “true E2E” seam is still useful because it uses:

- production selfinline.RenderBridge;
- production telegram.Service;
- generated gotd client;
- production Inline Engine;
- production Assistant AnswerInline;
- only tgmock.Invoker at the raw MTProto boundary.

Keep that structure.

But extend it from:

~~~text
render -> compile token -> Runtime.ResolveCallback
~~~

to:

~~~text
render
 -> compile token
 -> resolve
 -> Dispatcher.Prepare
 -> TaskEngine admission when applicable
 -> handler dispatch
 -> transition/mutation
 -> EditInlineBotMessage or expected side effect
~~~

### Required Help E2E

~~~text
QueryInlineBot
 -> AnswerInline
 -> SendInlineBotResult
 -> extract actual callback_data
 -> Assistant inline callback ingress
 -> Dispatcher Prepare
 -> Help handler
 -> Transition
 -> EditInlineBotMessage
~~~

### Required Downloader E2E

~~~text
.download URL
 -> QueryInlineBot
 -> AnswerInline
 -> SendInlineBotResult
 -> click Audio/Video
 -> Dispatcher handler
 -> format/quality transition or probe
 -> final download action
 -> TaskEngine/resource admission
~~~

Every interactive acceptance gate must prove handler availability, not just token validity.

---

## 9. P2 from the previous audit remains open

After live Help/Downloader P0 defects are fixed, continue P2.

### P2-A — generic callback state generation fence

Legacy callback Router can reject a callback that was already Prepared before handler registration changes. But opaque StateStore state itself is not plugin-generation-bound.

Current StateScope covers user/chat/message/namespace/single-use/expiry, not plugin Scope owner/generation.

Dangerous case:

~~~text
generation N creates opaque state/button
 -> plugin disabled
 -> plugin enabled as N+1
 -> old button is clicked for the first time
 -> Router Prepare sees current N+1 handler
 -> old N state can still resolve
 -> N+1 handler can consume N state
~~~

Existing tests cover a different case: Prepare at N, then re-register, then Dispatch the already-prepared lease.

#### Preferred design direction

Do not make every plugin stamp generation manually.

module.Runtime currently injects a raw global CallbackStore StateWriter into Help, Settings, MyXL and possibly other generic-callback consumers.

Prefer a plugin-scoped writer conceptually equivalent to:

~~~text
CallbackWriterFor(pluginID)
~~~

that lazily resolves plugin.Manager.Scope(pluginID) at Store time and stamps current owner/generation.

Generic callback Prepare should validate:

~~~text
state owner/generation
==
prepared handler owner/generation
==
current plugin scope
~~~

before consuming state.

Do not add another StateStore.

Required tests:

- state N first clicked after enable N+1 -> rejected;
- state N+1 -> accepted;
- stale single-use state is not incorrectly consumed;
- wrong user/chat still rejected;
- expiry unchanged;
- MyXL purchase single-use/idempotency unchanged.

### P2-B — direct Assistant /help and /settings split-brain

Assistant command Router checks core.Router commands before presentation-only handlers.

Only /start is presentation-only today.

Because Help and Settings expose SurfaceAssistant:

~~~text
/help
/settings
~~~

still enter the plugin command path rather than canonical shell a2.

#### Direct /help

Desired:

~~~text
/start -> Help -> a2
userbot .help -> self-inline a2
Assistant /help -> a2
~~~

One Help state/view/interaction implementation.

#### Direct /settings

This is correctness-relevant, not only UI.

Legacy Settings callback mutations use Service.Set/Reset through plugins/settings/usecase.

a2 Settings uses the stronger registered/revision-fenced mutation contract with SetRegisteredResult/ResetRegisteredResult.

Desired:

~~~text
Assistant /settings
 -> canonical a2 Settings session
~~~

Do not remove settings/help from the core command catalog just to route around this. Telegram native command menu is generated from core.Router.CommandsForSurface(SourceAssistant).

Implement a narrow presentation cutover while keeping core.Router canonical.

### P2-C — legacy Help reclamation

Only after all Help entry points use a2:

- userbot .help;
- /start -> Help;
- direct Assistant /help;

perform reference-driven deletion of legacy generic Help callback/state.

Do not delete by assumption.

### P2-D — P2 acceptance

Required matrix:

| Scenario | Expected |
| --- | --- |
| fresh process, no /start, then .help | buttons dispatch |
| .help root -> module -> command -> back | all transitions work |
| .help exact command/alias/module | correct canonical view |
| Assistant restart | .help works without /start |
| assistant_shell disable/enable | old token rejected, new token works |
| downloader clean startup | selection buttons work |
| downloader disable/enable | old token rejected, new token works |
| downloader reload while Assistant remains up | new generation handlers rebound |
| generic callback state N first clicked after N+1 | rejected |
| generic callback state N+1 | accepted |
| Assistant /help | a2 Help |
| Assistant /settings | a2 Settings with revision-fenced mutation |
| MyXL generic callback | generation-safe; existing single-use/idempotency preserved |
| shutdown | no retained old driver/shell handlers or sessions |

---

## 10. Files to read first next session

### Help/shell

- internal/assistant/client/shell_interaction.go
- internal/assistant/client/interaction_help.go
- internal/assistant/client/shell_continuation.go
- internal/assistant/client/help_commands.go
- internal/assistant/client/updates.go
- internal/assistant/shell/shell.go
- internal/assistant/shell/help.go
- internal/assistant/shell/inline.go
- internal/assistant/shell/inline_help.go
- plugins/help/help.go

### Root a2 dispatch

- internal/interaction/dispatcher.go
- internal/interaction/runtime.go
- internal/interaction/orchestration/*
- internal/services/inline/engine.go

Key invariant from Dispatcher.Prepare: a callback can resolve successfully but still fail with ErrHandlerUnavailable when the handler key is absent or its registered scope does not equal the resolved session scope.

### Assistant feature drivers

- internal/assistant/client/interaction_drivers.go
- internal/assistant/client/client.go
- internal/app/app.go
- internal/plugin/manager.go
- internal/plugin/features.go

### Downloader

- plugins/downloader/interactive.go
- plugins/downloader/downloader.go
- plugins/downloader/delivery.go
- plugins/downloader/interactive_test.go
- plugins/downloader/p5_acceptance_test.go

### Existing self-inline acceptance

- internal/assistant/client/selfinline_p1_e2e_test.go
- internal/app/selfinline_p1_lifecycle_test.go
- internal/app/selfinline_features_lifecycle_test.go
- internal/presentation/selfinline/*

### Generic callback P2

- internal/services/callback/router.go
- internal/services/callback/store.go
- internal/services/callback/router_test.go
- internal/services/callback/contract_test.go
- internal/module/module.go
- plugins/settings/settings.go
- plugins/settings/usecase/set.go
- plugins/settings/usecase/reset.go
- plugins/myxl/myxl.go

---

## 11. Recommended execution order

### Step 0 — refresh/reproduce

1. Fetch test-next.
2. Compare against 6c0cb3bcb174a332284d874d7a77c8adef50d39e or the handoff-doc commit if no later drift.
3. Read all drift.
4. Reproduce .help button failure.
5. Reproduce downloader before and after reload.
6. Capture action/scope/handler diagnostics.

### Step 1 — P0 Help handler lifecycle

Fix shell action registration so self-inline Help never depends on prior /start.

Add a regression that does not call /start and dispatches a real compiled Help callback.

gofmt, targeted tests, then commit.

### Step 2 — P0 feature-driver/plugin-generation lifecycle

Fix downloader current-generation action binding across disable/enable/reload without Assistant restart.

gofmt, targeted tests, then commit.

### Step 3 — strengthen true E2E callback acceptance

Extend P1 E2E from ResolveCallback to real action dispatch and transition/edit.

Cover Help and Downloader, not only calculator/self-inline transport.

### Step 4 — P2 generic callback generation fence

Implement current-generation ownership for callback state and validate it before claim/dispatch.

Cover Settings and MyXL.

### Step 5 — P2 direct Assistant entry cutover

Make direct /help and /settings enter canonical a2 while preserving core.Router as catalog.

### Step 6 — reference-driven legacy cleanup

Remove only proven-dead Help legacy callback/state.

### Step 7 — runtime closure

On real checkout run at minimum:

~~~bash
gofmt -w <changed-go-files>
go test ./internal/interaction/... ./internal/services/inline/... ./internal/assistant/client/... ./internal/app/... ./plugins/help/... ./plugins/downloader/... ./internal/services/callback/... ./plugins/settings/... ./plugins/myxl/...
go build -o bin/goultroid ./cmd/goultroid
~~~

Then live Telegram smoke:

- .help;
- Help module/detail/back;
- /help;
- /settings;
- .calc;
- .download <youtube-url>;
- Audio/Video selection;
- downloader cancel/retry;
- plugin disable/enable;
- Assistant restart;
- Telegram reconnect.

Do not check GitHub CI unless requested.

---

## 12. Key acceptance lesson

Do not treat this:

~~~text
token compiled
+ Runtime.ResolveCallback succeeds
~~~

as proof that a button works.

There are separate layers:

~~~text
1. Feature declaration and callback compilation
2. Session/token resolution
3. Current-generation Dispatcher handler availability
4. Preparation/admission
5. Handler dispatch
6. Transition or mutation
~~~

The live Help defect sits at layer 3 while layers 1 and 2 are healthy.

Final interactive gates must prove:

~~~text
render -> compile -> resolve -> prepare -> dispatch -> transition/mutation
~~~

---

## 13. Historical YouTube note

Older downloader handoff material that describes .download <youtube-url> as immediately starting a default download is stale for current HEAD.

Current source uses interactive/self-inline selection-first behavior. Do not recreate old YT-Z architecture and do not revert to an immediate default download to bypass callback bugs.

Fix the typed interactive lifecycle instead.

---

## 14. Status at handoff

~~~text
P0 self-inline transport lifecycle                 CLOSED source-level
P0 Assistant identity/readiness                    CLOSED source-level
P0 inline capability preflight                     CLOSED source-level
P0 Telegram self-inline diagnostics                CLOSED source-level

P1 .help userbot -> Assistant inline bridge        IMPLEMENTED
P1 Help inline action execution                    BROKEN LIVE
P1 downloader selection UI                         RENDERS, BUTTONS REPORTED BROKEN
P1 lifecycle/E2E acceptance                        INCOMPLETE: handler-dispatch gap

P0-next shell action lifecycle                     OPEN / confirmed
P0-next feature-driver reload binding              OPEN / high-confidence
P1 true callback dispatch acceptance               OPEN / confirmed test gap

P2 generic callback generation ownership           OPEN / confirmed design gap
P2 direct Assistant /help cutover                  OPEN
P2 direct Assistant /settings cutover              OPEN / correctness-relevant
P2 legacy Help reclamation                         OPEN

Full go test/build/live verification                OPEN
~~~

Do not declare the userbot UX redesign complete until the live callback matrix passes.

---

## 15. Definition of completion

The next phase is complete only when:

1. Fresh-process .help works without prior /start.
2. Help root/module/command/back/pagination all work.
3. .download <youtube-url> buttons work on fresh startup.
4. Downloader works after plugin disable/enable without Assistant restart.
5. Old callbacks die across generation changes.
6. New callbacks work after reload.
7. E2E tests exercise Dispatcher action dispatch, not only token resolution.
8. Generic callback state cannot cross plugin generations.
9. Assistant direct /help uses a2.
10. Assistant direct /settings uses revision-fenced a2 mutation.
11. No second runtime/registry/executor/protocol is introduced.
12. gofmt precedes Go commits.
13. Targeted tests and go build pass on a real checkout.
14. Live Telegram smoke passes.

---

## 16. Continuation update — 2026-09-25

This section supersedes the older open-status snapshot in section 14. The original diagnosis is kept for history, but current `test-next` has advanced substantially beyond handoff commit `8427b41ceca9e72514a082680ec036a7906b356b`.

### 16.1 Drift that was already present before this continuation

When work resumed, `test-next` was already at:

```text
723beb2369533deffe3ff1b965f5b7bb47ccb42f
test(callback): fence opaque state across reload generations
```

The intervening commits had already closed most of the original P0/P2-A lifecycle work:

- shell actions are synchronized before callback ingress and no longer depend on a prior `/start`;
- shell reload lifecycle regression coverage exists;
- Assistant feature-driver bindings are reconciled by plugin generation;
- Assistant binding refresh is wired to plugin-generation changes;
- real a2 callback dispatch through TaskEngine is covered by the calculator self-inline acceptance;
- generic callback state is fenced by plugin generation, including stale-generation rejection.

Do not recreate those fixes.

### 16.2 Commits added in this continuation

```text
2c2e6b6434b5a45abe0299255cb15549896d1cc4
feat(assistant): cut over direct help and settings to a2

3d12b2ca7838c8a99bc4463d227bc869d53ef53b
refactor(help): retire legacy callback state

8e7fa436e495adbd0cbdc6dbb686dc5e42b1e15f
fix(test): use canonical private chat type

ac124d26b59689995684b44953107558d7acd356
test(assistant): cover direct a2 help and settings mutation

7c69878978453a0d5341544f051e889c1261ac36
test(selfinline): dispatch help callbacks end to end

b3736726466a5be9aa9cc653a68147f0722ccb65
test(selfinline): cover downloader callback pipeline end to end
```

### 16.3 Direct Assistant cutover now implemented

Direct Assistant entry points now use explicit presentation overrides while keeping `core.Router` canonical for command discovery/menu metadata:

```text
/help
/h
/commands
/settings
```

The override is fail-closed: it runs only while the canonical Assistant command still exists and is allowed on the Assistant surface.

`/help` begins the canonical a2 Help session directly and supports:

- root Help;
- exact command name or alias;
- exact Help module/category.

`/settings` begins the canonical a2 Settings session directly and can open a matching category. It therefore uses the existing revision-fenced mutation contract rather than legacy Settings presentation logic.

No second interaction runtime, dispatcher, registry, executor, or Help/Settings view implementation was introduced.

### 16.4 Legacy Help callback state reclaimed

After all Help interactive entry points were routed through a2, the old generic Help callback/state path was removed reference-first:

- removed Help generic callback handler implementation;
- removed Help callback state writer injection;
- removed `helpMenuState`;
- removed generic Help callback markup/state generation;
- removed callback-only Help tests.

The userbot `.help` self-inline path and canonical a2 Help views remain intact.

### 16.5 New direct-entry acceptance

`internal/assistant/client/shell_direct_acceptance_test.go` now covers:

```text
/help
  -> a2 root
  -> module callback
  -> command callback
  -> back callback

/settings general
  -> a2 setting detail
  -> reset confirmation
  -> schema revision changes
  -> stale confirm rejected with ErrShellSettingBindingStale
  -> persisted value remains unchanged
  -> reopen fresh revision
  -> reset succeeds
```

This proves that direct Settings entry is connected to the same revision-fenced mutation semantics as Settings reached from `/start`.

### 16.6 True self-inline Help E2E now reaches Dispatcher handler

`internal/assistant/client/selfinline_help_e2e_test.go` covers the full production-shaped Help path:

```text
SelfInline renderer
  -> messages.getInlineBotResults
  -> production inline Engine
  -> messages.setInlineBotResults
  -> messages.sendInlineBotResult
  -> compiled a2 callback_data
  -> Assistant interaction ingress
  -> Dispatcher Prepare
  -> TaskEngine admission
  -> canonical shell Help action handler
  -> Transition
  -> messages.editInlineBotMessage
```

The regression no longer treats `ResolveCallback` alone as proof of a functioning button.

### 16.7 True self-inline Downloader E2E now covers final resource boundary

`internal/assistant/client/selfinline_downloader_e2e_test.go` uses the production-shaped self-inline transport without any live network download:

```text
dl <youtube-url>
  -> getInlineBotResults
  -> setInlineBotResults
  -> sendInlineBotResult
  -> click Audio
  -> ingress / Dispatcher / TaskEngine
  -> edit to audio-format view
  -> click MP3
  -> prepared action
  -> ingress / Dispatcher / TaskEngine
  -> edit to running view
  -> submit physical continuation
```

The captured extractor continuation must use:

```text
Pool: download
Resources:
  - download
  - process
```

The continuation task handler is deliberately not executed by the test, so the gate validates production admission/resource planning without depending on YouTube, yt-dlp, or external network availability.

This matches the current architecture: the callback task performs interaction preparation/state transition, while physical `download` / `process` ownership belongs to the queued continuation.

### 16.8 Current acceptance status

Source-level implementation and regression coverage now indicate:

| Scenario | Current source/test status |
| --- | --- |
| fresh process, no `/start`, then `.help` | covered by shell-action lifecycle regression |
| Help root -> module -> command -> back | covered |
| Help exact command/alias | covered by canonical inline Help test |
| Assistant restart / shell generation refresh | covered by existing lifecycle tests |
| `assistant_shell` generation stale/new token behavior | covered by existing reload tests |
| downloader clean startup selection callback | covered by true self-inline E2E |
| downloader feature-driver generation rebinding | covered generically by feature-driver generation test |
| generic callback generation N -> N+1 stale-state rejection | covered by callback-router generation tests |
| Assistant `/help` | cut over to a2 and covered |
| Assistant `/settings` | cut over to a2; revision-fenced mutation covered |
| legacy Help generic callback state | reclaimed |
| Downloader final extractor resource admission | covered by captured continuation WorkSpec |
| shutdown cleanup | existing lifecycle coverage remains authoritative |

### 16.9 Validation caveat

This continuation did **not** run GitHub CI, by request.

The new Go files were formatted with `gofmt` before their commits. However, because this AI session does not have a runnable checkout of the repository, the newly added tests and production changes have not yet been executed with `go test` / `go build` in this continuation.

The next real-checkout validation should therefore start with:

```bash
gofmt -w internal/assistant/command/presentation_override.go \
  internal/assistant/client/shell_commands.go \
  internal/assistant/command/presentation_override_test.go \
  internal/assistant/client/shell_commands_test.go \
  internal/assistant/client/shell_direct_acceptance_test.go \
  internal/assistant/client/selfinline_help_e2e_test.go \
  internal/assistant/client/selfinline_downloader_e2e_test.go \
  plugins/help/help.go \
  plugins/help/module.go \
  plugins/help/help_test.go

go test ./internal/assistant/command/... ./internal/assistant/client/... ./plugins/help/... ./plugins/downloader/...
go build -o bin/goultroid ./cmd/goultroid
```

Then run the live Telegram matrix from section 11.

Do not mark the overall userbot UX redesign finished until those real-checkout tests/build and live Telegram smoke pass.

### 16.10 Direct Help permission compatibility correction

A final semantic audit found that the canonical Help command is `PermissionEveryone`, while the migrated a2 Help screen/actions still used `OwnerPolicy`. Direct `/help` cutover would therefore have silently changed existing Assistant behavior for non-owner visitors.

This was corrected in:

```text
01f9f670b9e21fe676936d5e2d11b943ac9dad0c
fix(assistant): preserve public help cutover policy
```

The canonical a2 Help screen/action policy is now public on Assistant/inline surfaces, while `InteractionInlineHelp` remains owner-only as the self-inline entry gate. Interaction sessions/tokens remain actor-bound.

Regression coverage:

```text
TestAssistantDirectHelpPreservesPublicCommandPermission
```

This verifies that a non-owner visitor can still invoke direct `/help`, receives canonical a2 Help, and does not fall back to the legacy Help handler.

### 16.11 Downloader generation-specific lifecycle acceptance

The generic feature-driver generation test is now supplemented by a Downloader-specific lifecycle gate:

```text
26c415672a95bc4cf1df1f835a6095451a827636
test(selfinline): cover downloader generation reload
```

New test:

```text
TestSelfInlineDownloaderDisableEnableRejectsOldGenerationAndRebindsNew
```

The test uses the production Plugin Manager, lifecycle-owned inline registry, feature catalog, interaction runtime/dispatcher, Downloader FeatureDriver, and `RefreshInteractionBindings`. It proves:

```text
generation N inline callback
  -> dispatch succeeds through TaskEngine

create untouched generation N callback
  -> disable downloader
  -> generation-N sessions are reclaimed
  -> callback N is rejected before TaskEngine

enable downloader without restarting Assistant
  -> feature scope generation changes
  -> driver bindings refresh against new scope
  -> old callback N remains rejected
  -> newly minted callback N+1 dispatches and edits successfully
```

This closes the previous source-level ambiguity around Downloader-specific disable/enable behavior instead of relying only on the synthetic generation-driver canary.

### 16.12 Help pagination acceptance

Help pagination now has a direct a2 transition regression:

```text
a2d58b91d08a2f75368a21bdc4de3d21b90d6163
test(assistant): cover help pagination transitions
```

New test:

```text
TestAssistantDirectHelpPaginationTransitionsAndStalesOldButtons
```

It constructs enough modules and commands to force both pagination levels and verifies:

```text
/help root page 1
  -> module next -> page 2
  -> reused old next token is stale
  -> module previous -> page 1
  -> open Module00
  -> command page 1
  -> command next -> page 2
  -> reused old command-next token is stale
  -> command previous -> page 1
```

This closes the explicit pagination portion of Definition of Completion item 2 at source/regression level.

### 16.13 Runtime validation attempt

A real checkout was attempted from the execution environment before these commits:

```text
git clone --branch test-next --single-branch https://github.com/inipew/goultroid.git
fatal: unable to access 'https://github.com/inipew/goultroid.git/':
Could not resolve host: github.com
```

Therefore `go test` and `go build` still cannot be truthfully marked as executed in this session.

What **was** executed locally for the two new Go tests:

```text
gofmt -w internal/assistant/client/selfinline_downloader_reload_e2e_test.go
gofmt -w internal/assistant/client/shell_help_pagination_acceptance_test.go

gofmt -d ...  # clean, no diff
```

Their API contracts were also cross-checked against current `test-next` source for:

- Plugin Manager `Disable` / `Enable` lifecycle;
- capability-gated filesystem/task PluginContext;
- generation-owned inline registrations;
- feature-scope cleanup and session cancellation;
- `RefreshInteractionBindings` generation reconciliation;
- interaction stale-token semantics;
- Help module/command pagination state transitions.

GitHub CI was **not** checked.

### 16.14 Updated remaining work

After the commits above, the handoff implementation/regression work is effectively closed at source level. Remaining hard gates are:

1. run the targeted/full `go test` set on a real checkout;
2. run `go build -o bin/goultroid ./cmd/goultroid`;
3. fix any compile/test failures found by those executions;
4. perform the live Telegram smoke matrix from section 11;
5. record final resource/session/handler settling evidence and close the handoff.

Do not report P2/live callback closure as final until these runtime/live gates pass.
