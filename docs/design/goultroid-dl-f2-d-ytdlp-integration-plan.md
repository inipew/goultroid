# DL-F2-D — yt-dlp Cookie Integration Plan

## Status

- **Phase:** DL-F2-D (yt-dlp Integration)
- **Baseline HEAD:** `9a518403fcd4ef120e46b00095ea92e6c48a6081` (`test-next`)
- **Parent Plan:** `docs/design/goultroid-dl-f2-youtube-cookies-management-plan.md`
- **Audit Reference:** `docs/design/goultroid-dl-f2-a-youtube-cookies-audit-and-design.md`

---

## 1. Objectives

1. Pass configured YouTube cookies consistently to **YouTube Search**, **YouTube Probe**, and **YouTube Download**.
2. Use a unique, restricted (`0600`) ephemeral plaintext snapshot per operation under `p.files.TempDir()`.
3. Never write back `yt-dlp` cookie-jar mutations to the encrypted master (`yt_cookies.enc`).
4. Clean up ephemeral plaintext snapshots deterministically on success, error, timeout, cancellation, panic, and startup/lifecycle sweep.
5. Keep non-YouTube `ExtractorProvider` URLs and `DirectHTTPProvider` completely independent of YouTube cookies and `GOULTROID_YTDLP_COOKIES_KEY`.
6. Preserve existing `TaskEngine`, `process.Runner`, resource accounting, `mediaregistry`, `storage`, and `RPCExecutor` boundaries.

---

## 2. Design & File Changes

### 2.1 `internal/services/download` (Generic Service Layer)

- `types.go`: Add `CookiesFile string` to `SearchOptions`, `ProbeOptions`, and `DownloadOptions`.
- `provider_extractor.go`:
  - `Search`: when `strings.TrimSpace(opts.CookiesFile) != ""`, append `"--cookies", strings.TrimSpace(opts.CookiesFile)` before the `ytsearchN:` query argument.
  - `Download`: when `strings.TrimSpace(opts.CookiesFile) != ""`, append `"--cookies", strings.TrimSpace(opts.CookiesFile)` before `selectionArgs` / `-o` / URL.
- `provider_extractor_probe.go`:
  - `Probe`: when `strings.TrimSpace(opts.CookiesFile) != ""`, append `"--cookies", strings.TrimSpace(opts.CookiesFile)` before `parsedURL.String()`.
- `DirectHTTPProvider` remains untouched and ignores `CookiesFile`.

### 2.2 `plugins/downloader` (YouTube Policy & Snapshot Lifecycle)

- `downloader.go`:
  - Store `cookieStore *youtubeCookieStore` on `Plugin` (initialized in `InitPlugin` alongside `cookieSink`, plus startup sweep of stale `ytdlp-cookies-*.txt` files in `p.files.TempDir()`).
- `youtube_cookies_snapshot.go` (or in `youtube_cookies_store.go`):
  - `isYouTubeURL(rawURL string) bool`: parses URL and checks `isYouTubeHost(parsed.Hostname())`.
  - `acquireYouTubeCookieSnapshot(ctx context.Context) (path string, cleanup func(), err error)`:
    - If `p.cookieStore == nil` or `p.files == nil`: returns `"", noop, nil` (fallback when no store is configured).
    - Calls `p.cookieStore.LoadDecryptedCookies(ctx)`:
      - `errCookieNotFound`: returns `"", noop, nil` (no cookies configured -> normal unauthenticated fallback).
      - Any other error (`errCookieKeyMissing`, `errCookieKeyInvalid`, `errCookieDecryptFail`, `errCookieCorrupt`): fails closed with `fmt.Errorf("%w: ...", core.ErrUnavailable)` without leaking secrets or paths.
    - Creates a unique file via `p.files.CreateTempFile("ytdlp-cookies-*.txt")`, enforces `0600`, writes plaintext, zeroes the in-memory plaintext slice via `defer clear(plaintext)`, closes the file, and returns `tmpPath` + `cleanup` (`p.files.RemoveTempFile(tmpPath)`).
- Call sites:
  1. `youtube_search.go` (`searchYouTube`): acquires snapshot inside TaskEngine handler with `defer cleanup()`, passes `CookiesFile`.
  2. `interactive.go` (`actionVideo`): acquires snapshot only when `isYouTubeURL(state.URL)` with `defer cleanup()`, passes `CookiesFile`.
  3. `url_pipeline.go` (`submitURLPipeline`): acquires snapshot inside TaskEngine handler only when `isYouTubeURL(request.URL)` with `defer cleanup()`, passes `CookiesFile`.

---

## 3. Verification Matrix & Evidence

- `internal/services/download` (`provider_extractor_cookies_test.go`):
  - `Search`, `Probe`, and `Download` include `["--cookies", path]` when `CookiesFile` is set, and omit `--cookies` when empty.
- `plugins/downloader` (`youtube_cookies_integration_test.go`):
  - YouTube Search, Probe (`handleInteractiveAction` -> `actionVideo`), and Download (`submitURLPipeline`) receive unique `0600` per-operation snapshot paths.
  - Concurrent YouTube Probe, Download, and Search operations receive distinct snapshot paths (no shared plaintext file race) and never mutate `yt_cookies.enc`.
  - Non-YouTube extractor URLs (e.g. TikTok) and DirectHTTP URLs do not trigger `LoadDecryptedCookies` and succeed even when `GOULTROID_YTDLP_COOKIES_KEY` is missing.
  - Absent cookies (`errCookieNotFound`) preserve unauthenticated fallback.
  - Configured-but-unreadable cookies (missing key / corrupt ciphertext) fail closed with `core.ErrUnavailable` (`errCatUnavailable`).
  - Temp snapshot files are removed on success, process failure, timeout, cancellation, and startup sweep.
  - Snapshot load/cleanup logs emit sanitized categories only and do not expose filesystem paths or secrets.

### 3.1 Preserved Boundaries & Remaining Notes

- `errCookieDirSyncUnconfirmed` semantics from DL-F2-C remain intact: replacement is already active when directory sync is unconfirmed.
- `Dockerfile` currently installs `yt-dlp` via `releases/latest/download/yt-dlp`; pinning the deployment `yt-dlp` release tag remains an operational item for DL-F2-E closure.
