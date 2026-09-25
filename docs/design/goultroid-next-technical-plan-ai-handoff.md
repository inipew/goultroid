> **LATEST CONTINUATION (2026-09-26):** For the current userbot-first Assistant optionality audit/fix, read **docs/design/goultroid-assistant-optional-userbot-ai-session-handoff.md** first. It documents the cross-feature finding that Help, Calculator, and URL Downloader currently hard-depend on self-inline Assistant presentation, Settings has a hidden Assistant navigation dependency, and establishes the required progressive-enhancement contract plus stage-safe fallback plan. Current source always wins.
>
> **LATEST CONTINUATION (2026-09-25):** Read **docs/design/goultroid-selfinline-help-downloader-p2-ai-session-handoff.md** before continuing Assistant/self-inline work. Live testing after the P1 self-inline acceptance commit exposed Help callback handler-unavailable failures, unclickable downloader buttons, an incomplete callback-dispatch acceptance gate, and the previously identified P2 generic-callback/direct-Assistant cutover work. This dedicated handoff supersedes older assumptions that interactive self-inline acceptance was fully closed.

# Goultroid - AI Session Handoff

Current implementation state, post-P8 work, lifecycle/resource invariants, and next-analysis guide.

| Field | Value |
|---|---|
| Repository | github.com/inipew/goultroid |
| Branch | test-next |
| Branch HEAD before this handoff refresh | bb3af71bf8a2e21fc27f468bfab61cb4bd49f8e4 |
| Latest implementation commit | d307311d2dab084fb4bcdc01935e6448049cb32e |
| Latest implementation message | fix(downloader): deliver userbot URL downloads to Telegram |
| Snapshot | 25 September 2026, Asia/Jakarta |
| P1-P8 Assistant parity | CLOSED |
| Ultroid-facing V2-V4 | CLOSED |
| Owner-bound button lifetime | IMPLEMENTED |
| YouTube search-first | CLOSED through lifecycle UX |
| YT-Z Telegram media delivery | IMPLEMENTED, runtime hardening still OPEN |
| CI rule | Do not inspect CI unless the user explicitly asks |

This is the canonical handoff for the next AI session. Current source and regression tests are always higher authority than this document.

---

## 1. Mandatory bootstrap for the next session

Before changing code:

1. Refresh test-next and record exact HEAD.
2. Compare drift from the audited HEAD above.
3. Read every source/test file touched by drift.
4. Treat current source plus architecture/integration/regression tests as truth.
5. Re-audit any older issue before calling it still open.
6. Run gofmt before every commit that changes Go.
7. Do not inspect, poll, or wait for CI unless explicitly requested.
8. Do not solve local UX work by adding a second runtime, registry, TaskEngine, RPC executor, callback protocol, downloader, or retry engine.
9. Keep state/cache/cardinality bounded and idle footprint near zero.

Evidence order:

~~~text
current source
  -> architecture/integration/regression tests
  -> current handoff and closure docs
  -> design docs
  -> historical audits
  -> old chat/session memory
~~~

---

## 2. Frozen architecture authorities

Post-P8 work must continue to reuse these authorities:

~~~text
core.Router
    canonical command execution

feature.Registry
    canonical cross-surface feature metadata

interaction.Runtime
    bounded a2 session/token authority

interaction.Dispatcher
    typed callback validation/registration

interaction/orchestration.Engine
    state transition/action/media-delivery boundary

presentation.View / presentation.Port
    transport-neutral UI boundary

Inline vNext
    inline matching/result/cache/lifecycle

TaskEngine
    execution/backpressure/resources

telegram.RPCExecutor
    retry/FloodWait/RPC authority

selfinline.Renderer
    userbot -> own Assistant rendering

download.Registry
    canonical downloader provider registry

shared storage/media ownership
    retained-asset lifecycle
~~~

Still forbidden:

~~~text
second command registry
second callback protocol
second TaskEngine
second RPC executor
feature-local FloodWait/retry
feature-owned Telegram uploader when shared delivery exists
global per-user workflow maps
per-user/per-chat permanent workers
unbounded query/result caches
raw arbitrary callback_data
AssistantDownloader / InlineDownloader / YouTubeDownloader second engines
~~~

