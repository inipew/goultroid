# Audit total implementasi performa dan ketahanan

Tanggal audit: 17 September 2026.

Baseline repository: branch `test-next`, HEAD `691f19e`, ditambah seluruh perubahan staged dan unstaged pada worktree saat audit. Implementasi yang diaudit **belum merupakan commit**.

Dokumen acuan:

- [Pembahasan performa dan ketahanan](performance-and-resilience.md)
- [Spesifikasi teknis implementasi](performance-resilience-implementation-spec.md)

Status akhir: **belum siap merge**. Fondasi M0/M1 dan sebagian M2/M4 sudah dibuat, tetapi wiring produksi, klasifikasi operasi Telegram, rate limiting outbound, context propagation, architecture boundary, dan coverage migration belum memenuhi kontrak.

## 1. Metode dan batas audit

Audit dilakukan terhadap source aktual, diff dari HEAD, test baru, architecture tests, serta call site runtime. Tidak dilakukan koneksi ke Telegram produksi, profiling workload produksi, atau benchmark before/after yang dapat dipakai untuk klaim peningkatan.

Label bukti:

- **Source**: terlihat langsung pada kode;
- **Test**: dicakup test yang dijalankan;
- **Inference**: konsekuensi behavior dari wiring/interleaving source;
- **Missing evidence**: implementasi atau klaim belum mempunyai bukti.

Worktree saat audit bercampur antara staged dan unstaged changes. Dua file session besar juga staged tetapi tidak relevan terhadap fitur. Audit tidak mengubah source implementasi tersebut.

## 2. Hasil verifikasi

| Pemeriksaan | Hasil |
|---|---|
| Focused tests: core, plugin, telegram, database, rate limit, settings | Lulus |
| `go test -race ./...` | Lulus secara split pada snapshot terbaru; lihat catatan sandbox |
| Race report | Tidak ada race yang dilaporkan |
| `go vet ./...` | Lulus |
| `git diff --check` sebelum dokumen audit | Lulus |
| Production build | Belum dijalankan terpisah; package command terkompilasi dalam race suite |
| `golangci-lint` | Belum dijalankan |
| Benchmark before/after | Belum tersedia |
| Soak/failure injection | Belum tersedia |

Failure pada snapshot awal:

```text
TestLegacyFeatureDatabaseSurfaceIsExplicit:
internal/database contains unexpected production files [metrics.go]
```

Selama audit, worktree lain kemudian menambahkan `metrics.go` ke daftar infrastructure. Revalidasi `go test ./internal/architecture` lulus. Full race run pada snapshot terbaru meluluskan seluruh package kecuali dua test berbasis `httptest` yang tidak dapat membuka listener di sandbox. Kedua package tersebut kemudian dijalankan dengan izin localhost dan lulus dengan race detector:

```text
ok github.com/inipew/goultroid/internal/platform/network
ok github.com/inipew/goultroid/plugins/myxl
```

Dengan validasi split tersebut, tidak ada failure source/race yang tersisa pada snapshot terbaru saat audit selesai.

## 3. Ringkasan temuan berdasarkan severity

### Critical

#### C-01 — Seluruh operasi service diperlakukan sebagai read-only dan dapat diulang

**Source.** `retryOnFloodWait` di `internal/telegram/service.go` membuat `RPCMeta{Kind: RPCReadOnly}` untuk semua call site. Helper ini dipakai oleh operasi read maupun mutation: send, edit, delete, permission/admin, profile, dan operasi lain.

**Dampak.** Transient error pada mutation non-idempotent dapat menyebabkan executor mengulang request. Untuk send/forward/upload tertentu, outcome attempt pertama dapat ambiguous sehingga retry berpotensi menggandakan side effect.

**Status kontrak.** Contradictory. Spesifikasi mewajibkan operation kind eksplisit per method dan default `MaxAttempts: 1` untuk mutation non-idempotent.

