# Spesifikasi teknis implementasi performa dan ketahanan

Tanggal: 17 September 2026. Baseline wajib: branch `test-next`, commit `691f19e`.

Dokumen induk: [Performa dan ketahanan Ultroid-Go](performance-and-resilience.md).

Status: spesifikasi preskriptif untuk coding agent. Kata **HARUS**, **DILARANG**, dan **BOLEH** bersifat normatif. Bila source telah berubah setelah baseline, agent HARUS mengaudit ulang asumsi terkait sebelum mengedit.

## 1. Cara menggunakan spesifikasi ini

Agent tidak boleh mengimplementasikan seluruh dokumen dalam satu patch. Kerjakan satu milestone secara berurutan, jalankan acceptance test milestone tersebut, kemudian catat perubahan baseline sebelum melanjutkan.

Urutan wajib:

1. observability dan test seam;
2. context, timeout, dan panic boundary;
3. RPC executor tanpa mengubah semantics operasi;
4. migrasi call site Telegram per kelompok;
5. resolver cache dan singleflight;
6. database query/batch optimization;
7. lifecycle supervisor dan shutdown budget;
8. optimasi alokasi berdasarkan profil.

Agent HARUS membaca file yang akan diubah dan test terdekat secara lengkap. Jangan berasumsi nama API pada dokumen masih sama bila HEAD telah bergerak.

## 2. Invariant yang tidak boleh dilanggar

### 2.1 Arsitektur

- `internal/telegram` boleh bergantung pada `internal/core` dan service generik yang sudah diizinkan architecture test. Jangan membuat `internal/core` mengimpor `internal/telegram`.
- Plugin tidak boleh menerima raw `*tg.Client` bila operasi sudah tersedia melalui `core.TelegramServicer` atau adapter platform.
- Jangan membuat worker pool kedua. Finite work memakai `internal/taskengine`; long-lived loop memakai lifecycle component/supervisor.
- Jangan membuat database global atau Telegram client global.
- Generated plugin registration tetap dikelola oleh `tools/featuregen`.

### 2.2 Execution

- Satu `tasks.WorkSpec` adalah satu physical attempt.
- Accepted task HARUS berakhir tepat sekali pada terminal outcome.
- Queue timeout berbeda dari execution timeout.
- Retry yang ditunda HARUS menjadi attempt baru; jangan tidur lama di worker yang sama.
- Resource permit dilepas pada sukses, error, cancellation, timeout, dan panic.
- Callback `OnComplete` tidak boleh dipanggil sambil memegang lock engine.

### 2.3 Context

- Context request harus mengalir dari caller sampai RPC, DB, HTTP, process, dan filesystem.
- DILARANG mengganti context caller dengan `context.Background()` pada hot path.
- Nil context BOLEH dinormalisasi hanya pada public compatibility boundary; internal API baru sebaiknya menolak atau mendokumentasikan nil.
- Deadline anak tidak boleh melampaui deadline parent.
- Cancellation bukan failure yang boleh di-retry.

### 2.4 Telegram

- Semua outbound RPC runtime HARUS melewati satu policy executor setelah migrasi selesai.
- Raw Telegram error harus tetap dapat diperiksa dengan `errors.Is`/`errors.As`; wrapping wajib memakai `%w`.
- FloodWait tidak boleh menyebabkan disconnect/reconnect transport.
- Non-idempotent mutation tidak boleh diulang otomatis setelah outcome ambiguous.
- Stale peer hanya boleh memicu satu invalidate-and-refresh cycle per high-level operation.

### 2.5 Persistence

- SQLite transaction tidak boleh mencakup Telegram/network/process call.
- Durable state transition wajib fenced menggunakan revision, epoch, token, atau predicate state yang sesuai.
- Cache invalidation terjadi setelah commit berhasil.
- Error persistence tidak boleh diam-diam dibuang pada data durable.
- Writer concurrency SQLite tidak boleh diasumsikan meningkat dengan `MaxOpenConns`.

### 2.6 Lifecycle

- Constructor tidak boleh memulai goroutine baru.
- `Start` dan `Stop` harus idempotent sesuai contract component.
- Setiap goroutine mempunyai owner, cancellation source, join path, dan panic boundary.
- Shutdown caller yang timeout tidak boleh memulai teardown kedua.
- Telegram transport dan DB tetap hidup selama task yang sudah diterima masih melakukan drain.

