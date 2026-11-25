# MyXL Notification Routing — Design & Closure (N1–N9)

**Repository:** `inipew/goultroid`
**Branch:** `test-next`
**Baseline M1–M8:** `d6f8bb31e17cc4a41069643b16717b3ab8e94fc7`
**Audit N1:** `fa31384569ad96fbcb6372ff181ef0dc192b694d`
**Verified HEAD (pre-N9):** `24830d021263bec00b5599fe582fab8511a56efb`
**Date:** 2026-11-25
**Status:** Phase N1–N9 Complete (Documentation-Only Closure — Roadmap Closed)

---

## 1. Goals and Non-Goals

### 1.1 Goals
1. Memperluas notifikasi hasil MyXL Token Maintenance (baik terjadwal maupun `.myxl schedule run` manual) dari owner-only menjadi **configurable multi-destination routing** (maksimal 5 destination).
2. Memisahkan event `partial_failure` dari `failure` agar memiliki preferensi independen.
3. Mendukung 6 tipe tujuan: `owner`, `group`, `supergroup`, `global_log`, `bot_pm`, dan `channel`.
4. Mempertahankan 100% kompatibilitas dengan konfigurasi M6 (`NotifyOnCompletion=false`, `NotifyOnFailure=true`).
5. Menggunakan penuh infrastruktur Telegram Goultroid yang sudah ada (`core.MessageServicer`, `core.PeerResolver`, `userlog.Service`).

### 1.2 Non-Goals
- Membuat sender, RPC executor, rate limiter, retry engine, worker pool, notification queue, event bus, atau callback/session protocol baru.
- Mengubah `JobManager`, `TaskEngine`, atau `scheduler.Engine`.
- Menyalin konfigurasi global log group Goultroid ke database MyXL.
- Mengirim notifikasi pada `no_op` atau mengubah outcome maintenance ketika pengiriman notifikasi gagal.

---

## 2. Existing Architecture

Seluruh pengiriman memakai komponen runtime yang sudah diverifikasi di N1:
- **`rt.MessageService` (`core.MessageServicer`):** mengirim pesan HTML ke segala `tg.InputPeerClass` melalui `RPCExecutor` dan hierarchical rate limiter (`internal/telegram/service.go:619-664`).
- **`rt.Resolver` (`core.PeerResolver`):** me-resolve `@username` dan numeric ID ter-cache menjadi `tg.InputPeerClass` beserta `AccessHash` (`internal/telegram/resolver.go:128-345`).
- **`rt.UserlogService` (`*userlog.Service`):** menyediakan global log destination via `GetDestination(ctx)` -> `*userlog.LogDestination` dengan `InputPeer()` (`internal/services/userlog/service.go:36-54, 242-301`).
- **`MaintenanceService` (`plugins/myxl/maintenance_service.go`):** satu-satunya otoritas mutasi konfigurasi untuk command dan a2 menu.

---

## 3. Destination Model

### 3.1 Tipe Destination
```go
type NotificationDestinationType string

const (
    DestOwner      NotificationDestinationType = "owner"      // &tg.InputPeerUser{UserID: ownerID}
    DestGroup      NotificationDestinationType = "group"      // &tg.InputPeerChat{ChatID: peerID}
    DestSupergroup NotificationDestinationType = "supergroup" // &tg.InputPeerChannel{ChannelID: peerID, AccessHash: hash}
    DestGlobalLog  NotificationDestinationType = "global_log" // rt.UserlogService.GetDestination(ctx).InputPeer()
    DestBotPM      NotificationDestinationType = "bot_pm"     // &tg.InputPeerUser{UserID: peerID, AccessHash: hash}
    DestChannel    NotificationDestinationType = "channel"    // &tg.InputPeerChannel{ChannelID: peerID, AccessHash: hash}
)

const MaxNotificationDestinations = 5
```

### 3.2 Struktur Destination Konseptual
```go
type NotificationDestination struct {
    ID         string                      `json:"id"`                    // ID stabil pendek (mis. "owner", "global_log", "dest-1")
    Type       NotificationDestinationType `json:"type"`
    Label      string                      `json:"label"`                 // Nama tampilan aman (maks 48 rune)
    Enabled    bool                        `json:"enabled"`
    PeerID     int64                       `json:"peer_id,omitempty"`     // Kosong untuk owner & global_log
    AccessHash int64                       `json:"access_hash,omitempty"` // Wajib untuk supergroup, channel, bot_pm
}
```

