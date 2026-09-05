# GoUltroid — Production Audit & Next-Level Roadmap

> Audit terhadap repository `main`, implementasi yang ada, dan desain pada `docs/0_ultroid-go.md`, `docs/1_mvp.md`, `docs/2_mvp.md`, dan `docs/2.1_mvp.md`.

## 1. Executive summary

GoUltroid sudah jauh melewati MVP awal. Fondasi yang direncanakan di dokumen sebelumnya sudah terealisasi: MTProto melalui gotd/td, persistent session, update synchronization, dispatcher, high-level context, command router, middleware, permission Owner/Sudo/Everyone, SQLite persistence, scheduler, dan banyak built-in plugin.

Struktur saat ini sudah cukup layak untuk dikembangkan menjadi UserBot Ultroid-like dari nol. Namun, sebelum disebut production-grade, ada beberapa correctness dan lifecycle issue yang harus diperbaiki.

### Status saat ini

| Area | Status | Penilaian |
|---|---|---|
| MTProto transport | implemented | kuat |
| update synchronization | implemented | kuat; gotd menangani gap/state |
| command router | implemented | baik |
| quoted arguments | implemented | baik |
| permission middleware | implemented | perlu fail-closed hardening |
| plugin manager | implemented | baik untuk static loading |
| Context API | implemented | luas, tetapi masih terlalu gemuk |
| Telegram service abstraction | implemented | baik |
| SQLite | implemented | baik untuk single-instance |
| scheduler | implemented | perlu lifecycle/claim hardening |
| message interceptors | implemented | baik, perlu isolation |
| media | implemented | ada fondasi, perlu resource limits |
| graceful shutdown | implemented | perlu timeout/error aggregation |
| dynamic plugin loading | belum | phase berikutnya |
| observability/metrics | belum | production improvement |
| persistent migration versioning | belum | perlu sebelum schema berkembang |
| multi-account | belum | future |

## 2. Hal yang sudah benar

### 2.1 Telegram synchronization

`internal/telegram/client.go` menggunakan `updates.Manager` + peer manager dan tidak mencoba mengimplementasikan sendiri mekanisme `pts/qts/seq` atau gap recovery. Ini sesuai keputusan arsitektur pada dokumen MVP.

Prinsip yang dipertahankan:

```text
Telegram / MTProto
        ↓
      gotd/td
        ↓
 updates.Manager
        ↓
 tg.UpdateDispatcher
        ↓
 GoUltroid Dispatcher
```

Framework application layer tidak mengambil alih pekerjaan synchronization layer.

### 2.2 Context abstraction

Plugin menerima `*core.Context`, sedangkan operasi Telegram dipusatkan pada `TelegramServicer`. Ini adalah boundary yang tepat untuk menghindari plugin bergantung langsung pada generated MTProto types.

### 2.3 Router

Router sudah memiliki:

- case-insensitive command matching;
- aliases;
- quoted arguments;
- escaped characters;
- raw arguments;
- thread-safe registry.

Ini sudah lebih matang dibanding parser `strings.Fields` sederhana pada MVP pertama.

### 2.4 Middleware

Chain saat ini sudah menyediakan recovery, logging, permission, contextual filters, cooldown, dan timeout. Ini adalah fondasi yang benar untuk plugin ecosystem yang besar.

### 2.5 Plugin architecture

Static registration melalui `manager.Register(...)` tetap menjadi pilihan yang baik untuk core. Dynamic loading sebaiknya tidak dipaksakan terlalu dini.

## 3. Critical findings

### P0 — Permission middleware fail-open ketika Permissions nil

`PermissionMiddleware` hanya melakukan pengecekan jika `ctx.Perms != nil`. Artinya command yang membutuhkan Owner/Sudo dapat dieksekusi jika Context dibuat tanpa permission object.

Untuk command framework security boundary, perilaku yang benar adalah:

```text
command requires Owner/Sudo
        ↓
Permissions missing
        ↓
DENY
```

Bukan:

```text
Permissions missing
        ↓
continue
```

Fix: command dengan permission selain `Everyone` harus fail-closed jika permission provider tidak tersedia.

### P0 — Scheduled command melewati middleware

Scheduler saat ini memanggil `cmd.Handler(coreCtx)` secara langsung. Akibatnya scheduled command melewati:

- permission middleware;
- contextual filters;
- cooldown;
- timeout middleware;
- recovery middleware dari dispatcher.

Ini membuat scheduled command menjadi execution path berbeda dari command biasa.

Target desain:

```text
scheduled command
       ↓
router
       ↓
same middleware pipeline
       ↓
handler
```

Selain itu, scheduler sebaiknya menyimpan `created_by`/principal sehingga command Owner-only tidak otomatis dijalankan sebagai Owner hanya karena scheduler dipanggil oleh user lain.

### P0 — Async command execution menggunakan update context

Dispatcher menjalankan handler dalam goroutine tetapi memberikan Context yang berasal langsung dari update callback. Lifecycle context tersebut tidak ideal untuk pekerjaan background yang masih berjalan setelah callback kembali.