**Perbaikan wajib.** Hentikan penggunaan helper tanpa metadata. Migrasikan call site per kelompok read-only, idempotent mutation, dan non-idempotent mutation. Jangan mengaktifkan transient retry pada kelompok terakhir sebelum idempotency semantics `gotd` diverifikasi.

#### C-02 — Timeout executor tidak diteruskan ke operasi service

**Source.** Signature helper adalah `op func()`, lalu adapter executor menerima `opCtx` tetapi memanggil `op()` dan membuang `opCtx`. Closure call site service memakai context luar.

**Dampak.** `WithDefaultTimeout` di executor dapat timeout secara internal, tetapi RPC aktual tidak menerima child context tersebut. Bila context luar tanpa deadline, request dapat tetap block melampaui default 10/15 detik. Retry/cancellation policy tidak menguasai operasi yang hendak dilindungi.

**Status kontrak.** Contradictory.

**Perbaikan wajib.** Signature call site harus `func(context.Context) (T, error)` dan seluruh raw API call memakai context yang diberikan executor. Compatibility helper lama hanya boleh delegasi bila context tidak dibuang.

### High

#### H-01 — Executor produksi dibuat tetapi tidak di-wire

**Source.** `Client` membangun dan menyimpan `RPCExecutor`. Di `Client.Run`, `Service` dan `Resolver` dibuat tetapi tidak menerima executor; `Resolver.SetExecutor` tidak dipanggil dan `Service` tidak memiliki field executor.

**Dampak.** Executor milik client tidak dipakai oleh runtime. Resolver jatuh ke compatibility `RetryRPC`; service membuat executor baru untuk setiap pemanggilan. Limiter dan metrics executor produksi tetap noop.

**Status kontrak.** Partial secara tipe, missing secara behavior produksi.

**Perbaikan wajib.** Constructor injection: satu executor per Telegram client/account, diberikan ke service dan resolver. Jadikan dependency wajib setelah migration; jangan menyisakan silent fallback pada production wiring.

#### H-02 — Outbound RPC belum terpusat

**Source.** Raw API masih dipakai pada main client warm-up, assistant client/interaction/peer/menu, dan jalur lain. Service masih memakai compatibility helper. `RetryRPC` masih mempunyai call site fallback.

**Dampak.** Timeout, retry, FloodWait, limiter, metrics, dan idempotency berbeda antar jalur. Pernyataan “seluruh outbound Telegram memakai satu executor” belum benar.

**Status kontrak.** Missing.

#### H-03 — Hierarchical RPC limiter hanya interface/noop

**Source.** Executor default memakai `NoopRPCLimiter`. Tidak ada adapter production dari `internal/services/ratelimit`, tidak ada global dimension dalam `RPCExecutor.Do`, `Family` tidak dipakai, dan belum ada atomic multi-dimension implementation.

**Dampak.** FloodWait penalty tidak memengaruhi request lain. Command limiter hanya membatasi ingress actor dan tidak melindungi kuota outbound Telegram gabungan.

**Status kontrak.** Missing.

#### H-04 — Limiter-denied tanpa `RetryAfter` tetap menjalankan RPC

**Source.** Pada `RPCExecutor.Do`, ketika `Allowed == false`, executor hanya menangani cabang `RetryAfter > 0`. Bila limiter mengembalikan denial invalid dengan zero duration, flow jatuh ke pemanggilan operation.

**Dampak.** Fail-open pada contract violation limiter.

**Status kontrak.** Incorrect defensive behavior.

**Perbaikan wajib.** Treat denial dengan `RetryAfter <= 0` sebagai typed internal/rate-limit error dan jangan menjalankan operasi. Tambahkan unit test.

#### H-05 — Singleflight resolve terlepas dari lifecycle/caller

**Source.** Resolver memanggil `resolveUsernameUser(context.Background(), ...)` dan versi chat dari closure singleflight.