### 3.3 Aturan Model & Kompatibilitas Owner PM
1. **Implicit Fallback Owner PM:** Jika `destinations` kosong (`len == 0`, termasuk hasil upgrade dari M6), sistem otomatis menggunakan tujuan implicit `owner` (`Enabled: true`) sehingga perilaku M6 tetap terjaga 100%. Begitu pengguna menambah/mengelola daftar destination secara eksplisit, daftar `destinations` tersebut menjadi otoritas penuh (termasuk jika pengguna menonaktifkan `owner`).
2. **Global Log Reference:** `DestGlobalLog` tidak menyimpan `PeerID` maupun `AccessHash`. Saat pengiriman, resolver memanggil `rt.UserlogService.GetDestination(ctx)` secara dinamis.
3. **Pemisahan `InputPeerChat` vs `InputPeerChannel`:** `DestGroup` selalu menghasilkan `&tg.InputPeerChat{ChatID: abs(PeerID)}`, sedangkan `DestSupergroup` dan `DestChannel` selalu menghasilkan `&tg.InputPeerChannel{ChannelID: PeerID, AccessHash: AccessHash}`.
4. **Batas & Deduplikasi:** Maksimal `MaxNotificationDestinations = 5` destination terdaftar. Penambahan destination dengan `(Type, PeerID)` yang sama (atau `Type == DestOwner` / `DestGlobalLog` ganda) ditolak secara deterministik.
5. **Keamanan Mutasi Saat Run Aktif:** Handler maintenance membaca snapshot konfigurasi (termasuk `Destinations`) sekali sebelum pengiriman; penghapusan atau disable destination tidak memengaruhi iterasi yang sedang berjalan dan berlaku pada run berikutnya.

---

## 4. Event & Outcome Mapping

| Maintenance Outcome (`runTokenMaintenanceCycle`) | Kondisi | Event Key | Default |
|---|---|---|---|
| `RunStatusCompleted` dengan `RefreshedCount > 0` | Semua akun eligible selesai tanpa kegagalan | `completion` | `false` (`NotifyOnCompletion`) |
| `RunStatusCompleted` dengan `RefreshedCount == 0` | `no_op` (tidak ada token perlu refresh) | *(tidak ada event)* | Tidak pernah mengirim |
| `RunStatusPartialFailure` | Sebagian akun berhasil/fresh, sebagian gagal | `partial_failure` | `true` (`NotifyOnPartialFailure`) |
| `RunStatusFailed` | Maintenance gagal secara keseluruhan | `failure` | `true` (`NotifyOnFailure`) |
| `RunStatusCancelled` | Job dibatalkan / shutdown | `cancelled` | `false` (`NotifyOnCancelled`) |

- Setiap run dipetakan ke **tepat satu** event (atau none untuk `no_op`), sehingga `partial_failure` dan `failure` tidak pernah memicu dua notifikasi untuk satu run.
- Manual run (`.myxl schedule run`) dan scheduled run menggunakan kebijakan event yang identik.

---

## 5. Persistence Schema & Migration Strategy

### 5.1 Pilihan Desain: Opsi A (Kolom Terstruktur di `myxl_token_maintenance_config`)
Dipilih **Opsi A** via migrasi **`myxl.009`** dan **`myxl.010`** pada tabel `myxl_token_maintenance_config`:
- `myxl.009`: `notify_on_partial_failure INTEGER NOT NULL DEFAULT 1`, `destinations_json TEXT NOT NULL DEFAULT '[]'`
- `myxl.010`: `notify_on_cancelled INTEGER NOT NULL DEFAULT 0`

**Alasan Teknis:**
1. Jumlah destination dibatasi maks `5` (`bounded`), sehingga tidak memerlukan tabel relasional terpisah.
2. Menjaga seluruh update konfigurasi maintenance dalam **1 transaksi atomik** (`SaveTokenMaintenanceConfig`) tanpa risiko lock contention antar-tabel.
3. **Backward Compatibility M6:** Database lama otomatis mendapat `notify_on_partial_failure = 1` (`true`) dan `destinations_json = '[]'` (fallback implicit ke Owner PM).
4. **Invalid / Corrupt JSON:** Jika `destinations_json` rusak saat decode, `GetTokenMaintenanceConfig` mengembalikan error eksplisit (fail-closed, tidak diam-diam menimpa DB).

