# GoUltroid — Comprehensive Audit & Ultroid Parity Assessment

> Audit repository `inipew/goultroid` on `main` and compare the implemented behavior, architecture, lifecycle, security model, persistence, scheduler, plugin surface, deployment model, and feature ecosystem against `TeamUltroid/Ultroid`.
>
> Audit date: **2026-09-06**
>
> Reference: `https://github.com/TeamUltroid/Ultroid`

---

## 1. Executive verdict

**GoUltroid belum setara dengan Ultroid secara feature parity.**

Namun, secara **core architecture dan engineering discipline**, GoUltroid sudah berada pada arah yang lebih modern dan lebih terstruktur daripada sekadar port 1:1 dari Python/Telethon. Fondasi penting sudah tersedia:

- gotd/td MTProto client;
- update synchronization melalui `updates.Manager`;
- command router dengan alias/quoted arguments;
- unified command executor + middleware;
- Owner/Sudo/Everyone permission tiers;
- SQLite persistence;
- versioned migrations;
- durable scheduler dengan claim/lease/fencing token/retry/history;
- peer/access-hash persistence;
- AFK, filters, blacklist, notes, moderation, media, downloader, sticker, system, scheduler dan utility plugins;
- graceful shutdown dan lifecycle-aware contexts;
- command metrics;
- panic isolation.

Tetapi **Ultroid adalah ecosystem userbot yang jauh lebih luas**: selain command/plugin core, terdapat assistant/bot mode, inline/callback ecosystem, voice/video call music bot, banyak media/web/API tools, addon repository, localization, deployment targets, manager features, automation plugins, dan sejumlah convenience/security features yang belum ada di GoUltroid.

### Penilaian keseluruhan

| Area | GoUltroid | Dibanding Ultroid | Status |
|---|---|---|---|
| MTProto core | gotd/td | setara secara konsep | 🟢 |
| Update synchronization | updates.Manager | setara/lebih native Go | 🟢 |
| Command routing | kuat | setara untuk command dasar | 🟢 |
| Middleware/security | kuat | lebih terstruktur, tetapi perlu hardening | 🟢/🟡 |
| Plugin architecture | static Go plugins | belum ecosystem parity | 🟡 |
| Persistence | SQLite + migrations | kuat untuk single-instance | 🟢 |
| Scheduler | durable + lease + retry | sangat baik untuk scope saat ini | 🟢 |
| Admin/moderation | cukup luas | belum lengkap | 🟡 |
| Media | fondasi cukup | jauh dari parity | 🟡/🔴 |
| Inline | belum terlihat | Ultroid punya inline subsystem | 🔴 |
| Callback/query handlers | belum menjadi framework publik | Ultroid punya | 🔴 |
| Assistant bot | belum | Ultroid punya | 🔴 |
| Voice/video calls | belum | Ultroid + PyTgCalls | 🔴 |
| Multi-mode/client | belum | Ultroid punya dual/multi-client behavior | 🔴 |
| Localization | belum terlihat | Ultroid multilingual | 🔴 |
| External addons | belum | UltroidAddons ecosystem | 🔴 |
| Observability | basic metrics/logging | belum seluas ecosystem | 🟡 |
| CI/security automation | belum terlihat `.github` workflow | Ultroid punya CodeQL/lint/string checks | 🟡/🔴 |
| Deployment | Docker/local | Ultroid lebih banyak target | 🟡 |
| Feature parity | ~core subset | sangat belum lengkap | 🔴 |

**Kesimpulan:** jangan menyebut GoUltroid sebagai "Ultroid Go yang feature-complete" saat ini. Deskripsi yang lebih akurat adalah **Go-native Ultroid-like userbot foundation dengan core functionality yang sudah serius, tetapi ecosystem parity belum tercapai**.

---

# 2. Audit arsitektur GoUltroid

## 2.1 Application wiring

`internal/app/app.go` sudah melakukan dependency wiring yang cukup bersih:

```text
Config
  ↓
Logger + SQLite
  ↓
Permissions
  ↓
Router
  ↓
Dispatcher
  ↓
Telegram Client
  ↓
Event Bus
  ↓
Plugins + Scheduler
```

