# Assistant Parity P8-F — behavioral matrix and locale-aware Assistant UX

## Status

**P8-F is CLOSED for implementation/source acceptance.**

Baseline:

`812b853e2a40da6ddd9a5e91d59cd39e95444ac7` — P8-E representative interactive downloader workflow.

P8-F closed the remaining MUST UX residual frozen by P8-A. P8-J later reconciled this matrix through P8-I; the status table below reflects the final P8 closure.

## Canonical locale contract

There is exactly one persisted Assistant locale setting:

```text
namespace: ui
key:       locale
type:      enum
values:    en | id
default:   en
scope:     normal central settings hierarchy
```

The dedicated Assistant language selector writes a **user-scope override** for that same setting. It does not own a second preference store.

Resolution remains:

```text
chat override
    ↓
user override
    ↓
global override
    ↓
schema default (en)
```

The Assistant owner shell normally writes the user override because language is a user preference. Other canonical settings APIs can still provide the normal hierarchy.

## Production flow

```text
/start owner
    ↓
a2 Home
    ↓
Language
    ↓
English | Bahasa Indonesia
    ↓
a2 revision reservation
    ↓
settings.SetRegisteredResult(ui:locale)
    ↓
bounded settings cache invalidation
    ↓
same interaction re-rendered with effective locale
```

No locale is stored in:

- a plugin-local map;
- a per-user worker;
- interaction state as a preference authority;
- a second configuration file;
- a global mutable "current Assistant user" locale.

Interaction state only owns normal navigation/revision semantics.

## Locale implementation

### Built-in vocabulary

`internal/services/localization` remains the translation catalog authority.

P8-F adds a bounded canonical Assistant vocabulary:

- `en` — English;
- `id` — Indonesian.

Telegram-style language tags such as `en-US` and `id-ID` normalize to the canonical vocabulary. Unsupported locale identifiers fail back to English presentation; the persisted `ui:locale` enum itself rejects values outside `en|id`.

This is intentionally smaller than Ultroid's translation-pack catalog. Provider-for-provider language-pack cloning is not a P8 requirement.

### Central settings

`internal/settings/defaults.go` registers `ui:locale` as a normal enum definition.

The language selector uses:

`SetRegisteredResult`

with the current schema version. Therefore locale mutation inherits the existing P5-D settings guarantees:

- stable `namespace:key` identity;
- schema revision revalidation;
- typed canonicalization;
- user-scope persistence;
- bounded settings cache invalidation;
- durable settings outbox behavior when SQLite is active.

### a2 interaction ownership

The Assistant shell adds:

```text
screen:
  language

actions:
  language
  language_en
  language_id
```

All are ordinary typed FeatureSpec interactions.

The setter consumes the current session revision before persistence through `ctx.UpdateState`. Older buttons therefore remain stale under the canonical a2 runtime rather than acquiring special locale callback semantics.

## Locale-aware surfaces

P8-F converts the Assistant-owned presentation shell to explicit locale-aware rendering:

- owner Home;
- Status;
- Language selector;
- Help overview;
- Help module navigation;
- Help command detail;
- Settings category browser;
- Settings value browser;
- Settings detail;
- bounded Settings text-input prompt;
- public `/start`;
- shell inline root;
- shell inline ping;
- shell inline Help structural labels.

Inline shell handlers resolve the same `ui:locale` setting using the inline request user ID. They do not use a separate inline-language registry.

Feature-owned payloads remain feature-owned. For example a plugin's command description or remote Wikipedia content is not machine-translated by the shell.

## P8 A→J behavioral matrix — P8-J reconciled

