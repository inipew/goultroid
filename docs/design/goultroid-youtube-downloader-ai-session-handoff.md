
# Goultroid — YouTube / Downloader AI Session Handoff

Status: ACTIVE — search-first, selection-first userbot UX, live progress, retained delivery, and metadata-aware Telegram delivery are implemented in source; final live acceptance after the latest UX change remains OPEN.

Repository: github.com/inipew/goultroid  
Branch: test-next  
Audited source HEAD before handoff documentation updates: bb3af71bf8a2e21fc27f468bfab61cb4bd49f8e4 — fix and format  
Date: 25 September 2026, Asia/Jakarta

This document is the preferred starting point for the next AI session continuing YouTube/downloader work.

Current source and tests outrank this document if the branch has moved.

## 2026-09-25 follow-up — runtime diagnosis + selection/metadata UX

The previously reported real `.download <youtube-url>` error was traced to the host simply not having `yt-dlp` installed. After installation, extraction succeeded. Do not treat the old missing-binary report as evidence of a deeper downloader architecture defect.

The next product issue observed from the successful run was UX/metadata quality:

- userbot `.download <URL>` started extraction immediately with default/default selection;
- yt-dlp could therefore choose a native WebM result;
- completion UI rendered the generic fallback `Format: file`;
- extractor persistence carried the filename but not MIME/title/performer/duration/resolution;
- Telegram audio delivery therefore had insufficient semantic attributes and could display generic labels such as `Unknown Track`;
- quality selection existed only on the Assistant inline surface and was not reached from the userbot command.

The current implementation direction closes those source-level gaps without adding a second downloader/callback engine:

~~~text
.download <URL>
    ↓
existing self-inline RenderBridge
    ↓
dl <URL>
    ↓
existing a2 downloader interaction
    ├─ direct HTTP → Download File confirmation
    └─ extractor
         ├─ Audio → MP3 / M4A / Opus
         └─ Video → MP4 ≤360/480/720/1080/1440/2160 or Best (native)
    ↓
typed final action
    ↓
TaskEngine physical download
    ↓
yt-dlp after_move final-path + metadata record
    ↓
retained storage metadata
    ↓
release download/process
    ↓
media=1 Telegram delivery
~~~

Important invariants:

- no physical URL download begins before the final typed action;
- userbot and inline UX now converge through the existing self-inline/a2 authorities;
- quality is a bounded whitelist, never an arbitrary user-provided yt-dlp selector;
- MP4 quality presets are strict MP4 choices at or below the selected maximum height;
- Best remains the native merge-aware best selection and can produce a non-MP4 container;
- extractor output selection prefers yt-dlp's `after_move` final filepath and only uses a bounded sidecar-aware fallback scan;
- retained media now carries MIME, title, performer, duration, width, and height;
- Assistant Telegram media upload maps those fields to gotd filename/audio/video attributes;
- live transfer progress includes the chosen maximum video height;
- Telegram delivery still holds only `media=1`, never `download` or `process`.

The old source test `TestYTZUserbotURLCommandDeliversAfterDownloadResourcesRelease` is intentionally replaced by a selection-first acceptance: the userbot command must open the canonical inline chooser and must submit zero heavy tasks before the user chooses the final format/quality.

Real-world acceptance should still be rerun against the latest source after deployment before declaring the end-to-end UX CLOSED.

---

## 1. Mandatory bootstrap for the next session

Before changing code:

1. Refresh exact test-next HEAD.
2. Compare drift from bb3af71bf8a2e21fc27f468bfab61cb4bd49f8e4.
3. Read every downloader/search/delivery file changed after that source snapshot.
4. Reproduce or obtain the exact runtime error for:

~~~text
.download <youtube-url>
~~~

5. Do not guess the fix before capturing the failing stage and yt-dlp/Telegram error.
6. Run gofmt before every Go commit.
7. Do not inspect CI unless the user explicitly asks.
8. Current source plus regression tests are authority over older docs/chat memory.
9. Do not add a second downloader, provider registry, TaskEngine, process runner, callback protocol, uploader, retry engine, or query cache.

Canonical authorities:

~~~text
TaskEngine
  execution / backpressure / resource ownership

download.Registry
  provider selection and search capability

ExtractorProvider
  yt-dlp search / extraction

a2 interaction runtime
  callback/session/actor/target/revision/generation authority

Inline vNext
  inline lifecycle/cache authority