Target remains: Ultroid-facing UX plus Goultroid-native state/resource/lifecycle architecture.

---

## 3. P8 remains CLOSED

P8-A through P8-J are closed and must not be mechanically reopened.

Important closure commits:

~~~text
P8-A  8cb0d9cadfd6599dadcaf92677a15e9fecc77094
P8-B  3ae37805080134f571c936293528eeb46b823cf6
P8-C  aab6f94ef1cb0c90f59d0e62495fcba81b0dd93c
P8-D  ab6f98841a0b92062a785e3d2ececd84fe764727
P8-E  812b853e2a40da6ddd9a5e91d59cd39e95444ac7
P8-F  fb32d215b00045f0c38a4aff4c7f083808f00d44
P8-G  c0ef995d2dc238bfdb2006378e1e49c94ff743ce
P8-H  d19c2a8e2494ab6568360cf6d9c7c3a242e81c6e
P8-I  13f9cd6fd1cb2b55f3d5c09fd319d886d952eed1
P8-J  b5ceb8338ea14182bf58466b9e663a05b4db5b07
~~~

Post-closure lint/vet/race cleanup later reached 52978381b7b3fa4b8b293259774ac8ae0c2b511c and removed the old CI P7 acceptance job.

Later downloader/product work does not reopen P8.

---

## 4. Post-P8 Ultroid-facing presentation work

First wave:

~~~text
57409cae feat(assistant): align shell presentation with Ultroid UX
cd10177f feat(calculator): match Ultroid inline keypad presentation
cc512fd6 feat(downloader): align interactive presentation with Ultroid UX
f5e8b5d6 feat(assistant): adopt Ultroid-facing labels and navigation text
c93b22b1 docs(assistant): map Ultroid visual text and interaction parity
~~~

Effects:

- Home became shorter and action-first.
- Stats became compact.
- Calculator became keypad-style.
- Downloader stopped exposing TaskEngine/storage jargon to normal users.
- Visible wording/button grammar moved closer to Ultroid.
- Architecture remained Goultroid-native.

One malformed shell edit occurred temporarily and was restored before final presentation commits. Do not resurrect that intermediate state.

---

## 5. V2 public /start - CLOSED

Important commits:

~~~text
c4a433e2 make public start relay-aware and Ultroid-facing
991d95b1 add public-start localization
d6c01d69 expose live relay availability
052bfc89 close V2 public-start parity
~~~

Current contract:

~~~text
visitor /start
  -> public path
  -> canonical locale
  -> live relay availability
  -> compact greeting
  -> optional relay guidance
  -> audience touch source=start
~~~

Invariants:

- plain public /start remains sessionless;
- no a2 session is created;
- relay hint appears only when relay is actually enabled;
- username is escaped before HTML output;
- no worker/ticker/poller/global map;
- no fake owner-info button without a canonical owner-info capability.

---

## 6. V3 Help direct-grid - CLOSED

Commits:

~~~text
dbb0e65a add bounded direct-grid help slots
94a4403f avoid duplicate help catalog sorting
4b7551ea close V3
~~~

Current design:

~~~text
module page: max 8, two columns
command page: max 8, two columns
~~~

Fixed action vocabulary:

~~~text
8 x help_module_slot_N
8 x help_command_slot_N
~~~

No module/command identity is encoded into callback bytes.

A 128-bit visible-page fingerprint is stored in existing a2 binding bytes. On click the canonical catalog is rebuilt and fingerprint is revalidated. Catalog remap therefore fails closed with ErrShellHelpSelectionStale instead of silently remapping a slot.

Old help_open/help_cmd_open carousel actions were reclaimed.

---

## 7. V4 Settings direct-grid - CLOSED

Commits:

~~~text
45787b90 add bounded direct-grid settings slots
3158704d close V4
~~~

Current design:

~~~text
Settings root: max 8 categories/page, two columns
Category: max 8 settings/page, two columns
~~~

Fixed slots:

~~~text
8 x settings_category_slot_N
8 x setting_slot_N
~~~

settings.Registry remains the only schema/category authority.

