# DL-A — Downloader & YouTube Current-Source Inventory (Read-Only)

## Status

- Status: **DL-A Complete (Read-Only Inventory)**
- Workstream: P1 (Downloader + YouTube Domain Separation) of `docs/design/goultroid-existing-plugin-enhancement-roadmap.md`
- Branch: `test-next`
- Baseline HEAD: `3866097005081e1b4bef9bddd871b85d649c17e7`
- Scope: read-only source inventory. No production code, API, or refactoring was applied. Next step is **DL-B — Downloader Domain Separation Design**.

---

## 1. Entry Points Inventory

| Surface | Entry / Symbol | Source | Current Behavior | Domain |
|---|---|---|---|---|
| Userbot Command | `.download` / `.dl` (`Plugin.Commands`) | `plugins/downloader/downloader.go:123-140, 227-294` | URL arg → `handleURLDownload`; else replied/current Telegram media → `submitContinuation` (`executeMediaDownload`). Note: no standalone `.yt` command exists in `Commands()` yet. | Mixed (Generic + Extractor URLs) |
| Inline Query (`dl`) | `@bot dl <url>` / raw URL (`interactiveInlineHandler.HandleInline`) | `plugins/downloader/interactive.go:614-640` | Resolves provider (`http` or `extractor`), probes formats if `extractor`, opens `phaseChoose` a2/inline session. | Mixed (Generic + Extractor) |
| Inline Query (`yt`) | `@bot yt <query>` (`isYouTubeSearchQuery` → `handleYouTubeSearch`) | `plugins/downloader/youtube_search.go:34-37, 108-172` | Submits `youtube-search` task (`3s` timeout) to `TaskEngine`, calls `registry.Search("extractor", query, ...)`, renders YouTube-only inline results (`yt_<SourceID>`). | YouTube-specific |
| Assistant / a2 Actions | `interactiveActions` (`select_audio`, `select_video`, `format_*`, `video_*`, `download_file`, `back`, `retry`, `cancel`) | `plugins/downloader/interactive.go:30-67, 155-303` | `BindAssistant` registers `RegisterPreparedAction` / `RegisterAction` under `FeatureSpec().ID == "downloader"` (`SudoPolicy(SurfaceInline)`). | Mixed (Generic + Extractor/YouTube) |

---

## 2. Module, Registration & Capability Wiring

- **Module definition:** `plugins/downloader/module.go` (`ModuleType`, ID `"downloader"`, Version `"1.0.0"`).
- **Generated registration:** `internal/app/generated_modules.go:15, 51` (`downloader.Module`).
- **Module capabilities:** `CapHTTP`, `CapFilesystemTemp`, `CapFilesystemData`, `CapProcessExecute`, `CapTelegramRead`, `CapTelegramSendMessage`, `CapTasks`.
- **Privileged capability gate:** `internal/app/wiring_core.go:143` explicitly grants `capGate.AllowPrivileged("downloader", plugin.CapProcessExecute)`.
- **Shared services injected via `module.Runtime`:** `rt.DownloadRegistry`, `rt.Storage`, `mediaregistry.New(rt.DB)`, `rt.SettingsService`.
- **Clean split readiness:** `module.Runtime` already provides all shared services (`DownloadRegistry`, `Storage`, `DB`, `SettingsService`) needed by a separate YouTube `module.Module` without duplicating infrastructure, provided `wiring_core.go` capability gating (`CapProcessExecute`) and `tools/featuregen` registration are updated in DL-B/implementation.

---

## 3. Provider Layer (`internal/services/download/`)