Plugin yang saat ini diregistrasikan antara lain:

- ping
- help
- alive
- pin
- forward
- downloader
- sudo
- notes
- afk
- admin
- media
- sticker
- info
- system
- filters
- fun
- scheduler
- locks
- blacklist
- profile

Ini merupakan coverage core yang sudah cukup besar.

### Temuan

**Poin positif:** semua komponen utama diinisialisasi dari satu application composition root, sehingga dependency tidak tersebar secara acak.

**Poin yang perlu diperbaiki:** daftar plugin masih hard-coded di `app.go`. Untuk static plugin system ini masih valid, tetapi semakin banyak plugin akan membuat composition root menjadi bottleneck maintenance.

### Rekomendasi

Pertahankan static registration untuk core, tetapi pisahkan menjadi registry/group:

```text
plugins/
  core
  admin
  moderation
  media
  productivity
  fun
  system
```

Tidak perlu dynamic hot-loading dulu.

---

# 3. Command execution pipeline

Implementasi saat ini sudah memiliki `CommandExecutor` yang menyatukan execution path:

```text
Recovery
  ↓
Logging
  ↓
Permission
  ↓
Filter
  ↓
Cooldown
  ↓
Timeout
  ↓
Handler
```

Ini merupakan keputusan arsitektur yang benar.

Scheduler juga sudah menggunakan executor yang sama melalui `SetExecutor`, sehingga masalah lama berupa scheduled command bypass middleware sudah diperbaiki secara desain.

## 3.1 Permission

`PermissionMiddleware` sekarang fail-closed untuk command non-public:

```text
Permission != Everyone
        ↓
Permissions == nil ? DENY
        ↓
CanRun()
```

Ini sudah benar.

## 3.2 Scheduled command principal

Scheduler sekarang menyimpan `CreatedBy`, lalu melakukan dynamic principal resolution ketika job dieksekusi. Ini penting karena scheduled command tidak boleh diasumsikan selalu berasal dari Owner.

### Status

**🟢 Secara arsitektur sudah benar.**

### Masih perlu diuji

- owner dihapus/diubah setelah schedule dibuat;
- sudo dicabut setelah schedule dibuat;
- `CreatedBy == 0`;
- command permission berubah setelah schedule dibuat;
- scheduled command yang target chat-nya sudah tidak dapat diakses;
- restart ketika job sedang leased.

---

# 4. Context dan service boundary

`core.Context` sudah menjadi facade untuk plugin dan Telegram service abstraction sudah tersedia.

Ini jauh lebih sehat daripada membiarkan setiap plugin mengakses raw MTProto client.

Tetapi `Context` masih cenderung menjadi object besar yang memuat banyak domain operation.

## Target jangka panjang

```text
Context
├── Message
├── Chat
├── Sender
├── Args
├── Permissions
├── Messages service
├── Media service
├── Admin service
├── Peer service
└── Storage service
```

Context tetap boleh menjadi facade, tetapi implementasi domain sebaiknya semakin dipindahkan ke service.

---

# 5. Router

Router GoUltroid sudah memiliki fitur yang tepat untuk command framework modern:

- case-insensitive command matching;
- aliases;
- quoted arguments;
- escaped characters;
- raw arguments;
- thread-safe registry.

README saat ini mendokumentasikan `.ping`, `.p`, `.latency` dan command aliases lain sebagai bagian dari behavior router.

### Yang masih perlu diuji lebih agresif

- Unicode whitespace;
- malformed/unclosed quotes;
- escaped quote/backslash;
- prefix collision;
- alias collision;
- duplicate registration;
- concurrent registration + dispatch;
- command names dengan karakter tidak valid;
- empty command after prefix;
- very large raw arguments.

**Status: 🟢 core, 🟡 edge-case hardening.**

---

# 6. Scheduler

Scheduler adalah salah satu bagian terkuat dari GoUltroid saat ini.

Database sudah berkembang menjadi durable scheduler dengan:

- `created_by`;
- `status`;
- `max_attempts`;
- `lease_until`;
- `claimed_at`;
- `last_started_at`;
- `last_finished_at`;
- `claim_token`;
- execution history;
- peer/access-hash persistence;
- retry/backoff;
- lease renewal heartbeat.

Migration sudah versioned sampai schema version 7.

### Model saat ini

```text
DB
 ↓
claim due jobs
 ↓
lease + claim token
 ↓
execute
 ↓
complete / fail
 ↓
retry or finalize
```

Ini sudah jauh lebih production-oriented dibanding scheduler sederhana berbasis ticker.

## Remaining scheduler risks

### P1 — scheduler throughput/backpressure

`processDueJobs()` dapat claim hingga 10 job dan membuat goroutine untuk setiap job. Belum terlihat worker pool dengan concurrency limit yang eksplisit.

Akibatnya, jika jumlah job meningkat, execution concurrency ditentukan terutama oleh jumlah job yang berhasil di-claim per tick.

**Fix:** gunakan bounded worker pool atau semaphore.

Contoh target:

```text
scheduler
   ↓
claim batch
   ↓
bounded queue
   ↓
N workers
```

### P1 — retry classification

`IsPermanentError()` sudah lebih baik daripada retry semua error, tetapi taxonomy error perlu terus diperluas agar Telegram FloodWait, network timeout, invalid peer, permission error, media failure, dan invalid arguments mempunyai retry policy yang benar.

### P1 — lease-loss cancellation

Heartbeat mendeteksi kegagalan renewal, tetapi execution path harus memastikan job tidak terus melakukan side effect setelah lease hilang.

Target:

```text
lease lost
   ↓
cancel execution context
   ↓
stop side effects
```

Ini penting untuk mencegah duplicate execution ketika lease berpindah ke worker lain.

### P2 — misfire policy

One-shot job sudah memberi informasi jika terlambat lebih dari satu menit. Untuk recurring job, sebaiknya ada explicit policy:

- skip missed occurrences;
- run once immediately;
- catch-up N occurrences;
- mark misfired.

Jangan membiarkan behavior ini implisit.

---

# 7. Database

GoUltroid sudah memperbaiki salah satu weakness lama: schema sekarang menggunakan migration versions.

Current migration chain mencakup:

```text
v1  initial schema
v2  scheduler principal/error state
v3  scheduler lease state
v4  fencing token
v5  persistent peer storage
v6  scheduler execution history
v7  persistent peer entities
```

Setiap migration dijalankan dalam transaction.

**Status: 🟢.**

## Remaining database improvements

### P1 — migration integrity/checksum

Version number saja belum mendeteksi migration yang telah diedit setelah production deployment.

Tambahkan checksum:

```sql
schema_migrations(
    version,
    description,
    checksum,
    applied_at
)
```

### P1 — SQLite operational settings

Pastikan production startup menetapkan policy yang eksplisit untuk:

- WAL;
- busy timeout;
- foreign keys;
- synchronous level;
- connection pool size.

### P2 — backup/recovery

Tambahkan documented SQLite backup/restore procedure dan test restore.

---

# 8. Security audit

## 8.1 Permission

Sudah fail-closed untuk security-sensitive commands.

**🟢**

## 8.2 `.exec`

README mendokumentasikan owner-only shell execution dengan timeout 60 detik.

Ini masih merupakan **high-risk feature** karena Telegram input pada akhirnya menjadi command execution input.

Minimum hardening:

- strict owner identity verification;
- execution timeout;
- stdout/stderr output limit;
- environment sanitization;
- working-directory restriction;
- maximum process count/concurrency;
- cancellation propagation;
- optional command allowlist/denylist;
- avoid logging secrets;
- disk quota for generated output.

**Target: P0/P1 depending deployment threat model.**

## 8.3 Media commands

Media processing harus memiliki resource budgets:

- max download size;
- max output size;
- max concurrent jobs;
- FFmpeg timeout;
- temp directory isolation;
- automatic cleanup;
- disk free-space check;
- filename/path sanitization.

---

# 9. Peer resolution

GoUltroid sudah memiliki persistent peer/access-hash storage, yang merupakan improvement penting.

