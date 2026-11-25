# Phase M2 — Design & Implementation Contract: MyXL Scheduled Token Maintenance

**Repository:** `inipew/goultroid`  
**Branch:** `test-next`  
**Verified HEAD:** `c5d84e539ad103b49254bc9e7e84d8e001d7408d`  
**Date:** 2026-11-25  
**Status:** Phase M2 Complete (Design & Contract — No production code or migrations modified)

---

## 1. Scope dan Non-Goals

### 1.1 Scope
Dokumen ini menetapkan kontrak implementasi untuk:
1. Konfigurasi durable jadwal maintenance token MyXL.
2. Integrasi dengan `scheduler.Engine`, `jobs.Manager`, dan `taskengine.Engine` melalui `plugin.JobClient` dan `plugin.ScheduleClient`.
3. Evaluasi expiry token yang konsisten antara request normal dan scheduled maintenance.
4. Pemilihan akun, pemrosesan berurutan (sequential), penanganan kegagalan parsial, dan batas resource.
5. Pengelolaan penuh melalui Telegram (command `.myxl schedule` dan menu Assistant/native `MenuManager`) menggunakan satu `MaintenanceService`.
6. Diagnostics, structured logging, notifikasi owner opsional, dan keamanan/redaksi data sensitif.

### 1.2 Non-Goals
- Membuat scheduler, `TaskEngine`, `JobManager`, HTTP executor, retry framework, atau callback/session protocol baru.
- Membuat goroutine permanen, background ticker, atau worker per akun di dalam `plugins/myxl`.
- Mengubah protokol autentikasi CIAM atau melakukan login ulang OTP otomatis.
- Memaksa refresh token yang masih berada di luar jendela expiry (`RefreshWindow`).
- Menambahkan `NextRunAt` duplikat di tabel konfigurasi MyXL.

---

## 2. Existing Architecture Integration

Alur eksekusi end-to-end menggunakan komponen runtime Goultroid yang sudah ada:

```text
Telegram (.myxl schedule / MenuManager a2)
        |
        v
MyXL MaintenanceService (single configuration & trigger authority)
        |
        +---> SQLite: myxl_token_maintenance_config (user preferences)
        |
        +---> plugin.ScheduleClient.SaveSchedule / DisableSchedule
        |             |
        |             v
        |     jobs.Manager -> SQLite: job_schedules (sole due-time authority)
        |             |
        |             v
        |     scheduler.Engine (unified timing loop via SetScheduleWake)
        |             |
        |             v
        |     jobs.Manager.ProcessDueSchedules -> MaterializeDueSchedule
        |
        +---> (Manual run) plugin.JobClient.Trigger(ctx, "token_maintenance")
                      |
                      v
              jobs.Manager.SubmitOccurrence -> TaskEngine (pool: "scheduler", class: "maintenance")
                      |
                      v
              MyXL Job Handler ("token_maintenance")
                      |
                      v
              Token Expiry Evaluation -> Client.EnsureFreshTokenWithin (singleflight.Group)
                      |
                      v
              network.Client (CIAM HTTP) -> Repository.Save (myxl_accounts)
                      |
                      v
              SQLite: myxl_token_maintenance_runs (bounded) + Diagnostics + Optional Owner Notify
```