**Dampak.** Satu caller dapat berhenti menunggu, tetapi underlying resolve terus hidup. Compatibility executor memberi batas default sehingga bukan leak permanen pada jalur sekarang, namun request tidak berhenti ketika resolver/client lifecycle berhenti dan semantics bergantung fallback executor.

**Status kontrak.** Partial/contradictory.

**Perbaikan wajib.** Miliki resolver lifecycle context bounded. Shared call tidak dibatalkan oleh satu waiter, tetapi harus dibatalkan oleh shutdown resolver/client dan memiliki hard timeout.

#### H-06 — PeerManager dapat menjadi jalur network di luar executor

**Source.** Setelah memory dan SQLite miss, resolver memanggil `peerManager.Resolve(ctx, cleaned)` sebelum explicit singleflight `contacts.resolveUsername`.

**Inference.** `peers.Manager.Resolve` dapat melakukan resolusi Telegram, sehingga network request berpotensi terjadi sebelum executor/singleflight yang baru dan kemudian diulang pada fallback miss.

**Status kontrak.** Needs verification; tidak boleh dianggap memory-only.

**Perbaikan wajib.** Verifikasi kontrak library. Gunakan hanya API cache/storage-only bila tersedia, atau jadikan satu-satunya network resolver di bawah executor dan singleflight.

#### H-07 — Panic plugin dipulihkan tetapi diam-diam dibuang di produksi

**Source.** `Scope.Go` sekarang recover, menghitung counter, dan melapor hanya jika reporter tidak nil. Tidak ada production call ke `SetPanicReporter`. Cleanup callback masih memakai `recover` yang membuang panic tanpa report.

**Dampak.** Process tidak crash, tetapi operator tidak mengetahui plugin gagal. Ini mengubah crash menjadi silent functional failure.

**Status kontrak.** Partial.

**Perbaikan wajib.** Inject reporter pada scope creation/manager wiring, expose panic count pada diagnostics, log stack secara aman, dan report cleanup panic.

#### H-08 — Architecture boundary diselesaikan dengan perubahan allowlist, tetapi keputusan perlu dibenarkan

**Source/Test.** Pada awal audit, file `internal/database/metrics.go` tidak termasuk generic database production surface. Selama audit, `internal/architecture/imports_test.go` diubah untuk memasukkannya dan focused architecture test kemudian lulus.

**Dampak.** Gate teknis kini hijau, tetapi perubahan allowlist sendiri bukan bukti bahwa ownership package telah diputuskan dengan benar.

**Status kontrak.** Mechanically resolved, design decision pending.

**Perbaikan wajib.** Dokumentasikan alasan metrics merupakan infrastructure DB. Jika tidak, pindahkan interface ke package observability yang sesuai dan inject ke DB. Pertahankan architecture test sebagai guardrail.

### Medium

#### M-01 — Default configuration masih mutable package global

`DefaultExecutorPolicy`, `DefaultClock`, `DefaultSleeper`, dan `DefaultResolverCacheConfig` diekspor sebagai `var`. Consumer/test dapat mengubah global dan menyebabkan behavior lintas client/test. Spesifikasi meminta default disalin pada construction dan tidak menjadi mutable runtime state.

Gunakan function pembuat default atau unexported immutable-by-convention values yang selalu disalin.

#### M-02 — Backoff bukan full jitter

Dengan `JitterFraction: 0.5`, implementasi menghasilkan delay pada kisaran sekitar 50–100% exponential cap, bukan full jitter `[0, cap]`. Test hanya memeriksa tidak negatif dan tidak melewati max, sehingga perbedaan semantics tidak terdeteksi.

Pilih satu algoritma dan dokumentasikan. Bila spesifikasi tetap full jitter, implementasi dan test harus menguji lower/upper distribution seam melalui deterministic random source.

#### M-03 — `MaxElapsed` tidak menjadi hard elapsed budget

