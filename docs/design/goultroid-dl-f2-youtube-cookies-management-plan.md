# DL-F2 — YouTube Cookies Management via Telegram

## Status

- Status: CLOSED / APPROVED (DL-F2-A..E complete; no further implementation phases open).
- Repository: inipew/goultroid
- Target branch: test-next
- Planning baseline HEAD: ed1dac7b4502463c9c24c3d3b2f9530cf571528b
- DL-F2-A closure HEAD: 74c6d4e59351f9199f31c6fe9b6361f90a7270da
- Parent workstream: Downloader / YouTube enhancement (DL-F)
- Scope: securely ingest, validate, protect, use, replace, and delete a user-supplied YouTube cookies file through the existing Downloader plugin.

This plan records the requested behavior, goals, constraints, phases, and acceptance criteria. Detailed current-source audit decisions are finalized in `docs/design/goultroid-dl-f2-a-youtube-cookies-audit-and-design.md`.

## 1. Background

The YouTube extraction path can fail before format/quality selection when YouTube requires an authenticated session. The observed yt-dlp diagnostic includes a sign-in / bot-verification challenge and recommends browser cookies or another supported authentication method.

The shared ExtractorProvider serves more than YouTube. It is shared download infrastructure and must not become a YouTube-only subsystem. Authentication policy should be scoped to the YouTube feature while preserving the existing general Downloader and shared provider boundaries.

Requested operating model:

1. The owner uploads a browser-exported cookies.txt as a Telegram document in a private conversation.
2. Downloader receives the document through its existing Telegram/plugin integration.
3. Downloader validates the file before accepting it.
4. Downloader stores accepted cookies encrypted at rest, using a key supplied through deployment secrets rather than committed configuration.
5. After processing, Downloader attempts to delete the uploaded Telegram message and removes every local plaintext staging copy.
6. YouTube Search, Probe, and Download use the configured cookies consistently.
7. The owner can inspect configuration status and explicitly replace or delete cookies without exposing their contents.

This is an authentication aid, not a guarantee that every YouTube restriction can be bypassed. Google/YouTube may invalidate sessions, require additional verification, or reject an exported cookie set.

## 2. Goals

- Configure YouTube authentication without shell access or manual server file placement.
- Use Telegram document upload as the first-iteration ingestion surface.
- Restrict ingestion and management to the configured owner in a private chat only.
- Validate format and YouTube relevance before replacing a working credential set.
- Never persist accepted cookies in plaintext at rest.
- Keep the encryption key outside Git, ordinary application configuration, Telegram messages, and logs.
- Delete the incoming Telegram message after processing and attempt cleanup on failure paths where safe.
- Remove plaintext temporary files promptly and deterministically.
- Use one consistent credential source for YouTube search, probe, and download.
- Preserve .yt, .dl, .download, inline, and a2 behavior when cookies are absent.
- Keep general non-YouTube downloads independent from YouTube authentication.
- Keep memory, disk usage, process concurrency, and credential lifecycle bounded.

## 3. Explicit non-goals

- Implementing Google login, OAuth, browser automation, or password collection inside Goultroid.
- Asking the owner to send a Google password, 2FA code, session token, or cookie values as command arguments or chat text.
- Automatically scraping browser profiles from the server.
- Supporting arbitrary cookie-management UI or multiple named accounts in the first iteration.
- Promising that deleting a Telegram message immediately erases Telegram-side caches, backups, or prior copies.
- Changing the shared download architecture or creating a second downloader runtime.
- Adding a worker pool, scheduler, retry engine, provider registry, media registry, callback protocol, or persistent job engine.
- Changing non-YouTube extractor behavior unless required by a verified shared-provider contract.
- Logging or displaying cookie contents for debugging.

## 4. Security and trust model

### 4.1 Authorized upload