---

## 6. Delivery Semantics

1. **Urutan Eksekusi:** `runTokenMaintenanceCycle` -> `RecordTokenMaintenanceRun` -> `recordMaintenanceDiagnostics` -> `logMaintenanceFinished` -> `maybeNotifyDestinations`.
2. **Sequential & Bounded:** Destination aktif dikirim satu per satu secara berurutan dengan timeout `5s` per destination (`maintenanceNotifyTimeout`) dan total budget maks `15s` untuk seluruh daftar.
3. **Failure Isolation:** Kegagalan satu destination (mis. channel dihapus, bot diblokir, FloodWait) dicatat pada diagnostics/log dan **tidak** menghentikan pengiriman ke destination berikutnya serta **tidak** mengubah `MaintenanceRunSummary` maupun return error `handleTokenMaintenanceJob`.
4. **Test Notification:** Mengirim satu pesan verifikasi aman (`"🧪 Test notifikasi MyXL Token Maintenance — konfigurasi tujuan aktif."`) ke target destination tanpa mengubah state konfigurasi atau mencatat run history.

---

## 7. Peer Resolution & Validation

| Cara Registrasi | Sumber Peer | Validasi Sebelum Simpan |
|---|---|---|
| `owner` | `rt.OwnerID` | `ownerID > 0` |
| `global_log` | `rt.UserlogService.GetDestination(ctx)` | `UserlogService != nil` dan destinasi global sudah dikonfigurasi (`dest != nil`) |
| `group` / `supergroup` / `channel` (saat ini) | Perintah dijalankan di chat tujuan (`ctx.PeerID`, `ctx.Chat`) | Ekstrak `InputPeerChat` atau `InputPeerChannel` (`AccessHash != 0`), kirim pesan verifikasi (`SendMessage`) untuk memastikan izin tulis/posting |
| `bot_pm` / `@username` | Argumen `@username` atau input wizard a2 via `rt.Resolver.Resolve(ctx, ref)` | Pastikan hasil resolve bertipe `*tg.InputPeerUser` (untuk `bot_pm`) atau `*tg.InputPeerChannel` (untuk `channel`/`supergroup`), lalu kirim pesan verifikasi sebelum disimpan |

Pengguna **tidak pernah** diminta mengetik `AccessHash` secara manual.

---

## 8. Command & a2 Interaction

### 8.1 Command `.myxl schedule notify` (`core.PermissionOwner`)
- `.myxl schedule notify status` — Menampilkan event toggles & daftar destination (label + tipe + peer ID ter-masking + status enabled/disabled).
- `.myxl schedule notify completion <on|off>`
- `.myxl schedule notify partial_failure <on|off>`
- `.myxl schedule notify failure <on|off>`
- `.myxl schedule notify cancelled <on|off>`
- `.myxl schedule notify destination list`
- `.myxl schedule notify destination add <owner|global_log|group|supergroup|bot_pm|channel> [target]`
- `.myxl schedule notify destination remove <id>`
- `.myxl schedule notify destination enable <id>`
- `.myxl schedule notify destination disable <id>`
- `.myxl schedule notify destination test <id>`

### 8.2 Menu a2 (`MenuManager` + `assistant_interaction.go`)
- Memakai `feature.OwnerPolicy(execution.SurfaceAssistant)` (`PrivateOnly = true`) dan slot a2 existing (`assistantActionSlotCount = 32`).
- Submenu `BuildMaintenanceNotifyScreen` diakses dari `BuildMaintenanceScreen` (`myxl:maint_notify`):
  - Toggle event (`completion`, `partial_failure`, `failure`, `cancelled`).
  - Daftar destination (detail, enable/disable, tes kirim, hapus dengan konfirmasi).
  - Tambah destination (`owner`, `global_log`, serta `group`/`supergroup`/`bot_pm`/`channel` via `assistantScreenInput` atau panduan `.myxl schedule notify destination add <type>` di chat tujuan).

---

## 9. Security