Page fingerprint protects identity plus schema/presentation metadata. After selecting a concrete setting, authority returns to the existing namespace:key + schemaVersion mutation binding.

Important performance invariant:

~~~text
browse root/category/page -> zero effective-value reads
open concrete setting    -> resolve effective value
~~~

Do not regress this with eager per-setting reads.

---

## 8. Owner-bound button lifetime - IMPLEMENTED

Key commits:

~~~text
6c40c895 sustain owner-bound MyXL buttons safely
480dbd59 keep sensitive MyXL screens on short leases
7e475e7d sustain safe owner-bound interaction buttons
0386a62c document owner-bound lifetime
76b578ba repair shell TTL declaration syntax
~~~

Modern a2 sessions remain bound to:

~~~text
feature generation
actor ID
chat/message or inline target
session revision
~~~

Wrong actor or copied/wrong target fails before feature handler execution.

User-facing mismatch alert:

~~~text
This button can only be used by the user who opened it on the original message.
~~~

Safe navigation/read-only surfaces use sliding 24h leases. Context.Touch extends expiry without changing revision.

Long-lived examples:

- Assistant Home/Status/Help/Settings;
- MyXL read-only/navigation;
- MyXL legacy quota refresh;
- calculator;
- downloader choose/format UI.

Sensitive authority intentionally remains short:

~~~text
Settings input             2m
MyXL input/wizard          2m
MyXL purchase confirm      5m
MyXL delete confirm        5m
pending QRIS               5m
legacy transaction state   short + single-use
~~~

MyXL uses a screen-owned Sustain flag:

~~~text
navigation -> Sustain=true  -> 24h
input      -> Sustain=false -> 2m
confirm    -> Sustain=false -> 5m
~~~

Hard-expired sensitive state is never resurrected.

---

## 9. YouTube search-first downloader - CLOSED through lifecycle UX

The implementation reuses existing yt-dlp and P8-E. There is no second YouTube downloader.

Canonical flow:

~~~text
yt <query>
  -> yt-dlp structured search
  -> max 5 rich results
  -> select result
  -> canonical YouTube URL
  -> same interactiveState as dl <URL>
  -> Audio / Video
  -> format
  -> existing P8-E downloader
~~~

Direct URL and search-result paths converge before physical download.

### YT-A / YT-B

Commit b7a38284afe8ef58a075cc15888983c9c95b1fd9 added the shared search contract and yt-dlp search.

Bounds:

~~~text
results max          5
query max            256 bytes
service timeout      15s default / 30s max
structured stdout    <= 1 MiB
~~~

yt-dlp invocation uses:

~~~text
--ignore-config
--no-warnings
--simulate
--flat-playlist
--dump-single-json
ytsearchN:<query>
~~~

Search uses TaskEngine PriorityInteractive with process=1 only.

### YT-E through YT-I

Commit fb8605223ec557c1681ecfeeec639760651fa2e3 added inline yt search.

The same downloader inline binding recognizes:

~~~text
dl <URL>
yt <query>
~~~

Bare yt returns help without TaskEngine admission.

Rich results expose thumbnail/title/channel/duration/views when available.

Selected search result produces the same state as direct URL:

~~~text
interactiveState{
  URL: canonical URL,
  Provider: extractor,
  Phase: choose
}
~~~

Regression tests compare search state and dl-state for equality.

Cache policy remains CacheNone; no plugin query map/cache exists.

### YT-K / YT-N

Commit b3f27593c9ead1aebf2941792bfe9d9e6f329046 closed Search Again and lifecycle acceptance.

Search Again is SwitchInline, SamePeer=true, query "yt ", with no callback Data and no new action slot.

Plugin-scoped TaskClient owns search generation. Manager.Disable -> CancelScope cancels active search and releases process resource. Do not add a downloader-local cancellation registry.

---

## 10. YT-Z canonical Telegram media delivery - IMPLEMENTED

This supersedes the old P8-E metadata-only completion behavior.

Commit sequence:

~~~text
f154f1cc feat(downloader): deliver retained media to Telegram
cd246031 fix(downloader): harden retained media delivery lifecycle
83f46a7b fix(downloader): keep YT-Z completion RPCs task-owned
e0b82e13 cleanup(downloader): remove metadata-only completion fallback
d307311d fix(downloader): deliver userbot URL downloads to Telegram
~~~

