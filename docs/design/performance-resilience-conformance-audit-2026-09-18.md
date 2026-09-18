# Audit ulang konformitas performa dan ketahanan

Tanggal audit: 18 September 2026

Baseline repository: `447d3c5` (`test-next`) dengan worktree yang belum bersih

Dokumen acuan:

- `docs/design/performance-and-resilience.md`;
- `docs/design/performance-resilience-implementation-spec.md`;
- `docs/design/performance-resilience-implementation-audit.md`.

## 1. Kesimpulan

Implementasi **belum sesuai seluruh dokumen** dan belum memenuhi acceptance akhir lintas milestone. Kondisinya lebih maju daripada audit 17 September 2026: executor sudah di-wire ke service dan resolver, hard elapsed budget telah diterapkan, cache race diperbaiki, persistence error dilaporkan, panic reporter produksi tersedia, deferred durable retry tersedia, serta supervisor dan metrics collector telah dibuat.

Namun status keseluruhan masih **partial / not merge-ready terhadap spesifikasi penuh**. Empat blocker utama adalah:

1. tidak seluruh outbound Telegram melewati satu `RPCExecutor`;
2. limiter RPC produksi tetap `NoopRPCLimiter`, sehingga hierarchical global/family/method/peer limit belum aktif;
3. supervisor sudah terdaftar sebagai component tetapi belum memiliki worker produksi yang dikelola;
4. bukti performa, query/request regression budget, soak berdurasi representatif, dan observability lintas area belum lengkap.

Test hijau membuktikan implementasi yang diuji tidak race dan dapat dibangun. Test tersebut belum membuktikan konformitas arsitektur/behavior yang belum mempunyai guard test.

## 2. Metode audit

Audit dilakukan dari source saat ini, tidak hanya dari nama type atau hasil audit lama:

- membaca ulang ketiga dokumen acuan;
- menelusuri wiring produksi dari `app` ke Telegram client, service, resolver, limiter, jobs, plugin scope, runtime, dan diagnostics;
- menginventarisasi direct RPC, goroutine, `context.Background`, timer, dan metrics;
- memeriksa classification/idempotency, timeout, cancellation, FloodWait, retry, cache, database, panic, dan shutdown;
- menjalankan race suite, vet, build, architecture tests, soak-test yang tersedia, benchmark, dan diff check.

Angka hasil pencarian statis bukan jumlah defect final. Pada source produksi ditemukan 58 file yang memuat `context.Background()`, 20 file yang memuat `go func`, dan 5 file yang memuat `time.After`. Setiap kemunculan tetap perlu diklasifikasikan; angka ini menunjukkan audit context/goroutine yang diwajibkan dokumen belum dapat dinyatakan selesai.

## 3. Hasil verifikasi aktual

| Pemeriksaan | Hasil |
|---|---|
| `go test -race ./...` | Lulus seluruh repository |
| Architecture tests | Lulus sebagai bagian race suite |
| `go vet ./...` | Lulus |
| `go build -v ./cmd/goultroid` | Lulus |
| `git diff --check` | Lulus |
| `golangci-lint run --timeout=3m` | Tidak dijalankan: binary tidak terpasang |
| `TestResilience_CombinedFailureInjectionSoak` | Lulus, tetapi selesai sekitar 0,03 detik; ini stress unit singkat, bukan soak 30–60 menit |
| Benchmark Telegram | Selesai; snapshot tersedia, tanpa baseline before/after |
| Benchmark Task Engine | Tidak selesai; dihentikan setelah `BenchmarkB4_PeriodicDueBurst` tidak menghasilkan hasil selama total sekitar 110 detik |
| Benchmark settings | Tidak tercapai karena rangkaian benchmark dihentikan pada Task Engine |
| Goroutine/heap profile | Belum tersedia |

Snapshot benchmark yang berhasil:

| Benchmark | Hasil |
|---|---:|
| `BenchmarkRPCPolicyClassification` | 849,6 ns/op; 84 B/op; 7 allocs/op |
| `BenchmarkRPCRetryBackoffCalculation` | 0,2447 ns/op; 0 B/op; 0 allocs/op |
| `BenchmarkResolver_Self` | 9,569 ns/op; 0 B/op; 0 allocs/op |
| `BenchmarkResolverMemoryHit` | 192,5 ns/op; 69 B/op; 3 allocs/op |
| `BenchmarkPeerCache_GetSet` | 90,05 ns/op; 0 B/op; 0 allocs/op |
| `BenchmarkB0_IdleOverhead` | 3060 ns/op; 1136 B/op; 7 allocs/op |
| `BenchmarkB1_TinyEphemeralTask` | 8997 ns/op; 2893 B/op; 20 allocs/op |
| `BenchmarkB2_IOCommandMixFairness` | 9703 ns/op; 2805 B/op; 20 allocs/op |
| `BenchmarkB3_CPUAndInteractiveIsolation` | 11855 ns/op; 3087 B/op; 21 allocs/op |