- Accept the cookie document only in Saved Messages / self-chat with the configured owner identity (`ctx.Perms != nil && ctx.Perms.OwnerID != 0`, `ctx.IsOwner()`, `ctx.IsPrivate()`, `ctx.ChatID() == ctx.Perms.OwnerID`, and `ctx.PeerID` is `*tg.InputPeerSelf` or `*tg.InputPeerUser{UserID: OwnerID}`).
- Reject group, supergroup, channel, another user's private DM (even for outgoing owner commands), anonymous, and forwarded-upload paths.
- Enforce authorization in strict order:
  1. Self-chat numeric gate (`isAuthorizedCookieSelfChat(ctx)`) before `GetReply()`.
  2. Reply sender verification (`replied.IsOutgoing` or `replied.SenderID != 0 && replied.SenderID == OwnerID`) after `GetReply()` and before media download.
  3. Media type (`document`) and size (`<= 1 MiB`) validation before `DownloadMedia()`.
- Do not infer ownership from filename, caption, username, display name, or `SenderID == 0` without `IsOutgoing == true`.
- Accept a Telegram document attachment, not a path, URL, command argument, or pasted cookie text.
- A replacement becomes active only after complete validation and durable encrypted persistence.

### 4.2 Telegram transport caveat

The upload travels through Telegram. Deleting the message from the conversation is a best-effort reduction of exposure, not a cryptographic guarantee that Telegram infrastructure, client caches, notifications, or prior copies have been erased. The UI and documentation must not promise absolute deletion.

Use the existing supported Telegram message-deletion path and verify behavior for the actual account/client type and private-chat direction. If deletion fails, report that cleanup could not be confirmed and do not claim the message was removed. Never print or quote file contents in that warning.

### 4.3 File validation

Treat the uploaded file as untrusted input:

- Enforce a conservative maximum byte size before and during download/materialization.
- Enforce bounded line count and individual line length.
- Parse Netscape HTTP Cookie File format; filename extension alone is not validation.
- Reject malformed records, unsupported formats, empty files, and files with no usable YouTube-domain authentication cookies.
- Validate domain scoping, `(cookie-name, domain)` relevance pairs (do not treat two independent allowlists as sufficient, e.g., `SID` on `youtu.be` is not valid auth evidence), and cookie attributes without logging values.
- Handle expiry metadata conservatively; syntactic validity does not prove current authentication.
- Reject path-like names and never use the supplied filename as a filesystem path.
- Never execute, source, or interpret the upload as code.
- Sanitize parser errors; never echo offending lines or cookie values.

The detailed design must define accepted domains and the minimum evidence that makes a file YouTube-relevant. Avoid overfitting to a single cookie name if YouTube changes requirements.

### 4.4 Encryption and filesystem protection

- Encrypt accepted cookie payloads at rest with authenticated encryption such as AES-256-GCM.
- Use a fresh cryptographically secure nonce for every encryption.
- Bind a versioned format header as authenticated data.
- Obtain the encryption key from a dedicated deployment secret; never generate a new key on every restart.
- Do not store the key beside the ciphertext or in the repository.
- Fail closed when the key is absent, malformed, or cannot decrypt existing data. Never silently overwrite or discard existing credentials.
- Write replacements atomically: validate, encrypt, write a restricted temporary file, sync as appropriate, then atomically replace the active ciphertext.
- Restrict directory and file permissions to the service account (for example, 0700 directory and 0600 file where supported).
- Keep storage within existing application data/config ownership; do not create a general-purpose secret store.
- Document backup/restore: ciphertext backups are unusable without the separately managed key.
- Status output must never include key, nonce, ciphertext, cookie names/values, or filesystem path.

The implementation design must fix the cipher envelope, key encoding, file format, and secret name before coding and cover them with tests.

### 4.5 Plaintext lifetime and cleanup

For Telegram ingestion:

1. Download to a uniquely named, restricted temporary file.
2. Validate within configured size/time limits.
3. Encrypt and durably persist the accepted data atomically.
4. Remove plaintext staging files on success, validation failure, cancellation, panic, and shutdown cleanup.
5. Attempt Telegram message deletion after safe acceptance; also attempt deletion on rejected credential uploads where safe and supported.
6. Return a sanitized result distinguishing accepted-and-cleaned, accepted-but-message-delete-unconfirmed, rejected-and-cleaned, and cleanup-incomplete.

For yt-dlp execution:

- Decrypt only when a YouTube operation actually needs authentication.
- Use a restricted per-operation plaintext working copy if required by yt-dlp.
- Remove it after process exit, cancellation, timeout, and failure.
- Never put cookie contents in command-line arguments; pass only the protected file path through yt-dlp's supported cookies-file option.
- Ensure process arguments and logs do not expose cookie contents.
- Verify whether the pinned yt-dlp version mutates/rewrites its cookie file before deciding concurrency semantics.

### 4.6 Cookie concurrency and write-back

This is a required design decision, not an implementation detail to guess.

yt-dlp may update a cookies file while using it. Sharing one decrypted file across concurrent processes can cause races or corruption. Independent snapshots avoid concurrent writes to the same plaintext file but can lose updates if snapshots are later merged/promoted without version control.

Before implementation, inspect the pinned yt-dlp version and its documented/runtime behavior, then choose and test one bounded strategy:

- serialize cookie-bearing yt-dlp invocations using an existing managed process/resource limit; or
- use independent per-operation snapshots with an explicit safe versioned write-back/merge policy; or
- use read-only snapshots if the pinned invocation mode can reliably prevent write-back without breaking authentication.

Do not hold a global application lock around unrelated downloads or Telegram delivery. If serialization is necessary, scope it only to the authenticated yt-dlp process segment and account for TaskEngine worker/resource fairness. Do not add a new worker pool or execution engine.

## 5. User-facing behavior

### 5.1 Upload flow

- Owner sends a cookies.txt document in the private chat with the userbot.
- Downloader recognizes it only through an explicit, unambiguous upload flow.
- Bot replies with a short processing status and never quotes or previews file content.
- After validation and encrypted persistence, bot confirms that YouTube cookies are configured.
- Incoming Telegram message is deleted automatically when possible.
- If deletion cannot be confirmed, report that local processing completed but Telegram message deletion could not be confirmed.
- If validation or persistence fails, the previous active credential set remains intact.
- Local plaintext staging files are cleaned regardless of outcome.

Do not silently treat every arbitrary document sent to the bot as cookies. Define a deliberate trigger (for example, a documented command/reply flow or narrowly scoped upload mode) that does not interfere with other document handlers.

### 5.2 Minimal management surface

- .ytcookies status — report configured/not configured and safe local health state only.
- .ytcookies delete — remove active encrypted credentials, with owner confirmation if supported by the existing interaction model.
- Uploading a new valid document — atomically replace the current credential set.

A separate .ytcookies set command is unnecessary if document upload is already explicit. Do not add a broad menu or account manager without demonstrated need.

Status should distinguish:
- not configured;
- configured and decryptable;
- configured but unavailable because encryption key is missing/invalid;
- configured but unreadable/corrupt;
- cleanup requiring operator attention, if durable and useful.

Never expose cookie metadata that could fingerprint the browser session.

### 5.3 Failure messages

Use the existing Downloader localization mechanism (English and Indonesian where supported) and stable sanitized categories. Distinguish owner/private-chat requirements, wrong media type, size limit, malformed/unsupported file, no usable YouTube cookies, missing encryption configuration, secure-storage failure, Telegram deletion unconfirmed, and YouTube still requiring verification.

Never expose raw yt-dlp stderr, local paths, process command lines, cookie names/values, encryption internals, or stack traces to users.

## 6. Architecture and ownership constraints