Current implementation is centered on plugins/downloader/delivery.go.

### 10.1 Canonical delivery boundary

Presentation now exposes presentation.Media and presentation.MediaDeliverer.

orchestration.Context.PrepareMediaDelivery snapshots a target-bound delivery handle without retaining mutable session state.

~~~text
interactive session
  -> PrepareMediaDelivery
  -> detached target-bound handle
  -> download finishes
  -> shared presentation transport delivers media
~~~

Downloader does not own Telegram MTProto upload logic.

### 10.2 Message and inline target behavior

presentation/telegram.Bridge.DeliverMedia handles both:

~~~text
message target
  -> SendMediaContext / SendMedia

inline target
  -> UploadInlineMedia
  -> reusable Telegram media
  -> EditInlineBotMedia
~~~

Assistant transport additions include SendMedia, SendMediaContext, UploadInlineMedia, and InlineClientInteraction.EditMedia, still using shared RPC executor semantics.

### 10.3 Resource separation

Critical invariant:

~~~text
SEARCH:
process=1
download=0
media=0

DOWNLOAD:
direct HTTP -> download=1
extractor   -> download=1 + process=1

DELIVERY:
media=1
download=0
process=0
~~~

Delivery timeout is currently 30 minutes.

Telegram upload must never keep download/process leases alive.

### 10.4 Retained asset semantics

Retained ownership is registered before delivery.

Success does not blindly delete the retained asset.

Failure also preserves retained ownership and tells the user the asset remains safely retained.

Materialization is bounded by upload-size checks, bounded copy, temp cleanup, and sanitized name/extension handling.

### 10.5 Completion callback hardening

Do not perform Telegram RPC directly inside TaskEngine completion callback.

Required pattern:

~~~text
task completion callback
  -> enqueue task-owned continuation
  -> Telegram RPC
~~~

Commit 83f46a7b hardened this.

Commit e0b82e13 removed normal metadata-only completion fallback.

### 10.6 Userbot URL delivery

Current HEAD d307311d... adds automatic Telegram delivery for userbot URL downloads as well.

Reply/topic context is preserved.

Do not assume YT-Z is Assistant-inline-only.

---

### 10.7 Runtime defect still open: .download <youtube-url>

The user reports that the real userbot command:

~~~text
.download <youtube-url>
~~~

still errors on the current branch even though YT-Z architecture/tests are present.

Treat this as an **OPEN runtime defect**, not as evidence that the YT-Z architecture is absent.

The exact runtime error/stderr was not captured in this handoff. The next session must reproduce it or obtain the exact log before changing behavior.

Current path:

~~~text
.download URL
  -> handleURLDownload
  -> Registry.Resolve(URL)
  -> submitInteractivePipeline
  -> TaskEngine download(+process for extractor)
  -> ExtractorProvider.Download
  -> retained storage + media ownership
  -> TaskEngine media=1
  -> current.Media().SendMedia(...)
  -> terminal status edit
~~~

High-value audit targets:

1. Capture the exact error and identify whether it occurs in command admission, yt-dlp extraction, retained persistence, ownership registration, Telegram upload/send, or terminal edit.
2. Inspect exact yt-dlp argv and stderr for the failing YouTube URL.
3. Search uses --ignore-config; ExtractorProvider.Download currently does not. Host yt-dlp.conf can change output/sidecars/format behavior.
4. Direct userbot URL download currently uses MediaModeDefault + MediaFormatDefault, so no explicit -f selection is added. Verify whether current yt-dlp chooses split streams/merge requiring ffmpeg.
5. Audit modern YouTube failure classes from stderr: 403, PO-token requirement, cookies/account requirement, player-client issues, or outdated yt-dlp.
6. ExtractorProvider.Download currently picks the first non-directory file in its temp directory. Verify no sidecar/config-generated file can be selected.
7. Persisted extractor assets may have empty MIME metadata; default delivery can classify them as generic file.
8. Verify the detached real core.Context still carries correct Telegram service/peer/topic into asynchronous delivery.
9. Preserve resource split: extractor download = download=1 + process=1; Telegram delivery = media=1 only.
10. Do not add a new downloader/uploader/retry engine to fix this defect.
## 11. YT-Z acceptance evidence already present

