# Audit implementasi execution runtime

Tanggal: 15 September 2026. Acuan: [ADR 0006](../../adr/0006-execution-runtime-redesign.md), [analisa baseline](01-technical-analysis.md), dan [rencana P0–P8](03-implementation-plan.md).

Checkpoint yang diaudit: `8c02d7ed0f302ee0638937e302846cbc7ba25026`, setelah commit `df2a96d` berjudul “complete execution runtime redesign”. Workspace bersih ketika audit dimulai. Go: `go1.27.1-X:nodwarf5 linux/amd64`, deklarasi modul `go 1.27.0`.

## Kesimpulan

**Implementasi belum memenuhi ADR 0006 dan belum layak disebut redesign selesai.** Ada fondasi baru, tetapi producer utama masih memakai execution legacy. TaskEngine dan persistence pump didaftarkan ke Runtime; pendaftaran itu belum membuktikan integrasi execution/prepare/commit. Patch audit ini memperbaiki bug terisolasi dengan tes regresi. Gap arsitektur, migrasi, dan bukti operasional di bagian berikut tetap terbuka.

Audit meliputi domain/types, admission, worker/permit, TaskEngine, JobManager/store/pump, scheduler/periodic, wiring App/Runtime, Telegram ingress, plugin scope, resource media, diagnostics, architecture tests, dan deliverable migrasi/performance. Inventaris producer dilakukan melalui source search; ini bukan pembuktian semua interleaving maupun pengujian live Telegram. Update cutover workspace dicatat pada bagian berikut.

## Update cutover workspace — 15 September 2026

Patch lanjutan setelah audit ini menghapus implementation path lama dan memindahkan ingress aktif ke kontrak redesign.

| Area | Kode lama yang dihapus | Pengganti yang aktif |
|---|---|---|
| Execution runtime | `internal/workers`, `tasks.Manager`, mutable `tasks.Task` | `taskengine.Engine` dengan `tasks.WorkSpec`, permit privat, dan result `TaskResult` |
| Telegram | `Dispatcher.SetWorkers`, worker/direct fallback callback dan observer | `tasks.Client` wajib untuk command, callback, inline, dan observer |
| Jobs | `jobs.Job`, repository lama, waiter per JobID | `JobDefinition` → `JobOccurrence` → `JobAttempt`; TaskID dan lease epoch per attempt |
| Persistence | schema yang tidak dipakai oleh manager | definition disimpan saat register; trigger mematerialisasi occurrence, menyiapkan lease, dan completion dikomit melalui `PersistencePump` |
| Scheduler | submit langsung ke executor legacy | due row diserahkan ke `jobs.Manager.SubmitOccurrence`; JobManager yang masuk ke TaskEngine |
| Plugin boundary | client global tanpa scope | scoped client memaksa owner/generation dan membatasi cancel/snapshot ke scope sendiri |

`TestLegacyExecutionPackagesAreRemoved` mencegah `internal/workers`, `tasks.Manager`, mutable `tasks.Task`, serta model/repository Job lama muncul kembali. `TestManagerPersistsOccurrenceAttemptAndCompletion` memverifikasi satu trigger membentuk identity durable dan completion dicatat sebagai terminal attempt/occurrence.

Masih ada pekerjaan ADR yang tidak boleh diklaim selesai hanya karena cutover ini lolos build: TaskEngine belum memakai inbox control-loop bounded/fixed worker mailbox (B02–B03), payload/retention belum seluruhnya bounded (B04), completion persistence belum menahan result credit hingga acknowledgement (B05), store belum memiliki seluruh schedule/recovery/cancel protocol (B07), dan periodic scheduler masih memiliki policy retry sendiri (B10). Bagian tersebut tetap backlog desain, bukan jalur legacy yang aktif.

## 1. Temuan yang diperbaiki pada patch audit

P1 = correctness/availability; P2 = validation/maintainability. Lokasi menggunakan nama fungsi agar tetap mudah ditemukan setelah perubahan baris.