Namun peer resolution sebaiknya tetap dipusatkan pada satu abstraction.

Target API:

```go
type PeerResolver interface {
    ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, error)
    ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error)
}
```

Plugin tidak seharusnya perlu mengetahui detail `InputPeerUser`, `InputPeerChannel`, access hash, username cache, dan fallback resolution.

**Status: 🟡 — foundation bagus, abstraction perlu diperdalam.**

---

# 10. Functional feature comparison dengan Ultroid

Ultroid bukan hanya router + plugin loader. Repository resminya memiliki struktur core (`pyUltroid`), assistant, plugins, resources, localization, deployment helpers, dan ekosistem addon.

README Ultroid sendiri mendeskripsikannya sebagai stable pluggable userbot sekaligus Voice & Video Call music bot.

## 10.1 Feature matrix

| Capability | GoUltroid | Ultroid | Gap |
|---|---:|---:|---|
| MTProto userbot | ✅ | ✅ | none conceptually |
| Command router | ✅ | ✅ | low |
| aliases | ✅ | ✅ | low |
| quoted args | ✅ | ✅ | low |
| Owner/Sudo | ✅ | ✅ | low |
| Admin moderation | ✅ | ✅ | medium |
| Locks | ✅ | ✅ | low |
| Blacklist | ✅ | ✅ | medium |
| Filters/snips | ✅ | ✅ | medium |
| Notes | ✅ | ✅ | low |
| AFK | ✅ | ✅ | low |
| Scheduler | ✅ | ✅ | low/medium |
| Media info | ✅ | ✅ | medium |
| Downloader | partial | extensive | **high** |
| Audio tools | partial | extensive | **high** |
| Video tools | partial | extensive | **high** |
| Sticker tools | partial | extensive | **high** |
| Image manipulation | limited | extensive | **high** |
| Web/API utilities | limited | extensive | **high** |
| Inline mode | ❌ | ✅ | **critical** |
| Inline search tools | ❌ | ✅ | **critical** |
| Callback handlers | not public framework | ✅ | **high** |
| Assistant bot | ❌ | ✅ | **critical** |
| Bot mode/dual mode | ❌ | ✅ | **critical** |
| Voice calls | ❌ | ✅ | **critical** |
| Video calls | ❌ | ✅ | **critical** |
| Music bot | ❌ | ✅ | **critical** |
| Multi-client ecosystem | ❌ | partial/available | **high** |
| Localization | not implemented as core feature | ✅ | **high** |
| External addon repository | ❌ | ✅ | **high** |
| User logs/tag logger | partial/❌ | ✅ | **high** |
| Broadcast tools | partial/❌ | ✅ | **high** |
| PM permit/security | limited | ✅ | **high** |
| Auto profile/pic tools | ❌ | ✅ | medium/high |
| Greetings | ❌ | ✅ | medium |
| Anti-flood | ❌ | ✅ | medium |
| Warn system | ❌ | ✅ | medium |
| Force subscribe | ❌ | ✅ | medium |
| Zip tools | ❌ | ✅ | medium |
| QR/code/image utility ecosystem | limited | extensive | medium/high |
| Deployment helpers | Docker/local | broader | medium |
| CI security automation | limited/not present | present | medium |

---

# 11. Missing feature groups — priority order

## P0 — Required for serious Ultroid parity

### 11.1 Inline framework

Implement:

- inline query registration;
- inline result builders;
- callback query handling;
- inline pagination;
- inline button helpers;
- answer/edit/delete callback lifecycle;
- permission checks for inline actions.

Target architecture:

```text
Telegram Update
├── Message Update → Command Dispatcher
├── Inline Query → Inline Dispatcher
├── Callback Query → Callback Dispatcher
└── Raw/Service Events → Event Bus
```

### 11.2 Assistant bot

Ultroid has assistant functionality separate from normal userbot interaction.

GoUltroid should define an optional assistant subsystem:

```text
Userbot Client
      │
      ├── user commands
      │
      └── assistant bridge
                │
                ▼
            Bot Client
```

Do not couple assistant behavior directly to userbot command handlers.

