# Goultroid — Assistant-Optional Userbot AI Session Handoff

> **Purpose:** continue the audit/fix that makes Assistant and self-inline presentation an optional UX enhancement rather than a functional dependency of userbot commands.
>
> **Audited branch:** `test-next`
>
> **Audited HEAD:** `1b5cd807e9c5223d5854becb23edf37018711037` — `fix(help): disambiguate command detail navigation`
>
> **Date:** 2026-09-26, Asia/Jakarta
>
> **CI rule:** do not inspect or wait for CI unless the user explicitly asks.
>
> **Authority rule:** refresh current source first. If branch drift exists after the audited HEAD above, current source/tests outrank this handoff.

---

## 1. User intent and architecture decision

The triggering question was broader than `.help`:

> What happens if Goultroid is used without Assistant at all? Should userbot features become text-only, partially unavailable, or something else?

The architectural decision for the next session is:

> **Assistant is an optional presentation/control enhancement. It must not be a functional dependency of any command that declares `execution.SurfaceUserbot`.**

In other words:

```text
canonical userbot capability
        |
        +-- Assistant/self-inline healthy
        |      -> richer inline/a2 presentation
        |
        +-- Assistant absent/unconfigured/not ready
               -> native userbot presentation/workflow
```

The loss of Assistant may reduce UI richness — inline keyboard, a2 transitions, session-bound callbacks, interactive selection — but must not disable the underlying userbot capability.

If a feature truly requires Assistant and has no meaningful native userbot semantics, it must not declare `SurfaceUserbot`; it should be explicitly Assistant/Inline-only.

This is a **userbot-first invariant**, not a Help-specific workaround.

---

## 2. Frozen architecture constraints

Do not solve this by creating duplicate stacks.

Continue to reuse:

```text
core.Router
    canonical command execution

feature.Registry
    cross-surface metadata/admission

interaction.Runtime + Dispatcher + orchestration.Engine
    canonical a2 interaction authority

Inline vNext
    canonical inline result lifecycle

selfinline.Renderer
    optional userbot -> own Assistant presentation bridge

TaskEngine
    execution/backpressure/resource authority

telegram.RPCExecutor
    Telegram limiter/retry/FloodWait authority

download.Registry
    canonical download provider authority

shared retained media/storage ownership
    retained asset lifecycle

shared presentation/Telegram boundaries
    Telegram message/media delivery
```

Still forbidden:

- second command registry;
- second interaction runtime;
- second callback protocol;
- second downloader/provider registry;
- second TaskEngine;
- second RPC executor;
- plugin-local FloodWait/retry;
- unbounded fallback state;
- permanent per-feature workers;
- automatic duplicate Telegram output after an ambiguous self-inline send.

---

## 3. Why the current behavior is architecturally wrong

Application composition already treats Assistant as optional.

Relevant current source:

### `internal/app/selfinline.go`

`App.SelfInlineRenderer()` returns nil when either userbot client or Assistant is missing.

`newSelfInlineRenderer(...)` uses a lazy/current Telegram transport plus the current Assistant inline identity.

### `internal/app/selfinline_features.go`

`wireSelfInlineRenderers(...)` injects the shared renderer only into plugins implementing:

```go
SetSelfInlineRenderer(selfinline.Renderer)
```

and returns without wiring when Assistant is unavailable.

That composition model is correct.

The bug is at feature level: some `SurfaceUserbot` commands treat a nil/unavailable renderer as a fatal capability loss instead of a presentation downgrade.

---

## 4. Audit matrix

### 4.1 HARD DEPENDENCY — Help

Files:

- `plugins/help/help.go`
- `plugins/help/module.go`
- `internal/architecture/assistant_help_selfinline_p1_test.go`

Current userbot routing:

```text
.help
  -> handleHelp()
  -> SourceUserbot
  -> openUserbotHelp()
  -> renderer required
```

Current hard failure when renderer is nil:

```text
Interactive help is unavailable because the Assistant inline renderer is not running.
```

Current hard failure when render fails:

```text
Unable to open help through the Assistant: ...
```

This is particularly unnecessary because `plugins/help/help.go` already contains native formatted renderers for:

- root overview;
- module/category list;
- command lookup;
- alias lookup;
- detailed command information;
- category/module;
- permission;
- invocation;
- usage;
- aliases;
- group authorization;
- cooldown;
- timeout;
- group/private/reply constraints.

Those native renderers are currently bypassed for `SourceUserbot` by:

```go
if source == execution.SourceUserbot {
    return p.openUserbotHelp(ctx, prefix)
}
```

### Required target

```text
.help / .help <module> / .help <command|alias>
  |
  +-- self-inline safe + healthy
  |      -> existing interactive a2 Help
  |
  +-- Assistant unavailable before send
         -> existing formatted native Help
         -> edit/reply from userbot
         -> no "Assistant unavailable" UX
```

Do not remove interactive Help. Make it progressive enhancement.

---

### 4.2 HARD DEPENDENCY — Calculator

Files:

- `plugins/calculator/calculator.go`
- `plugins/calculator/evaluator.go`
- `internal/app/selfinline_features_lifecycle_test.go`
- `internal/assistant/client/selfinline_p1_e2e_test.go`

Current command:

```text
.calc [expression]
Surfaces: SurfaceUserbot
```

Current `handleCommand` requires `p.renderer`.

Nil renderer returns:

```text
Interactive calculator is unavailable because the Assistant inline renderer is not running.
```

Render failure similarly becomes a visible Assistant error.

But the calculator already owns a bounded evaluator and formatting functions. The arithmetic capability itself does not require Assistant.

### Required target

When Assistant/self-inline is healthy:

```text
.calc [expression]
  -> keep current interactive keypad/self-inline behavior
```

When Assistant is unavailable before send:

```text
.calc 1+2*3
  -> native evaluation
  -> formatted result

.calc
  -> native compact usage/help
  -> e.g. explain ".calc <expression>"
  -> no Assistant-unavailable failure
```

Do not create a second evaluator.

Reuse:

- `compactExpression`;
- `evaluateExpression`;
- `formatResult`;
- existing max expression/depth validation.

The interactive calculator remains an enhancement; direct arithmetic remains native.

---

### 4.3 HARD DEPENDENCY — URL Downloader

Files:

- `plugins/downloader/downloader.go`
- `plugins/downloader/interactive.go`
- `plugins/downloader/delivery.go`
- `internal/services/download/registry.go`
- `internal/services/download/types.go`
- `internal/services/download/provider_extractor.go`

Important distinction:

```text
.download reply-to-Telegram-media
    -> already native userbot flow

.download <URL>
    -> handleURLDownload()
    -> always openInteractiveURLDownload()
    -> Assistant/self-inline required
```

Current nil-renderer failure:

```text
Interactive downloader is unavailable. Start the Assistant inline renderer and try again.
```

Therefore only the URL branch is functionally coupled to Assistant.

#### Existing canonical downloader capabilities

`download.Registry.Download(...)` already supports a UI-neutral `DownloadOptions`:

```text
Mode:
  default
  audio
  video

Format:
  default
  best
  m4a
  mp3
  mp4
  opus

MaxHeight:
  0 / 360 / 480 / 720 / 1080 / 1440 / 2160
```

The extractor provider explicitly accepts:

```text
ModeDefault + FormatDefault + MaxHeight=0
```

and passes no explicit `-f` selector to yt-dlp, allowing its canonical default selection.

Direct HTTP intentionally ignores media mode selection.

`plugins/downloader/delivery.go` already owns the bounded TaskEngine download -> retained asset -> media delivery pipeline. Do not add a second noninteractive downloader.

### Required baseline fallback

If Assistant is unavailable **before a self-inline result may have been sent**:

```text
direct HTTP URL
  -> native default download
  -> resource: download
  -> retained ownership
  -> media delivery through shared boundary

extractor/YouTube URL
  -> native default selection
       ModeDefault
       FormatDefault
       MaxHeight=0
  -> resources: download + process
  -> retained ownership
  -> release download/process before media delivery
  -> media=1 delivery continuation
```

This baseline does not require inventing a second selection UI.

Optional CLI selectors such as:

```text
.download <url> audio mp3
.download <url> video mp4 720
```

may be added later, but **must not be required to close Assistant optionality**. First close the default functional path.

### Refactor preference

Do not copy `submitInteractivePipeline`.

Prefer extracting a UI-neutral canonical URL pipeline from the existing implementation, then provide two presenters:

```text
interactive a2 presenter
native userbot presenter
```

Both must converge on the same:

- provider resolution;
- TaskEngine resources;
- download options;
- retained ownership;
- delivery continuation;
- cancellation/lifecycle semantics.

---

### 4.4 HYBRID / HIDDEN DEPENDENCY — Settings