- **Registry (`registry.go:16-90`):** Wired in `internal/app/wiring_services.go:106-109` with `[ExtractorProvider, DirectHTTPProvider]` in priority order.
- **`ExtractorProvider` (`provider_extractor.go`):**
  - `Match(rawURL)` matches `extractorDomains` (`youtube.com`, `youtu.be`, `instagram.com`, `twitter.com`, `x.com`, `tiktok.com`, `pinterest.com`, `pin.it`, `reddit.com`, `facebook.com`, `fb.watch`, `vimeo.com`, `soundcloud.com`, `twitch.tv`).
  - Implements `Provider`, `ProbeProvider`, and `SearchProvider` (`yt-dlp` via `process.NewOSRunner(3, 5m, 4MiB)` in `wiring_services.go:101`).
- **`DirectHTTPProvider` (`provider_http.go`) & `SafeDownloader` (`client.go`):**
  - Matches any valid `http://` or `https://` URL not matched by `ExtractorProvider`.
  - Enforces `ValidateURL`, max 5 redirects, `Dialer.Control` SSRF check, `500 MiB` max bytes, `5m` timeout.

---

## 4. Execution, Retained Media & Delivery (`plugins/downloader/`)

- **URL Pipeline (`url_pipeline.go`):** Shared by native URL (`url_native.go`) and interactive/YouTube (`interactive.go`, `delivery.go:153`). Submits `download` task (`Pool: "download"`, `{download: 1}` + `{process: 1}` if `extractor`, timeout `10m`) with `defer reporter.Close()`.
- **Progress (`progress.go`):** `downloadProgressReporter` (`2s` interval, `8s` edit timeout, lock-free `Callback`).
- **Retained Media (`media_registry.go`):** Registers downloaded `storage.Asset` under `Owner: "downloader"`, `Lifecycle: LifecycleRetained`, `Producer: "downloader.<provider>"` (or `"downloader.telegram"`), with `store.Delete` rollback on registration failure.
- **Delivery (`delivery.go`):** `OnComplete` submits `delivery` task (`Pool: "general"`, `{media: 1}`, timeout `30m`), materializes regular file or temp copy (`<= 500 MiB`), and sends via `core.Context.SendMedia` (native) or `orchestration.MediaDelivery` (inline/a2) → `telegram.Service.SendMedia` → shared `RPCExecutor`.

---

## 5. Domain Separation Assessment (Answers to Roadmap §4.1 Questions)

1. **Who classifies HTTP vs Extractor vs YouTube?**
   - `download.Registry.Resolve` classifies `extractor` vs `http` by hostname (`extractorDomains`).
   - `plugins/downloader/youtube_search.go` classifies `yt <query>` inline search and filters `SearchResult` (`Provider == "extractor" && Source == "youtube"`).
2. **Does `plugins/downloader` own both generic and YouTube state?**
   - **Yes.** `Plugin` owns generic Telegram media/URL downloads, extractor interactive menus, and YouTube inline search/selection in one package (`plugins/downloader`).
3. **What can move behind a YouTube feature boundary without duplicating provider logic?**
   - `youtube_search.go` (`isYouTubeSearchQuery`, `searchYouTube`, `handleYouTubeSearch`, `youtubeInlineResult`, `youtubeSearchChoiceView`, `youtubeSearchDescription`), a dedicated `.yt` command, and YouTube-specific format/quality UX — while **reusing** `internal/services/download` (`Registry`, `ExtractorProvider`), `url_pipeline`/`delivery` (or shared download-delivery helper), `TaskEngine`, `mediaregistry`, and `RPCExecutor`.
4. **Does module registration support a separate YouTube module?**
   - **Yes**, via `module.Module` + `tools/featuregen`, subject to capability gating (`CapProcessExecute` in `internal/app/wiring_core.go`) and inline/a2 token backward compatibility.

---

## 6. Test Coverage Snapshot (Inherited from P2 Closure)
- `plugins/downloader/...`, `internal/services/download/...`, `internal/services/mediaregistry/...`, `internal/services/storage/...` all pass `-count=1 -race` (verified in P2-C `0d30a194`).

---

## 7. Next Step
Proceed to **DL-B — Downloader Domain Separation Design** (separate design document before any code refactoring).