### 11.3 Voice/video call + music subsystem

This is a major parity gap.

Required modules:

- voice call client;
- video call support if retained as a goal;
- audio source resolver;
- playback queue;
- stream lifecycle;
- pause/resume/skip/seek;
- volume;
- playlist;
- leave/stop;
- reconnect;
- FFmpeg/process supervisor.

This should be a separate subsystem, not embedded into the normal command executor.

---

# 12. P1 — Feature ecosystem expansion

## 12.1 Media toolbox

Add modular plugins for:

```text
media/
├── download
├── upload
├── audio
├── video
├── image
├── sticker
├── gif
├── thumbnail
├── compress
├── resize
├── rotate
├── watermark
├── metadata
├── zip
└── file-share
```

Every plugin must use shared resource limits and temp-file lifecycle.

## 12.2 Web/API utilities

Ultroid's ecosystem contains a broad range of API-backed utilities and addons.

Go implementation should use typed service clients rather than raw HTTP calls scattered through plugins.

```text
internal/services/http
internal/services/youtube
internal/services/github
internal/services/search
internal/services/image
internal/services/translation
```

## 12.3 Moderation expansion

Current admin foundation should be extended with:

- warn system;
- anti-flood;
- greetings;
- force subscribe;
- automated moderation;
- global ban/banlist where appropriate;
- channel/admin utilities;
- user/chat restrictions;
- permission-aware moderation reports.

---

# 13. P1 — Userbot productivity features

Missing/high-value areas:

- broadcast;
- tag logger/user logger;
- message search/history helpers;
- save/get message helpers;
- PM permit/security;
- username tracking;
- profile/autopic automation;
- autocorrect;
- custom triggers;
- auto responses;
- custom command snippets;
- night mode/automation;
- channel utilities.

These should be implemented as independent plugins on top of stable core services.

---

# 14. P1 — Plugin ecosystem architecture

Ultroid's model extends beyond its main repository. The official ecosystem also includes `UltroidAddons`, demonstrating the value of an external plugin model.

GoUltroid should eventually support a manifest-based plugin package:

```yaml
name: example
version: 1.0.0
author: author
min_goultroid: 0.5.0
commands:
  - example
permissions:
  - sudo
requirements:
  - ffmpeg
```

But **do not implement arbitrary runtime Go plugin loading first**.

Prefer:

1. source-level external plugins;
2. compile-time registration;
3. signed/reproducible release packages;
4. only later consider dynamic loading.

Go's native `plugin` package is not a good portability foundation for this project.

---

# 15. P1 — Event system parity

GoUltroid already has an event bus and message handlers.

Expand it into typed event families:

```text
MessageReceived
MessageEdited
MessageDeleted
CallbackQuery
InlineQuery
ChatMemberUpdated
ChatAction
RawUpdate
MediaDownloaded
MediaUploaded
SchedulerJobStarted
SchedulerJobFinished
```

Plugins should be able to subscribe without knowing the Telegram transport internals.

---

# 16. P1 — Localization

Ultroid supports multiple languages. GoUltroid currently does not expose a comparable localization subsystem.

Implement:

```text
internal/i18n/
├── catalog.go
├── locale.go
├── formatter.go
└── locales/
```

Rules:

- command metadata translatable;
- user-facing errors translatable;
- plugin help translatable;
- fallback locale mandatory;
- no hard-coded user-facing strings inside domain logic.

---

# 17. P1 — Deployment parity

GoUltroid currently documents local execution and Docker.

Ultroid historically supports more deployment paths and includes deployment helpers.

Recommended Go targets:

### Required

- Linux binary;
- Docker;
- Docker Compose;
- Termux-compatible build/runtime documentation.

### Optional

- systemd service;
- health check;
- container healthcheck;
- architecture builds: amd64/arm64/armv7 where dependencies allow.

Avoid chasing obsolete hosting providers merely for historical feature parity.

---

# 18. CI/CD and repository engineering

The current repository contains a checked-in `coverage.out`, but no comparable `.github` workflow surface was visible in the repository tree.

Ultroid's repository has GitHub workflows including CodeQL, pylint, and string analysis.