### API Aktual yang Diverifikasi
| Komponen | Lokasi | API / Kontrak Terverifikasi |
|---|---|---|
| Plugin Context | `internal/plugin/context.go`, `context_jobs.go` | `pctx.Jobs() (plugin.JobClient, error)` (`CapJobs`), `pctx.Schedules() (plugin.ScheduleClient, error)` (`CapScheduler`) |
| Scoped Job Client | `internal/plugin/context_jobs.go:76-90` | `RegisterHandler(handlerType, handler)`, `Register(def)`, `Trigger(ctx, jobID)` — otomatis di-prefix `plugin:myxl:` |
| Scoped Schedule Client | `internal/plugin/context_jobs.go:92-100` | `SaveSchedule(ctx, jobs.JobSchedule)`, `DisableSchedule(ctx, scheduleID)` — otomatis di-prefix `plugin:myxl:` (tidak ada `GetSchedule`) |
| Jobs Manager | `internal/jobs/manager.go`, `manager_schedule.go` | `OverlapForbid`, `MisfireRunOnce`, `MisfireSkip`, `Recurrence: "once" \| "interval"`, `NextIntervalDueAfter` |
| TaskEngine | `internal/taskengine/engine_config.go:69` | Pool `"scheduler"` (`Concurrency: 4`, `ZeroIdle: true`, `IdleTimeout: 30s`, `BacklogLimit: 100`), `tasks.PriorityMaintenance` |
| Plugin Lifecycle | `internal/plugin/manager.go:928-933` | `Disable` / `ShutdownWithContext` memanggil `jobsMgr.CancelByOwner(owner)` |
| Network Service | `internal/platform/network/service.go` | `*network.Client` (`30s` default timeout, `MaxIdleConnsPerHost: 10`) |
| MyXL Migrations | `plugins/myxl/migration.go` | `SchemaInvariantMigration` (`myxl.001`–`myxl.007`; berikutnya `myxl.008`) |
| Assistant a2 | `plugins/myxl/assistant_interaction.go` | `feature.Spec`, `assistantActionSlotCount = 32`, `assistantScreenHome` / `assistantScreenInput` |

---

## 3. Component Responsibility

- **`scheduler.Engine`**: Pemilik tunggal timing loop; membaca `EarliestScheduleDue` dan memicu `ProcessDueSchedules`.
- **`jobs.Manager`**: Pemilik `JobDefinition`, `JobSchedule` (`job_schedules`), `JobOccurrence`, `JobAttempt`, pencegahan overlap (`OverlapForbid`), dan recovery saat restart.
- **`taskengine.Engine`**: Admission control, antrean pool `"scheduler"`, prioritas `"maintenance"`, timeout eksekusi, dan propagasi `context.Context` cancellation.
- **`myxl.MaintenanceService`**: Satu-satunya otoritas validasi konfigurasi, sinkronisasi ke `ScheduleClient`, trigger manual via `JobClient`, orkestrasi evaluasi expiry, dan pencatatan hasil run.
- **`myxl.Client`**: Otoritas tunggal singleflight refresh (`refreshGroup`) dan klasifikasi error HTTP CIAM.
- **`myxl.Repository`**: Otoritas tunggal persistensi akun (`myxl_accounts`), konfigurasi maintenance (`myxl_token_maintenance_config`), dan riwayat bounded (`myxl_token_maintenance_runs`).

---

## 4. Configuration Contract

### 4.1 Struktur Konfigurasi Konseptual
```go
type AccountScope string

const (
    AccountScopeAll      AccountScope = "all"
    AccountScopeActive   AccountScope = "active"
    AccountScopeSelected AccountScope = "selected"
)

type TokenMaintenanceConfig struct {
    Enabled            bool
    Interval           time.Duration
    AnchorTime         string       // "HH:MM" 24-jam
    Timezone           string       // IANA TZ, default "Asia/Jakarta"
    RefreshWindow      time.Duration
    AccountScope       AccountScope
    SelectedMSISDNs    []string     // dipakai hanya jika AccountScope == "selected"
    NotifyOnCompletion bool
    NotifyOnFailure    bool
    UpdatedAt          time.Time
}
```

### 4.2 Default & Batas Validasi
| Field | Default | Batas Valid | Aturan Penolakan (Reject Explicitly) |
|---|---|---|---|
| `Enabled` | `false` | `true` / `false` | — |
| `Interval` | `6 * time.Hour` | `1h <= Interval <= 24h` (kelipatan menit/jam, nilai rekomendasi: `1h, 3h, 6h, 12h, 24h`) | Tolak `<= 0`, `< 1h`, `> 24h` |
| `AnchorTime` | `"03:00"` | Format `HH:MM` (`00:00`–`23:59`) | Tolak format non-`HH:MM`, jam `>23`, menit `>59` |
| `Timezone` | `"Asia/Jakarta"` | Valid `time.LoadLocation(tz)` dan tidak kosong | Tolak string kosong atau zona waktu tidak dikenal |
| `RefreshWindow` | `30 * time.Minute` | `5m <= RefreshWindow <= 120m` dan `RefreshWindow < Interval` | Tolak `<= 0`, `< 5m`, `> 120m`, atau `>= Interval` |
| `AccountScope` | `"all"` | `"all"`, `"active"`, `"selected"` | Tolak nilai selain 3 scope tersebut; jika `"selected"` dan daftar kosong atau MSISDN tidak terdaftar di `myxl_accounts`, tolak |
| `NotifyOnCompletion` | `false` | `bool` | — |
| `NotifyOnFailure` | `true` | `bool` | — |