Karena tidak ada baseline pembanding, hasil ini tidak boleh digunakan untuk klaim “lebih cepat” atau persentase peningkatan.

## 4. Rekonsiliasi terhadap audit sebelumnya

| Temuan audit lama | Status sekarang | Bukti/ringkasan |
|---|---|---|
| C-01 semua service dianggap read-only | Sebagian diperbaiki | Helper read-only, idempotent, dan non-idempotent sudah terpisah; legacy helper dan call site mentah masih ada |
| C-02 child timeout tidak diteruskan | Diperbaiki pada helper baru | Helper menerima `opCtx`; legacy `retryOnFloodWait` masih mengabaikannya |
| H-01 executor tidak di-wire | Diperbaiki | `client.go:209-220` memasang executor ke service dan resolver |
| H-02 outbound belum terpusat | Masih terbuka | Service tertentu, assistant, warm-up, upload/download, dan peer-manager path masih bypass |
| H-03 limiter hanya interface/noop | Masih terbuka | Konstruktor client tidak mengisi `Limiter`; fallback executor adalah noop |
| H-04 limiter denial nol tetap menjalankan RPC | Diperbaiki | Executor sekarang langsung mengembalikan typed failure |
| H-05 singleflight detached | Sebagian diperbaiki | Shared call memakai lifecycle context dan timeout 15 detik; fallback background masih ada |
| H-06 PeerManager mungkin network di luar executor | Masih terbuka | `peerManager.Resolve` tetap dijalankan sebelum explicit executor resolve |
| H-07 panic reporter tidak di-wire | Diperbaiki | Reporter manager/scope dan supervisor sudah di-wire |
| H-08 allowlist architecture | Belum ditutup secara desain | Test lulus, tetapi ADR/justifikasi boundary metrics belum ditambahkan |
| M-01 mutable globals | Masih terbuka | `DefaultExecutorPolicy`, clock/sleeper, dan cache config masih mutable package globals |
| M-02 bukan full jitter | Masih terbuka | Backoff memakai rentang `[exp-jitter, exp)`, bukan `[0, cap]` |
| M-03 `MaxElapsed` bukan hard budget | Diperbaiki | Timeout dibatasi `MaxElapsed` dan dicek sebelum wait/retry |
| M-04 dimension global/family tidak dipakai | Sebagian diperbaiki | Executor membentuk dimensions; limiter produksinya noop |
| M-05 cache expiry race | Diperbaiki | Entry diperiksa ulang saat write lock |
| M-06 cache config typed app | Masih terbuka | Client menggunakan `NewResolver` dengan global default |
| M-07 negative cache tidak lengkap | Sebagian diperbaiki | Typed not-found/invalid ditangani, tetapi seluruh sumber miss belum terbukti konsisten |
| M-08 persistence error diabaikan | Diperbaiki | Resolver sekarang melaporkan error melalui logger |
| M-09 DB metrics belum production-ready | Sebagian diperbaiki | Collector di-wire, tetapi coverage dan dimensi minimum belum lengkap |
| M-10 RPC metrics tidak di-wire | Sebagian diperbaiki | Collector di-wire; belum masuk `DiagnosticsSnapshot` dan coverage outbound belum penuh |
| M-11 limiter lifecycle belum lengkap | Sebagian diperbaiki | Limiter command punya Start/Stop, tetapi bukan limiter atomic RPC yang dipakai executor |
| M-12 waiter goroutine cleanup | Perlu audit lanjutan | Tidak cukup bukti untuk menyatakan seluruh close path bebas waiter leak |
| L-01 benchmark evidence | Masih terbuka | Snapshot ada, before/after dan profile tidak ada |
| L-02 high-cardinality test lemah | Masih terbuka | Belum ada bukti menyeluruh untuk seluruh metrics labels |
| L-03 success class naming | Masih terbuka | Success masih dicatat sebagai `RPCUnknown` |
| L-04 session artifacts staged | Masih terbuka | Dua file `codex-session-*.md` masih staged/uncommitted |