| ID | Prioritas | Bukti sebelum patch dan dampak | Fix yang diterapkan | Bukti regresi |
|---|---|---|---|---|
| F01 | P1 | `workers.ExecuteAssignment` memanggil `OnComplete`; `taskengine.executeAssignment` memanggilnya lagi. Callback worker yang blocking menahan hasil; panic callback dapat keluar dari recovery boundary. | Worker hanya menghasilkan result; callback dimiliki engine. Jalur cancel queued diberi panic isolation juga. | `TestEngineCompletionRunsOnceOutsideWorker`; worker tests. |
| F02 | P1 | Permit release memanggil dispatch sebelum `OnTaskTerminal` melepas quota/ordering. Task berikutnya milik owner yang mencapai MaxActive bisa tertahan selamanya tanpa submit baru. Completion di pool A juga tidak membangunkan pool B. | Setelah quota/ordering dilepas, evaluasi dispatch seluruh pool. Context attempt dibersihkan dan timestamp start memakai result worker. | `TestEngineCompletionUnblocksGlobalQuota`, kasus pool sama dan berbeda. |
| F03 | P1 | `Engine.Submit` menimpa `registry[TaskID]`. Ticket lama membaca record baru; queued duplicate merusak index/accounting. | Tolak ID yang sudah terdaftar sebelum reservasi/overwrite. | `TestEngineRejectsDuplicateAndInvalidAdmission`. |
| F04 | P1 | `SelectCandidate` tidak berpindah class setelah berhasil; deficit bertambah tetapi tidak mengatur pemilihan. Owner selalu bergilir satu kali terlepas Weight. Class pertama dapat men-starve maintenance. | Habiskan quantum class/owner sebelum rotasi, reset kredit idle, batasi loop ketika ring habis. | `TestControllerWeightedClassesDoNotStarve` membuktikan 8:4:2:1; `TestControllerWeightedOwners` membuktikan 3:1. |
| F05 | P1 | `Permit.Release` tidak membuat `Use` gagal; assignment tidak mengecek pool/TaskID. Assignment tanpa handler dianggap sukses. | State permit atomik unused/used/released; tolak use setelah release, identitas mismatch, unresolved handler, dan deadline yang telah lewat di boundary. Pre-start failure tidak punya StartedAt. | `TestExecuteAssignmentRejectsInvalidPermitsAndUnresolvedHandler`. |
| F06 | P1 | Deadline yang sudah lewat diterima; priority sembarang memicu assignment ke nil map; HandlerRef tidak pernah di-resolve tetapi dianggap berhasil. | Tolak expired admission, class invalid, timeout negatif, unresolved HandlerRef. Cek ulang context sesudah memperoleh mutex. | Admission rejection tests; spec tests. Kontrak inbox/decision timeout penuh masih B02. |
| F07 | P2 | Input `[]byte` dan `Job` pointer disimpan dari caller tanpa copy. | Salin byte payload dan occurrence reference sebelum ownership berpindah. | `TestEngineCopiesAdmittedPayloadAndOccurrence`. `Input any`, closure capture, dan output arbitrary tetap dibahas B04. |
| F08 | P1 | `Start` bisa membuka ulang engine aktif; tiap `Drain` membuat goroutine waiter baru; forced Stop meninggalkan queued task di belakang handler yang tidak kembali. | Start sekali; satu drain channel dengan active counter; queued task diselesaikan cancelled/shutdown setelah forced stop. | `TestEngineStopFinalizesQueuedBehindUncooperativeHandler`; lifecycle tests. |
| F09 | P1 | `IndexedHeap.Push` selalu append meski disebut update; `Remove` hanya memeriksa index, sehingga pointer asing berindex nol bisa menghapus timer lain. Deadline heap memiliki kelemahan yang sama. | Update pointer terdaftar memakai heap.Fix; Remove memverifikasi identitas pointer. | `TestIndexedHeapUpdateAndForeignRemoval`; existing deadline/heap tests. Key-based timer registry tetap B10. |
| F10 | P1 | Prepare SQLite membaca sebelum writer intent; lease epoch berasal wall clock. | Ambil writer intent sebelum membaca; update occurrence memakai revision/cancel epoch; epoch monoton per occurrence dari attempt number, bukan jam. | `TestStoreConcurrentPrepareWithProductionSQLite`: file DB melalui `database.Open`, WAL/pooled connections, delapan claimant, tepat satu grant. |
| F11 | P1 | Completion SQLite hanya memfilter attempt ID/epoch; replay dengan hasil berbeda menimpa hasil terminal dan parent occurrence; cancelled menjadi failed. | Validasi outcome terminal dan latest attempt/occurrence dispatched; replay identik no-op; replay konflik ditolak; cancelled diproyeksikan sebagai cancelled. | `TestStoreCompletionReplayCannotOverwriteResult`; `TestStoreFutureReadyAndCancelledOutcome`. |
| F12 | P1 | Ready query dan prepare bisa mengambil occurrence yang ReadyAt-nya masih di masa depan; limit caller tidak punya hard cap. | Filter due time, stable tie-break ID, cap page 500; prepare menolak future-ready dan durasi lease tidak valid. | `TestStoreFutureReadyAndCancelledOutcome`. Paging fairness lintas owner tetap B07. |
| F13 | P1 | Pump hanya memberi timeout bila ctx nil; ctx caller tidak terhubung pump shutdown. Drain kedua mengembalikan sukses meski worker pertama masih aktif. Panic operation mematikan worker/proses. | Deadline setiap operation, hubungkan lifetime pump, panic menjadi error, satu completion channel untuk semua drain caller; cegah restart instance. | `TestPersistencePumpConcurrentDrainAndCancellation`, `TestPersistencePumpPanicDoesNotKillWorker`. |
| F14 | P2 | Error marshal/unmarshal retry policy SQLite diabaikan. NaN policy atau JSON korup dapat tersimpan/terbaca sebagai policy lain. | Propagasi encode/decode error. Hapus enum/field operation pump yang tidak digunakan dan gagal lint. | Store tests, vet/lint. |
| F15 | P1 | `dispatchAsyncHandlers` menambah `inFlight`, tetapi Done hanya di `Run`. Cancellation sebelum handler mulai tidak pernah mengurangi counter; dispatcher Stop menunggu sampai timeout. | Pindah release counter ke `Task.OnComplete`, yang juga berjalan untuk accepted task batal sebelum start. | `TestObserverCancelledBeforeStartDoesNotLeakInFlight`. |
| F16 | P2 | Architecture gate lama belum melindungi fondasi execution baru. | Tambah guard imports untuk tasks/admission/workers/taskengine terhadap SQL, Telegram, jobs implementation, dan feature/composition yang terlarang. | `TestExecutionRedesignFoundations`. Larangan scheduler penuh menunggu pemisahan legacy B10. |