Check hanya dilakukan sebelum transient backoff. Waktu RPC, limiter wait, FloodWait wait, dan stale refresh tidak dibandingkan menyeluruh dengan `MaxElapsed`. Default operation timeout saat ini lebih kecil dari default `MaxElapsed`, sehingga field tersebut sebagian redundant dan semantics-nya menyesatkan untuk policy custom.

Turunkan deadline internal `min(parent deadline, start+MaxElapsed, operation timeout)` atau check seluruh wait/attempt secara konsisten.

#### M-04 — Global dimension dan family metadata tidak digunakan

Executor hanya membentuk key method dan peer. `Family` tidak berpengaruh. Dengan limiter nyata nanti, account-global pressure tetap tidak terkoordinasi.

#### M-05 — Cache expiration mempunyai race logis

`PeerCache.Get` membaca entry dengan `RLock`, melepas lock, lalu bila expired mengambil write lock dan menghapus key tanpa memastikan entry masih sama. Concurrent `Set` dapat memasang entry baru di antara kedua lock, lalu `Get` menghapus entry baru tersebut.

Race detector tidak menemukan data race karena lock benar, tetapi semantics cache salah. Saat write lock diperoleh, baca ulang dan hapus hanya jika current entry masih expired/sama.

#### M-06 — Cache belum configurable melalui typed app config

Resolver selalu memakai `DefaultResolverCacheConfig`. Max entries dan TTL belum berasal dari `internal/config`; tidak ada wiring per environment/deployment.

#### M-07 — Negative cache tidak mencakup semua not-found hasil

Negative entry dibuat ketika shared call mengembalikan `ErrNotFound` atau error diklasifikasikan invalid. Classification terhadap error yang telah dibungkus berlapis harus diverifikasi. Hasil “resolved response tidak mengandung entity sesuai kind” memang menghasilkan `ErrNotFound`, tetapi test nyata untuk wrapped executor path belum cukup.

#### M-08 — Persistence error resolver diabaikan

`storage.Save` dan `SaveEntity` error dibuang. Resolve tetap boleh sukses ketika cache persistence gagal, tetapi kegagalan harus diobservasi agar cache miss berulang dapat didiagnosis.

#### M-09 — DB metrics belum production-ready

- observer belum di-wire;
- hanya beberapa operasi peer diinstrumentasi;
- `FindByUsername`/`SaveEntity` dan repository lain tidak dicakup;
- snapshot DB metrics belum masuk diagnostics;
- `DB.metrics` tidak dilindungi synchronization bila `SetMetrics` dipanggil bersamaan dengan operation;
- test hanya menguji collector, bukan query latency/error integration menyeluruh.

#### M-10 — Metrics RPC tidak di-wire dan kehilangan dimensi berguna

Client executor memakai noop metrics. `InMemoryRPCMetrics` mengabaikan method, attempt, wait scope, dan deferred flag meskipun diterima interface. Snapshot belum terhubung ke App diagnostics.

#### M-11 — Limiter lifecycle migration belum lengkap sebagai contract reusable

App sekarang mendaftarkan `Start`/`Close`, sehingga production cleanup worker dapat berjalan. Namun:

- `Start(ctx)` mengabaikan context parameter dan hanya berhenti melalui `Stop`;
- component tidak dapat restart setelah stop;
- `Close()` memakai unbounded background wait;
- constructor tests tidak memulai cleanup sehingga tidak menguji eviction;
- belum ada atomic multi-dimension reserve/penalize untuk RPC.

Sebagian poin boleh sesuai one-shot application lifecycle, tetapi harus dinyatakan eksplisit dan diuji.

#### M-12 — Cleanup `Scope.Close` masih membuat waiter goroutine per close pertama

Saat context timeout, waiter goroutine tetap hidup sampai semua plugin goroutine selesai. Hanya satu yang dibuat karena scope ditandai closed, sehingga bounded per scope, tetapi plugin yang tidak menghormati cancellation membuat waiter dan plugin goroutine tertinggal. Supervisor/reaper lifecycle yang dijanjikan belum ada.

### Low

#### L-01 — Benchmark belum membuktikan peningkatan