GoUltroid should add:

```text
.github/workflows/
├── test.yml
├── race.yml
├── lint.yml
├── security.yml
└── release.yml
```

Minimum checks:

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
golangci-lint run
govulncheck ./...
```

Also add:

- dependency update automation;
- CodeQL or equivalent static analysis;
- secret scanning;
- release artifact checksums;
- SBOM for releases.

`coverage.out` should normally be generated by CI rather than committed unless there is a deliberate reason to version it.

---

# 19. Testing audit

Current GoUltroid test coverage is already substantial across core/database/scheduler areas.

The next step should be **behavioral coverage**, not merely line coverage.

## Required command tests

```text
router
├── quoting
├── escaping
├── aliases
├── case sensitivity
├── malformed input
└── concurrent registry

permission
├── owner
├── sudo
├── everyone
├── nil permissions
└── dynamic sudo removal

executor
├── panic
├── timeout
├── cooldown
├── permission denial
├── filter failure
└── cancellation
```

## Scheduler tests

```text
due claim
lease acquisition
lease renewal
lease loss
retry
permanent failure
misfire
restart recovery
duplicate worker prevention
principal revocation
```

## Telegram integration tests

Use a dedicated test account/environment.

Never make the main personal Telegram account the automated integration-test fixture.

---

# 20. Error model

The existing error taxonomy is moving in the correct direction.

Continue standardizing:

```text
ErrPermissionDenied
ErrInvalidArgs
ErrRateLimited
ErrTelegram
ErrNotFound
ErrUnsupported
ErrMedia
ErrStorage
ErrTimeout
ErrInternal
ErrLeaseLost
ErrCancelled
```

Then map each category to:

```text
retryable?
user-visible?
log level?
metric label?
```

This becomes especially important once media, inline, assistant, and voice subsystems are added.

---

# 21. Observability

Current command metrics are a good start.

Production target:

```text
commands_total{command,status}
command_duration_seconds
telegram_requests_total{method,status}
telegram_errors_total{type}
scheduler_jobs_total{action,status}
scheduler_lag_seconds
scheduler_lease_losses_total
media_bytes_total{direction}
media_jobs_total{type,status}
active_workers
plugin_count
```

Avoid high-cardinality labels such as raw user IDs, chat IDs, message IDs, or arbitrary command arguments.

---

# 22. Important behavior differences from Ultroid

Do **not** attempt a literal source translation.

Ultroid is Python/Telethon/asyncio-centric. GoUltroid uses gotd/td and should preserve desired **behavior**, not Python implementation details.

Good parity means:

```text
same user-facing capability
        +
similar command semantics
        +
similar permission behavior
        +
similar persistence expectations
        +
similar plugin extensibility
        +