Files:

- `plugins/settings/settings.go`
- `plugins/settings/module.go`

Settings is **not** hard-dependent on self-inline.

Current userbot `.settings` already:

1. renders a plugin-owned screen;
2. optionally sends callback markup if `ui:inline_buttons` is enabled;
3. falls back to text reply if markup sending fails.

`.config` is native CLI.

However, the userbot Settings home still contains a cross-namespace callback:

```go
callback.EncodeCallbackData("assistant", "start", ...)
```

for the button:

```text
« Back to Menu
```

This means a userbot-owned Settings screen has a navigation edge whose owner is Assistant.

That violates the new optionality invariant even though the main Settings feature still works.

### Required target

Userbot Settings must not require an Assistant callback handler.

For userbot presentation:

- root Settings should be self-contained;
- Back/Home must use Settings-owned navigation or be omitted when already at root;
- Close remains Settings-owned;
- no raw `assistant:start` dependency.

Assistant direct `/settings` already has the newer a2 shell cutover; do not reintroduce legacy Assistant callback coupling to solve this.

### Resource follow-up

Current `renderScreen` builds callback state before deciding whether markup will be used. If it ultimately returns text-only, callback state may have been allocated unnecessarily.

Audit and, if useful, make text-only rendering avoid creating callback OIDs. Keep the state store bounded either way.

---

### 4.5 GOOD REFERENCE — Wikipedia

File:

- `plugins/wikipedia/wikipedia.go`

Wikipedia already demonstrates the desired separation:

```text
.wiki <query>
  -> native userbot/Assistant command
  -> direct lookup + formatted result

Inline interaction
  -> separate rich Inline vNext surface
```

The native command does not require inline/Assistant availability.

Use this as a design reference.

---

### 4.6 GOOD REFERENCE — MyXL

Files:

- `plugins/myxl/myxl.go`
- `plugins/myxl/assistant_interaction.go`

MyXL also demonstrates the desired high-level behavior:

```text
Assistant context
  -> a2 interactive dashboard / input / confirmation

Userbot context
  -> native text menu
  -> explicit CLI subcommands
  -> login / otp / refresh / accounts / use / alias /
     status / delete / quota / family / package / saved /
     buy / qris
```

The userbot feature remains usable without Assistant.

Do not regress MyXL back into Assistant dependency.

---

## 5. Critical prerequisite: stage-aware self-inline failure semantics

Do **not** implement “fallback on any `renderer.Render()` error”.

Current `selfinline.RenderBridge.Render` performs:

```text
validate/preflight
  -> resolve Assistant identity
  -> QueryInlineBot
  -> select result
  -> generate random_id
  -> SendInlineBotResult
```

On all errors it returns an empty `Result`.

Current diagnostics include:

- `ErrUnavailable`;
- `ErrInlineDisabled`;
- `ErrNoResults`;
- `ErrAssistantResponseTimeout`;
- `ErrAssistantInvalid`;
- `ErrPeerInlineRestricted`;
- `ErrInlineResultExpired`;
- `ErrQueryFailed`;
- `ErrSendFailed`.

The problem: error kind alone does not always tell whether a Telegram send was attempted.

Examples:

1. `ErrUnavailable` can happen during initial identity/transport resolution **or** if `CurrentTransport` disappears between query and send.
2. `ErrPeerInlineRestricted` is produced by both query and send normalization.
3. A generic send transport error can be ambiguous: the server may have processed the send even if the caller did not receive the final response.

Blindly sending native fallback text after an ambiguous send error can produce duplicate output.

### P0-A — add stage-aware render failure contract

Recommended shape, while preserving `errors.Is` compatibility:

```go
type RenderStage uint8

const (
    RenderStagePreflight RenderStage = ...
    RenderStageQuery
    RenderStageSelect
    RenderStageSend
)

type RenderFailure struct {
    Stage            RenderStage
    MayHaveCommitted bool
    Err              error
}
```

Provide helpers such as:

```go
func FailureStage(error) RenderStage
func FallbackSafe(error) bool
```

Rules:

```text
preflight/query/select failure
    -> safe to use native fallback

send attempted + definitive/ambiguous failure
    -> DO NOT automatically emit a second result
    -> report a concise delivery diagnostic
    -> log original cause
```

The interface may remain:

```go
Render(context.Context, Request) (Result, error)
```

Only error wrapping needs richer metadata.

Do not create a second renderer interface unless absolutely required.

---