Fix di atas **tidak** menyatakan kontrak permit/durable/memory telah lengkap. Khusus F02, evaluasi lintas pool memperbaiki lost wake, tetapi satu control loop dengan work budget tetap belum dibangun.

## 2. Implementasi salah/kurang dan rencana fix yang masih terbuka

| ID | Prioritas | Gap terkonfirmasi dan lokasi | Dampak | Fix konkret dan gate penyelesaian |
|---|---|---|---|---|
| B01 | P1 | `app/wiring_core.go`, `app/app.go`: WorkerManager+TaskManager lama dan TaskEngine baru hidup bersamaan. `jobs.NewManager` menerima worker lama; `wiring_services.go` memasang scheduler lama. Production `WorkSpec` submit belum menggantikan command/job/periodic. | Kuota, physical slots, cancellation, dan result tidak universal. TaskClient plugin memperoleh inventory terpisah dari command user yang sama. | P2/P4/P6: pilih satu executor per partition; adapter legacy submit menerjemahkan ke engine yang sama; uji ordinary+durable saturation dan kuota lintas surface. Hapus executor kedua setelah caller nol, bukan dengan langsung mengganti routing. |
| B02 | P1 | `taskengine.Engine` berupa mutex dengan dispatch/sweep dari caller dan completion goroutine; `Submit` menunggu mutex tanpa deadline keputusan/default timeout atau inbox bounded. | Cancellation API tidak bounded; kerja panjang di bawah lock menahan cancel/ingress. Recheck ctx pada F06 hanya mengurangi satu race. | P1/P2: bounded request inbox dan reply cell dengan Pending/Accepted/Rejected/Cancelled atomik; default decision timeout; control messages terpisah dan per-turn work budget. Uji cancel-vs-accept dan saturated inbox. |
| B03 | P1 | `tryDispatchLocked` membuat goroutine per assignment dan menandai Running sebelum worker boundary. Permit tidak punya prepare deadline, immutable identity, atau validasi generation terhadap inventory aktif. | Snapshot menampilkan start sebelum benar-benar terjadi; grant lama/forged belum dapat difencing sesuai ADR. | Fixed physical workers/mailbox, Started event, permit state milik coordinator, generation dan expiry check terakhir; prepare ports bounded. Model test slot conservation dan expired/wrong-generation grant. |
| B04 | P1 | `WorkSpec.Input any`, closure Handler/OnComplete, `TaskResult.Output any`; payload budget hanya menghitung []byte yang sedang queued, dilepas saat dispatch; registry terminal tidak dievict. Callback memakai goroutine terpisah setelah result credit dilepas. | Payload aktif/history/callback dapat tumbuh tanpa batas walau count budget tampak aman. Callback yang blocking tidak tercakup Drain. | Value payload/ref dengan ukuran eksplisit, budget sampai resource benar-benar dilepas; bounded result delivery; ticket menyimpan immutable result; terminal TTL/count eviction; owner eviction. Uji repeated burst, blocked sink, retained bytes dan heap plateau. |
| B05 | P1 | `Engine` tidak memiliki prepare/commit-ack port, persistence state, atau durable retention. `spec.Job` hanya metadata. Pump terdaftar tetapi tidak dipakai JobManager baru. | Task durable dapat dinyatakan completed tanpa persistence; DB down tidak menahan result credits. | P3/P4: result inbox terpesan sejak admission; physical release terpisah dari durable ack; CommitPending/Committed/RecoveryRequired; fixed pump. Uji DB down, lost ack, queue saturation, shutdown flush. |
| B06 | P1 | `jobs/manager.go`: mutable Job bersama, `completionWaiters[jobID]`, notify broadcast, `_ = repo.UpdateState`, rollback dengan JobID saja. | Trigger A memenuhi waiter B; hasil/rollback attempt lama menimpa run baru; persistence failure disembunyikan. Ini jalur produksi, tidak diperbaiki oleh Store baru. | Ganti trigger internal menjadi occurrence handle; AttemptID/TaskID per run; completion waiter per occurrence/attempt; persistence lewat ack/fencing. Uji trigger paralel dengan hasil berbeda dan completion terbalik sebelum cutover. |
| B07 | P1 | `jobs/sqlite`: tabel schedule/outbox/migration tersedia, tetapi API save schedule/materialize+advance atomic, retry+outbox transaction, cancel tombstone API, lease renewal, recovery planner, payload-version registry belum ada. `MaterializeOccurrence` duplicate hanya error, bukan mengembalikan identity; definition update bukan expected-revision CAS. | Struktur schema belum membentuk protokol durable. Crash windows, dedupe manual, unknown effect, retry, dan perubahan definition belum punya perilaku lengkap. | P3: implementasikan port/store transitions lengkap dan shared in-memory store; idempotent materialization+schedule update; CAS definitions; cancellation epoch captured dalam attempt; stable bounded page per owner; recovery dispositions. Jalankan seluruh crash matrix §7 rencana. |
| B08 | P1 | `pluginContext.TaskClient()` hanya capability check saat mengambil global client, lalu mengembalikannya langsung. `Scope` belum punya generation engine yang enforced. | Client dapat menetapkan scope/quota/pool/class sendiri, cancel/snapshot ID milik owner lain, dan dipakai setelah disable. | P1/P6: scoped wrapper dengan scope generation dan quota identity dari trusted invocation factory; validasi pool/class capability setiap operasi; restrict cancel/snapshot; revoke/close barrier atomik. Tes cross-plugin access dan late submit setelah unload/reload. |
| B09 | P1 | `CancelScope` scan snapshot lalu Cancel satu per satu; tidak menutup admission generation. Running cancel tidak mencatat CancelRequested/Cause atau late-cancel metadata. | Submit paralel dapat lolos setelah scope cancellation; sukses setelah cancel tidak dapat diaudit sebagai late cancellation. | Coordinator-owned scope tombstones/generation barrier; cancel flag terpisah terminal outcome; clock/ID injection. Tes reload race dan success-after-cancel. |
| B10 | P1 | `scheduler.Engine` masih mengeksekusi message/command/ActionJob dengan worker reservation lama. `periodicCoordinator` memakai retry domain sendiri dan `syncHeapLocked` scan semua entries. Heap belum identity map `(kind,owner,id,generation)`/hard bound/clock abstraction; Data menampung pointer registration berisi handler. | Scheduler belum “hanya timing”; periodic bukan Job in-memory; O(N) scan tetap ada; durable wrapper completion berbeda dari job outcome. | P4: scheduler mengirim due refs/version, JobManager materialize dan retry; heap diubah saat mutation tanpa rebuild scan; fixed-rate/fixed-delay/misfire eksplisit; lease timers satu control service. Uji clock jump, re-register, 10k timers, retry attempt identity. |
| B11 | P1 | `app/lifecycle.go` mulai Runtime sebelum transport ready; App tetap punya state machine sendiri. `Shutdown` dapat pulang dari Runtime.Stop saat context caller habis lalu membatalkan transport/root meski teardown Runtime masih berjalan; context.Canceled bahkan diabaikan. Pump Quiesce menutup seluruh intake, belum ada completion port yang tetap terbuka. | RPC/dependency dapat mati sebelum execution/result drain; clean stop dapat dilaporkan sebelum teardown selesai. | P5: Runtime sole teardown owner; transport readiness component dan ingress gate terakhir; satu stop outcome independent dari waiter context; public admission terpisah internal completion. Tes explicit Shutdown dengan expired ctx, startup failure, transport exit, RPC-through-drain, pending commit. |
| B12 | P1 | Callback/inline sudah dialihkan ke worker lama jika tersedia, tetapi direct fallback tetap ada di `dispatcher_callback.go`. Rejection callback hanya log; belum reserved ack/control RPC budget. Observer juga fallback sinkron. | Perilaku capacity/timeout berbeda ketika dependency hilang; callback overload tidak mendapat feedback terdefinisi. | P6: constructor client wajib, gate permission/order tetap sinkron, bounded adapter semua surface, accepted/overload ack dan stale-inline suppression; golden permission/disabled-plugin/overload tests. |
| B13 | P1 | `Scope.Go` tetap goroutine tanpa service-count bound; plugin menerima legacy manager; `media/transcoder.go` menunggu `guard.Acquire` di execution body. | Finite work bypass admission; worker menunggu semaphore resource kedua. | P6: SubmitWork vs StartService, service budget/lifecycle, resource requirements di WorkSpec dan atomic dispatch reservation, continuation untuk kebutuhan yang baru diketahui. Pertahankan size/disk/rate validations. |
| B14 | P2 | `workers/reservation.go` masih package-level `sync.Map` dan counter busy+reserved, bukan universal slot permit. Legacy FIFO/Task mutable tetap aktif; `Task.Retry`, setters, `cmdSem` diagnostics dan `PublishDurable` masih ada. | Klaim resource nyata/telemetry/durability belum sesuai nama dan invariant. | P6/P8: inventaris adapter dengan owner+exit criteria; hapus setelah caller nol dan rollback window selesai. Rename/deprecate PublishDurable sebagai synchronous in-process; outbox untuk durability. |
| B15 | P2 | `taskengine.sweepLoop` polling 250 ms; `admission.SelectCandidate` membaca real clock; active owner arrays di-scan, tidak ada blocked index; owner counter maps/history belum bounded. | Idle wake dan latency expiry tidak sesuai deadline-driven design; fairness loop masih O(owner backlog), bukan amortized O(1) yang ditargetkan. | P2/P8: injected clock, indexed next-deadline timer; active/blocked owner indexes dan bounded work per turn; evict state idle. Benchmark B0/B1/B6/B7 sebelum mengklaim efisiensi. |
| B16 | P2 | Config engine punya default mutable map dan silent fallback; belum memvalidasi limits/retention/decision/prepare timeout atau budgets resource; sebagian batas 0 berarti unlimited. | Caller dapat mengubah config map setelah constructor, dan konfigurasi invalid tampak valid. | P1: salin config, validasi sebelum Start, pisahkan default dari nilai invalid; expose semua required budgets melalui konfigurasi runtime. Tambah invalid-config dan caller-mutation tests. |
| B17 | P1 | Tidak ada deliverable P7 di tree yang diaudit: dry-run migrator, consistent backup/restore rehearsal, marker generation cutover, reverse projection, runbook unknown/rollback. | Legacy row/side effect tidak aman dipindah hanya karena schema baru tersedia. | P7: tooling fixture-only dulu, mapping setiap row/blocked disposition, freeze+delta validation, single owner marker; kill/restart, restore dan rollback exercise. Tidak melakukan migrasi live sebelum hasil konkret dapat ditinjau. |
| B18 | P2 | Tidak ada laporan B0–B8 before/after, simulator event permutations, canary outcome/history, atau comprehensive surface compatibility matrix. Dokumen plan masih berstatus tahap dokumentasi. | Unit/race hijau tidak membuktikan performance, bounded memory, atau redesign selesai. | P0/P8: manifest baseline/candidate, seed/config, measured artifacts dan trace; matrix outcome serta residual risk. Update status hanya berdasarkan bukti gate, jangan mengubah checklist menjadi selesai karena compile. |