plugins/downloader/delivery_test.go covers:

- media=1 only in delivery stage;
- no download/process resource during upload;
- retained asset survives success;
- retained asset survives delivery failure;
- download resources release before delivery;
- lifecycle cancellation classification;
- userbot URL delivery;
- reply/topic preservation.

plugins/downloader/delivery_completion_test.go covers:

- terminal edit deferred out of completion callback;
- delivery-completion UI continuation is task-owned;
- delivery-failure UI continuation is task-owned.

Preserve these as regression fences.

---

## 12. Current end-to-end downloader flow

Search-first Assistant/inline:

~~~text
yt query
  -> process=1 search
  -> rich results
  -> select
  -> actor/target/generation/revision-bound a2 state
  -> Audio/Video
  -> format
  -> download (+ process for extractor)
  -> retained asset + ownership
  -> release download/process
  -> media=1 delivery
  -> presentation.MediaDeliverer
  -> Telegram
~~~

Direct inline URL:

~~~text
dl URL
  -> same choose/format path
  -> same download backend
  -> same retained ownership
  -> same delivery path
~~~

Userbot URL:

~~~text
userbot URL command
  -> download/process
  -> retained asset
  -> release download/process
  -> media=1
  -> SendMediaContext
  -> reply/topic-aware Telegram output
~~~

---

## 13. Known stale-document hazards

docs/design/assistant-parity-p8e-interactive-downloader.md is historically correct for P8-E closure but stale for current TTL and Telegram-delivery behavior.

Do not use it to conclude automatic media delivery is still missing.

docs/design/assistant-ultroid-visual-text-parity.md correctly describes V2-V4 architecture but some "next work" text predates completed YouTube search-first and YT-Z.

Current source always wins.

---

## 14. Working rules that remain binding

1. test-next is the working branch.
2. Refresh exact HEAD before each phase.
3. Run gofmt before every Go commit.
4. Do not inspect CI unless explicitly requested.
5. Reuse canonical runtime/registry/executor boundaries.
6. Keep state/cache/cardinality bounded.
7. Prefer event-driven/zero-idle design over polling.
8. Telegram retry/FloodWait remains in telegram.RPCExecutor.
9. Heavy work/resources remain in TaskEngine.
10. Callback/session authority remains in a2 runtime.
11. Inline lifecycle remains in Inline vNext.
12. Retained ownership remains in shared storage/media infrastructure.
13. Telegram media delivery goes through shared presentation/Telegram boundaries.
14. Fresh authority is required before delayed mutation.
15. Generation-scoped work must fail closed after disable/reload.

---

## 15. High-risk regression checklist

Before closing future downloader/Assistant work, verify:

- no second downloader/provider registry;
- no raw callback protocol;
- no plugin-local retry/FloodWait;
- no full search-result JSON retained in session;
- no N full metadata fetches for N search results;
- no process lease while waiting for button selection;
- no download/process lease during Telegram upload;
- retained asset is not deleted merely because send succeeded;
- retained asset is not lost when send fails;
- no direct Telegram RPC from completion callback;
- no wrong-actor button execution;
- no hard-expired sensitive-state resurrection;
- no plugin disable/reload resurrection;
- no unbounded per-query map/cache;
- no permanent feature worker/ticker;
- no query text in metric labels.

---

## 16. Remaining ordinary product breadth

P8 parity is closed. Remaining work is product work, not parity debt.

Potential future areas:

- final YT-Z audit and dedicated closure documentation;
- reconcile stale P8-E/visual-parity docs;
- optional Share UX after Search Again;
- additional rich search providers such as F-Droid/web search;
- more locale packs through canonical localization service;
- dedicated first-run onboarding using bounded a2 + central Settings;
- arbitrary-code/Piston style work remains security-gated and must not run on host shell/process without a separate threat model and isolated backend.

Do not add Ultroid providers mechanically.

---

## 17. Recommended next-session analysis order

### Step 1 - refresh and drift audit