Gunakan detached context yang tetap mempertahankan cancellation aplikasi, lalu terapkan timeout per command.

Minimal:

```go
execCtx := context.WithoutCancel(ctx)
```

kemudian middleware membuat deadline dari `execCtx`.

### P1 — Scheduler periodic task dapat dipanggil sebelum Engine.Start

`RegisterPeriodicTask` membuat child context dari `e.ctx`. Jika `Start()` belum dipanggil, `e.ctx` nil dan `context.WithCancel(nil)` akan panic.

API harus:

- menolak registration ketika engine belum running; atau
- menyimpan task dan menjalankannya saat Start.

Untuk behavior yang predictable, pilihan pertama lebih sederhana.

### P1 — Dispatcher logger nil safety

`dispatch()` mengakses `d.logger.Warn(...)` pada error interceptor tanpa guard. Constructor memang biasanya menerima logger, tetapi type ini tetap dapat dibuat dengan nil logger oleh test atau consumer lain.

Logger sebaiknya dinormalisasi ke `zap.NewNop()` pada constructor.

### P1 — Scheduler DB claim/update error diabaikan

`processDueJobs()` menghapus atau memajukan `next_run_at`, tetapi error DB diabaikan. Jika update gagal, job dapat dieksekusi lagi pada tick berikutnya.

Minimal fix: jangan execute job jika claim/update gagal.

Long-term fix: atomic claim transaction dengan lease/claimed timestamp.

### P1 — SQLite migration belum versioned

Schema dibuat melalui satu `CREATE TABLE IF NOT EXISTS` block. Ini cocok untuk prototype, tetapi tidak cukup untuk evolution schema yang aman.

Gunakan tabel:

```sql
schema_migrations(version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)
```

dan migration files/steps yang immutable.

### P1 — Database path/session path perlu permission hardening

Directory database dibuat `0755`. Untuk data UserBot yang dapat berisi state sensitif, gunakan `0700`; file SQLite mengikuti umask/driver behavior tetapi directory boundary tetap sebaiknya private.

## 4. Important correctness issues

### Peer/access hash

Beberapa operasi menerima `InputPeerUser` hanya dengan `UserID`, terutama ketika user diberikan sebagai numeric ID atau berasal dari reply. Telegram sering membutuhkan access hash untuk user/channel yang belum dapat di-resolve dari local entity cache.

Satu abstraction `PeerResolver` sebaiknya menjadi dependency resmi Context:

```go
type PeerResolver interface {
    ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error)
    ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error)
}
```

Plugin tidak perlu membuat `tg.InputPeer*` secara manual.

### Admin operations

`UnbanUser` untuk legacy/basic chat tidak melakukan operasi dan mengembalikan nil pada unsupported path. Silent success berbahaya untuk admin tooling.

Behavior sebaiknya:

```text
supported -> execute
unsupported -> explicit error
```

### Purge

Purge saat ini memakai bounded history/replies fetch dan membangun ID set. Untuk chat besar, purge perlu pagination/chunking dan hasil operasi harus dilaporkan secara eksplisit. Error fetch tidak boleh diperlakukan sebagai empty result.

### Message Context terlalu besar

`core.Context` sekarang memuat banyak admin/media/helper methods. Ini nyaman untuk plugin tetapi meningkatkan coupling.

Target jangka panjang:

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
└── Storage service
```

Context menjadi facade, bukan tempat seluruh implementasi domain.

## 5. Plugin lifecycle

Interface sekarang minimal:

```go
type Plugin interface {
    Name() string
    Commands() []core.Command
    Init() error
}
```

Ini tepat untuk static MVP. Untuk next stage, tambahkan metadata tanpa membuat core bergantung pada implementation detail:

```go
type Metadata struct {
    Name        string
    Version     string
    Author      string
    Description string
}
```

Lifecycle target:

```text
Discover
  ↓
Validate metadata
  ↓
Init
  ↓
Register commands/hooks
  ↓
Active
  ↓
Shutdown
```

Hot unload/reload sebaiknya ditunda sampai lifecycle contract benar-benar stabil.

## 6. Scheduler redesign

Scheduler production sebaiknya menggunakan model durable job:

```text
scheduled_jobs
├── id
├── principal_id
├── chat_id
├── peer_type
├── access_hash
├── action_type
├── payload
├── interval_seconds
├── next_run_at
├── lease_until
├── attempt_count
├── last_error
├── created_at
└── updated_at
```

Claim harus atomic:

```text
SELECT due job
      ↓
atomic UPDATE/transaction
      ↓
lease acquired
      ↓
execute
      ↓
ack/reschedule
```

Ini akan mencegah duplicate execution jika nanti lebih dari satu worker digunakan.

## 7. Command execution model

Semua execution path harus convergent:

```text
                    ┌──────────────┐
Telegram update ───>│              │
                    │ Command      │
Scheduled job ─────>│ Execution    │
                    │ Pipeline     │
Internal trigger ──>│              │
                    └──────┬───────┘
                           ↓
                     middleware
                           ↓
                        handler