## 3. Traceability P0–P8

| Phase | Status audit sesudah patch | Yang tersedia | Gate yang belum dipenuhi |
|---|---|---|---|
| P0 | Sebagian | Checkpoint audit; tests existing; inventaris surface di bawah | Baseline B0–B8, deterministic harness/model, compatibility matrix lengkap, transport spike |
| P1 | Sebagian | Typed IDs/spec/results, models jobs, client interface, tambahan import guards | Scoped capabilities, bounded decision protocol, config/payload validation lengkap, prepare/ack ports |
| P2 | Sebagian | Queues/indexed deadline, fixed slot counts, fairness tests dan bug fixes | Single control loop/fixed workers, resource/result bounds, scope fencing, retention, model tests |
| P3 | Sebagian | Additive schema, prepare/completion dasar diperketat, pump, file SQLite contention test | Full transactions retry/outbox/schedule/cancel/recovery, shared memory store, crash matrix |
| P4 | Belum terintegrasi | Heap dipakai periodic; execution masih legacy | Job ready→permit→prepare→result ack, periodic unified, scheduler timing-only |
| P5 | Sebagian | Dispatcher Quiesce, Runtime phase mechanism, transport context terpisah | Sole teardown ownership/readiness, RPC-through-drain, internal result port lifetime |
| P6 | Belum cutover | TaskClient exposed; callback/observer worker adapters lama | Scoped capability enforcement, seluruh producer satu authority, surface compatibility, service bounds |
| P7 | Belum ada bukti | Schema migration map | Tooling migration/restore/canary/recovery/rollback |
| P8 | Belum ada bukti | Regression/race/vet checks | Before-after benchmark, retention/metrics, legacy removal dan laporan acceptance |