shared storage + media ownership
  retained asset authority

presentation.MediaDeliverer + Telegram bridge
  delivery authority

shared Telegram RPC executor
  retry / FloodWait authority
~~~

---

## 2. Wider project state relevant to YT work

Assistant parity P1–P8 is CLOSED.

Recent Ultroid-facing UX work:

~~~text
V2 public /start            CLOSED
V3 Help direct-grid         CLOSED
V4 Settings direct-grid     CLOSED
~~~

Owner-bound button lifetime is implemented:

~~~text
safe navigation/read-only      24h sliding owner-bound lease
Settings free-form input       2m
MyXL free-form input           2m
MyXL purchase confirmation     5m
MyXL delete confirmation       5m
wrong actor / wrong target     fail closed before feature handler
~~~

Relevant design doc:

~~~text
docs/design/assistant-owner-bound-button-lifetime.md
~~~

Downloader Audio/Video/format buttons inherit the same actor/target/generation/revision protection.

---

## 3. Search-first YouTube work already completed

### YT-A / YT-B — shared search contract + yt-dlp search

Status: CLOSED.

Commit:

~~~text
b7a38284afe8ef58a075cc15888983c9c95b1fd9
feat(download): add bounded yt-dlp search foundation
~~~

Implemented:

- optional download.SearchProvider;
- Registry.Search(providerName, query, options);
- ExtractorProvider.Search();
- structured yt-dlp search output;
- normalized SearchResult;
- no second search/downloader engine.

Search invocation:

~~~text
yt-dlp
  --ignore-config
  --no-warnings
  --simulate
  --flat-playlist
  --dump-single-json
  ytsearchN:<query>
~~~

Bounds:

~~~text
query max             256 bytes
results default/max   5
service timeout       15s default / 30s max
stdout cap            1 MiB
~~~

Normalized metadata:

~~~text
Provider
Source
SourceID
URL
Title
Description
Thumbnail
Channel
DurationSeconds
Views
PublishedAt
~~~

Search physically holds:

~~~text
process=1
download=0
media=0
~~~

---

## 4. Search-first Inline UX already completed

### YT-E through YT-I

Status: CLOSED.

Commit:

~~~text
fb8605223ec557c1681ecfeeec639760651fa2e3
feat(downloader): add search-first YouTube inline UX
~~~

The downloader keeps one canonical inline binding.

Matcher recognizes:

~~~text
dl <URL>
yt <query>
~~~

Flow:

~~~text
yt <query>
  -> TaskEngine PriorityInteractive
  -> process=1
  -> Registry.Search("extractor")
  -> yt-dlp structured search
  -> <=5 rich Inline vNext results
  -> user selects result
  -> interactiveState{
       URL: canonical YouTube URL,
       Provider: "extractor",
       Phase: "choose",
     }
~~~

The selected search result intentionally creates the same interactive state as:

~~~text
dl <same-youtube-url>
~~~

This convergence is regression-tested.

Rich result presentation includes:

- thumbnail;
- title;
- channel;
- duration;
- views;
- typed Audio / Video actions.

Inline search uses a dedicated 3-second budget so it remains below Inline vNext's default 4-second handler deadline.

Cache policy is CacheNone.

No plugin-local search-result map exists.

---

## 5. YT-K / YT-N already completed

Status: CLOSED.

Commit:

~~~text
b3f27593c9ead1aebf2941792bfe9d9e6f329046
feat(downloader): close YouTube search lifecycle UX
~~~

Search Again is transport-neutral:

~~~go
ui.NewSwitchInlineButton("🔎 Search Again", "yt ", true)
~~~

It is not callback data and does not create a new action slot/session.

Audio/Video remain typed a2 actions.

Search task lifecycle is plugin-generation scoped:

~~~text
PluginContext.TaskClient
  -> scoped TaskClient
  -> active search
  -> Manager.Disable
  -> CancelScope(plugin:downloader, generation)
  -> context cancelled
  -> process released
~~~

Acceptance uses production TaskEngine for scope cancellation.

---

## 6. YT-Z delivery architecture already exists

Status: IMPLEMENTED IN SOURCE.

Do not recreate YT-Z from an older TODO.

Implementation sequence already present:

~~~text
f154f1cc  feat(downloader): deliver retained media to Telegram
cd246031  fix(downloader): harden retained media delivery lifecycle
83f46a7b  fix(downloader): keep YT-Z completion RPCs task-owned
e0b82e13  cleanup(downloader): remove metadata-only completion fallback
d307311d  fix(downloader): deliver userbot URL downloads to Telegram
~~~

Primary source:

~~~text
plugins/downloader/delivery.go
plugins/downloader/interactive.go
plugins/downloader/downloader.go

internal/presentation/port.go
internal/presentation/telegram/bridge.go
internal/interaction/orchestration/context.go
~~~

Presentation now exposes:

~~~text
presentation.Media
presentation.MediaDeliverer
~~~

Interactive delivery snapshots a target-bound handle through:

~~~text
orchestration.Context.PrepareMediaDelivery()
~~~

Downloader does not own Telegram MTProto upload internals.

---

## 7. YT-Z resource model

Preserve exactly:

~~~text
SEARCH
  process=1
  download=0
  media=0

DIRECT HTTP DOWNLOAD
  download=1
  process=0
  media=0

EXTRACTOR DOWNLOAD
  download=1
  process=1
  media=0

TELEGRAM DELIVERY
  media=1
  download=0
  process=0
~~~

Telegram upload must never hold download/process leases.

Current delivery timeout is 30 minutes.

---

## 8. Retained asset and completion semantics

Download stage:

~~~text
Registry.Download
  -> storage.Asset
  -> registerRetainedAsset
  -> download task completes
~~~

Only after retained ownership is registered does delivery begin.

Success does not blindly delete the retained asset.

Delivery failure also preserves the asset.

This is intentional for recovery and ambiguous Telegram failures.

Task completion callbacks must not perform Telegram RPC directly.

Required pattern:

~~~text
download completion
  -> enqueue delivery task

delivery completion
  -> enqueue terminal status task
~~~

Current delivery completion tests explicitly fence this.

---

## 9. Current interactive end-to-end flow

~~~text
yt <query>
  -> process=1 search
  -> rich results
  -> select
  -> owner-bound a2 state
  -> Audio / Video
  -> format
  -> extractor download
  -> retained asset
  -> ownership registration
  -> release download/process
  -> media=1 delivery
  -> presentation.MediaDeliverer
  -> Telegram
~~~

Direct inline URL:

~~~text
dl <URL>
  -> same choose/format path
  -> same physical backend
  -> same retained ownership
  -> same delivery path
~~~

---

## 10. Userbot URL command path currently present

Commands:

~~~text
.download <URL>
.dl <URL>
~~~

Current flow:

~~~text
handleDownload
  -> handleURLDownload
  -> Registry.Resolve(URL)
  -> interactiveState{
       URL,
       Provider,
       Phase: running,
     }
  -> submitInteractivePipeline
  -> TaskEngine download stage
  -> Registry.Download
  -> registerRetainedAsset
  -> TaskEngine media=1 delivery
  -> current.Media().SendMedia(...)
  -> terminal delivered/failure status
~~~

For an extractor URL such as YouTube, urlResources requests:

~~~text
download=1
process=1
~~~

A source-level test exists:

~~~text
TestYTZUserbotURLCommandDeliversAfterDownloadResourcesRelease
~~~

That test proves resource separation, retained ownership, and reply/topic propagation with test doubles.

It does not prove real yt-dlp + real Telegram behavior.

---

## 11. OPEN RUNTIME DEFECT

Immediate next task:

~~~text
.download <youtube-url>
~~~

still errors in real use according to the user.

Therefore current status is:

~~~text
YT-Z architecture/source implementation   IMPLEMENTED
YT-Z live userbot YouTube URL acceptance  OPEN
~~~

Do not call the product path CLOSED until the real command succeeds.

The exact runtime error was not included in this handoff.

The next session must preserve and inspect the exact error/stderr.

---

## 12. Diagnose by stage

Do not patch multiple layers at once.

### Stage A — command parsing/provider resolution

Inspect:

~~~text
ctx.Args
targetURL
Registry.Resolve(targetURL)
provider.Name()
urlResources(targetURL)
~~~

Expected for YouTube:

~~~text
provider = extractor
resources = download=1 + process=1
~~~

### Stage B — TaskEngine admission

Verify:

- downloader scoped TaskClient is live;
- plugin generation is not closed;
- download pool exists;
- download/process capacities exist;
- task starts rather than only being admitted.

### Stage C — yt-dlp execution

Highest priority runtime evidence:

- yt-dlp binary path;
- yt-dlp version;
- exact argv;
- exit code;
- stderr;
- ffmpeg availability;
- whether direct yt-dlp outside Goultroid reproduces the failure.

Current download invocation is approximately:

~~~text
yt-dlp
  --no-playlist
  --no-warnings
  --max-filesize <cap>
  [selection args]
  -o <tmp>/%(title).150B.%(ext)s
  <URL>
~~~

Important mismatch:

~~~text
Search   uses --ignore-config
Download currently does not
~~~

Therefore host yt-dlp.conf can alter format, sidecars, cookies, output layout, thumbnails, subtitles, or other behavior.

Audit whether deterministic download should also use --ignore-config, but only after confirming evidence.

### Stage D — default format behavior

Userbot direct URL currently uses:

~~~text
MediaModeDefault
MediaFormatDefault
~~~

extractorSelectionArgs then adds no explicit -f.

Verify whether the installed yt-dlp:

- chooses split audio/video;
- requires ffmpeg merge;
- chooses WebM or another container;
- produces multiple files;
- behaves differently because of config.

Interactive inline flow avoids this ambiguity because Audio/Video and final format are explicit.

After root-cause confirmation, an acceptable product decision might be:

~~~text
.download YouTube URL
  -> deterministic default video format
~~~

or:

~~~text
.download YouTube URL
  -> enter the same Audio/Video selection flow
~~~

Do not choose before diagnosis.

### Stage E — current YouTube extractor restrictions

If stderr points to YouTube access:

inspect:

- 403;
- PO-token requirements;
- player-client selection;
- cookies/auth requirements;
- rate limiting/IP reputation;
- outdated yt-dlp.

Do not auto-import browser cookies.

Do not hard-code credentials/tokens.

If auth is necessary, add explicit configuration.

### Stage F — output-file discovery

ExtractorProvider.Download currently:

~~~text
os.ReadDir(tmpDir)
  -> choose first non-directory file
~~~

This can be brittle when sidecars exist.

Audit:

- .part;
- thumbnails;
- subtitles;
- metadata JSON;
- multi-stream intermediate outputs;
- host-config generated files.

If implicated, make final-output discovery deterministic.

### Stage G — storage metadata

Current extractor persistence primarily records the filename.

MIME may be empty.

With userbot default mode, delivery can classify the asset as generic file.

This may not be the current failure, but should be audited after the root cause is identified.

### Stage H — Telegram delivery

If yt-dlp succeeds, verify:

- asset exists;
- retained ownership registration succeeds;
- download/process leases are gone;
- media task acquires media=1;
- materialized file exists;
- file is regular and non-symlink;
- upload size is valid;
- detached core.Context retains correct Telegram service/peer/topic;
- SendMedia or SendMediaContext error is surfaced.

Always distinguish:

~~~text
download failed
~~~

from:

~~~text
download succeeded
Telegram delivery failed
~~~

---

## 13. Reproduction matrix

Use one known public YouTube URL.

Test separately:

~~~text
.download <youtube-url>
~~~

~~~text
inline: dl <same-url>
  -> Video
  -> MP4
~~~

~~~text
inline: yt <query>
  -> select same video
  -> Video
  -> MP4
~~~

This identifies whether the defect is:

- userbot-command-specific;
- generic extractor download;
- default-format-specific;
- delivery-specific.

Also capture:

~~~text
yt-dlp --version
ffmpeg -version
~~~

and compare a direct yt-dlp invocation using the same URL/output options.

---

## 14. Current acceptance evidence

Important regression files:

~~~text
internal/services/download/provider_extractor_search_test.go
internal/services/download/registry_search_test.go

plugins/downloader/youtube_search_test.go
plugins/downloader/interactive_test.go

plugins/downloader/delivery_test.go
plugins/downloader/delivery_completion_test.go
plugins/downloader/downloader_test.go
plugins/downloader/downloader_reply_regression_test.go
plugins/downloader/media_registry_test.go
~~~

Notable YT-Z tests:

~~~text
TestYTZZRetainedDeliveryUsesMediaResourceOnlyAndKeepsAsset
TestYTZZDeliveryFailureKeepsRetainedAsset
TestYTZPipelineReleasesDownloadResourcesBeforeMediaDelivery
TestYTZLifecycleCancellationDoesNotEmitPostDisableFailureUI
TestYTZUserbotURLCommandDeliversAfterDownloadResourcesRelease
~~~