1. **Zero Credential Leakage:** `AccessToken`, `IDToken`, `RefreshToken`, prefix token, MSISDN mentah, raw provider response, dan stack trace dilarang masuk ke pesan notifikasi, test message, atau log.
2. **Peer Privacy:** `AccessHash` tidak pernah ditampilkan ke Telegram atau dicetak ke log. `PeerID` di-masking pada tampilan ringkasan (mis. `-100...7890`) dan tidak dijadikan label metrik atau log.
3. **Authorization:** Seluruh mutasi dan test delivery wajib `core.PermissionOwner` / `feature.OwnerPolicy(PrivateOnly=true)`.

---

## 10. Observability

Memperluas `MaintenanceDiagnostics` (`atomic.Int64` low-cardinality tanpa label peer ID) untuk membedakan:
- `NotificationsAttempted`
- `NotificationsSucceeded`
- `NotificationsFailed`
- `NotificationsSkipped` (`skipped_disabled`, `skipped_deadline`, `skipped_cancelled`)
- `NotificationsInvalid` (peer tidak ter-resolve / `global_log` belum dikonfigurasi)

Structured `zap.Logger` mencatat hasil per pengiriman dengan field aman (`plugin`, `job_id`, `run_id`, `dest_id`, `dest_type`, `notification_status`) tanpa `PeerID` mentah maupun `AccessHash`.

---

## 11. Resource Constraints

- **0** goroutine permanen, **0** ticker, **0** worker pool, **0** queue, **0** retry engine baru.
- Batas keras `MaxNotificationDestinations = 5` dan budget waktu total pengiriman `15s`.

---

## 12. Failure Handling

| Kondisi | Perilaku |
|---|---|
| `global_log` belum diset di UserLog | Catat `NotificationsInvalidDest`, skip, lanjut destination lain |
| Peer tidak bisa dikirimi pesan (`CHAT_WRITE_FORBIDDEN`, `USER_IS_BLOCKED`, `PEER_ID_INVALID`) | Catat `NotificationsFailed`, lanjut destination lain, tidak ubah outcome maintenance |
| Timeout pengiriman (`5s`) | Batalkan attempt destination tersebut, lanjut destination lain |
| Test destination gagal | Tolak penyimpanan destination, kembalikan pesan error yang aman |
| `destinations_json` korup di DB | Tolak pembacaan dengan error eksplisit (fail-closed) |

---

## 13. Compatibility with M6

- Konfigurasi M6 tanpa kolom baru otomatis bermigrasi via `myxl.009` dengan `notify_on_partial_failure = 1` dan `destinations_json = '[]'`.
- Selama `destinations_json` kosong, `maybeNotifyDestinations` menggunakan fallback implicit `owner` (`rt.OwnerID`), sehingga perilaku identik dengan M6.
- Perintah lama `.myxl schedule notify completion <on|off>` dan `.myxl schedule notify failure <on|off>` tetap berfungsi penuh.

---

## 14. Proposed N3–N9 Implementation Sequence