~~~text
fetch test-next
record HEAD
compare against d307311d2dab084fb4bcdc01935e6448049cb32e
inspect all drifted files
~~~

### Step 2 - build/test sanity if real checkout is available

A real build error previously exposed an escaped newline in shell.go and was fixed in 76b578....

Prefer actual verification:

~~~text
gofmt check
go build -o bin/goultroid ./cmd/goultroid
targeted downloader tests
targeted interaction tests
~~~

Do not claim green without running them.

### Step 3 - audit current YT-Z implementation

Read:

~~~text
plugins/downloader/delivery.go
plugins/downloader/delivery_test.go
plugins/downloader/delivery_completion_test.go
plugins/downloader/downloader.go
plugins/downloader/interactive.go

internal/presentation/port.go
internal/presentation/telegram/bridge.go
internal/interaction/orchestration/context.go
internal/assistant/interaction/message.go
internal/assistant/client/interaction_ingress.go
~~~

Questions:

1. Is every retained asset registered before delivery?
2. Are download/process leases released before media=1?
3. Does every Assistant message/inline target use the shared presentation bridge?
4. Does userbot delivery always preserve reply/topic context?
5. Do failure paths keep retained ownership?
6. Do lifecycle cancellations avoid fake delivery-failed terminal errors?
7. Are completion-related Telegram RPCs always task-owned?
8. Can ambiguous send/upload failure produce duplicate media if retried?
9. Does inline media edit preserve intended caption/markup?
10. Is disable/reload cancellation leak-free?
11. Are temp materialization files always cleaned?
12. Is upload-size enforcement consistent across materialization and sender?

Fix real defects before adding provider breadth.

### Step 4 - reconcile stale docs

At minimum review:

~~~text
docs/design/assistant-parity-p8e-interactive-downloader.md
docs/design/assistant-ultroid-visual-text-parity.md
~~~

---

## 18. Recent commit timeline

~~~text
57409cae  align shell presentation with Ultroid UX
cd10177f  calculator keypad presentation
cc512fd6  downloader Ultroid-facing presentation
f5e8b5d6  Ultroid-facing labels/navigation
c93b22b1  visual/text parity map

052bfc89  close V2 public start

dbb0e65a  Help direct-grid slots
94a4403f  avoid duplicate Help sorting
4b7551ea  close V3

45787b90  Settings direct-grid slots
3158704d  close V4

6c40c895  owner-bound MyXL buttons
480dbd59  short sensitive MyXL leases
7e475e7d  safe owner-bound interaction lifetime
0386a62c  owner-bound lifetime doc
76b578ba  repair shell TTL syntax

b7a38284  bounded yt-dlp search foundation
fb860522  search-first YouTube inline UX
b3f27593  YouTube search lifecycle UX

f154f1cc  retained media Telegram delivery
cd246031  delivery lifecycle hardening
83f46a7b  task-owned completion RPCs
e0b82e13  remove metadata-only fallback
d307311d  userbot URL Telegram delivery
~~~

Use full SHA from Git history when editing/cherry-picking.

---

## 19. Current one-line state

~~~text
P1-P8 architecture/parity                 CLOSED
V2 public start                           CLOSED
V3 Help direct-grid                       CLOSED
V4 Settings direct-grid                   CLOSED
Owner-bound safe long-lived buttons       IMPLEMENTED
MyXL short sensitive authority            IMPLEMENTED
YouTube search-first discovery            CLOSED
Search Again + search lifecycle           CLOSED
Canonical retained-media delivery         IMPLEMENTED
Userbot URL automatic media delivery      IMPLEMENTED IN SOURCE / LIVE .download DEFECT OPEN

NEXT:
refresh HEAD
-> build/test sanity if possible
-> audit YT-Z resource/lifecycle correctness
-> fix only real defects
-> reconcile stale docs
-> then choose next ordinary product feature
~~~

---

## 20. Session memory consolidation — 25 September 2026

This section records the latest cross-session state explicitly so a future AI does not regress to an older chat memory.

### Completed architecture/product work

~~~text
P1-P8 Assistant parity                 CLOSED
V2 public /start                       CLOSED
V3 Help direct-grid                    CLOSED
V4 Settings direct-grid                CLOSED