Benchmark baru hanya classification, arithmetic backoff lama, resolve `self`, memory hit, dan cache get. Tidak ada recorded baseline result, SQLite hit, Telegram miss fake, peer batch, dispatcher ingress, Task Engine, atau before/after comparison.

#### L-02 — Test high-cardinality hanya memeriksa total request

Case 16 memberi `PeerKey` sensitif lalu hanya mengecek `TotalRequests == 1`. Test tidak membuktikan bahwa peer key tidak tersimpan pada metrics/log.

#### L-03 — Naming success class

Success dicatat sebagai `RPCUnknown`. Ini mencampur sukses dengan error yang tidak terklasifikasi dalam `RequestsByClass`. Metrics perlu outcome terpisah atau `RPCSuccess`.

#### L-04 — Session artifacts ikut staged

Dua `codex-session-*.md` berukuran besar ikut staged. Tidak terkait implementasi dan berisiko masuk commit/PR. Audit tidak menghapusnya karena perubahan tersebut milik user.

## 4. Status per area spesifikasi

| Area | Status | Ringkasan |
|---|---|---|
| Baseline/guardrail tests | Implemented | Unit dan race suite lulus secara split; gap behavior/integration tetap ada |
| Default timeout helper | Implemented | Parent deadline dipertahankan; nil compatibility tersedia |
| Context audit hot path | Missing | Banyak `context.Background`, delayed work, dan direct goroutine belum diklasifikasi/dimigrasi |
| Task Engine timeout/panic | Baseline implemented | Tidak diregresikan oleh patch ini |
| Plugin panic boundary | Partial | Recovery/test ada; reporter production dan cleanup reporting belum ada |
| RPC executor core | Partial | Retry/classification/FloodWait/clock seam ada; beberapa semantics salah/tidak lengkap |
| Executor production wiring | Missing | Client executor tidak diberikan ke service/resolver |
| Explicit operation idempotency | Failed | Service compatibility helper memberi label read-only untuk semua operasi |
| Outbound Telegram coverage | Missing | Service compatibility, assistant, warm-up, dan raw API lain belum terpusat |
| FloodWait short wait | Implemented in core | Cancellable di executor |
| FloodWait long defer | Partial | Typed return ada; durable reschedule belum diimplementasikan |
| Hierarchical rate limiting | Missing | Interface/noop saja; tidak ada global/family/atomic reserve |
| Resolver memory cache | Implemented with defect | Bounded FIFO/TTL/negative cache ada; expiration race logis |
| Persistent-first username lookup | Partial | SQLite sebelum explicit resolve, tetapi PeerManager path perlu verifikasi network |
| Singleflight | Partial | Ada; underlying context tidak lifecycle-owned |
| Stale invalidate-refresh | Missing in call sites | Executor hook ada, metadata resolver/service tidak memakainya |
| DB instrumentation | Partial | Contract/collector dan beberapa peer operation saja; architecture gagal |
| Query budget | Partial | Settings cache test ada; belum lintas command/repository kritis |
| Batch writes | Baseline/partial | Peer batching sudah ada; belum ada audit domain lain |
| Goroutine supervisor | Missing | Belum ada lifecycle supervisor/restart budget/circuit |
| Graceful shutdown budget | Baseline implemented | Tidak ada implementasi phase metrics/flush tambahan dari proposal |
| Allocation optimization | Missing evidence | Tidak ada profile dan before/after |
| Observability integration | Missing | Collector baru tidak masuk App diagnostics/wiring |
| Soak/failure injection | Missing | Belum dijalankan |

## 5. Analisa performa aktual

### 5.1 Request Telegram

Potensi pengurangan request sudah ada melalui memory cache, persistent lookup, dan singleflight. Akan tetapi manfaat produksi belum dapat dinyatakan karena:

- executor client tidak di-wire;
- PeerManager mungkin melakukan network sebelum explicit singleflight;
- stale recovery belum terhubung;
- assistant memiliki resolver/interaction stack sendiri;
- tidak ada counter request sebelum/sesudah.