- **N3 — Persistence & Destination Core [Complete]:** Migrasi `myxl.009`, model `NotificationDestination`, validasi, batas 5, fallback M6, dan repository tests (`plugins/myxl/notification_destinations.go`, `notification_destinations_test.go`, `migration.go`, `maintenance_types.go`, `maintenance_repo.go`).
- **N4 — Telegram Delivery Integration [Complete]:** Inject `rt.Resolver` & `rt.UserlogService` di `Module.Register`, destination resolver (`notification_delivery.go`), pemisahan event `NotifyOnPartialFailure` vs `NotifyOnFailure`, sequential bounded delivery (`5s`/destination, total cap `15s` dengan skip sisa destination saat deadline tercapai), dan isolation/deadline tests (`notification_delivery_test.go`).
- **N5 — Command Management [Complete]:** Perluasan `MaintenanceService` (`SetNotifyEvent`, `AddNotificationDestination`, `RemoveNotificationDestination`, `SetNotificationDestinationEnabled`, `TestNotificationDestination`) dan subcommand `.myxl schedule notify ...` (`plugins/myxl/notification_commands.go`, `notification_commands_test.go`) dengan `core.PermissionOwner` dan pre-save test delivery.
- **N5.1 — Cancelled Persistence Correction [Complete]:** Migrasi `myxl.010` (`notify_on_cancelled INTEGER NOT NULL DEFAULT 0`), dukungan penuh pada `SQLiteRepository`, `MaintenanceService.SetNotifyEvent(EventCancelled, ...)`, dan `.myxl schedule notify cancelled <on|off>`.
- **N6 — a2 Menu Integration [Complete]:** `BuildMaintenanceNotifyScreen`, `BuildMaintenanceNotifyDestScreen`, `BuildMaintenanceNotifyDestDeleteConfirmScreen`, dan `BuildMaintenanceNotifyAddScreen` (`plugins/myxl/notification_menu.go`), tombol `"🔔 Notifikasi Routing"` di `BuildMaintenanceScreen` (`plugins/myxl/menu.go`), slot action & wizard di `plugins/myxl/assistant_interaction.go`, serta pengujian a2 end-to-end & sinkronisasi dua arah (`plugins/myxl/notification_a2_test.go`).
- **N7 — Diagnostics & Security Hardening [Complete]:** Audit N7-A..E, klasifikasi `DeliverySkippedDisabled` & `DeliverySkippedCancelled` (pembatalan context tidak dihitung sebagai RPC failure), akurasi counter `NotificationsSkipped` untuk destination disabled, eksekusi `EventCancelled` pada scheduler dengan bounded context, serta sanitasi log & test hardening (`plugins/myxl/notification_delivery.go`, `maintenance_observability.go`, `maintenance_scheduler.go`, `notification_n7_test.go`).
- **N8 — Correctness, Soak, SQLite Contention & Performance Validation [Complete]:** Perbaikan cancellation/deadline saat `resolveNotificationPeer` (`notification_delivery.go`), diagnostics missing `MessageServicer` pada fallback Owner PM (`maintenance_observability.go`), perluasan redaction log semua `zapcore.FieldType` (`notification_n7_test.go`), pengujian edge cases, SQLite contention, bounded soak, dan benchmark (`notification_n8_test.go`, `notification_n8_bench_test.go`).
- **N9 — Documentation Closure [Complete]:** Konsolidasi dokumentasi M1–M8 & N1–N9, verifikasi exact commit SHA dari Git history, pencatatan hasil verifikasi/benchmark N8, serta pencatatan risiko tersisa (`docs/design/myxl-notification-routing-design.md`).

---

## 15. Acceptance Criteria (N2)

- [x] Seluruh 6 tipe destination memiliki representasi `tg.InputPeerClass` dan kontrak validasi yang jelas.
- [x] `global_log` menggunakan referensi dinamis ke `userlog.Service` tanpa duplikasi peer ID.
- [x] Pemetaan outcome maintenance ke event (`completion`, `partial_failure`, `failure`, `cancelled`, `no_op`) eksplisit tanpa double notification.
- [x] Strategi persistensi (`myxl.009` Opsi A) dan kompatibilitas M6 terdokumentasi.
- [x] Multi-destination failure isolation, peer resolution, command, a2, security, dan batas resource lengkap.
- [x] Tidak ada runtime baru, tidak ada perubahan production code atau migration.


---

## 16. N7 — Diagnostics & Security Hardening

### 16.1 Temuan Audit (N7-A – N7-E)
1. **High (`notification_delivery.go`) — Pembatalan context salah diklasifikasikan sebagai RPC failure / deadline:**
   - Saat `parentCtx` dibatalkan sebelum atau selama `SendMessage`, batch sebelumnya menandai sisa destination sebagai `DeliverySkippedDeadline` (`total_deadline_exceeded`) dan panggilan yang sedang berjalan sebagai `DeliveryFailed`.
   - **Perbaikan:** Menambahkan `DeliverySkippedCancelled` (`"skipped_cancelled"`, reason `"context_cancelled"`). Pembatalan context kini meningkatkan `Skipped` dan `NotificationsSkipped`, bukan `Failed` / `NotificationsFailed`.
2. **Medium (`maintenance_observability.go`) — Destination disabled tidak tercatat akurat pada `NotificationsSkipped`:**
   - Destination dengan `Enabled: false` sebelumnya diabaikan oleh `effectiveNotificationDestinations` (atau hanya dihitung `1` jika seluruhnya disabled).
   - **Perbaikan:** Menambahkan `DeliverySkippedDisabled` (`"skipped_disabled"`) dan menghitung jumlah destination disabled secara akurat ke `NotificationsSkipped` serta `skipped_count` log terstruktur tanpa menaikkan `NotificationsAttempted`.