## 3. Baseline source yang relevan

| Concern | Baseline |
|---|---|
| Retry umum | `internal/telegram/rpc_policy.go` — `RetryRPC` |
| Retry service | `internal/telegram/service.go` — generic `retryOnFloodWait`, lebih dari 30 call site |
| Resolver | `internal/telegram/resolver.go` — username Telegram-first, ID cache/storage-aware |
| Peer batching | `internal/telegram/dispatcher_peer.go` — batch 50, timeout 100 ms, pending 4096 |
| Rate limiter | `internal/services/ratelimit` — token bucket dan cleanup goroutine dari constructor |
| Task execution | `internal/taskengine/executor.go` — timeout dan panic-to-result sudah ada |
| Plugin goroutine | `internal/plugin/scope.go` — budget/join ada, panic recovery belum ada |
| Runtime shutdown | `internal/runtime/runtime.go` — quiesce/drain/stop/force-stop dengan global timeout |
| App shutdown | `internal/app/lifecycle.go`, `shutdown.go` — Runtime drain sebelum transport cancel |
| SQLite | `internal/database/db.go` — WAL, busy timeout, pooled connection |
| Diagnostics | `internal/core/metrics.go`, `internal/app/diagnostics.go` |

Agent HARUS mempertahankan test yang membuktikan baseline ini.

## 4. Package dan tipe target

### 4.1 Lokasi

Implementasi policy Telegram tetap di `internal/telegram`. Jangan membuat package generik `internal/retry` sebelum ada consumer non-Telegram yang benar-benar memakai semantics sama. Error classification Telegram bersifat platform-specific.

Susunan yang disarankan:

```text
internal/telegram/
  rpc_classification.go
  rpc_executor.go
  rpc_policy.go          # compatibility sementara, lalu diperkecil/dihapus
  rpc_metrics.go
  resolver.go
  resolver_cache.go
```

### 4.2 Generic return value

Go tidak mendukung generic method pada non-generic type. Karena call site Telegram mengembalikan banyak tipe, gunakan method non-generic untuk policy dan helper generic tingkat package:

```go
type RPCExecutor struct {
    limiter RPCRequestLimiter
    clock   Clock
    metrics RPCMetrics
    policy  RPCPolicies
}

func (e *RPCExecutor) Do(
    ctx context.Context,
    meta RPCMeta,
    operation func(context.Context) error,
) error

func ExecuteRPC[T any](
    ctx context.Context,
    executor *RPCExecutor,
    meta RPCMeta,
    operation func(context.Context) (T, error),
) (T, error)
```

`ExecuteRPC[T]` hanya menjembatani return value dan HARUS memanggil `executor.Do`; ia tidak boleh menduplikasi retry logic.

### 4.3 Metadata operasi

```go
type RPCOperationKind uint8

const (
    RPCReadOnly RPCOperationKind = iota + 1
    RPCIdempotentMutation
    RPCNonIdempotentMutation
)

type RPCMeta struct {
    Method       string
    Family       string
    PeerKey      string
    Kind         RPCOperationKind
    Timeout      time.Duration
    RetryPolicy  RetryPolicy
    RefreshPeer  func(context.Context) error
}
```

Ketentuan:

- `Method` berupa label bounded, misalnya `messages.sendMessage`, bukan string dinamis.
- `PeerKey` tidak boleh masuk metrics/log plaintext. Ia hanya untuk bucket internal dan boleh di-hash dengan process-local salt bila disnapshot.
- `RefreshPeer` opsional dan maksimal dipanggil sekali.
- `Timeout <= 0` memakai default berdasarkan family/kind.
- `Kind == 0` adalah validation error; jangan default diam-diam ke read-only.

### 4.4 Policy

```go
type RetryPolicy struct {
    MaxAttempts        int
    BaseDelay          time.Duration
    MaxDelay           time.Duration
    MaxElapsed         time.Duration
    InlineFloodWaitMax time.Duration
    JitterFraction     float64
}
```

Validasi saat construction:

- `MaxAttempts >= 1`;
- delay tidak negatif;
- `MaxDelay >= BaseDelay` bila keduanya non-zero;
- `0 <= JitterFraction <= 1`;
- `MaxElapsed > 0` untuk policy runtime;
- policy immutable setelah executor mulai dipakai.