## 4. Inventaris surface dan urutan implementasi lanjutan

| Surface | Jalur aktual | Target/fix |
|---|---|---|
| Interactive commands | `telegram/dispatcher_workers.go` → workers.Manager → tasks.Manager/pool | Adapter TaskEngine; quota user/account dan scope command plugin |
| Callback/inline | `telegram/dispatcher_callback.go` → legacy interactive pool atau direct fallback | Mandatory scoped client, deadline dan feedback overload; tanpa fallback |
| Observers | `telegram/dispatcher_dispatch.go` → legacy general pool atau sinkron | Bounded task dengan immutable invocation; release pada completion (F15) |
| Managed jobs | `jobs/manager.go` → legacy workers, mutable JobID state | Occurrence/attempt handles, persistence port dan result ack |
| Scheduled actions | `scheduler/engine.go` → reserved legacy scheduler task | Versioned Job handlers; scheduler hanya due reference |
| Periodic | `scheduler/periodic_timer.go` → heap + legacy Task retry sendiri | In-memory Job memakai retry/overlap/misfire policy yang sama |
| Plugin TaskClient | `plugin/context.go` → global TaskEngine | Trusted scoped factory dan generation revocation |
| Plugin services/resources | `plugin/scope.go`, `services/media` | Supervised service budget, resource reservation sebelum dispatch |
| Diagnostics | `app/diagnostics.go`, `telegram/dispatcher_accessors.go` | Snapshot physical/waiting/prepare/commit/cancel dengan bounded cardinality |

