# DL-F3 — Production Runtime Validation & Operational Security Audit Plan

## Status

- **Phase:** DL-F3 (Production Runtime Validation & Operational Security Audit — Plan)
- **Baseline HEAD:** `bd002cfc4bbd99cd841f4be836f1ea192159355b` (`test-next`)
- **Parent Workstream:** `docs/design/goultroid-dl-f2-youtube-cookies-management-plan.md` (DL-F2-A..E `CLOSED / APPROVED`)
- **Production Code Changes:** None (validation & operational audit plan only)

---

## 1. Objective

Validate the end-to-end runtime behavior, operational security, and failure handling of the DL-F2 YouTube cookies subsystem in a deployed Goultroid environment before expanding Downloader features.

---

## 2. Pre-Validation Environment Checklist

1. **Build & Deployment:**
   - Binary/container is built from `test-next` commit `bd002cfc4bbd99cd841f4be836f1ea192159355b` or newer.
   - `yt-dlp` is installed and executable in `$PATH` (record exact `yt-dlp --version`).
2. **Configuration & Secrets:**
   - `OWNER_ID` in deployment `.env` matches the numeric Telegram user ID of the logged-in userbot account.
   - `GOULTROID_YTDLP_COOKIES_KEY` is provisioned as a 32-byte key (base64 or 64-char hex, e.g. `openssl rand -base64 32`) via deployment secret, outside Git.
   - `LOG_LEVEL=info` (or `debug` during validation, then restored to `info`).
3. **Clean Initial State:**
   - Confirm `.ytcookies status` in Saved Messages reports `not configured` before uploading.

### 2.1 Operator Credential Hygiene & Sign-Off Rules

Because Telegram message deletion does not erase prior copies on the operator's device or Telegram infrastructure:

1. **Dedicated test session:** Export cookies from a dedicated test YouTube account or isolated browser session/profile whenever possible, rather than a primary daily-driver browser session.
2. **Safe local handling:** Never place exported `cookies.txt` files inside the Git repository, shared directories, or auto-synced/backed-up folders.
3. **Post-validation cleanup:** Securely delete the plaintext exported `cookies.txt` from the operator's machine as soon as validation finishes.
4. **Session revocation:** Sign out / revoke the test YouTube session after validation completes so exported credentials do not remain active.
5. **Sanitized evidence only:** Never paste cookie values, `GOULTROID_YTDLP_COOKIES_KEY`, or raw unsanitized logs into the sign-off matrix or reports; record only sanitized pass/fail observations.

---

## 3. Runtime Validation Scenarios

### Scenario 1 — Saved Messages Upload & Persist-Before-Delete

- **Steps:**
  1. Export a fresh Netscape `cookies.txt` from a signed-in YouTube browser session.
  2. Upload `cookies.txt` to **Saved Messages** and reply with `.ytcookies` (and repeat with `.ytcookies set` or document caption).
  3. Also test uploading a non-auth/consent-only file or invalid format, and test running `.ytcookies status` from a group or another user's private DM.