Kesimpulan: desain mengarah benar, bukti pengurangan request belum ada.

### 5.2 Alokasi

Ada regresi potensial: setiap `retryOnFloodWait` membuat executor, random source, dan object policy baru. Karena helper dipakai puluhan call site service, ini menambah allocation pada hot path. Satu executor per client harus dipakai ulang.

Cache sendiri bounded 1000 entries, tetapi `order` menyimpan tombstone sampai eviction berikutnya; ukuran slice dapat tumbuh akibat invalidate/expiry/set churn meskipun map bounded. Pada workload churn tinggi, backing array `order` dapat menjadi retention problem. Gunakan queue/index yang dapat compact atau lakukan periodic compaction berdasarkan rasio tombstone.

### 5.3 Goroutine

Panic recovery Scope membaik. Belum ada inventory/migrasi keseluruhan direct goroutine. Singleflight background request dan scope waiter masih merupakan pekerjaan yang dapat hidup setelah caller selesai. Tidak ada hasil soak/leak profile.

### 5.4 Blocking I/O

Executor memakai cancellable sleeper, tetapi service membuang child context sehingga timeout belum efektif. Database metrics tidak mengurangi blocking. Beberapa hot path lama, termasuk PM permit dan delayed deletes, masih memakai background context.

### 5.5 Database

Settings query-budget tests mengonfirmasi cache bekerja pada mock repository. Peer storage telah mendapat sebagian timing instrumentation. Tidak ada perubahan query plan, index evidence, pool benchmark, atau query budget command end-to-end. Karena metrics belum di-wire, produksi belum mengumpulkan bukti.

## 6. Analisa ketahanan aktual

### 6.1 Timeout/cancellation

Helper core benar dan teruji. Executor langsung juga menggunakan helper dengan benar. Jalur service compatibility melanggar propagation. Singleflight sengaja memisahkan cancellation satu waiter, tetapi belum mempunyai lifecycle owner.

### 6.2 Retry

Executor membatasi attempts dan melakukan classification. Permanent errors berhenti. Risiko terbesar adalah metadata idempotency call site yang salah. Retry engine yang benar dengan metadata salah tetap menghasilkan behavior berbahaya.

### 6.3 FloodWait

Executor mengubah FloodWait panjang menjadi typed rate-limit return dan menunggu pendek secara cancellable. Belum ada durable deferred attempt, limiter penalty efektif, atau satu policy di seluruh aplikasi.

### 6.4 Rate limiting

Ingress limiter tetap ada. Outbound limiter belum ada. Karena executor produksi noop, FloodWait pada satu request tidak menahan request method/peer/global lain.

### 6.5 Graceful shutdown

Baseline Runtime tetap kuat: quiesce/drain/stop/force-stop dan transport dihentikan setelah Runtime. Patch limiter telah dihubungkan sebagai resource component. Belum ada supervisor, phase metrics, resolver close, executor close, atau shared resolve cancellation saat shutdown.

### 6.6 Panic recovery

Task Engine baseline tetap mengubah panic menjadi terminal result. Plugin scope sekarang tidak merobohkan process, tetapi produksi tidak melaporkan detail panic. Recovery tanpa visibility belum memenuhi reliability operational.

## 7. Kualitas test implementasi

Hal yang baik:

- 16 skenario executor dibuat sebagai table-like individual tests;
- cancellation limiter/backoff dan parent deadline diuji;
- FloodWait pendek/panjang dan stale refresh diuji;
- cache bound, TTL, negative hit, invalidation, dan singleflight diuji;
- Scope panic/limit/cleanup order diuji;
- focused tests dan vet lulus.

Gap test prioritas:

1. mutation call site tidak pernah diuji agar tidak retry;
2. helper service tidak diuji menggunakan executor child context;
3. client wiring tidak diuji bahwa service/resolver berbagi executor yang sama;
4. limiter denial dengan zero/invalid delay tidak diuji;
5. atomic global/method/peer limiter belum ada;
6. cache expiration-vs-concurrent-set race logis tidak diuji;
7. singleflight lifecycle shutdown tidak diuji;
8. PeerManager network bypass tidak diuji;
9. reporter production wiring tidak diuji;
10. architecture test gagal;
11. high-cardinality test tidak benar-benar memeriksa stored labels;
12. tidak ada golden/request-count integration test per command;
13. tidak ada soak/goroutine profile;
14. benchmark tidak mempunyai hasil baseline.

## 8. Urutan perbaikan yang direkomendasikan

### P0 — Kembalikan correctness sebelum optimasi

1. Ubah helper service agar menerima executor context.
2. Beri `RPCMeta` eksplisit pada setiap call site; mutation non-idempotent satu attempt.
3. Wire satu executor client ke service dan resolver.
4. Tambahkan test yang membuktikan send/mutation tidak diulang pada ambiguous transient error.
5. Tegaskan keputusan ownership `metrics.go` dan pertahankan architecture test hijau.
6. Buat denied limiter selalu fail-closed.

### P1 — Lengkapi behavior produksi

1. Implementasikan adapter atomic hierarchical limiter dengan global/method/family/peer dimensions.
2. Wire RPC dan DB metrics ke diagnostics.
3. Beri resolver lifecycle context untuk shared singleflight.
4. Verifikasi/hilangkan network bypass PeerManager.
5. Hubungkan stale invalidation/refresh pada high-level operation.
6. Inject panic reporter ke seluruh plugin scope dan report cleanup panic.

### P2 — Tutup gap performa

1. Hilangkan executor-per-call.
2. Perbaiki cache expiry race dan bound/compact order tombstones.
3. Tambahkan typed cache config.
4. Instrument repository kritis dan buat query-budget integration tests.
5. Jalankan benchmark before/after dan CPU/heap/goroutine profiles.

### P3 — Selesaikan proposal jangka lanjut

1. Lifecycle supervisor dan restart budget;
2. deferred durable retry untuk FloodWait panjang;
3. context audit seluruh runtime;
4. shutdown phase metrics dan flush verification;
5. soak/failure injection.

## 9. Definition of merge-ready untuk patch saat ini

Patch minimum belum boleh digabung sebelum seluruh kondisi berikut terpenuhi:

- full `go test -race ./...` hijau pada snapshot final yang tidak berubah selama run;
- service operation memakai executor child context;
- tidak ada blanket `RPCReadOnly` pada mutation;
- client executor benar-benar di-wire dan dipakai ulang;
- unit test membuktikan mutation ambiguous tidak retry;
- limiter denial tidak fail-open;
- production panic mempunyai reporter/log/diagnostic;
- architecture boundary untuk DB metrics diselesaikan dan alasannya didokumentasikan;
- `go vet`, lint, dan build lulus;
- staged session artifacts ditinjau dan dikeluarkan bila bukan bagian PR.

Ini belum berarti seluruh roadmap selesai. Ini hanya batas aman untuk menggabungkan milestone awal tanpa correctness regression.

## 10. Kesimpulan

Implementasi menunjukkan kemajuan nyata pada test seams, timeout helper, struktur RPC executor, bounded resolver cache, singleflight, panic boundary, dan query-budget testing. Namun integrasi produksi belum mengikuti struktur tersebut. Risiko terbesar bukan kekurangan fitur, melainkan **abstraksi baru yang terlihat aktif tetapi sebenarnya tidak di-wire**, serta compatibility helper yang memberi klasifikasi read-only kepada mutation.

Prioritas berikutnya harus correctness dan wiring, bukan menambah cache, metrics, atau optimasi baru. Setelah satu executor benar-benar menjadi jalur wajib dan operation metadata benar, barulah pengukuran performa, hierarchical limiter, serta optimasi database dapat dipercaya.