Jangan menggunakan package-global mutable `DefaultRPCPolicy`. Default harus disalin ke executor saat wiring.

## 5. Algoritma wajib RPC executor

### 5.1 Pseudocode

```text
validate meta and executor
derive operation context:
  preserve shorter parent deadline
  otherwise apply default/meta timeout

for attempt = 1..MaxAttempts:
  if context cancelled/deadline: return context error
  acquire all limiter dimensions in deterministic order
  if limiter wait exceeds remaining deadline: return typed rate-limit error

  execute operation exactly once
  record latency and result class
  if nil: return success

  classify error
  if cancellation/deadline: return wrapped context error
  if stale peer and refresh not used:
      invalidate/refresh once
      continue only if operation kind permits retry
  if FloodWait:
      publish cooldown to limiter
      if wait > inline threshold: return RateLimitError(RetryAfter)
      if remaining deadline insufficient: return RateLimitError(RetryAfter)
      cancellable wait, then continue if attempts remain
  if transient:
      if operation kind is non-idempotent: return ambiguous operation error
      calculate exponential delay with full jitter and cap
      if MaxElapsed/deadline insufficient: return last error
      cancellable wait
      continue
  return permanent error

return last error with attempt metadata
```

### 5.2 Attempt semantics

`MaxAttempts` termasuk initial call. `MaxAttempts: 1` berarti tidak ada retry. Ini harus diuji eksplisit agar tidak terjadi off-by-one.

### 5.3 Backoff

Gunakan overflow-safe calculation. DILARANG melakukan `BaseDelay * (1 << attempt)` tanpa clamp sebelum shift pada attempt tak tervalidasi.

Full jitter:

```text
cap = min(MaxDelay, BaseDelay * 2^(attempt-1))
delay = random duration in [0, cap]
```

Random source dan sleeper harus injectable pada test. Test tidak boleh benar-benar tidur.

### 5.4 Matriks keputusan

| Error/class | Read-only | Idempotent mutation | Non-idempotent mutation |
|---|---|---|---|
| Context canceled | return | return | return |
| Deadline exceeded | return | return | return |
| Permission/auth/invalid | return | return | return |
| Transport sebelum request terkirim dan dapat dibuktikan | retry | retry | retry hanya bila transport memberi kepastian |
| Transport outcome ambiguous | retry | retry dengan idempotency protection | return ambiguous |
| FloodWait pendek | wait + retry | wait + retry | wait + retry hanya bila server memastikan request ditolak sebelum efek |
| FloodWait panjang | typed defer/return | typed defer/return | typed return |
| Stale peer | refresh + retry sekali | refresh + retry sekali | hanya bila request ditolak sebelum efek |

Jika library tidak menyediakan bukti “request belum diterapkan”, klasifikasikan sebagai ambiguous.

### 5.5 Typed errors

Gunakan atau perluas error domain yang sudah ada; jangan mematahkan `errors.Is(err, core.ErrRateLimit)`.

```go
type RPCFailure struct {
    Method     string
    Class      RPCErrorClass
    Attempts   int
    RetryAfter time.Duration
    Ambiguous  bool
    Err        error
}

func (e *RPCFailure) Error() string
func (e *RPCFailure) Unwrap() error
```

Error string tidak boleh menyertakan phone, token, message body, username, atau raw peer key.

## 6. Rate limiter contract

### 6.1 Jangan langsung memakai limiter saat ini untuk waiting

`ratelimit.Limiter.Take` menerima context tetapi saat ini tidak menunggu dan tidak membaca cancellation. Agent tidak boleh menganggap `Take` sebagai blocking acquire.

Tambahkan API terpisah:

```go
type Reservation struct {
    Allowed    bool
    RetryAfter time.Duration
}

type RPCRequestLimiter interface {
    Reserve(now time.Time, dimensions []LimitKey, cost int) Reservation
    Penalize(now time.Time, dimensions []LimitKey, retryAfter time.Duration)
}
```

Executor yang melakukan cancellable wait. Limiter hanya menghitung state di bawah lock; jangan tidur sambil memegang lock.

### 6.2 Atomic multi-dimension decision

Global, method, dan peer bucket harus dicek sebagai satu keputusan. DILARANG mengonsumsi global token lalu gagal pada peer bucket tanpa rollback. Implementasikan dua fase di bawah satu mutex:

1. refill dan hitung semua bucket;
2. bila semuanya cukup, kurangi seluruhnya; bila tidak, jangan mengubah token.