- **Expected Results:**
  - Valid upload in Saved Messages responds with `YouTube cookies validated and accepted` and deletes both the uploaded document message and the command message.
  - Invalid/consent-only upload is rejected with a sanitized error and never replaces active cookies.
  - Non-Saved-Messages invocations (group/supergroup/other user's DM) are rejected before media download.
  - `data/plugins/downloader/yt_cookies.enc` exists with `0600` permissions inside `0700` directory, and `data/tmp/plugins/downloader/` has zero leftover `ytcookies-ingest-*` directories.

### Scenario 2 — `.ytcookies status`, `.ytcookies delete`, and Replacement

- **Steps:**
  1. Run `.ytcookies status` after valid upload.
  2. Upload a replacement `cookies.txt` (verify atomic replacement) and run `.ytcookies status`.
  3. Run `.ytcookies delete`, then run `.ytcookies status` and `.ytcookies delete` again.
  4. Re-upload valid `cookies.txt` for subsequent scenarios.
- **Expected Results:**
  - `status` reports `configured and decryptable` (without exposing keys, paths, or cookie values).
  - First `delete` removes `yt_cookies.enc` and reports `Stored YouTube cookies deleted`.
  - Subsequent `status` reports `not configured`; second `delete` reports `No stored YouTube cookies to delete`.

### Scenario 3 — Authenticated YouTube Search, Probe, and Download

- **Steps:**
  1. Run `.yt <query>` and inline `@bot yt <query>`.
  2. Run `.dl <youtube-url>` (or `.yt <youtube-url>`), click **Video**, observe quality selection buttons, and download a video quality.
  3. Download an **Audio** format (`m4a`/`mp3`).
- **Expected Results:**
  - Probe succeeds and displays canonical MP4 quality buttons (no `Sign in to confirm you're not a bot` error when cookies are valid).
  - Search, Probe, and Download complete normally.
  - `data/tmp/plugins/downloader/` contains zero leftover `ytdlp-cookies-*.txt` files after operations finish.

### Scenario 4 — Concurrent `yt-dlp` Operations & Snapshot Isolation

- **Steps:**
  1. Trigger two or more YouTube Probe/Download operations concurrently (e.g. `.dl <url1>` Video and `.dl <url2>` Video simultaneously).
- **Expected Results:**
  - Both operations complete without file-lock errors or cookie-jar corruption.
  - `yt_cookies.enc` mtime/contents are unchanged by `yt-dlp` execution (no write-back).
  - `data/tmp/plugins/downloader/` is empty after all operations finish.

### Scenario 5 — Restart Persistence

- **Steps:**
  1. Restart the Goultroid process/container with the same `GOULTROID_YTDLP_COOKIES_KEY` and persistent `/app/data` volume.
  2. Run `.ytcookies status` in Saved Messages and run a YouTube Probe.
- **Expected Results:**
  - `.ytcookies status` immediately reports `configured and decryptable`.
  - YouTube Probe uses the persisted encrypted cookies without requiring re-upload.

### Scenario 6 — Log, Error & Diagnostic Security Audit

- **Steps:**
  1. Inspect container/application logs produced across Scenarios 1–5 (including an intentional failed probe or invalid upload).
  2. Inspect Telegram messages and `.health` / diagnostics output.
- **Expected Results:**
  - Zero occurrences of: `GOULTROID_YTDLP_COOKIES_KEY`, cookie names/values (`SID`, `LOGIN_INFO`, `__Secure-1PSID`, etc.), `yt_cookies.enc` path, `ytdlp-cookies-*` path, `ytcookies-ingest-*` path, or raw `yt-dlp` stderr in Telegram messages or sanitized probe failure logs.

### Scenario 7 — Key Misconfiguration & Rotation Handling

- **Steps:**
  1. Stop Goultroid, unset `GOULTROID_YTDLP_COOKIES_KEY`, and restart.
     - Check `.ytcookies status` in Saved Messages.
     - Test YouTube `.dl <youtube-url>` -> Video.
     - Test non-YouTube/DirectHTTP `.dl <direct-https-url>`.
  2. Stop Goultroid, set a wrong/different `GOULTROID_YTDLP_COOKIES_KEY`, and restart.
     - Check `.ytcookies status` and test `.dl <youtube-url>` -> Video.
  3. Restore the original `GOULTROID_YTDLP_COOKIES_KEY` and restart.
- **Expected Results:**
  - Missing key: `.ytcookies status` reports `encryption key is missing or invalid`; YouTube fails closed with `unavailable`; DirectHTTP and non-YouTube downloads continue working normally; `yt_cookies.enc` is **not** overwritten or deleted.
  - Wrong key: `.ytcookies status` reports `corrupt` / unreadable; YouTube fails closed; restoring the original key recovers the intact credential set.

---

## 4. Validation Sign-Off Matrix

| Scenario | Status | Operator Notes / Evidence |
|---|---|---|
| 1. Saved Messages upload & delete | PENDING | |
| 2. `.ytcookies status` / `delete` / replace | PENDING | |
| 3. Search, Probe & Download with cookies | PENDING | |
| 4. Concurrent `yt-dlp` snapshot isolation | PENDING | |
| 5. Restart persistence | PENDING | |
| 6. Log, error & diagnostic secret audit | PENDING | |
| 7. Missing / wrong / rotated key handling | PENDING | |