3. **Medium (`maintenance_scheduler.go`) — `EventCancelled` tidak pernah terpicu pada scheduled run yang dibatalkan:**
   - `runScheduledMaintenance` sebelumnya mengembalikan `ctx.Err()` / `context.Canceled` sebelum memanggil `maybeNotifyDestinations`.
   - **Perbaikan:** Saat `summary.Status == RunStatusCancelled`, `maybeNotifyDestinations` dipanggil dengan `context.Background()` (dibatasi `maintenanceNotifyTotalBudget = 15s` di dalam delivery) sebelum mengembalikan error pembatalan.
4. **Low (`maintenance_observability.go`) — Akurasi status log terstruktur:**
   - Menambahkan `fallback_owner` (boolean berdimensi rendah) serta membedakan `notification_status` (`sent`, `partial_failure`, `failed`, `skipped`).
5. **N7-C / N7-D / N7-E (Tanpa Temuan Kebocoran / Race):**
   - Tidak ditemukan kebocoran `AccessHash`, token, credential CIAM, atau `err.Error()` mentah ke pengguna/log.
   - `MaintenanceService` sudah menegakkan `maintConfigMu`, batas maksimum 5 destination, kontrak `AccessHash`, deduplikasi deterministik, dan isolasi `TestNotificationDestination`.

### 16.2 Semantik Diagnostics Final
- `NotificationsAttempted = NotificationsSucceeded + NotificationsFailed + NotificationsInvalid`.
- `NotificationsSkipped` mencakup seluruh destination yang tidak dicoba (`skipped_disabled`, `skipped_deadline`, `skipped_cancelled`).
- Tidak ada counter per-`PeerID`, label berdimensi tinggi, goroutine tambahan, atau mekanisme retry baru.

### 16.3 Kebijakan Sanitization & Batasan Tersisa
- Output UI/command hanya menampilkan `PeerID` termasking (`maskPeerID`), label, tipe, dan pesan error statis/aman.
- Log terstruktur hanya mencatat angka agregat, `event_type`, `fallback_owner`, `duration`, dan `notification_status`.
- Batasan tersisa: skenario soak/kontensi SQLite penuh dan hardened edge cases dijadwalkan pada **N8 — Hardening & Performance**.


---

## 17. N8 — Correctness, Soak Testing, SQLite Contention & Performance Validation

### 17.1 Perbaikan Follow-up Review N7 (N8-A)
1. **N8-A1 (`notification_delivery.go`) — Cancellation/Deadline saat `resolveNotificationPeer`:**
   - `resolveNotificationPeer` kini meneruskan `context.Canceled` / `context.DeadlineExceeded` dari `UserlogService.GetDestination(ctx)` tanpa membungkusnya dengan `errGlobalLogMissing`.
   - `deliverMaintenanceNotifications` mengklasifikasikan pembatalan/deadline saat resolusi sebagai `DeliverySkippedCancelled` / `DeliverySkippedDeadline` (`Skipped++`), bukan `DeliveryInvalidDest`.
2. **N8-A2 (`maintenance_observability.go`) — Missing `MessageServicer` pada implicit Owner PM:**
   - Menghapus silent `return` saat `msgSvc == nil && ownerID <= 0 && len(cfg.Destinations) == 0`.
   - Kegagalan runtime tersebut kini tercatat sebagai `DeliveryFailed` (`message_service_unavailable`) dengan `NotificationsFailed++` dan structured warning log tanpa panic maupun peer ID palsu.
3. **N8-A3 (`notification_n7_test.go`) — Redaction Seluruh `zapcore.FieldType`:**
   - Memperluas pengecekan sentinel ke tipe `String`, `Error`, `Stringer`, `ByteString`, `Int*`/`Uint*`, `Duration`, dan `Interface`, serta menambahkan self-check untuk memastikan field numerik/byte sensitif terdeteksi.