## 6. Global invariant to encode in tests

After this work, the repository should enforce:

> **Any command exposed on `SurfaceUserbot` must retain its core function when Assistant is nil, not configured, stopped, not ready, or inline mode is disabled.**

Assistant-specific presentation can disappear.

Suggested acceptance matrix:

| Condition | Help | Calc | Download URL | Settings | MyXL | Wiki |
|---|---|---|---|---|---|---|
| Assistant healthy | interactive a2 | interactive keypad | interactive selector | native/userbot UI | native userbot CLI | native result |
| Assistant nil | native text | native evaluate/usage | native default pipeline | native | native | native |
| Assistant not ready | native fallback if pre-send | native fallback if pre-send | native fallback if pre-send | native | native | native |
| inline disabled | native fallback | native fallback | native fallback | native | native | native |
| query timeout/failure | native fallback | native fallback | native fallback | native | native | native |
| send-stage ambiguous failure | no duplicate fallback | no duplicate fallback | no duplicate fallback | N/A | N/A | N/A |
| plugin disable/reload | existing generation fencing | existing fencing | existing fencing | scoped callback state | existing | inline lifecycle |

---

## 7. Proposed implementation phases

### P0 — establish optional-presentation contract

#### P0-A — stage-aware self-inline errors

Files:

- `internal/presentation/selfinline/render.go`
- `internal/presentation/selfinline/diagnostics.go`
- corresponding tests

Add stage/fallback-safety semantics.

Tests must prove:

- nil renderer/identity unavailable -> preflight, safe fallback;
- inline disabled during query -> query, safe fallback;
- no result/select failure -> select, safe fallback;
- transport disappears only before SendInlineBotResult -> send-stage, not safe fallback;
- send failure -> send-stage, not safe fallback;
- existing `errors.Is` diagnostics remain valid.

#### P0-B — architecture acceptance

Add a cross-feature acceptance test documenting that Assistant absence is a supported mode.

Avoid brittle source-string tests where behavior tests are practical.

---

### P1 — Help progressive enhancement

Refactor `plugins/help/help.go` so userbot Help is:

```text
try interactive
  -> success: delete original command, done
  -> safe pre-send failure: native Help
  -> send-stage ambiguous failure: concise diagnostic, no duplicate
```

Important:

- reuse existing native renderers;
- root/module/command/alias must all work;
- preserve source filtering so userbot Help only lists commands available to userbot;
- only delete the original `.help` message after confirmed interactive success;
- do not expose raw Assistant error to normal users for safe fallback cases.

Update `internal/architecture/assistant_help_selfinline_p1_test.go`:

Old invariant:

```text
userbot Help uses self-inline Assistant presentation
```

New invariant:

```text
userbot Help prefers self-inline presentation when available
AND native fallback remains complete without Assistant
```

Keep the “no second runtime/TaskEngine/RPC executor” checks.

---

### P2 — Calculator progressive enhancement

Files:

- `plugins/calculator/calculator.go`
- calculator tests
- self-inline lifecycle tests

Target:

```text
.calc 1+2
  Assistant healthy -> interactive result initialized with expression
  no Assistant      -> native evaluated result

.calc
  Assistant healthy -> keypad
  no Assistant      -> native usage/help
```

Do not alter evaluator semantics.

Add tests for:

- renderer nil;
- pre-send self-inline failure;
- send-stage failure does not duplicate;
- invalid/oversized expression remains bounded;
- healthy self-inline path remains unchanged.

Existing P1 true E2E self-inline calculator test remains valuable and must stay.

---

### P3 — Downloader URL progressive enhancement

This is the highest-risk implementation phase.

First re-read current downloader code at the latest HEAD.

Target:

```text
.download <url>
  |
  +-- self-inline healthy
  |      -> existing Audio/Video/format UI
  |
  +-- safe pre-send Assistant failure
         -> native default URL pipeline
         -> same download.Registry
         -> same TaskEngine
         -> same retained ownership
         -> same Telegram delivery boundary
```

Baseline native selection:

```text
ModeDefault
FormatDefault
MaxHeight=0
```

Resource rules remain:

```text
direct HTTP:
    download=1

extractor:
    download=1 + process=1

Telegram delivery:
    release download/process
    then media=1
```

Preserve:

- reply/topic target;
- retained asset safety;
- cancellation semantics;
- terminal edits through TaskEngine;
- no Telegram RPC from completion callback;
- no extra provider registry;
- no plugin-local uploader.