Go-native implementation
```

Not:

```text
Python file → Go file
```

---

# 23. Recommended target architecture

```text
goultroid/
├── cmd/
│   └── goultroid/
├── internal/
│   ├── app/
│   ├── config/
│   ├── core/
│   │   ├── command/
│   │   ├── context/
│   │   ├── middleware/
│   │   ├── permission/
│   │   ├── router/
│   │   └── events/
│   ├── telegram/
│   │   ├── client/
│   │   ├── updates/
│   │   ├── peers/
│   │   ├── service/
│   │   ├── inline/
│   │   └── callback/
│   ├── assistant/
│   ├── voice/
│   ├── media/
│   ├── database/
│   ├── scheduler/
│   ├── plugin/
│   ├── i18n/
│   ├── observability/
│   └── services/
├── plugins/
│   ├── core/
│   ├── admin/
│   ├── moderation/
│   ├── media/
│   ├── productivity/
│   ├── inline/
│   ├── assistant/
│   ├── voice/
│   └── fun/
├── migrations/
├── docs/
├── data/
└── .github/workflows/
```

This should be reached incrementally. **Do not perform a big-bang rewrite.**

---

# 24. Implementation roadmap

## Phase 0 — Correctness baseline

- [ ] Add comprehensive race/concurrency tests.
- [ ] Verify scheduler lease-loss cancellation.
- [ ] Add scheduler bounded concurrency.
- [ ] Validate migration checksum strategy.
- [ ] Harden `.exec` resource and output limits.
- [ ] Harden all media temp-file lifecycle.
- [ ] Add CI test/race/lint/security workflows.

## Phase 1 — Core Ultroid parity

- [ ] Inline query framework.
- [ ] Callback query framework.
- [ ] Inline buttons/builders.
- [ ] Save/get message utilities.
- [ ] Broadcast.
- [ ] User/tag logging.
- [ ] PM permit/security.
- [ ] Warn/anti-flood.
- [ ] Greetings.
- [ ] Custom triggers/snippets.

## Phase 2 — Media ecosystem

- [ ] Downloader abstraction.
- [ ] YouTube/media providers.
- [ ] Audio tools.
- [ ] Video tools.
- [ ] GIF tools.
- [ ] Image tools.
- [ ] Sticker toolbox.
- [ ] Compression.
- [ ] Thumbnail/resize/rotate.
- [ ] ZIP tools.
- [ ] File-share helpers.

## Phase 3 — Assistant

- [ ] Bot client abstraction.
- [ ] Assistant command router.
- [ ] Userbot ↔ assistant bridge.
- [ ] Assistant permission model.
- [ ] Assistant persistence.

## Phase 4 — Voice/video

- [ ] Voice call engine.
- [ ] Playback queue.
- [ ] YouTube/source resolver.
- [ ] Streaming process supervisor.
- [ ] Reconnect state machine.
- [ ] Queue persistence where useful.
- [ ] Video support if still desired.

## Phase 5 — Ecosystem

- [ ] Localization.
- [ ] External plugin manifests.
- [ ] Addon repository.
- [ ] Plugin compatibility/version checks.
- [ ] Release automation.
- [ ] SBOM/checksums/signing.

---

# 25. Final feature-gap classification

### Already strong

- MTProto client foundation
- update synchronization
- command router
- middleware
- permission tiers
- context abstraction
- SQLite
- migrations
- scheduler
- peer persistence
- graceful shutdown
- static plugin manager
- core moderation
- notes/filters/AFK
- media foundation

### Functional but incomplete versus Ultroid

- admin/moderation ecosystem
- downloader/media ecosystem
- system tools
- user/profile utilities
- fun plugins
- scheduler UX
- deployment ecosystem

### Major missing systems

- inline framework
- callback framework
- assistant bot
- voice/video calls
- music bot
- multi-mode/client behavior
- localization
- addon ecosystem
- large API/web utility ecosystem
- broad media toolbox
- mature automation/security plugins

---

# 26. Final conclusion

GoUltroid **sudah memiliki core yang cukup serius dan secara arsitektur tidak perlu dirombak total**.

Yang belum tercapai adalah **breadth of functionality and ecosystem maturity** yang membuat Ultroid menjadi userbot multi-featured.

Prioritas engineering yang paling benar adalah:

```text
Correctness & security
        ↓
Inline + callback infrastructure
        ↓
Core userbot productivity/moderation
        ↓
Media ecosystem
        ↓
Assistant bot
        ↓
Voice/video/music
        ↓
Localization + external addons
        ↓
Ecosystem/release maturity
```

Jangan mengejar jumlah command secara acak. Bangun **platform capability** terlebih dahulu, lalu plugin menjadi implementasi tipis di atas service yang reusable.

Target akhir yang direkomendasikan:

> **Ultroid-like feature coverage + Go-native architecture + unified command/event pipeline + durable state + fail-closed security + bounded resource usage + extensible plugin ecosystem.**

---

## Audit evidence

Primary repository inspected:

- `inipew/goultroid`
- default branch: `main`
- language: Go
- repository status: public, active

Reference repository inspected:

- `TeamUltroid/Ultroid`
- language: Python
- architecture: Telethon-based userbot with plugins, assistant, and voice/video ecosystem

Additional reference:

- `TeamUltroid/UltroidAddons` — external plugin ecosystem

This document is intended to be the baseline parity/audit document for subsequent implementation work. Subsequent audits should update this document rather than creating contradictory feature lists.