```

Jangan membuat jalur khusus yang memanggil handler secara langsung.

## 8. Error taxonomy

Saat ini error handling masih banyak menggunakan formatted errors. Untuk framework besar, gunakan sentinel/domain errors untuk kategori yang perlu diketahui dispatcher:

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
```

Dispatcher dapat menentukan apakah error:

- diam-diam dilog;
- dibalas ke user;
- dicatat sebagai warning;
- memicu retry.

## 9. Resource safety

Media processing adalah area yang berpotensi menghabiskan CPU/RAM/disk. Production hardening harus menambahkan:

- maximum download size;
- maximum upload size;
- temporary directory isolation;
- automatic cleanup;
- concurrency semaphore;
- FFmpeg timeout;
- output size limit;
- filename sanitization;
- disk-space check;
- cancellation propagation.

Khusus command `.exec`, gunakan timeout, output limit, environment sanitization, working-directory policy, dan jangan pernah menganggap input Telegram sebagai trusted input.

## 10. Observability

Minimal production baseline:

```text
structured logs
    +
command execution metrics
    +
Telegram API error metrics
    +
scheduler metrics
    +
media processing metrics
```

Metric yang berguna:

- commands_total{command,status};
- command_duration_seconds;
- telegram_requests_total;
- telegram_errors_total;
- scheduler_jobs_total{action,status};
- scheduler_lag_seconds;
- media_bytes_total;
- active_downloads;
- plugin_count.

OpenTelemetry dapat ditambahkan setelah lifecycle dan error taxonomy stabil.

## 11. Testing strategy

Current unit-test coverage sudah cukup luas pada core, database, scheduler, dispatcher, dan plugin tertentu. Next step bukan sekadar menambah jumlah test, tetapi menambah scenario tests:

### Router

- empty command;
- Unicode whitespace;
- unmatched quotes;
- escaped quote;
- prefix collision;
- alias collision;
- concurrent registration/read.

### Permission

- owner;
- sudo;
- everyone;
- nil permission provider;
- owner removed from sudo list;
- zero/invalid user ID.

### Dispatcher

- private/group/channel;
- outgoing/incoming;
- missing entity/access hash;
- interceptor failure;
- handler panic;
- handler timeout;
- context cancellation.

### Scheduler

- overdue one-shot;
- recurring drift;
- DB claim failure;
- duplicate prevention;
- restart recovery;
- unsupported action;
- cancellation during execution.

### Integration

Tambahkan integration suite menggunakan Telegram test account/environment terpisah. Jangan menjadikan live Telegram account sebagai unit test.

## 12. Recommended architecture for v1

```text
goultroid/
├── cmd/goultroid/
├── internal/
│   ├── app/
│   ├── config/
│   ├── core/
│   │   ├── command/
│   │   ├── context/
│   │   ├── middleware/
│   │   ├── permission/
│   │   └── router/
│   ├── telegram/
│   │   ├── client/
│   │   ├── updates/
│   │   ├── peers/
│   │   └── service/
│   ├── plugin/
│   ├── database/
│   ├── scheduler/
│   └── observability/
├── plugins/
├── migrations/
├── docs/
└── data/
```

Tidak perlu melakukan big-bang refactor sekarang. Struktur existing masih dapat dipakai; pemisahan domain dapat dilakukan secara incremental.

## 13. Roadmap

### Phase 1 — Correctness hardening

1. Fail-closed permission middleware.
2. Detached execution context.
3. Nil-safe logger.
4. Scheduler start-state validation.
5. Scheduler claim/update error handling.
6. Explicit unsupported errors pada admin operations.

### Phase 2 — Durable state

1. Versioned migrations.
2. Scheduler principal/lease fields.
3. Atomic job claim.
4. Persistent command execution state.
5. Better restart recovery.

### Phase 3 — Framework maturity

1. PeerResolver.
2. Service-oriented Context facade.
3. Plugin metadata.
4. Unified command execution pipeline.
5. Error taxonomy.
6. Metrics/tracing.

### Phase 4 — Ultroid-like ecosystem

Prioritas plugin:

```text
core
├── help
├── ping
├── alive
├── system
├── sudo

moderation
├── admin
├── locks
├── blacklist
├── filters

media
├── downloader
├── media
├── sticker

productivity
├── notes
├── afk
├── scheduler

fun
└── fun
```

Kemudian baru multi-account, external plugin repository, hot reload, web dashboard, Redis/PostgreSQL, dan distributed execution jika benar-benar dibutuhkan.

## 14. Conclusion

GoUltroid **sudah bukan sekadar skeleton**. Implementasi saat ini sudah memiliki fondasi yang cukup serius untuk menjadi UserBot Go yang setara secara konsep dengan Ultroid.

Yang tidak disarankan adalah mencoba menerjemahkan source Ultroid Python 1:1. Pendekatan yang lebih sehat adalah mempertahankan behavior/features yang diinginkan, lalu mengimplementasikan ulang framework dan plugin dengan API Go-native.

Target engineering yang tepat:

> **Ultroid-like behavior, Go-native architecture, deterministic command pipeline, durable state, fail-closed security, dan production-grade resource management.**

Dokumen ini menjadi baseline audit untuk perubahan berikutnya.