Do not weaken these to make a fix pass.

Once the real root cause is known, add a regression that represents that exact failure.

---

## 15. Remaining YT work after the live defect

Do not assume all of these are required immediately.

Potential follow-ups after .download is proven working:

1. Better real-world yt-dlp diagnostics and typed failure classification.
2. Deterministic default format policy for userbot .download YouTube URLs.
3. Deterministic final-output discovery instead of first-file scanning.
4. MIME/media metadata enrichment for retained extractor assets.
5. Optional explicit YouTube auth/PO-token configuration if runtime evidence requires it.
6. Share UX, if product value justifies it.
7. Final YT closure doc after real command acceptance.

Do not add more providers until the existing YouTube path is reliable.

---

## 16. Working rules

- Refresh HEAD before every phase.
- Current source/tests outrank old memory.
- gofmt before every Go commit.
- Do not inspect CI unless explicitly requested.
- Prefer one focused commit per correctness boundary.
- No second downloader/registry/TaskEngine/RPC executor/uploader.
- No plugin-local retry/FloodWait mechanism.
- No unbounded maps/caches/cardinality.
- No permanent per-feature workers/tickers/pollers.
- Do not hold resources while waiting for UI input.
- Preserve actor/target/generation/revision callback ownership.

---

## 17. Recommended next-session execution order

~~~text
P0
refresh test-next and inspect drift

P1
reproduce .download <youtube-url>
capture exact error + yt-dlp stderr

P2
classify failure into Stage A-H

P3
write a regression for the verified root cause

P4
fix only the narrow canonical layer
  provider/extractor
  command/default-selection
  storage metadata/output discovery
  or presentation/Telegram delivery

P5
gofmt

P6
run targeted downloader tests
run go build -o bin/goultroid ./cmd/goultroid

P7
commit/push test-next

P8
do not inspect CI unless user asks

P9
rerun real .download <same-youtube-url>

P10
only after real success mark live userbot YouTube acceptance CLOSED
~~~

---

## 18. Key commit map

Visual/navigation work:

~~~text
dbb0e65a  feat(assistant): add bounded direct-grid help slots
94a4403f  perf(assistant): avoid duplicate help catalog sorting
45787b90  feat(assistant): add bounded direct-grid settings slots
~~~

Owner-bound button lifetime:

~~~text
6c40c895  fix(assistant): sustain owner-bound MyXL buttons safely
480dbd59  fix(myxl): keep sensitive screens on short leases
7e475e7d  feat(assistant): sustain safe owner-bound interaction buttons
0386a62c  docs(assistant): document owner-bound button lifetime
~~~

YouTube/search:

~~~text
b7a38284  feat(download): add bounded yt-dlp search foundation
fb860522  feat(downloader): add search-first YouTube inline UX
b3f27593  feat(downloader): close YouTube search lifecycle UX
~~~

YT-Z:

~~~text
f154f1cc  feat(downloader): deliver retained media to Telegram
cd246031  fix(downloader): harden retained media delivery lifecycle
83f46a7b  fix(downloader): keep YT-Z completion RPCs task-owned
e0b82e13  cleanup(downloader): remove metadata-only completion fallback
d307311d  fix(downloader): deliver userbot URL downloads to Telegram
~~~

Audited source snapshot:

~~~text
bb3af71bf8a2e21fc27f468bfab61cb4bd49f8e4
fix and format
~~~

Refresh before relying on any SHA as current.

---

## 19. Final state

~~~text
P1-P8 Assistant parity                    CLOSED
V2-V4 Ultroid-facing navigation           CLOSED
Owner-bound button lifetime               IMPLEMENTED

YT-A/YT-B search service                  CLOSED
YT-E..YT-I search-first inline UX         CLOSED
YT-K Search Again                         CLOSED
YT-N search lifecycle acceptance          CLOSED

YT-Z delivery architecture                IMPLEMENTED
YT-Z retained ownership/resource split    IMPLEMENTED
YT-Z shared Telegram delivery boundary    IMPLEMENTED
YT-Z userbot source path                  IMPLEMENTED

REAL .download <youtube-url> ACCEPTANCE    OPEN / REPORTED ERROR
~~~

The next AI should not design another YouTube downloader.

The immediate job is to identify why the existing canonical .download YouTube path fails in real use, fix the narrowest verified layer, and prove the same URL works end-to-end.