- YouTube feature owns policy for whether a YouTube operation may use configured YouTube cookies.
- Downloader plugin owns Telegram upload and user-facing management, using existing plugin lifecycle/resource ownership.
- Shared internal/services/download remains generic; it must not become a Telegram credential manager.
- Existing ExtractorProvider remains shared by supported extractor-backed platforms.
- Existing downloader module and FeatureSpec ID remain stable unless a separately reviewed architecture decision proves otherwise.
- Existing TaskEngine remains the admission/execution/resource boundary for yt-dlp processes.
- Existing process runner and process resource accounting remain authoritative.
- Existing retained-media registration, storage ownership, delivery pipeline, Telegram service, and RPCExecutor remain unchanged.
- No cookie-specific scheduler, job manager, worker pool, registry, runtime, retry loop, or callback protocol.
- No direct Telegram or process-execution bypass around existing managed services.
- Cookie storage has one canonical owner and bounded lifecycle.
- Cookie management must not make generic Downloader operations depend on an encryption key.

## 7. Proposed implementation phases

Each phase starts by refreshing and recording exact origin/test-next HEAD, then syncing the working checkout with git pull --ff-only. Do not inspect/poll CI unless explicitly requested. Format all changed Go files before committing, and update tests whenever production logic or APIs change.

### DL-F2-A — Source audit and detailed design

- Re-read Downloader module registration, Telegram message/document handling, plugin Scope lifecycle, file download helpers, storage/config conventions, localization, process runner, TaskEngine resource accounting, and yt-dlp invocation code.
- Verify exact pinned yt-dlp version and cookie-file behavior.
- Verify Telegram deletion semantics for the userbot's private chat and incoming document messages.
- Choose upload trigger, storage location, encryption envelope, secret name, file limits, cookie validation policy, and concurrency/write-back strategy.
- Produce a threat model and data-flow diagram.
- No production code changes.

Exit criteria: all decisions are explicit, feasible with existing platform services, and reviewed before implementation.

### DL-F2-B — Telegram ingestion and owner-only workflow

- Add the smallest explicit private-chat document ingestion path.
- Enforce owner identity and private-chat restrictions at the handler boundary.
- Bound file size and download duration.
- Prevent interference with unrelated document handlers.
- Attempt Telegram message deletion and report unconfirmed deletion honestly.
- Test owner, non-owner, group, wrong media type, oversized document, duplicate upload, cancellation, and deletion failure.

Exit criteria: unauthorized input cannot replace credentials; all local temporary input is cleaned on every terminal path.

### DL-F2-C — Encrypted credential storage

- Implement a small purpose-specific encrypted store under existing storage/config ownership.
- Validate key configuration and versioned authenticated ciphertext.
- Implement atomic replacement and explicit delete.
- Preserve previous valid credentials on failed replacement.
- Enforce file permissions and plaintext cleanup.
- Test encrypt/decrypt, wrong/missing key, tampering, truncation, atomic replacement, permissions where portable, delete, and restart persistence.

Exit criteria: no plaintext cookie file remains at rest; ciphertext is unusable without the external deployment key; failed writes cannot destroy previous valid credentials.

### DL-F2-D — yt-dlp integration

- Pass configured cookies to YouTube Search, Probe, and Download consistently.
- Keep non-YouTube extractor and DirectHTTP behavior independent.
- Use short-lived restricted plaintext operation files only if required.
- Implement selected bounded concurrency/write-back strategy.
- Clean plaintext on success, error, timeout, cancellation, panic, reload, and shutdown.
- Preserve probe diagnostics and error classification without leaking secrets.
- Test no-cookie fallback, configured-cookie arguments, process start failure, cancellation, concurrent probes/downloads, and cleanup.

Exit criteria: all YouTube stages use the same credential policy and concurrent processes cannot corrupt a shared plaintext cookie file.

### DL-F2-E — Management UX, security verification, and closure