Ambil dimension dalam urutan canonical untuk hasil deterministik.

### 6.3 Lifecycle limiter

Baseline `ratelimit.New` langsung memulai cleanup goroutine. Migrasi target:

- `New` hanya membuat state;
- `Start(ctx)` memulai cleanup;
- `Stop(ctx)` cancel dan join;
- compatibility `Close()` memanggil stop bounded selama masa migrasi;
- register sebagai runtime-owned service atau milik component yang jelas.

Jangan ubah constructor lifecycle dan semua wiring dalam patch berbeda tanpa compatibility test.

## 7. Resolver cache contract

### 7.1 Cache key dan value

```go
type peerCacheKey struct {
    Kind normalizedPeerKind
    Ref  string
}

type peerCacheEntry struct {
    Prefix     string
    ID         int64
    AccessHash int64
    ExpiresAt  time.Time
    Negative   bool
}
```

Normalization:

- username: trim space, hapus satu leading `@`, lower-case;
- numeric ID: canonical base-10;
- `me` dan `self` tidak perlu disimpan;
- jangan menyamakan user dan channel yang memiliki ref ambigu tanpa kind.

### 7.2 Lookup order

```text
self literal
-> parse numeric
-> memory cache
-> SQLite/storage
-> singleflight Telegram resolve
-> persist result
-> publish memory cache
```

Catatan: baseline numeric ID menggunakan peer manager sebelum storage. Agent boleh mempertahankan peer manager sebagai memory layer, tetapi lookup Telegram network harus tetap berada setelah persistent storage.

### 7.3 Stale behavior

Saat high-level Telegram operation menerima stale-peer class:

1. invalidate memory entry;
2. invalidate persistent access hash yang tepat;
3. forced resolve satu kali;
4. retry operasi hanya sesuai matriks idempotency;
5. bila tetap stale, return tanpa loop.

### 7.4 Singleflight

Gunakan `golang.org/x/sync/singleflight` hanya jika dependency sudah diterima, atau implementasi kecil scoped pada resolver. Key wajib menyertakan kind dan normalized ref.

Caller yang cancel harus dapat berhenti menunggu shared result. Jangan membatalkan underlying resolve hanya karena satu dari beberapa waiter cancel. Underlying call menggunakan context yang terikat lifecycle dan bounded timeout; hasil tidak boleh dipublish setelah resolver ditutup.

### 7.5 Bounds

Cache HARUS mempunyai `MaxEntries`, TTL positive, negative TTL, dan deterministic eviction policy. Tidak boleh map tanpa batas. Default awal harus konservatif dan configurable melalui typed config, bukan environment read di package Telegram.

## 8. Context dan timeout contract

### 8.1 Helper

Letakkan helper generik di package yang tidak menyebabkan import cycle, atau private di consumer bila hanya dipakai satu package.

```go
func withDefaultTimeout(parent context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
    if parent == nil {
        parent = context.Background()
    }
    if _, exists := parent.Deadline(); exists || fallback <= 0 {
        return context.WithCancel(parent)
    }
    return context.WithTimeout(parent, fallback)
}
```

Selalu panggil cancel. Jangan memakai `context.WithoutCancel` pada runtime path kecuali durable commit protocol sudah memesan lane dan memberi deadline sendiri.

### 8.2 Audit kategori `context.Background()`

Setiap temuan harus diberi salah satu klasifikasi:

- `composition-root`: dibolehkan;
- `bounded-cleanup`: dibolehkan hanya dengan `WithTimeout`;
- `compatibility-nil`: sementara, diberi test;
- `hot-path-bug`: harus diganti parent context;
- `durability-lane`: dibolehkan bila lane memiliki capacity, deadline, dan shutdown join.

Jangan melakukan replacement mekanis seluruh repository.

### 8.3 Delayed action

Delayed delete/notification tidak boleh memakai goroutine + sleep tak terlacak. Pilihan:

- delay sangat pendek dan lifecycle-bound: scheduler timer yang dimiliki scope;
- perlu bertahan restart: durable job;
- best effort: Task Engine maintenance task dengan queue deadline.

## 9. Panic recovery contract

### 9.1 Utility

```go
type PanicReport struct {
    Owner     string
    Component string
    Value     any
    Stack     []byte
    At        time.Time
}

type PanicReporter interface {
    ReportPanic(PanicReport)
}
```