**Aturan Validasi:** Nilai invalid **wajib ditolak** dengan pesan error yang jelas dan **tidak boleh** diam-diam diganti ke default.

### 4.3 Sumber Kebenaran (Source of Truth)
1. `myxl_token_maintenance_config` menyimpan preferensi pengguna (tanpa `NextRunAt`, tanpa token).
2. `jobs.JobSchedule` (`job_schedules`) adalah otoritas tunggal `next_due_at`, `enabled` jadwal, `misfire_policy`, dan `overlap_policy`.
3. `jobs.Manager` adalah otoritas lifecycle occurrence & attempt.
4. `myxl_accounts` adalah otoritas tunggal token CIAM.

---

## 5. Persistence Contract

### 5.1 Migrasi `myxl.008` (Rencana untuk M3/M4)
1. **`myxl_token_maintenance_config`** (singleton row `id = 1`):
   - `id INTEGER PRIMARY KEY CHECK (id = 1)`
   - `enabled INTEGER NOT NULL DEFAULT 0`
   - `interval_seconds INTEGER NOT NULL DEFAULT 21600`
   - `anchor_time TEXT NOT NULL DEFAULT '03:00'`
   - `timezone TEXT NOT NULL DEFAULT 'Asia/Jakarta'`
   - `refresh_window_seconds INTEGER NOT NULL DEFAULT 1800`
   - `account_scope TEXT NOT NULL DEFAULT 'all'`
   - `selected_msisdns_json TEXT NOT NULL DEFAULT '[]'`
   - `notify_on_completion INTEGER NOT NULL DEFAULT 0`
   - `notify_on_failure INTEGER NOT NULL DEFAULT 1`
   - `updated_at DATETIME NOT NULL`
2. **`myxl_token_maintenance_runs`** (bounded history, maksimal 20 baris terbaru):
   - `id INTEGER PRIMARY KEY AUTOINCREMENT`
   - `trigger_source TEXT NOT NULL` (`"scheduled"` | `"manual"`)
   - `status TEXT NOT NULL` (`"completed"` | `"partial_failure"` | `"failed"` | `"cancelled"`)
   - `started_at DATETIME NOT NULL`
   - `completed_at DATETIME NOT NULL`
   - `duration_ms INTEGER NOT NULL`
   - `total_accounts INTEGER NOT NULL`
   - `refreshed_count INTEGER NOT NULL`
   - `already_fresh_count INTEGER NOT NULL`
   - `skipped_count INTEGER NOT NULL`
   - `failed_count INTEGER NOT NULL`
   - `cancelled_count INTEGER NOT NULL`
   - `error_summary TEXT NOT NULL DEFAULT ''` (hanya kode/klasifikasi error non-sensitif; tanpa token/MSISDN penuh)
   - Pruning dilakukan atomik saat insert: `DELETE FROM myxl_token_maintenance_runs WHERE id NOT IN (SELECT id FROM myxl_token_maintenance_runs ORDER BY id DESC LIMIT 20)`.

**Alasan tabel `myxl_token_maintenance_runs`:** `jobs.JobAttempt.Result` hanya menyimpan metadata disposisi internal `JobManager` (`attemptResultMetadata`), tidak menyimpan counter per-kategori akun (`refreshed`, `already_fresh`, `skipped`, `failed`). Tabel ini bounded (maks 20 baris) dan tidak menyimpan token maupun MSISDN.

### 5.2 Aturan Payload Job
`jobs.JobDefinition.Payload` dibiarkan kosong (`nil`) atau hanya metadata statis non-secret. Handler membaca snapshot konfigurasi dari `Repository` saat eksekusi dimulai, sehingga perubahan konfigurasi tidak pernah menyalin token atau objek `Account` ke `job_definitions`.

---

## 6. Schedule Semantics