### 17.2 Evaluasi Lifecycle & Cancellation (`EventCancelled`)
- `handleTokenMaintenanceJob` memanggil `maybeNotifyDestinations(context.Background(), ...)` saat `summary.Status == RunStatusCancelled`.
- Pengiriman tetap bounded oleh `maintenanceNotifyTotalBudget` (`15s`) dan `maintenanceNotifyTimeout` (`5s` per destination) tanpa goroutine detached atau retry.
- **Batasan terdokumentasi:** karena interface `JobHandler` saat ini hanya meneruskan satu `ctx` job yang sudah dibatalkan saat `RunStatusCancelled`, pengiriman notifikasi cancellation menggunakan `context.Background()` dengan batas keras `15s` sehingga dapat menahan worker scheduler maksimal `15s` jika seluruh destination timeout saat shutdown.

### 17.3 Ringkasan Hasil Pengujian N8 (`notification_n8_test.go`, `notification_n8_bench_test.go`)
- **Edge Cases (`TestN8_C_TelegramEdgeCasesAndIsolation`):** `PEER_ID_INVALID`, `USER_IS_BLOCKED`, `CHAT_WRITE_FORBIDDEN` terisolasi per destination dan tidak menghentikan destination berikutnya.
- **SQLite Contention & Cross-Surface (`TestN8_D_SQLiteContentionAndCrossSurfaceConcurrency`):** 8 worker × 20 iterasi mutasi/baca bersamaan (`SetNotifyEvent`, `Add/RemoveNotificationDestination`, `GetStatus`, command `.myxl schedule notify status`) lulus tanpa lost update, tanpa duplikasi, dan tetap menjaga batas `MaxNotificationDestinations = 5`.
- **Bounded Soak (`TestN8_E_BoundedSoakAndGoroutineStability`):** 100 iterasi siklus mutasi + status + notifikasi selesai tanpa pertumbuhan goroutine di luar baseline dan counter tepat (`Attempted=100`, `Succeeded=100`).
- **Benchmark (`BenchmarkN8_*`, Ryzen 7 5700U, `linux/amd64`, `-benchtime=200ms`):**
  - `Delivery/1_dest_ok`: `2388 ns/op`, `2071 B/op`, `14 allocs/op`
  - `Delivery/5_dest_ok`: `6579 ns/op`, `7833 B/op`, `38 allocs/op`
  - `Delivery/5_dest_disabled`: `57.54 ns/op`, `0 B/op`, `0 allocs/op`
  - `Persistence/repo_get_config`: `39464 ns/op`, `3524 B/op`, `81 allocs/op`
  - `Persistence/repo_save_config`: `75420 ns/op`, `3327 B/op`, `54 allocs/op`
  - `Service/svc_set_notify_event`: `135291 ns/op`, `8530 B/op`, `158 allocs/op`
  - `Service/svc_test_send`: `45466 ns/op`, `5454 B/op`, `94 allocs/op`
  *(Catatan: tidak ada baseline benchmark N7 terpisah pada file benchmark ini; angka di atas adalah baseline terukur N8.)*


---

## 18. N9 — Documentation Closure & Roadmap Sign-off

### 18.1 Konsolidasi Kontrak M1–M8 (Scheduled Token Maintenance)
- `myxl_token_maintenance_config` menyimpan preferensi pengguna; `jobs.JobSchedule` adalah otoritas tunggal `next_due_at`.
- Kebijakan scheduler: `MisfireRunOnce`, `OverlapForbid`, pool scheduler, `PriorityMaintenance`, timeout `10m`, `MaxAttempts = 1`.
- Parameter jadwal & pemrosesan: interval `1–24h` (default `6h`), default timezone `Asia/Jakarta`, pemrosesan akun sequential (maksimum `50` akun per cycle), history maksimum `20` row.
- Klien & concurrency: MyXL CIAM memakai managed HTTP client dengan shared refresh `singleflight`, tanpa ticker atau runtime tambahan.

### 18.2 Traceability Commit M1–M8 & N1–N9 (Terverifikasi dari Git History)