- Add localized status/delete/replace responses.
- Verify no secrets appear in logs, errors, metrics, traces, process argument contents, or user messages.
- Verify Telegram deletion behavior and cleanup warnings.
- Run focused tests, relevant race tests, architecture tests, vet/build, and diff checks.
- Record residual risks, deployment key provisioning/rotation, backup/restore, and recovery.
- Update this plan with implementation commits and evidence only after phases complete.

Exit criteria: acceptance checklist passes, limitations are documented, and work is explicitly reviewed and closed.

No later phase is automatically authorized by completion of an earlier phase. Review each phase before proceeding.

## 8. Test and verification matrix

| Area | Required cases |
|---|---|
| Authorization | owner, non-owner, missing owner configuration |
| Chat context | private accepted; group, supergroup, channel rejected |
| Upload input | document accepted; photo, text, URL, malformed document rejected |
| Limits | byte limit, line count, line length, download timeout |
| Format | valid Netscape file; malformed records; empty file; unsupported format |
| Relevance | YouTube cookie domains; unrelated domains only; expired/unusable set |
| Persistence | encrypt/decrypt; wrong key; missing key; tamper; truncation; atomic replace |
| Replacement | valid replacement; invalid replacement preserves old credential |
| Cleanup | temp cleanup on success/error/panic/cancel; Telegram delete success/failure |
| yt-dlp | Search, Probe, Download all receive cookies; absent cookies preserve fallback |
| Concurrency | parallel authenticated operations; no shared plaintext race or lost write-back |
| Isolation | non-YouTube extractor and direct HTTP unaffected |
| Diagnostics | no cookie value/name, plaintext line, key, raw stderr, or sensitive path leaked |
| Lifecycle | reload/shutdown during upload or yt-dlp operation |
| Compatibility | .yt, .dl, .download, inline and a2 contracts retained |

Finalize exact verification commands against the changed packages during implementation. Never claim a test passed unless it was run.

## 9. Acceptance criteria

The feature is complete only when all applicable conditions are demonstrated:

1. Owner can upload a Netscape-format YouTube cookies document through a private Telegram conversation.
2. Non-owner and non-private-chat uploads cannot configure or replace credentials.
3. Upload is bounded by explicit size and time limits.
4. File is validated before activation; malformed or irrelevant files are rejected safely.
5. Failed replacement leaves the previous valid credential set usable.
6. Active credentials are encrypted at rest using an external deployment-managed key.
7. No plaintext cookie file remains after ingestion or after a yt-dlp operation.
8. Telegram message deletion is attempted and reported accurately; no absolute-erasure claim is made.
9. Search, Probe, and Download use configured cookies consistently.
10. Concurrent yt-dlp operations cannot race on one mutable plaintext cookie file or silently lose cookie updates.
11. Missing cookies or missing encryption configuration do not break generic Downloader behavior.
12. Shared provider, TaskEngine, retained media, storage, Telegram delivery, and RPCExecutor ownership remain intact.
13. Secrets are absent from logs, metrics, traces, errors, process argument contents, and user-visible messages.
14. Status and delete work without exposing credential material.
15. Tests cover security, failure, cleanup, concurrency, and lifecycle paths.
16. Resource use remains bounded; no new runtime/worker/retry subsystem is introduced.
17. Deployment instructions explain key creation, provisioning, backup, rotation, and restore.

## 10. Operational considerations & deployment key management