- **ID Lokal:** `const maintenanceJobID = "token_maintenance"` (di-scope otomatis oleh `JobClient` dan `ScheduleClient` menjadi `plugin:myxl:token_maintenance`).
- **Recurrence:** `"interval"` (satu-satunya recurrence periodik yang didukung `jobs.JobSchedule`).
- **Anchor & Timezone:**
  - `AnchorTime` + `Timezone` menghitung `NextDueAt` awal yang **strictly after `now`**:
    1. Parse `AnchorTime` (`hour`, `min`) dan `loc, _ := time.LoadLocation(cfg.Timezone)`.
    2. Buat kandidat hari ini di `loc`: `time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, min, 0, 0, loc)`.
    3. Jika kandidat `<= now`, majukan dengan kelipatan `Interval` (`NextIntervalDueAfter(candidate, now, interval)`). Jika kandidat `> now`, mundurkan per `Interval` selama masih `> now` untuk menemukan fase terdekat di masa depan.
  - Contoh: `AnchorTime = "03:00"`, `Interval = 6h`, `Timezone = "Asia/Jakarta"` → `03:00`, `09:00`, `15:00`, `21:00` WIB.
  - **DST Semantics:** `jobs.NextIntervalDueAfter` menambah durasi tetap (`time.Duration`). Untuk `Asia/Jakarta` (UTC+7 tanpa DST), fase jam dinding selalu tepat. Untuk zona waktu dengan DST, interval `24h` bergeser 1 jam saat transisi DST.
- **Misfire & Overlap Policy:**
  - `MisfirePolicy: jobs.MisfireRunOnce` — jika jadwal terlewat saat downtime, `ProcessDueSchedules` mematerialisasi **maksimal 1** occurrence catch-up dan langsung memajukan `next_due_at` ke slot masa depan berikutnya dalam O(1) (mencegah catch-up storm).
  - `OverlapPolicy: jobs.OverlapForbid` — `MaterializeDueSchedule` memeriksa `job_occurrences` aktif (`ready`, `dispatched`, `blocked`); jika masih ada yang berjalan, slot baru dilewati.
- **Perubahan Konfigurasi:**
  - Mengubah `Interval`, `AnchorTime`, atau `Timezone` saat `Enabled == true` memanggil `ScheduleClient.SaveSchedule` dengan `NextDueAt` baru hasil kalkulasi ulang. Eksekusi yang sedang berjalan tetap memakai snapshot konfigurasi awal siklusnya.
  - `Disable`: memanggil `ScheduleClient.DisableSchedule(ctx, "token_maintenance")` lalu menyimpan `Enabled = false` di `myxl_token_maintenance_config` (preferensi interval/anchor/window tetap tersimpan).
  - `Enable`: menyimpan `Enabled = true` lalu memanggil `ScheduleClient.SaveSchedule` dengan `NextDueAt` terhitung.

---

## 7. Expiry Policy

### 7.1 Klasifikasi `Account.TokenExpiresAt`
1. **Valid / Known Expiry:** `!acc.TokenExpiresAt.IsZero() && acc.TokenExpiresAt.After(time.Unix(0, 0))` (setelah `1970-01-01T00:00:00Z`).
   - Mencakup expiry dari `expires_in` CIAM maupun estimasi lokal `DefaultTokenExpiryFallback` (`60m`) yang dicatat `applyTokensToAccount`.
2. **Unknown / Legacy Expiry:** `acc.TokenExpiresAt.IsZero()` atau `!acc.TokenExpiresAt.After(time.Unix(0, 0))` (termasuk default migrasi `myxl.004`: `'1970-01-01 00:00:00'`).

### 7.2 Tabel Keputusan Evaluasi (per Akun)
Evaluasi menggunakan satu fungsi murni `evaluateAccountToken(acc *Account, now time.Time, window time.Duration)` dengan urutan prioritas:

| Prioritas | Kondisi Akun / Token | Keputusan | Status Hasil |
|---|---|---|---|
| 1 | `acc == nil` atau `strings.TrimSpace(acc.MSISDN) == ""` | Skip | `Skipped` |
| 2 | `acc.RefreshToken == ""` dan `acc.SubscriberID == ""` | Skip (tidak bisa refresh tanpa sesi) | `MissingRefreshToken` |
| 3 | `TokenExpiresAt` **Unknown / Legacy** (`<= 1970-01-01`) | Evaluasi refresh via shared singleflight | `UnknownExpiry` (jika refresh tidak bisa dilakukan) atau hasil refresh (`Refreshed` / gagal) |
| 4 | `TokenExpiresAt <= now` (sudah kedaluwarsa) | Prioritaskan refresh via shared singleflight | `Refreshed` / classified error |
| 5 | `now.Add(window).After(TokenExpiresAt)` (masuk `RefreshWindow`, termasuk `<= DefaultTokenRefreshSkew`) | Refresh via shared singleflight | `Refreshed` / classified error |
| 6 | `now.Add(window).Before(TokenExpiresAt)` atau sama dengan batas | Skip (masih segar) | `AlreadyFresh` |

### 7.3 Shared Singleflight Extension
- Tambahkan method pada `myxl.Client`:
  ```go
  func (c *Client) EnsureFreshTokenWithin(ctx context.Context, acc *Account, window time.Duration) error
  ```
- Jika `window < DefaultTokenRefreshSkew`, gunakan `DefaultTokenRefreshSkew`.
- `EnsureFreshToken(ctx, acc)` mendelegasikan ke `EnsureFreshTokenWithin(ctx, acc, DefaultTokenRefreshSkew)`.
- `refreshSingleflight` menerima parameter `skew time.Duration` sehingga pengecekan freshness di dalam singleflight (`!force && failedIDToken == "" && isTokenFresh(targetAcc.TokenExpiresAt, now, skew)`) konsisten dengan caller dan tetap melindungi token yang baru diperbarui oleh request normal.
- Maintenance **dilarang** memanggil `ForceRefreshToken()`.

---

## 8. Account Selection

1. Saat handler mulai berjalan, ambil snapshot konfigurasi dan panggil `repo.List(ctx)` **satu kali** di awal siklus.
2. Filter berdasarkan `AccountScope`:
   - `all`: semua akun tersimpan.
   - `active`: hanya akun dengan `acc.IsActive == true`.
   - `selected`: hanya akun yang `MSISDN`-nya ada dalam `SelectedMSISDNs`.
3. Urutkan kandidat berdasarkan prioritas expiry:
   1. Sudah expired (`TokenExpiresAt <= now`)
   2. Masuk `RefreshWindow` (terdekat ke expiry lebih dulu)
   3. Unknown expiry
   4. Sisanya (`AlreadyFresh`, dicatat sebagai `AlreadyFresh` tanpa HTTP call)
4. Batasi maksimal `MaxAccountsPerCycle = 50` akun yang diproses HTTP refresh per siklus; selebihnya dicatat `Skipped` jika melebihi batas.
5. Jika akun dihapus saat siklus berjalan, `refreshSingleflight` membaca `repo.GetByMSISDN(ctx, msisdn)` dan jika `nil`/not found, kembalikan `Skipped` tanpa error fatal.
6. Maintenance **tidak boleh** mengubah `IsActive` akun manapun dan **tidak boleh** menghapus token lama saat refresh gagal.

---

## 9. Execution Contract

- **JobDefinition:**
  - `ID`: `"token_maintenance"` (scoped: `"plugin:myxl:token_maintenance"`)
  - `HandlerType`: `"token_maintenance"`
  - `Pool`: `"scheduler"`
  - `Class`: `string(tasks.PriorityMaintenance)`
  - `Timeout`: `10 * time.Minute` (diturunkan dari `MaxAccountsPerCycle` × budget per-akun; cukup untuk 20+ refresh berurutan dengan timeout HTTP `25s` per akun)
  - `RetryPolicy`: `jobs.JobRetryPolicy{MaxAttempts: 1}` (kegagalan parsial antar-akun ditangani per-akun di dalam handler, bukan retry seluruh batch oleh `JobManager`)
- **Pemrosesan Sequential:**
  - Akun diproses satu per satu secara berurutan di dalam handler `TaskEngine`.
  - Sebelum memproses setiap akun, periksa `if err := ctx.Err(); err != nil` → tandai sisa akun `Cancelled` dan hentikan loop secara kooperatif.
  - Tidak ada worker pool tambahan, tidak ada goroutine per akun, tidak ada `time.Sleep`.