Prefer one shared URL pipeline used by interactive and native presentation.

Add no-network/fake-provider tests for fallback.

---

### P4 — Settings hidden dependency cleanup

Files:

- `plugins/settings/settings.go`
- settings tests

Remove userbot dependence on:

```text
assistant:start
```

from Settings-owned screens.

Use Settings-owned Home/Close navigation.

Then audit text-only mode so it does not retain unnecessary callback state.

Do not reopen the newer direct Assistant `/settings` a2 shell migration.

---

### P5 — repository-wide optionality audit

After P1-P4, re-scan every `SurfaceUserbot` command.

Questions for every command:

1. Does execution require `App.assistant` or self-inline identity?
2. Does a userbot callback point to an Assistant-owned namespace?
3. Does an Assistant error become a user-visible functional failure where native behavior exists?
4. Does presentation fallback accidentally duplicate after an ambiguous Telegram send?
5. Does fallback bypass TaskEngine/resources/RPC executor?
6. Does fallback allocate extra unbounded state?
7. Does disabling Assistant alter only presentation, not userbot capability?

Record the final matrix in this document or a closure doc.

---

## 8. Tests that should be added or changed

### Self-inline core

- `TestRenderFailureStagePreflightIsFallbackSafe`
- `TestRenderFailureStageQueryIsFallbackSafe`
- `TestRenderFailureStageSendIsNotFallbackSafe`
- `TestCurrentTransportLossBeforeSendIsMarkedSendStage`
- preserve diagnostic `errors.Is` compatibility

### Help

- renderer nil -> native overview;
- renderer nil -> native module;
- renderer nil -> native command;
- alias lookup fallback;
- inline disabled/query failure -> native;
- interactive success -> no native duplicate;
- send-stage failure -> no native duplicate.

### Calculator

- nil renderer + expression -> correct native result;
- nil renderer + empty expression -> usage;
- safe render failure -> native;
- send-stage failure -> no duplicate;
- healthy renderer -> existing self-inline path.

### Downloader

- direct HTTP safe fallback uses only `download`;
- extractor safe fallback uses `download + process`;
- default extractor selection is `ModeDefault/FormatDefault/0`;
- retained asset registered before media delivery;
- download/process released before `media=1`;
- reply/topic preserved;
- interactive healthy path remains unchanged;
- ambiguous send-stage self-inline failure does not start native download automatically.

### Settings

- userbot Settings markup contains no `assistant:start`;
- text-only mode works with Assistant absent;
- callback state remains bounded / preferably not allocated for pure text fallback.

### App-level

Construct or simulate application composition with Assistant absent and confirm:

```text
.help
.calc <expr>
.download <fake-url>
.settings
.myxl
.wiki
```

do not fail solely because Assistant is nil.

---

## 9. User-facing behavior after closure

### With Assistant

No regression in rich UX:

```text
.help
  -> interactive Help grid

.calc
  -> inline keypad

.download <url>
  -> Audio / Video selection
```

### Without Assistant

Userbot remains fully usable:

```text
.help
  -> formatted native Help

.help download
  -> formatted detailed command card

.calc 10/4
  -> formatted result

.calc
  -> usage/help

.download <url>
  -> canonical default download + Telegram delivery

.settings
  -> native settings UI/text fallback

.myxl
  -> existing native CLI/menu

.wiki query
  -> existing native result
```

Normal users should not see messages instructing them to “start Assistant” for features that have native userbot semantics.

---

## 10. Logging and observability

Safe fallback should be quiet to the user but observable.

Recommended distinction:

```text
presentation.selfinline_unavailable
presentation.selfinline_fallback
presentation.selfinline_send_ambiguous
```

Do not put raw query/URL/user identifiers into unbounded metric labels.

For safe fallback:

- debug/info log is enough;
- user sees native result, not Assistant failure.

For send-stage ambiguous failure:

- warn with stage/method/error;
- user sees concise “interactive presentation could not be confirmed; retry” style diagnostic;
- do not automatically create native duplicate.

---

## 11. Important current-source contradictions with older handoff text

Older handoff text may say “Userbot URL automatic delivery IMPLEMENTED”.

At audited HEAD `1b5cd807...`, current source in `plugins/downloader/downloader.go` still routes every URL through:

```text
handleURLDownload
  -> openInteractiveURLDownload
```

and hard-fails if `p.renderer == nil`.

Therefore, for this task:

> **Current source wins. Treat URL-without-Assistant as OPEN regardless of older closure prose.**

Likewise, older architecture tests that require Help to use self-inline should be interpreted as “self-inline enhancement remains supported”, not “Help may require Assistant”.

---

## 12. Suggested commit sequence

Keep changes bisectable.

Recommended sequence:

```text
1. fix(selfinline): expose fallback-safe render stages
2. test(architecture): require assistant-optional userbot surfaces
3. fix(help): fall back to native userbot presentation
4. fix(calculator): keep native evaluation without assistant
5. refactor(downloader): share url execution pipeline
6. fix(downloader): fall back to native url download
7. fix(settings): remove assistant-owned userbot navigation
8. test(userbot): close assistant-optional acceptance matrix
9. docs(handoff): close assistant optionality work
```

Run `gofmt` before every Go commit.

Do not inspect CI unless explicitly asked.

---

## 13. Definition of completion

Do not close this work until all of these are true:

1. Assistant can be completely unconfigured/absent and userbot still starts normally.
2. Every `SurfaceUserbot` command retains its core function without Assistant.
3. `.help` works root/module/command/alias without Assistant.
4. `.calc <expr>` computes without Assistant.
5. `.calc` without Assistant gives useful native guidance, not an unavailable error.
6. `.download <url>` works without Assistant using canonical default selection.
7. Downloader fallback uses the same TaskEngine/resource/registry/retained-delivery authorities.
8. `.settings` userbot navigation has no Assistant-owned callback dependency.
9. MyXL native path remains intact.
10. Wikipedia native path remains intact.
11. Healthy Assistant still provides richer interactive Help/Calculator/Downloader UX.
12. Fallback occurs only when self-inline failure is known to be pre-send/safe.
13. Send-stage ambiguous failures never generate automatic duplicate native output.
14. Plugin disable/reload generation fencing remains intact.
15. No second runtime/registry/executor/downloader/callback protocol is introduced.
16. State/cache/cardinality remains bounded.
17. Targeted tests pass.
18. `go build -o bin/goultroid ./cmd/goultroid` passes.
19. Live smoke is performed both with Assistant enabled and with Assistant disabled/unconfigured.
20. Final handoff/closure doc records evidence.

---

## 14. Local validation commands

When a real checkout is available:

```bash
gofmt -w <changed-go-files>

go test ./internal/presentation/selfinline/...
go test ./plugins/help/...
go test ./plugins/calculator/...
go test ./plugins/downloader/...
go test ./plugins/settings/...
go test ./internal/app/...
go test ./internal/architecture/...

go build -o bin/goultroid ./cmd/goultroid
```

Then live matrix:

```text
A. Assistant configured + running + inline enabled
B. Assistant token/config absent
C. Assistant configured but stopped/not ready
D. Assistant inline disabled
E. forced query-stage self-inline failure
F. forced send-stage/ambiguous self-inline failure
```

Validate Help, Calculator, Downloader, Settings for each relevant row.

---

## 15. Immediate next-session bootstrap

The next AI session should do exactly this:

1. Refresh `test-next`; record exact HEAD.
2. Read this handoff.
3. Diff from audited HEAD `1b5cd807e9c5223d5854becb23edf37018711037`.
4. Re-read:
   - `internal/presentation/selfinline/render.go`
   - `internal/presentation/selfinline/diagnostics.go`
   - `internal/app/selfinline.go`
   - `internal/app/selfinline_features.go`
   - `plugins/help/help.go`
   - `plugins/calculator/calculator.go`
   - `plugins/downloader/downloader.go`
   - `plugins/downloader/interactive.go`
   - `plugins/downloader/delivery.go`
   - `plugins/settings/settings.go`
   - `plugins/myxl/myxl.go`
   - `plugins/wikipedia/wikipedia.go`
   - relevant tests.
5. Implement **P0-A stage-aware self-inline failures first**.
6. Add behavior tests before broad feature changes.
7. Continue Help -> Calculator -> Downloader -> Settings in that order.
8. Do not inspect CI.
9. Do not claim runtime/build green without actually running the Go toolchain.

---

## 16. Final principle

The intended Goultroid product model is:

```text
Userbot = product capability
Assistant = optional rich control/presentation surface
Inline = optional rich discovery/presentation surface
```

Not:

```text
Userbot command
    -> Assistant required
    -> command unavailable if Assistant is absent
```

The next session should fix the dependency direction, not merely add special-case error messages.
