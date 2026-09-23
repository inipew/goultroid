# Assistant Parity P8-E — representative interactive downloader workflow

## Status

**P8-E is CLOSED for implementation/source acceptance.**

Baseline:

`ab6f98841a0b92062a785e3d2ececd84fe764727` — P8-D rich Wikipedia inline lookup.

Representative feature:

`plugins/downloader`

## Objective

P8-E proves that a callback-driven heavy Assistant workflow can reuse the existing Goultroid downloader, bounded interaction runtime, shared TaskEngine, shared process resource, and retained-media ownership without introducing an Assistant-only downloader engine.

The parity target is the Ultroid-style UX contract:

```text
inline source
    ↓
choose audio/video or direct file
    ↓
choose bounded output format
    ↓
TaskEngine admission
    ↓
download resource
    ├─ direct HTTP: no process resource
    └─ extractor: process resource
    ↓
retained asset
    ↓
interaction completion
```

## Canonical production flow

The inline entry point is:

```text
dl <https://media-url>
```

Direct HTTP sources receive:

- Download file
- Cancel

Extractor-backed sources receive:

- Audio → M4A / MP3
- Video → MP4 / Best
- Cancel

All buttons are typed a2 actions. No downloader-owned callback protocol or global callback map exists.

## TaskEngine and resource admission

Lightweight selection actions use the normal orchestration action path.

Final download actions use:

`orchestration.Engine.RegisterPreparedAction`

The preparation step validates the current bounded session state, resolves the current downloader provider, and publishes dynamic TaskEngine resources before execution.

Resource ownership remains:

| Source | Resources |
| --- | --- |
| Direct HTTP | `download:1` |
| Extractor | `download:1`, `process:1` |

The existing extractor checks `tasks.HasHeldResource(ctx, "process")`; therefore an interactive extractor task that already owns `process` does not create nested process admission.

The callback task is generation-scoped to the downloader feature, so plugin disable/reload can invalidate queued or active generation-owned work through the existing TaskEngine/session lifecycle.

## Typed format selection

The shared download service gains a small bounded vocabulary:

```text
MediaMode:
  default
  audio
  video

MediaFormat:
  default
  best
  m4a
  mp3
  mp4
```

The extractor maps those values to fixed yt-dlp argument sets. Feature input cannot supply arbitrary extractor arguments.

Supported combinations:

- audio + M4A
- audio + MP3
- video + MP4
- video + Best
- default legacy extraction

Unsupported combinations fail closed with `core.ErrInvalidArgs`.

## Bounds

P8-E introduces these explicit bounds:

```text
URL bytes             <= 2048
serialized state      <= 2304 bytes
interaction TTL       = 15 minutes
inline results        = 1
inline cache          = none
inline visibility     = private
plugin-owned workers  = 0
plugin-owned tickers  = 0
plugin-owned maps     = 0
```

Only absolute HTTP(S) URLs are accepted.

The inline matcher accepts only `dl` or `dl <url>`; prefix collisions such as `dlfoo` do not match.

## Cancellation and lifecycle

The running operation inherits the interaction/session context through the prepared callback dispatch path.

Consequences:

- session cancellation propagates to the heavy download context;
- plugin generation invalidation cannot dispatch an old prepared callback;
- old callback revisions remain stale through the existing a2 runtime;
- extractor temporary directories keep their existing deferred cleanup;
- no permanent downloader worker is introduced.

The running view keeps a typed Cancel action. Selecting it cancels the bounded interaction session and therefore the session-owned action context.

## Progress behavior

P8-E deliberately uses coarse stage progress rather than high-frequency Telegram edits:

```text
selection
→ TaskEngine admitted / downloading
→ complete | failed | cancelled
```

This avoids turning byte-level progress callbacks into Telegram RPC amplification.

## Media ownership and delivery boundary

Downloaded URL assets continue to use the existing canonical downloader retention path and are registered in the shared media ownership registry.

The current canonical URL downloader retains the asset and reports its path; it does not yet upload that retained URL asset back into Telegram as a second step. P8-E does **not** create a separate Assistant uploader merely to imitate Ultroid.

That intentional behavioral difference must remain visible in the P8-F final parity matrix. If product parity later requires automatic Telegram delivery, it should be added once at the canonical downloader/media-delivery boundary rather than as Assistant-only logic.

## Acceptance coverage

P8-E adds tests for:

- FeatureSpec inline/action declarations;
- private `CacheNone` interactive result;
- direct HTTP versus extractor first-step UX;
- audio/video format selection;
- URL/state hard bounds;
- inline keyword-boundary matching;
- dynamic prepared-action resources;
- direct HTTP holding download only;
- extractor holding download + process;
- typed extractor option mapping;
- rejection of invalid mode/format combinations;
- no second worker/runtime/cache architecture.

Architecture fence:

`internal/architecture/assistant_p8e_test.go`

## Non-goals

P8-E does not:

- create an AssistantDownloader or YouTubeDownloader subsystem;
- add another TaskEngine or process scheduler;
- add another Telegram RPC/retry/FloodWait authority;
- add byte-level Telegram progress spam;
- create per-user/per-chat downloader workers;
- implement every Ultroid provider;
- inspect CI.

## Formatting rule

All Go source changed for P8-E was passed through `gofmt` immediately before commit preparation.

## Next phase

Proceed to **P8-F — complete behavioral matrix + remaining MUST UX residuals**, including canonical locale-aware Assistant UI and explicit recording of intentional downloader delivery differences.