- **Single-Run Guard (Manual vs Scheduled):**
  - `MaintenanceService` menjaga `atomic.Bool` in-process (`running`) untuk mencegah eksekusi ganda pada instance yang sama, sekaligus mengandalkan `OverlapForbid` pada `jobs.Manager` untuk jadwal.
  - Jika `.myxl schedule run` dipanggil saat maintenance sedang berjalan, tolak/coalesce secara informatif (`"Maintenance token sedang berjalan."`) tanpa membuat antrean baru.

---

## 10. Concurrency & Resource Limits

- **Goroutine:** `0` goroutine permanen milik MyXL. Seluruh eksekusi berjalan di worker pool `"scheduler"` milik `TaskEngine` (`ZeroIdle: true`).
- **Memory:** Riwayat run dibatasi maks `20` baris di SQLite; diagnostics in-memory hanya menyimpan ringkasan run terakhir dan counter atomik berukuran tetap.
- **Database:**
  - 1 query `repo.List(ctx)` di awal siklus.
  - Tidak ada transaksi DB yang ditahan selama HTTP request CIAM.
  - `repo.Save` dipanggil per akun hanya ketika token benar-benar diperbarui.
- **Idle Footprint:** Saat `Enabled == false` atau belum jatuh tempo, `0` aktivitas DB, `0` aktivitas HTTP, dan `0` polling dari plugin MyXL.

---

## 11. Restart & Lifecycle

1. **Startup (`Plugin.InitPlugin(pctx)`):**
   - Ambil `pctx.Jobs()` dan `pctx.Schedules()`.
   - Daftarkan handler `"token_maintenance"` dan `JobDefinition` `"token_maintenance"`.
   - Baca `myxl_token_maintenance_config` dari DB.
   - **Penting:** Jika `Enabled == true`, **jangan** menimpa `NextDueAt` pada `job_schedules` yang sudah ada (agar `MisfireRunOnce` catch-up tetap bekerja setelah downtime!). Untuk menjaga sinkronisasi saat `Enabled == false`, panggil `Schedules().DisableSchedule(ctx, "token_maintenance")` secara defensif; saat `Enabled == true`, hanya panggil `SaveSchedule` jika `ScheduleClient` belum pernah diaktifkan (atau gunakan flag `schedule_synced` / biarkan `Enable()` yang memanggil `SaveSchedule`).
   - *Catatan M4:* Karena `ScheduleClient` tidak memiliki `GetSchedule`, saat `Enabled == true` pada startup pertama setelah migrasi, `Enable` dari Telegram sudah memanggil `SaveSchedule`; `job_schedules` persisten di SQLite dan otomatis dipulihkan oleh `jobs.Manager` + `scheduler.Engine` saat restart.
2. **Plugin Disable / Shutdown (`plugin.Manager`):**
   - `plugin.Manager` memanggil `jobsMgr.CancelByOwner("myxl")` dan `TaskEngine` membatalkan `ctx` handler yang sedang berjalan.
   - Handler berhenti kooperatif pada batas akun berikutnya (`ctx.Err()`).

---

## 12. Telegram Command & Menu Contract

### 12.1 Command `.myxl schedule` (`PermissionOwner`, Userbot & Assistant)
Semua subcommand memanggil `MaintenanceService`:
- `.myxl schedule` / `.myxl schedule status` — Status aktif/nonaktif, interval, anchor time, timezone, refresh window, scope akun, hasil run terakhir, dan counter ringkas.
- `.myxl schedule enable` — Mengaktifkan jadwal maintenance.
- `.myxl schedule disable` — Menonaktifkan jadwal tanpa menghapus konfigurasi.
- `.myxl schedule interval <1h|3h|6h|12h|24h>` — Mengatur interval pemeriksaan.
- `.myxl schedule time <HH:MM>` — Mengatur `AnchorTime`.
- `.myxl schedule timezone <IANA_TZ>` — Mengatur `Timezone` (contoh: `Asia/Jakarta`).
- `.myxl schedule window <durasi>` — Mengatur `RefreshWindow` (contoh: `30m`).
- `.myxl schedule accounts <all|active>` — Mengatur `AccountScope`.
- `.myxl schedule run` — Memicu satu siklus maintenance segera melalui `JobClient.Trigger(ctx, "token_maintenance")` (tetap mengevaluasi expiry, bukan force refresh).
- `.myxl schedule history` — Menampilkan maks 5–10 riwayat run terakhir.