| Capability / behavior | Ultroid behavior target | Goultroid evidence | Class | P8-F status |
| --- | --- | --- | --- | --- |
| Separate Assistant runtime | bot account alongside user account | Assistant client/runtime | BASELINE-CLOSED | CLOSED |
| Public `/start` | visitor welcome / entry | typed start/deep-link path + public fallback | BASELINE-CLOSED | CLOSED |
| Owner control home | callback owner menu | a2 Assistant shell | BASELINE-CLOSED | CLOSED |
| Status | callback runtime status | a2 Status screen | BASELINE-CLOSED | CLOSED |
| Help/module navigation | HELP maps / callback menus | canonical router/catalog generated Help | BASELINE-CLOSED | CLOSED |
| Settings browser | callback settings | central settings registry + a2 screens | BASELINE-CLOSED | CLOSED |
| Free-form settings input | conversation/input | bounded actor+chat input claim | BASELINE-CLOSED | CLOSED |
| Locale/language selector | language callback menu | canonical `ui:locale` + a2 Language screen | MUST / P8-F | CLOSED |
| Locale-aware shell presentation | translated Assistant UI | localization catalog + per-render central setting resolution | MUST / P8-F | CLOSED |
| Canonical dual commands | same feature on userbot/Assistant | one `core.Command` + surface policy | BASELINE-CLOSED | CLOSED |
| Resource-bearing Assistant command | bounded execution | shared TaskEngine admission | BASELINE-CLOSED | CLOSED |
| Saved/custom Assistant responses | custom bot commands/media | SavedResponse command/inline/callback/deep-link surfaces | BASELINE-CLOSED | CLOSED |
| PM relay | visitor ↔ owner mailbox | durable P6 relay | BASELINE-CLOSED | CLOSED |
| Audience/ban/force-sub/broadcast | public bot audience plane | P6 registry/policy/shared broadcast | BASELINE-CLOSED | CLOSED |
| Group/manager plane | manager/admin bot behavior | P7-A→L contextual authorization and group features | BASELINE-CLOSED | CLOSED |
| Reply/media/topic semantics | Telegram context preservation | P7-J boundaries | BASELINE-CLOSED | CLOSED |
| Complex transactional workflow | callback + input workflow | MyXL a2 migration | BASELINE-CLOSED | CLOSED |
| Self-inline rendering | userbot queries own Assistant and inserts result | P8-B RenderBridge | MUST | CLOSED |
| Callback-heavy calculator | calculator inline application | P8-C bounded calculator | MUST canary | CLOSED |
| Rich network lookup | inline search provider | P8-D Wikipedia | REPRESENTATIVE | CLOSED |
| Interactive heavy downloader | source → media/format → heavy task | P8-E downloader | REPRESENTATIVE heavy proof | CLOSED |
| Legacy a1/menu stack absent | migration off old callback/menu stack | `legacy_stack_test.go` regression fence | BASELINE-CLOSED | CLOSED |
| Cross-surface disable/re-enable matrix | old generation fully invalidated | P8-H generation-scoped command/inline/a2/deep-link/self-inline/input/TaskEngine acceptance | MUST / P8-H | CLOSED |
| Combined idle/high-load/resource matrix | bounded under mixed workloads | P8-I 10k inline + callback/session pressure + RPC/cache/resource/restart/settle acceptance | MUST / P8-I | CLOSED |

## P8-B audit reconciliation

P8-B remains closed.

Evidence still matches the frozen target:

- one feature-facing `selfinline.Renderer`;
- no retained result map;
- current Assistant username read at render time;
- read/send capabilities checked per render;
- Telegram query and insertion use existing managed Telegram service/RPC executor;
- topic/reply context preserved;
- a2 first-inline-target claim remains the callback continuity authority.

P8-F does not redesign P8-B.

## P8-C audit reconciliation

P8-C remains closed.

The calculator continues to provide:

- canonical userbot entry;
- FeatureSpec inline entry;
- fixed typed action vocabulary;
- state only in the bounded interaction runtime;
- no calculator worker/cache/session map;
- stale revision/cross-user/inline-target fencing;
- bounded recursive-descent evaluator rather than arbitrary code execution.

P8-F does not add Piston-style remote arbitrary execution.

## P8-D audit reconciliation

P8-D remains closed.

Wikipedia remains the representative rich lookup proof:

- one canonical backend shared with command behavior;
- one search request per inline lookup;
- at most five emitted results;
- capability-gated HTTP;
- explicit Inline vNext cache policy;
- no plugin-owned query cache/worker;
- sanitized excerpts and HTTPS thumbnail policy.