Urutan fix yang direkomendasikan:

1. Tutup lubang scoped capability (B08/B09), stabilkan admission/physical contracts (B02–B04/B16), dan ukur baseline sebelum routing berubah.
2. Implementasikan durable prepare/ack/store/recovery lengkap (B05–B07); uji failure matrix sebelum memakai database produksi.
3. Satukan scheduler/periodic (B10), selesaikan Runtime/transport phase contract (B11).
4. Migrasi producer per partition lewat compatibility adapters (B01/B12/B13), lalu fixture migration/restore/canary (B17).
5. Validasi benchmark/retention/observability dan hapus legacy setelah exit criteria (B14/B15/B18).

## 5. Verifikasi dan batas hasil

Hasil unit/race test membuktikan kasus yang dijalankan, bukan keseluruhan invariant ADR. Bug dasar pada implementasi awal lolos karena tes completion hanya menunggu callback pertama, fairness tidak memeriksa weighted distribution, dan SQLite hanya diuji in-memory single connection. Patch menambahkan regression cases untuk celah tersebut.

Verifikasi patch audit:

- `go test -race ./... -timeout=120s`: lulus seluruh package.
- `go vet ./...`: lulus.
- `golangci-lint run --timeout=3m` versi 2.13.2: **0 issues**.
- `go build -o /tmp/ultroid-audit-goultroid ./cmd/goultroid`: lulus.
- `go run ./tools/featuregen` lalu diff `internal/app/generated_modules.go` terhadap HEAD: tidak berubah.
- Changed Go files diformat dengan gofmt/goimports; `git diff HEAD --check`: lulus.
- Penyesuaian terakhir memindahkan copy payload sesudah pemeriksaan budget (agar rejection tidak mengalokasikan copy) dan menambahkan tes ownership payload/reference; race test TaskEngine dijalankan ulang.

Binary lint/goimports ditemukan dalam cache lokal; validasi tidak memerlukan unduhan tool. Satu percobaan fixture observer awal timeout karena blocker test belum mengisi Owner wajib; fixture diperbaiki dan suite final lulus. Ini tidak disajikan sebagai kegagalan implementasi produksi. Tidak ada angka throughput/p95/RSS yang diklaim, tidak ada kill/restart proses atau Telegram network exercise, dan tidak ada klaim bahwa backlog B01–B18 sudah diperbaiki.