### 12.2 Menu Assistant / Native (`MenuManager` + `assistant_interaction.go`)
- Tambahkan tombol `"🕒 Token Maintenance"` (`myxl:maintenance`) pada dashboard utama (`BuildDashboardScreen`).
- Submenu `BuildMaintenanceScreen` menampilkan status ringkas dan tombol aksi menggunakan slot a2 yang sudah ada (`assistantActionSlotCount = 32` + `assistantScreenInput` wizard untuk input interval/time/timezone/window):
  - Status / Refresh tampilan (`myxl:maint_status`)
  - Aktifkan / Nonaktifkan (`myxl:maint_toggle`)
  - Atur Interval (`myxl:maint_interval`)
  - Atur Waktu Anchor (`myxl:maint_time`)
  - Atur Timezone (`myxl:maint_tz`)
  - Atur Jendela Refresh (`myxl:maint_window`)
  - Atur Scope Akun (`myxl:maint_scope`)
  - Jalankan Sekarang (`myxl:maint_run`)
  - Riwayat (`myxl:maint_history`)
  - Kembali (`myxl:home`)

---

## 13. Result & Diagnostics

### 13.1 Status Per Akun
```go
type AccountMaintenanceStatus string

const (
    StatusRefreshed             AccountMaintenanceStatus = "refreshed"
    StatusAlreadyFresh          AccountMaintenanceStatus = "already_fresh"
    StatusSkipped               AccountMaintenanceStatus = "skipped"
    StatusMissingRefreshToken   AccountMaintenanceStatus = "missing_refresh_token"
    StatusUnknownExpiry         AccountMaintenanceStatus = "unknown_expiry"
    StatusAuthenticationRejected AccountMaintenanceStatus = "authentication_rejected"
    StatusNetworkError          AccountMaintenanceStatus = "network_error"
    StatusRateLimited           AccountMaintenanceStatus = "rate_limited"
    StatusPersistenceError      AccountMaintenanceStatus = "persistence_error"
    StatusCancelled             AccountMaintenanceStatus = "cancelled"
)
```

### 13.2 Ringkasan Siklus & Notifikasi
- `MaintenanceRunSummary` memuat: `TriggerSource`, `Status`, `StartedAt`, `CompletedAt`, `Duration`, `TotalAccounts`, `Refreshed`, `AlreadyFresh`, `Skipped`, `Failed`, `Cancelled`, `ErrorSummary`.
- **Notifikasi Owner:** Opsional & best-effort via `rt.Telegram.MessageService` (ke `rt.OwnerID`) atau `UserlogService`. Default: `NotifyOnCompletion = false`, `NotifyOnFailure = true`. Kegagalan kirim notifikasi dicatat di log dan **tidak** mengubah status hasil refresh.
- **Observability:** Counter atomik di `MaintenanceService` + structured `zap.Logger` untuk event `started`, `completed`, `cancelled`, `schedule_changed`, `schedule_disabled` (tidak logging per tick idle).

---

## 14. Error Handling

| Kondisi | Tindakan |
|---|---|
| HTTP 400/401/403 (`invalid_grant` / rejected) | Klasifikasikan `AuthenticationRejected`, tandai perlu login ulang, pertahankan token lama, lanjut akun berikutnya |
| HTTP 429 | Klasifikasikan `RateLimited`, jangan retry loop di luar singleflight, lanjut akun berikutnya |
| HTTP 5xx | Klasifikasikan `NetworkError` / server error, jangan retry tak terbatas, lanjut akun berikutnya |
| Network timeout | Klasifikasikan `NetworkError`, lanjut akun berikutnya |
| `repo.Save` gagal setelah CIAM sukses | Klasifikasikan `PersistenceError`, jangan nyatakan siklus sukses penuh |
| `ctx.Err()` / Shutdown | Tandai sisa akun `Cancelled`, hentikan loop secara kooperatif |
| `JobClient.Trigger` ditolak admission | Kembalikan pesan status jelas ke Telegram tanpa panic |
| Config invalid | Tolak perubahan dengan alasan spesifik; pertahankan config lama |
| Notifikasi Telegram gagal | Log warning terpisah; status maintenance tetap sesuai hasil refresh & DB |