Owner-bound button lifetime            IMPLEMENTED
MyXL safe navigation lease             24h sliding
MyXL input                             2m
MyXL purchase/delete confirmation      5m
wrong actor / wrong target             fail closed before handler

YT-A/YT-B search contract + yt-dlp     CLOSED
YT-E..YT-I inline yt UX/convergence    CLOSED
YT-K Search Again                      CLOSED
YT-N search lifecycle acceptance       CLOSED
YT-Z retained media Telegram delivery  IMPLEMENTED
Userbot URL automatic delivery         IMPLEMENTED
~~~

### Latest downloader architecture

~~~text
yt <query>
  -> TaskEngine process=1
  -> download.Registry.Search("extractor")
  -> yt-dlp structured ytsearch
  -> <=5 normalized rich results
  -> Inline vNext result selection
  -> actor/target/generation/revision-bound state
  -> same interactiveState as dl <URL>
  -> Audio / Video
  -> format
  -> existing downloader
  -> retained asset ownership
  -> release process/download
  -> TaskEngine media=1 delivery continuation
  -> shared presentation.MediaDeliverer
  -> Telegram message/inline media
~~~

There is no second YouTube downloader, no plugin-local Telegram uploader, no query-result global map, and no second callback protocol.

### YT-Z is already implemented

Do **not** start a new YT-Z implementation from the older chat TODO.

The implementation sequence already present is:

~~~text
f154f1cc feat(downloader): deliver retained media to Telegram
cd246031 fix(downloader): harden retained media delivery lifecycle
83f46a7b fix(downloader): keep YT-Z completion RPCs task-owned
e0b82e13 cleanup(downloader): remove metadata-only completion fallback
d307311d fix(downloader): deliver userbot URL downloads to Telegram
~~~

The next session should audit and harden this implementation, not recreate it.

### Immediate next analysis

1. Refresh current `test-next`.
2. Compare source drift from implementation baseline `d307311d2dab084fb4bcdc01935e6448049cb32e`.
3. If a real checkout is available, run:
   - `gofmt` cleanliness;
   - `go build -o bin/goultroid ./cmd/goultroid`;
   - targeted downloader delivery/search tests;
   - targeted interaction ownership/lifecycle tests.
4. Reproduce the reported live .download <youtube-url> failure first and preserve exact error/stderr.
5. Audit YT-Z resource/lifecycle correctness:
   - retained asset registered before delivery;
   - download/process released before media=1;
   - no Telegram RPC directly in completion callback;
   - inline/message/userbot targets all use shared delivery boundary;
   - reply/topic preserved;
   - failure keeps retained ownership;
   - lifecycle cancellation does not produce misleading terminal failure;
   - materialized temp files always cleaned;
   - ambiguous upload/send failure cannot trivially duplicate media on retry.
6. Fix only defects verified in current source/runtime evidence.
7. Reconcile stale historical docs after correctness work.
8. Then choose the next ordinary product feature.

### Binding working rules

- current source/tests outrank old handoff/chat memory;
- refresh HEAD before each phase;
- `gofmt` before every Go commit;
- never inspect CI unless explicitly asked;
- TaskEngine remains physical execution/resource authority;
- telegram.RPCExecutor remains Telegram retry/FloodWait authority;
- a2 remains callback/session authority;
- Inline vNext remains inline lifecycle authority;
- shared storage/media ownership remains retained-asset authority;
- shared presentation/Telegram boundary remains media-delivery authority;
- no duplicate runtime/registry/executor/uploader;
- no unbounded cache/cardinality;
- no unnecessary idle worker/ticker/poller.

---

## 21. Final handoff rule

Do not continue from an old TODO list merely because it exists.

~~~text
refresh source
  -> establish HEAD
  -> read drift
  -> reconstruct actual behavior
  -> test architecture/resource/lifecycle invariants
  -> only then choose or fix the next item
~~~

Current Goultroid direction:

> reliable, lightweight, responsive userbot behavior with bounded state/resources, one authority per concern, zero unnecessary idle work, and richer Ultroid-like UX without importing Ultroid global-state architecture.