## 5. Temuan blocker dan high severity

### B-01 — Outbound Telegram belum terpusat

Target dokumen adalah satu policy retry/FloodWait untuk seluruh outbound runtime. Saat ini:

- warm-up menjalankan goroutine langsung dan memanggil `MessagesGetDialogs` pada `client.go:258-270`;
- `Service.GetMessage` memanggil RPC mentah pada `service.go:704` dan `service.go:733`;
- `GetFullUser`, `ResolveUsername`, dan `GetFullChat` memanggil RPC mentah pada `service.go:1235-1273`;
- `profile_photo.go` masih mempunyai raw `UsersGetFullUser`;
- assistant interaction memanggil callback/edit/delete/get/send RPC secara langsung, misalnya `interaction/message.go:184`, `224`, `302-309`, `361-366`, dan `440`;
- assistant peer fetcher memanggil user/channel RPC langsung;
- upload/download builder melakukan external I/O langsung tanpa metadata executor yang seragam.

Dampaknya: timeout, retry, FloodWait, limiter, classification, metrics, dan stale refresh berbeda menurut jalur. Acceptance “hanya satu outbound Telegram retry/FloodWait policy” gagal.

### B-02 — Legacy retry policy masih hidup dan salah meneruskan context

`retryOnFloodWait` pada `service.go:97-127`:

- membuat executor baru per panggilan;
- memberi semua operasi kind `RPCReadOnly`;
- callback menerima child context tetapi memanggil `op()` tanpa context tersebut;
- masih digunakan `PurgeMessagesSafe` pada `purge_safe.go:67` dan `87`.

Ini melanggar single executor, explicit operation metadata, dan child-timeout propagation. Helper harus dihapus setelah dua call site dimigrasikan ke executor milik service.

### B-03 — Hierarchical RPC limiter belum aktif

Executor telah membentuk key global/family/method/peer, tetapi `NewClient` pada `client.go:147-150` tidak mengisi `RPCExecutorConfig.Limiter`. `NewRPCExecutor` menggantinya dengan `NoopRPCLimiter` pada `rpc_executor.go:183-186`.

Limiter di `internal/services/ratelimit` hanya melakukan `Take` satu dimension per call. Ia belum mengimplementasikan atomic reserve seluruh dimensions dan belum menjadi adapter `RPCRequestLimiter`. Karena itu:

- tidak ada proteksi kuota Telegram produksi;
- penalti FloodWait tidak mempengaruhi request berikutnya;
- global/family/method/peer decision tidak atomic;
- test executor dengan fake limiter tidak membuktikan wiring produksi.

### B-04 — Supervisor belum mengawasi worker produksi

Supervisor dibuat dan diregistrasikan pada `app.go:272-277`, tetapi pencarian source produksi tidak menemukan `supervisor.Register(...)` atau `supervisor.Go(...)`. Direct goroutine tetap tersebar, termasuk warm-up Telegram dan worker subsystem lain.

Keberadaan component tidak memenuhi fase migrasi. Acceptance “tidak ada runtime goroutine tanpa owner/cancel/join/panic boundary” belum tercapai.

### H-01 — Resolver masih mempunyai kemungkinan network bypass

Urutan cache memory dan persistent lookup sudah benar. Namun `peerManager.Resolve(ctx, cleaned)` dipanggil sebelum explicit singleflight `contacts.resolveUsername`. Bila implementasi dependency tersebut melakukan network, request itu berada di luar executor, cache singleflight, dan metrics pusat. Contract ini perlu dibuktikan atau peer-manager lookup harus dibatasi ke local lookup.

Shared singleflight sudah memakai lifecycle context dengan timeout 15 detik, tetapi fallback `context.Background()` tetap ada bila lifecycle context nil. Production constructor saat ini membuat lifecycle context, jadi fallback terutama compatibility risk.

### H-02 — Stale refresh hook executor belum dipakai call site produksi

`RPCMeta.RefreshPeer` tersedia dan teruji, tetapi tidak ditemukan pemakaian produksi yang memasangnya. Assistant mempunyai retry stale tersendiri; service melakukan invalidation setelah error tanpa standard one-shot executor refresh. Acceptance stale invalidate-refresh belum terpenuhi secara terpusat.