| Fase | Kategori | Tujuan | Commit SHA | Status |
|---|---|---|---|---|
| M1 | Audit / Docs | Architecture audit & plan refinement | `83498855` (lanjutan: `da6b6e08`, `e8312f1b`, `c5d84e53`) | Complete |
| M2 | Design / Docs | Token maintenance implementation contract | `f9170fbb` | Complete |
| M3 | Implementation | Persistence, configuration & core | `caf2d7df` | Complete |
| M4 | Implementation | Scheduler & JobManager integration | `8a01ed78` | Complete |
| M5 | Implementation | Telegram schedule management | `585607c0` | Complete |
| M6 | Implementation | Observability & diagnostics | `880f368e` | Complete |
| M7 | Test / Hardening | Lifecycle hardening & performance validation | `ec6b10cd` | Complete |
| M8 | Docs Closure | Token maintenance roadmap closure | `d6f8bb31` | Complete |
| N1 | Audit / Docs | Notification routing architecture audit | `fa313845` | Complete |
| N2 | Design / Docs | Notification routing design & contract | `5deb3c3a` | Complete |
| N3 | Implementation | Persistence (`myxl.009`) & destination core | `117f187c` | Complete |
| N4 | Implementation | Telegram delivery integration | `8c40660c` | Complete |
| N5 | Implementation | Command management (`.myxl schedule notify`) | `c5096e88` | Complete |
| N5.1 | Correction | Cancelled preference persistence (`myxl.010`) | `4b47597b` | Complete |
| N6 | Implementation | a2 menu & wizard integration | `99429828` | Complete |
| N7 | Correction / Hardening | Diagnostics & security hardening | `bc69653b` | Complete |
| N8 | Test / Benchmark | Correctness, contention, soak & benchmark | `24830d02` | Complete |
| N9 | Docs Closure | Documentation-only roadmap closure | *(commit N9 pada branch `test-next`)* | Complete |

### 18.3 Ringkasan Verifikasi Final (Diambil dari Eksekusi N8)
- `gofmt -l plugins/myxl/`: bersih.
- `go vet ./plugins/myxl/...`: lulus.
- `go test -race ./plugins/myxl/...`: lulus (`26.464s`).
- `go test ./internal/architecture/...`: lulus (`1.798s`).
- `go test ./...`: lulus pada seluruh package repository.
- `git diff --check`: bersih.
- **SQLite Contention:** `8` worker × `20` iterasi mutasi/baca bersamaan lulus tanpa lost update, tanpa `SQLITE_BUSY` yang lolos, dan konfigurasi akhir valid.
- **Soak Test:** `100` iterasi selesai dengan `NotificationsAttempted = 100`, `NotificationsSucceeded = 100`, tanpa pertumbuhan goroutine di luar baseline.
- **Benchmark N8** (lihat §17.3 untuk angka lengkap `ns/op`, `B/op`, `allocs/op` pada AMD Ryzen 7 5700U, `linux/amd64`; ini merupakan baseline terukur N8).

### 18.4 Risiko Tersisa & Batasan Pengujian
1. **Keterbatasan Lifecycle `EventCancelled` (`15s` cap):** `EventCancelled` memakai `context.Background()` dengan hard total budget `15s` karena `JobHandler` hanya menyediakan context job yang sudah dibatalkan saat `RunStatusCancelled`. Best-effort notification tetap terkirim tanpa goroutine detached dan tetap bounded, namun worker scheduler berpotensi tertahan maksimal `15s` ketika seluruh destination mengalami timeout saat shutdown.
2. **Batasan Pengujian:**
   - Soak test `100` iterasi bukan bukti matematis bebas memory leak untuk seluruh durasi produksi jangka panjang.
   - Angka benchmark merupakan baseline lingkungan pengujian lokal, bukan jaminan latency produksi.
   - Pengiriman Telegram pada suite N1–N8 menggunakan fake `core.MessageServicer` / `core.PeerResolver` / `userlog.Service`, bukan Telegram live API.

### 18.5 Final Architecture Review
Implementasi tetap sepenuhnya berada di atas infrastruktur Goultroid yang sudah ada:
- `TaskEngine` untuk admission, execution, fairness, dan resource management.
- `JobManager` dan `jobs.JobSchedule` untuk durable logical work, attempts, dan jadwal.
- Shared Telegram `RPCExecutor` (`core.MessageServicer`, `core.PeerResolver`) dan `userlog.Service`.
- Managed HTTP client untuk MyXL CIAM.
- `MaintenanceService` sebagai sumber otoritas konfigurasi tunggal untuk command `.myxl schedule notify` dan menu a2.
- **Tidak ada** runtime kedua, worker/scheduler tambahan, retry engine, RPC executor kedua, callback protocol kedua, destination registry kedua, atau queue/cache/history tanpa batas.