`debug.Stack()` hanya dibuat ketika panic benar-benar terjadi.

### 9.2 `plugin.Scope.Go`

Perubahan minimum:

- wrap `fn(s.ctx)` dengan defer recovery;
- selalu decrement `activeGoroutines`, release resource, dan `wg.Done`;
- report panic ke injected reporter;
- panic tidak boleh dilempar ulang dari plugin goroutine;
- tambahkan counter/snapshot panic per owner;
- test panic memastikan scope masih dapat `Close` dan tidak leak.

Jangan mengubah signature `Go` menjadi mengembalikan runtime error dari goroutine; return saat ini hanya menyatakan admission. Runtime failure dilaporkan melalui reporter/diagnostics.

### 9.3 Long-lived worker

Restart hanya untuk error/panic yang diklasifikasikan transient. Maksimal restart dalam sliding window harus bounded. Setelah budget habis, health menjadi unhealthy/degraded sesuai criticality dan worker berhenti.

## 10. Database implementation contract

### 10.1 Instrumentation boundary

Instrumentasi sebaiknya berada pada repository method atau wrapper DB yang mengetahui operation label bounded. Jangan memakai normalized SQL penuh sebagai label metrics.

```go
type DBMetrics interface {
    Observe(operation string, elapsed time.Duration, err error)
}
```

Label contoh: `peer.find`, `peer.save_batch`, `settings.resolve`, `pmpermit.warn_ids`.

### 10.2 Query budget test

Gunakan counting wrapper/fake pada repository interface. Jangan mengandalkan parsing SQLite logs. Test harus memverifikasi batas atas, misalnya repeated cached settings resolve menghasilkan nol query setelah warm-up.

### 10.3 Batch write

Batch queue harus mempunyai:

- item capacity;
- byte capacity bila payload variable;
- maximum batch size;
- maximum flush delay;
- flush pada shutdown;
- explicit behavior ketika penuh: block cancellable, reject, atau coalesce;
- terminal acknowledgement untuk durable writes.

Peer metadata boleh coalesce last-write-wins per key. Job result/outbox tidak boleh di-drop atau di-coalesce tanpa protocol domain.

### 10.4 SQLite tuning

Jangan mengubah pool hanya dari jumlah CPU. Buat benchmark untuk:

- read-only parallel;
- one writer + readers;
- burst writers;
- busy timeout/cancellation;
- shutdown dengan transaction aktif.

Semua rows, statement, transaction, dan dedicated connection harus ditutup pada semua path.

## 11. Lifecycle supervisor contract

### 11.1 API minimum

```go
type RestartPolicy uint8

const (
    NeverRestart RestartPolicy = iota
    RestartTransient
)

type WorkerSpec struct {
    Name          string
    Restart       RestartPolicy
    MaxRestarts   int
    RestartWindow time.Duration
    Run           func(context.Context) error
}
```

Supervisor lifecycle:

```text
New -> Start(root context) -> Go/Register worker -> Quiesce -> Stop/join
```

Lebih aman mendaftarkan worker sebelum `Start`. Bila dynamic registration dibutuhkan plugin, registration ditutup saat quiesce.

### 11.2 Jangan migrasikan transport ke Task Engine

Telegram `client.Run`, EventBus workers, persistence pump, and outbox loop adalah long-lived service. Mereka tidak menggunakan physical task slot sepanjang umur proses.

### 11.3 Shutdown budget

Runtime sudah mempunyai global 30 detik. Jangan menambahkan timeout 30 detik baru pada setiap component. Turunkan child deadline dari remaining global budget.

App shutdown HARUS mempertahankan urutan:

```text
dispatcher ingress quiesce
-> runtime quiesce/drain/stop
-> transport cancel/join
-> logger sync
```

Jika DB close belum dimiliki Runtime pada HEAD baru, tempatkan setelah seluruh persistence consumer stop dan sebelum logger finalization.

## 12. Metrics dan cardinality

Perluas interface dengan hati-hati. Bila menambah banyak method akan mematahkan semua fake, buat interface terpisah:

```go
type RPCMetrics interface {
    ObserveRequest(method string, class RPCErrorClass, attempt int, elapsed time.Duration)
    ObserveWait(scope string, elapsed time.Duration)
    ObserveFloodWait(method string, retryAfter time.Duration, deferred bool)
}
```