### H-03 — Context, blocking I/O, dan direct goroutine audit belum selesai

Masih ada banyak runtime `context.Background`, `go func`, dan `time.After`. Contoh yang perlu dimigrasikan atau didokumentasikan sebagai lifecycle root:

- Telegram warm-up goroutine;
- delayed work pada pmpermit, broadcast, dan userlog;
- dispatcher peer persistence menggunakan background timeout;
- assistant lifecycle fallback background;
- file upload/download dan profile photo I/O yang belum memakai policy timeout per ukuran.

Tidak semua `context.Background()` salah. Defectnya adalah klasifikasi owner, cancel, join, timeout, dan panic boundary belum lengkap dan belum dijaga test.

### H-04 — Observability minimum belum tersedia dalam satu diagnostics snapshot

DB dan RPC in-memory collectors sudah dibuat dan dapat diakses lewat accessor `App.DBMetrics()` dan `App.RPCMetrics()`. Namun `DiagnosticsSnapshot` pada `internal/app/diagnostics.go` belum memuat:

- RPC request/error/retry/FloodWait/limiter/latency;
- resolver cache hit/miss/negative/singleflight/invalidation;
- DB busy/locked/rollback/batch size dan latency distribution;
- supervisor worker/restart/panic/leak;
- shutdown phase duration/forced finalization/component timeout.

Collector DB saat ini terutama di-instrument pada peer storage; ini belum sama dengan instrumentation boundary seluruh repository kritis.

## 6. Status per area dokumen

| Area | Status | Penilaian |
|---|---|---|
| Baseline inventory | Partial | Inventaris parsial ada; profile idle/steady/burst/cancel/shutdown belum ada |
| Default timeout helper | Implemented | Parent deadline dipertahankan |
| Context propagation | Partial | Helper baru benar; raw/fallback/delayed paths masih banyak |
| Explicit Telegram idempotency | Partial | Banyak service method sudah classified; semua method belum |
| Central RPC executor | Partial | Core dan wiring utama ada; coverage outbound belum penuh |
| Retry bounded | Mostly implemented in core | Attempt dan elapsed bounded; legacy helper tetap ada |
| Backoff full jitter | Missing | Formula bukan full jitter yang dipreskripsikan |
| FloodWait pendek | Implemented in core | Wait cancellable dan bounded |
| FloodWait panjang | Implemented for durable jobs | `RateLimitError` dapat menjadi deferred durable occurrence |
| Rate limiting hierarchy | Missing in production | Executor memakai noop limiter |
| Resolver cache-first | Mostly implemented | Memory/persistent/negative/singleflight/bounds ada |
| Resolver stale recovery | Partial | Invalidasi ada; executor refresh hook tidak dipakai produksi |
| DB repeated query reduction | Partial | Settings budget dan peer cache/batch ada; audit N+1 lintas domain belum selesai |
| DB instrumentation | Partial | Collector production ada, coverage/metrics minimum belum lengkap |
| Panic recovery plugin | Implemented | Handler/go/cleanup reporting tersedia |
| Lifecycle supervisor | Infrastructure only | Type/test/wiring ada, worker produksi nol |
| Graceful shutdown | Baseline/partial | Runtime phases ada; phase metrics dan failure-injection lengkap belum ada |
| Allocation optimization | Missing evidence | Benchmark snapshot saja; tidak ada before/after/profile |
| Soak/failure injection | Partial | Unit stress singkat ada; tidak ada soak 30–60 menit |
| Observability integration | Partial | Accessor ada; central diagnostics belum lengkap |
| Verification | Partial | Race/vet/build lulus; lint tidak tersedia; benchmark suite tidak selesai |

## 7. Penilaian acceptance akhir

| Acceptance spesifikasi | Status |
|---|---|
| Satu outbound retry/FloodWait policy | Gagal |
| Seluruh Telegram method memiliki operation kind | Gagal |
| Resolver steady-state cache sebelum network | Lulus dengan caveat PeerManager |
| Limiter global/method/peer atomic dan bounded | Gagal |
| Tidak ada runtime goroutine tanpa owner/cancel/join/panic boundary | Gagal/belum terbukti |
| Hot-path I/O memiliki deadline dan cancellation | Gagal/belum lengkap |
| Retry panjang menjadi deferred attempt | Lulus untuk Job Manager path |
| Shutdown dependency-safe dan hard deadline | Sebagian lulus; bukti failure injection belum lengkap |
| Query/request/allocation regression evidence | Gagal |
| Race, vet, lint, build, failure injection, soak lulus | Sebagian; lint tidak tersedia dan soak representatif belum ada |