Google, Play Store, F-Droid, OrangeFox, Twitter/X, Saavn and similar provider copies remain non-blocking.

## P8-E audit reconciliation

P8-E remains closed as the representative heavy workflow proof.

The interactive flow still provides:

```text
dl <url>
    ↓
direct HTTP: Download File
or
extractor: Audio | Video
    ↓
M4A | MP3 | MP4 | Best
    ↓
prepared a2 action
    ↓
TaskEngine resources
    ↓
canonical downloader Registry
    ↓
retained asset
```

Resource ownership stays:

| source | resources |
| --- | --- |
| direct HTTP | `download:1` |
| extractor | `download:1 + process:1` |

The extractor reuses `tasks.HasHeldResource(ctx, "process")`, preventing nested process admission.

## Intentional differences from Ultroid

These differences are **deliberate**, not unresolved P8-F defects.

### 1. Architecture is Goultroid-native

Goultroid does not reproduce Ultroid's:

- global callback maps;
- plugin-local Assistant client access;
- decorator-owned Telegram handlers;
- per-feature retry/FloodWait logic;
- separate execution engines.

Behavioral parity is implemented through FeatureSpec, a2, Inline vNext, TaskEngine, RPCExecutor and canonical service boundaries.

### 2. Translation breadth is intentionally bounded

P8-F ships canonical English and Indonesian Assistant presentation.

It does not clone every Ultroid language pack.

Plugin-owned command descriptions, third-party/provider content and arbitrary feature prose remain owned by those features; the shell does not machine-translate them.

### 3. Representative providers are sufficient

Wikipedia proves rich network lookup. P8 does not require separate Google/F-Droid/Play Store/etc. implementations.

### 4. No arbitrary remote code execution

Piston-style arbitrary code execution remains explicitly excluded.

The calculator remains a bounded arithmetic evaluator.

### 5. Downloader delivery boundary remains canonical

P8-E stores the downloaded URL asset through the canonical storage/media ownership path and presents the retained asset result/path.

It does **not** add an Assistant-only uploader merely to mirror Ultroid's YouTube UX.

If automatic Telegram delivery of retained downloader assets is desired later, it should be implemented once at the canonical downloader/media-delivery boundary and reused by all surfaces.

### 6. Locale is a setting, not interaction state

Ultroid-style language menu semantics are preserved, but Goultroid persists the preference through `ui:locale` rather than a language-specific Assistant store/global.

## Resource profile

P8-F locale support adds:

```text
per-user locale workers = 0
locale goroutines       = 0
locale tickers          = 0
locale timers           = 0
locale preference maps  = 0
new persistence store   = 0
```

Translations are static bounded built-in catalogs. Effective locale lookup reuses the existing bounded settings cache.

## Acceptance coverage

P8-F adds coverage for:

- canonical locale normalization;
- explicit built-in English/Indonesian translation lookup;
- `ui:locale` enum definition and rejection of unsupported persisted values;
- typed a2 Language actions;
- Indonesian Home/Status presentation;
- localized `ui:locale` metadata inside Settings;
- user-scope locale persistence through central settings;
- old locale callback staleness after revision change;
- subsequent shell render using the persisted locale;
- source architecture fences against a second locale runtime/map;
- shell inline locale resolution through the same central setting.

## Final P8 closure status — P8-J reconciliation

```text
P8-A CLOSED
P8-B CLOSED
P8-C CLOSED
P8-D CLOSED
P8-E CLOSED
P8-F CLOSED
P8-G CLOSED
P8-H CLOSED
P8-I CLOSED
P8-J CLOSED
```

The reference-driven reclamation rule remains frozen: `internal/assistant/legacy_stack_test.go` stays as an architecture regression fence, while media/storage compatibility that belongs to independent migrations is not reclassified as Assistant legacy.

## Formatting and CI

All changed Go source for P8-F must be processed with `gofmt` before the P8-F commit.

P8-F does not inspect CI unless explicitly requested.