Aturan label:

- boleh: method tetap, family, error class, attempt bucket, outcome, cache layer;
- dilarang: user/chat/task/job ID, username, URL, filename, raw error text;
- histogram buckets ditentukan dari SLO, bukan default acak;
- diagnostics snapshot harus menyalin state dan tidak mengembalikan map mutable internal.

## 13. Urutan migrasi call site Telegram

### Tahap A — Read-only

Migrasikan dan uji terlebih dahulu:

- resolve username;
- get dialogs/contacts/profile/info;
- download metadata dan read operations.

Semantics retry paling aman dan menghasilkan data cache/metrics awal.

### Tahap B — Idempotent mutation

Contoh hanya setelah memverifikasi semantics Telegram method:

- setting state yang mengembalikan `*_NOT_MODIFIED` sebagai success;
- delete yang menggunakan stable message IDs dan menganggap already absent sebagai success;
- permission update dengan desired-state semantics.

### Tahap C — Non-idempotent mutation

Send message/media, forward, dan operasi yang dapat menggandakan efek HARUS default `MaxAttempts: 1` untuk transient ambiguous failure. Retry hanya setelah tersedia random/idempotency identifier yang dipertahankan antar attempt dan library/API menjamin dedup semantics.

Agent DILARANG menyimpulkan “Telegram send aman di-retry” hanya karena request memiliki random ID; verifikasi bagaimana `gotd` membangun dan mempertahankan ID pada closure setiap attempt.

## 14. Perubahan file per milestone

### M0 — Test seam dan metrics

Kemungkinan file:

- `internal/telegram/rpc_policy.go` dan test;
- `internal/core/metrics.go` atau interface metrics khusus;
- fake clock/sleeper di test-only helper;
- benchmark baru di package terkait.

Tidak boleh mengubah behavior produksi pada M0.

### M1 — Timeout dan panic

- `internal/plugin/scope.go` + lifecycle tests;
- hot-path `context.Background()` yang telah diklasifikasi;
- process/network service timeout tests;
- tidak menyentuh resolver cache dulu.

### M2 — RPC executor

- tambah executor/classification/metrics;
- wiring di `internal/telegram/client.go` dan service/resolver constructor;
- compatibility wrapper `RetryRPC` boleh delegasi sementara;
- `retryOnFloodWait` boleh delegasi sementara, tidak boleh menyimpan logic sendiri.

### M3 — Call-site migration

- migrasikan kelompok A/B/C dalam commit berbeda;
- setiap method mempunyai `RPCMeta` eksplisit;
- hapus helper lama hanya setelah `rg` menunjukkan nol call site produksi.

### M4 — Resolver cache

- cache terpisah dari SQLite storage;
- config dan wiring;
- singleflight, invalidation, close behavior;
- benchmark hit/miss.

### M5 — DB dan lifecycle

- query instrumentation dan budget tests;
- batch/coalesce yang domain-safe;
- limiter constructor lifecycle;
- supervisor dan shutdown phase metrics.

## 15. Test wajib

### 15.1 RPC executor table test

Minimal case:

1. success initial attempt;
2. transient then success;
3. transient exhaust;
4. permanent no retry;
5. canceled before first attempt;
6. canceled during limiter wait;
7. canceled during backoff;
8. parent deadline shorter than default;
9. FloodWait below threshold;
10. FloodWait above threshold;
11. stale peer refresh once;
12. stale peer twice stops;
13. non-idempotent ambiguous no retry;
14. attempts count includes initial request;
15. jitter bounded;
16. no metrics high-cardinality value.

### 15.2 Resolver test

- memory hit: zero SQLite dan zero RPC;
- persistent hit: one SQLite, zero RPC, then memory hit;
- miss burst N caller: one active RPC;
- one waiter cancel tidak membatalkan waiter lain;
- negative hit tidak mengulang RPC sebelum TTL;
- expiry melakukan refresh;
- stale invalidation menghapus memory dan persistent entry;
- cache never exceeds max entries;
- close/cancel tidak leak goroutine.

### 15.3 Panic/goroutine test

- plugin panic tidak crash test process;
- cleanup dan resource release tetap jalan;
- active count kembali nol;
- restart budget tidak off-by-one;
- Stop dengan deadline tidak meninggalkan waiter baru;
- repeated Start/Stop sesuai contract.

### 15.4 Shutdown test