## 8. Urutan koreksi yang aman

### P0 — Correctness dan policy tunggal

1. Migrasikan seluruh raw Telegram call di service, `purge_safe`, profile photo, assistant, peer fetcher, dan warm-up ke shared executor.
2. Hapus `retryOnFloodWait` dan hentikan pembuatan executor per call.
3. Tetapkan `RPCMeta` method, family, peer key, kind, timeout, dan refresh hook untuk setiap call site.
4. Tambahkan architecture/AST guard test yang gagal bila raw runtime RPC baru ditambahkan di luar adapter yang diizinkan.
5. Pastikan upload/download mempunyai context deadline yang sesuai ukuran dan tidak salah di-retry sebagai operasi biasa.

### P1 — Limiter dan stale recovery

1. Implementasikan adapter atomic multi-dimension untuk global/family/method/peer.
2. Wire limiter tersebut ke `NewClient`; tambahkan test wiring produksi, bukan hanya fake executor test.
3. Terapkan `Penalize` sehingga FloodWait menahan dimension relevan.
4. Pasang one-shot `RefreshPeer` pada operasi yang aman dan pertahankan aturan ambiguous mutation.
5. Buktikan `PeerManager.Resolve` local-only atau pindahkan network path ke executor/singleflight.

### P2 — Ownership dan shutdown

1. Daftarkan long-lived worker nyata ke supervisor.
2. Migrasikan direct goroutine berdasarkan klasifikasi Task Engine, deferred job, supervisor, atau bounded cleanup.
3. Hilangkan `context.Background()` dari child runtime operation; pertahankan hanya lifecycle root yang terdokumentasi.
4. Tambahkan integration test shutdown saat RPC, DB, persistence, dan plugin macet.
5. Masukkan supervisor dan phase shutdown metrics ke diagnostics.

### P3 — Evidence performa dan observability

1. Perbaiki atau beri batas eksplisit `BenchmarkB4_PeriodicDueBurst` agar suite benchmark selalu selesai.
2. Simpan baseline before/after yang reproducible.
3. Ambil CPU, heap, goroutine, block, dan mutex profile pada idle/steady/burst/cancel/shutdown.
4. Tambahkan request/query budget pada command kritis dan instrumentation DB lintas repository.
5. Jalankan soak 30–60 menit dengan network timeout, FloodWait, SQLite busy, panic acak, dan shutdown berulang.
6. Pasang `golangci-lint` di environment CI/audit dan jalankan check yang dipersyaratkan.

## 9. Definition of done revisi

Implementasi hanya boleh dinyatakan sesuai seluruh dokumen bila seluruh kondisi berikut benar:

- tidak ada raw outbound Telegram call di runtime di luar transport/adapter allowlist yang eksplisit;
- tidak ada `retryOnFloodWait` atau policy retry Telegram kedua;
- limiter produksi bukan noop dan atomic lintas seluruh dimensions;
- setiap RPC mempunyai kind dan timeout eksplisit, serta non-idempotent ambiguity tidak di-retry;
- seluruh long-lived worker muncul pada supervisor snapshot atau mempunyai owner lifecycle lain yang terdokumentasi;
- seluruh delayed retry panjang durable dan tidak menahan physical worker;
- diagnostics menampilkan minimum metrics dari dokumen dengan bounded labels;
- benchmark suite selesai dan mempunyai baseline before/after;
- goroutine/heap profile tidak menunjukkan pertumbuhan monoton setelah workload berhenti;
- soak representatif, race, vet, lint, build, architecture, cancellation, and shutdown failure-injection tests lulus;
- worktree hanya berisi perubahan yang memang hendak diserahkan, tanpa session artifacts atau secret/runtime data.

## 10. Putusan akhir

Putusan audit: **BELUM SESUAI SELURUH DOKUMEN**.

Implementasi core sudah cukup kuat untuk menjadi fondasi dan banyak defect audit lama telah diperbaiki. Akan tetapi, klaim total baru valid setelah coverage outbound, limiter produksi, worker supervision, context/I/O audit, observability, dan bukti performa ditutup. Prioritas paling aman adalah menyelesaikan P0 dan P1 sebelum melakukan optimasi alokasi lanjutan.