---

## 15. Security

1. **Zero Secret Leakage:** `access_token`, `id_token`, `refresh_token`, `OTP`, `BasicAuth`, dan `APIKey` dilarang masuk log, job payload, tabel riwayat, atau pesan Telegram.
2. **MSISDN Masking:** Gunakan `MaskMSISDN` pada seluruh output group chat dan pada log/diagnostics/notifikasi ringkasan.
3. **Authorization:** `.myxl schedule` wajib `core.PermissionOwner`; callback menu wajib melalui policy `feature.OwnerPolicy` pada sesi a2.
4. **Hardening `.myxl refresh` (Temuan M1):** `handleRefreshToken` di `plugins/myxl/myxl.go:561-583` wajib diperbaiki (masking MSISDN di group chat dan redaksi prefix token) sebagai bagian dari implementasi M5/M7 sebelum rilis.

---

## 16. Testing Matrix (untuk M3–M7)

- **Configuration:** default config, validasi batas interval (`<1h`, `>24h`, `<=0`), format `AnchorTime`, validasi `Timezone`, `RefreshWindow >= Interval`, scope `selected` kosong/tidak terdaftar, persistensi & update atomik.
- **Expiry & Selection:** token segar (`AlreadyFresh`), masuk `RefreshWindow`, expired, unknown/epoch `1970-01-01`, fallback `60m`, missing refresh token, prioritas urutan, batas `MaxAccountsPerCycle`, akun dihapus saat berjalan.
- **Refresh & Concurrency:** `EnsureFreshTokenWithin` mempertahankan `refreshGroup` singleflight dengan request normal, klasifikasi error (401/429/5xx/timeout/DB save failure), partial failure tidak menghentikan batch, token lama tidak terhapus saat gagal, tidak ada goroutine leak.
- **Scheduler & Lifecycle:** `SaveSchedule` / `DisableSchedule` via `ScheduleClient`, `Trigger` via `JobClient`, `MisfireRunOnce` setelah restart, `OverlapForbid` saat overlap/manual collision, `CancelByOwner` saat disable/shutdown.
- **Telegram & Security:** otorisasi `PermissionOwner`, konsistensi command vs menu, redaksi token & masking MSISDN, `internal/architecture/` suite lulus, `go test -race ./...` lulus.

---

## 17. Implementation Sequence (M3–M7)

- **M3 — Token Maintenance Core:** `EnsureFreshTokenWithin` & error classification di `client.go`, expiry evaluator, account selector, sequential maintenance runner, unit & concurrency tests.
- **M4 — Scheduler Integration:** Migrasi `myxl.008` + repository config & bounded runs, `CapJobs`/`CapScheduler` di `module.go` & `InitPlugin`, `JobDefinition` + `ScheduleClient` wiring, lifecycle & overlap tests.
- **M5 — Telegram Schedule Management:** `MaintenanceService`, command `.myxl schedule ...`, `MenuManager` + `assistant_interaction.go` screens/actions, perbaikan redaksi `.myxl refresh`.
- **M6 — Observability & Notifications:** Diagnostics counters, structured logging, best-effort owner notification.
- **M7 — Hardening & Performance:** Full `go test -race ./...`, resource/goroutine/SQLite contention checks, architecture tests, `gofmt`.

---

## 18. Open Decisions

1. **Tampilan `NextDueAt` di `.myxl schedule status`:** Karena `plugin.ScheduleClient` saat ini sengaja hanya mengekspos `SaveSchedule` dan `DisableSchedule` tanpa read API, status Telegram pada implementasi awal menampilkan konfigurasi aktif (`Interval`, `AnchorTime`, `Timezone`) dan hasil run terakhir tanpa menduplikasi `NextRunAt` di tabel MyXL. Penambahan read-only `GetSchedule` pada `ScheduleClient` hanya dipertimbangkan jika dibutuhkan dan disetujui terpisah.