- ingress ditutup sebelum drain;
- accepted RPC dapat selesai karena transport masih hidup;
- new admission ditolak;
- persistence flush sebelum DB close;
- forced stop menghasilkan terminal outcomes;
- concurrent Shutdown callers menerima result yang sama;
- short caller timeout tidak membatalkan actual teardown owner.

## 16. Larangan implementasi

Agent DILARANG:

- menambahkan `recover()` yang membuang panic tanpa log/metric/result;
- memakai `time.Sleep` atau `time.After` untuk retry pada hot loop tanpa cancellation;
- retry `for {}` tanpa max attempts dan max elapsed;
- membuat goroutine per retry/FloodWait;
- memegang mutex ketika sleep, RPC, DB, callback, atau channel blocking;
- memakai unbounded map/channel/queue/cache;
- menggunakan raw ID sebagai metrics label;
- menambahkan cache authorization tanpa invalidasi kuat;
- mengubah semua `context.Background()` secara mekanis;
- menganggap semua Telegram mutation idempotent;
- menutup transport sebelum runtime drain;
- menambah dependency baru jika standard library atau dependency existing cukup, tanpa alasan terukur;
- mengoptimasi dengan `sync.Pool` tanpa benchmark before/after;
- mencampur perubahan behavior, schema, lifecycle, dan optimization dalam satu commit besar;
- menghapus compatibility API sebelum semua call site dan tests bermigrasi.

## 17. Checklist agent sebelum menyerahkan patch

### Scope

- [ ] Satu milestone saja yang dikerjakan.
- [ ] Baseline/HEAD dan dirty worktree diperiksa.
- [ ] Perubahan user yang tidak terkait tidak disentuh.
- [ ] Architecture boundary tetap lulus.

### Correctness

- [ ] Context parent diteruskan.
- [ ] Semua timer dihentikan dengan benar.
- [ ] Semua goroutine dapat dicancel dan dijoin.
- [ ] Semua lock dilepas sebelum external call.
- [ ] Retry sesuai idempotency dan error class.
- [ ] Queue/cache/map mempunyai bound.
- [ ] Error wrapping mempertahankan cause.
- [ ] Panic menghasilkan diagnostic dan terminal state.

### Verification

- [ ] Focused unit tests lulus.
- [ ] `go test -race ./...` lulus.
- [ ] `go vet ./...` lulus.
- [ ] `golangci-lint run --timeout=3m` lulus bila tersedia.
- [ ] `go build -v ./cmd/goultroid` lulus.
- [ ] Benchmark relevan dilaporkan sebelum/sesudah bila mengklaim performa.
- [ ] Goroutine/heap profile dilaporkan bila mengklaim leak/alokasi membaik.
- [ ] Generated registration diperbarui hanya bila plugin berubah.

## 18. Format laporan implementasi

Setiap patch yang mengikuti spesifikasi ini harus melaporkan:

```text
Milestone:
Baseline commit:
Masalah yang diperbaiki:
Invariant yang terdampak:
File yang diubah:
Perubahan behavior:
Compatibility/migration:
Test yang ditambahkan:
Perintah verifikasi dan hasil:
Benchmark before/after:
Risiko tersisa:
Rollback plan:
```

Tanpa benchmark, gunakan kalimat “mengurangi potensi” atau “membatasi”, bukan “lebih cepat” atau persentase peningkatan.

## 19. Acceptance akhir lintas milestone

Implementasi keseluruhan baru dinyatakan selesai ketika:

- hanya ada satu outbound Telegram retry/FloodWait policy;
- seluruh method Telegram telah diberi operation kind eksplisit;
- resolver steady-state menggunakan memory/persistent cache sebelum network;
- rate limiter global/method/peer mengambil keputusan atomik dan bounded;
- tidak ada runtime goroutine tanpa owner/cancel/join/panic boundary;
- hot-path external I/O mempunyai deadline dan mengikuti cancellation;
- retry panjang menjadi deferred attempt, bukan worker sleep;
- shutdown mempertahankan dependency sampai consumer selesai dan memenuhi hard deadline;
- query/request count serta allocation mempunyai regression benchmark/test;
- race, vet, lint, build, failure injection, dan soak test lulus;
- dokumentasi induk diperbarui dari “proposal” menjadi “implemented” hanya untuk bagian yang benar-benar telah diverifikasi.