- **Key generation & provisioning:** Generate a 32-byte key (`openssl rand -base64 32`) and provide it via deployment secret `GOULTROID_YTDLP_COOKIES_KEY` (base64 or 64-char hex). Never commit the key to Git or `.env` files in source control.
- **Owner cookie export:** Owner exports cookies from a browser session they control. Prefer a dedicated browser profile used only for YouTube to minimize unrelated session data.
- **Telegram transport caveat:** Treat Telegram upload as transmission of a bearer-like session credential through Telegram. Use only Saved Messages and never forward the file.
- **No write-back & expiry:** Cookie exports expire and may be invalidated by YouTube. Local validation checks syntax and auth cookie pairs only; it cannot guarantee remote authentication. Because per-operation snapshots are never written back to `yt_cookies.enc`, the owner must re-upload a fresh `cookies.txt` if YouTube invalidates the session.
- **Key rotation:** Decrypt/re-export fresh cookies, update `GOULTROID_YTDLP_COOKIES_KEY` in the deployment secret manager, restart Goultroid, and re-upload `cookies.txt` via `.ytcookies` in Saved Messages (or run `.ytcookies delete` before rotating). Changing `GOULTROID_YTDLP_COOKIES_KEY` without re-uploading renders existing `yt_cookies.enc` unreadable (`status_key_unavailable` / `corrupt`).
- **Backup, restore & recovery:** Back up both `data/plugins/downloader/yt_cookies.enc` and the separately managed `GOULTROID_YTDLP_COOKIES_KEY`. Ciphertext-only backups cannot be decrypted without the matching key. If `yt_cookies.enc` becomes corrupt, run `.ytcookies delete` and upload a fresh `cookies.txt`. If `.ytcookies` reports `dir_sync_unconfirmed`, the replacement is already active; verify with `.ytcookies status`.
- **Telegram deletion failure:** If Telegram deletion cannot be confirmed, surface that fact honestly and advise the owner to remove the message manually; never imply remote attachment erasure.
- **Residual risks:** `Dockerfile` currently fetches `yt-dlp` from `releases/latest/download/yt-dlp`; pinning an explicit release tag in `Dockerfile` is recommended for reproducible container builds.
- Do not retry authentication failures indefinitely. Classify sign-in challenges as requiring owner action.

## 11. Progress register

| Phase | Status | Evidence |
|---|---|---|
| DL-F2-A — Source audit and detailed design | CLOSED / APPROVED | `docs/design/goultroid-dl-f2-a-youtube-cookies-audit-and-design.md` (commit `74c6d4e59351f9199f31c6fe9b6361f90a7270da`) |
| DL-F2-B — Telegram ingestion | CLOSED / APPROVED | `plugins/downloader/youtube_cookies.go`, `youtube_cookies_test.go`, `module.go`, `downloader.go` (commit `4bf5553af849f26b377e321390a0964036040e4a`) |
| DL-F2-C — Encrypted storage | CLOSED / APPROVED | `plugins/downloader/youtube_cookies_store.go`, `youtube_cookies_store_test.go`, `downloader.go` (commit `9a518403fcd4ef120e46b00095ea92e6c48a6081`) |
| DL-F2-D — yt-dlp integration | CLOSED / APPROVED | `docs/design/goultroid-dl-f2-d-ytdlp-integration-plan.md`, `internal/services/download/*`, `plugins/downloader/youtube_cookies_snapshot.go`, `youtube_cookies_integration_test.go` (commit `c8afe980ef2a253f10400a176348aa6a1d4393ff`) |
| DL-F2-E — UX/security/closure | CLOSED / APPROVED | `.ytcookies status`/`delete`/replace EN+ID UX, sanitized logs, E1 `Delete()` `(bool, error)` + `Status()` `unavailable` semantics, E2 Saved Messages outgoing dispatch audit & `dispatcher_saved_messages_test.go`, `.env.example`, and security audit tests (commits `009346c7907123bf86d3700cd2f1855203d63a74`, `607fc6690b3a82bfe105d76dbedd8a6e353c1082`, `1945e5bc3b56be6c362a16dcae8a97ca432ca068`) |

## 12. Closure

All phases **DL-F2-A through DL-F2-E** are **CLOSED / APPROVED**. No new implementation phase is open. Operational deployment requires setting `OWNER_ID` to the logged-in userbot account ID and provisioning `GOULTROID_YTDLP_COOKIES_KEY` in deployment secrets.
