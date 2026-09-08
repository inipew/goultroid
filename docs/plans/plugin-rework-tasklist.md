# Goultroid Plugin Rework — Master Task List & Migration Roadmap

Dokumen ini adalah panduan eksekusi teknis *step-by-step* untuk merealisasikan arsitektur modular pada seluruh plugin di Goultroid.

---

## Progress Overview

- [x] **Pilot: `clone`** *(Selesai di commit ded1363)*
- [x] **Phase 0: Baseline Fixes & Runtime Contract Expansion**
- [x] **Phase 1: Stateless Features Migration (Batch 1 — Core Utilities)**
- [x] **Phase 2: Stateless Features Migration (Batch 2 — Information & Media)**
- [x] **Phase 3: Persistent Features Migration (Decoupling Database)**
- [x] **Phase 4: Domain Subsystems Presentation Migration**
- [x] **Phase 5: Catalog Deprecation & Architecture Enforcement**

---

## Detailed Task Breakdown

### Phase 0: Baseline Fixes & Runtime Contract Expansion
*Tujuan: Memastikan seluruh baseline test hijau dan `module.Runtime` siap menampung injeksi untuk seluruh tipe plugin.*

- [x] **Task 0.1**: Perbaiki `internal/app/catalog.go` (tambahkan kembali import `"github.com/inipew/goultroid/plugins/ping"` yang hilang).
- [x] **Task 0.2**: Perbaiki `internal/architecture/imports_test.go` (hapus import `"go/ast"` yang tidak terpakai dan izinkan intra-feature subpackages).
- [x] **Task 0.3**: Hapus test usang `internal/database/clone_test.go` (sudah diuji mandiri di `plugins/clone/migration_test.go`).
- [x] **Task 0.4**: Perluas struct `Runtime` di `internal/module/module.go` agar memuat:
  - `Permissions *core.Permissions`
  - `Router *core.Router`
  - `EventBus *core.EventBus`
  - `Metrics core.MetricsCollector`
  - `Logger *zap.Logger`
  - `StartTime time.Time`
  - `TelegramService func() core.TelegramServicer`
  - `Resolver core.PeerResolver`
  - `Callbacks *callback.Router`
  - `CallbackStore *callback.StateStore`
  - `Storage storage.Storage`
  - `SettingsService *settings.Service`
  - Domain service accessors (Downloader, Media, PM-Permit, Userlog, Broadcast, Addons, ModService, Scheduler)
- [x] **Task 0.5**: Perbarui konstruksi `module.Runtime` di `internal/app/app.go` (`New(...)`).
- [x] **Task 0.6**: Verifikasi `go test ./...` awal untuk memastikan test inti lulus.

---

### Phase 1: Stateless Features Migration — Batch 1 (Core Utilities)
*Tujuan: Memigrasikan plugin utilitas sederhana yang tidak memiliki dependensi database.*

- [x] **Task 1.1 — `ping`**:
  - Buat `plugins/ping/module.go`.
  - Hapus `ping.New()` dari `internal/app/catalog.go`.
- [x] **Task 1.2 — `alive`**:
  - Buat `plugins/alive/module.go` (mengambil `rt.StartTime`).
  - Hapus `alive.New(...)` dari `internal/app/catalog.go`.
- [x] **Task 1.3 — `info`**:
  - Buat `plugins/info/module.go`.
  - Hapus `info.New()` dari `internal/app/catalog.go`.
- [x] **Task 1.4 — `pin`**:
  - Buat `plugins/pin/module.go`.
  - Hapus `pin.New()` dari `internal/app/catalog.go`.
- [x] **Task 1.5 — `forward`**:
  - Buat `plugins/forward/module.go`.
  - Hapus `forward.New()` dari `internal/app/catalog.go`.
- [x] **Task 1.6 — `fun`**:
  - Buat `plugins/fun/module.go`.
  - Hapus `fun.New()` dari `internal/app/catalog.go`.
- [x] **Task 1.7**: Jalankan `go run ./tools/featuregen` untuk memperbarui `generated_modules.go`.
- [x] **Task 1.8**: Jalankan `go test ./...` dan pastikan semua batch 1 lulus.

---

### Phase 2: Stateless Features Migration — Batch 2 (Media & Lookups)
*Tujuan: Memigrasikan plugin fungsionalitas media dan lookup API pihak ketiga.*

- [x] **Task 2.1 — `quote`**:
  - Buat `plugins/quote/module.go`.
  - Hapus `quote.New()` dari `catalog.go`.
- [x] **Task 2.2 — `wikipedia`**:
  - Buat `plugins/wikipedia/module.go`.
  - Hapus `wikipedia.New()` dari `catalog.go`.
- [x] **Task 2.3 — `ocr`**:
  - Buat `plugins/ocr/module.go`.
  - Hapus `ocr.New()` dari `catalog.go`.
- [x] **Task 2.4 — `sticker`**:
  - Buat `plugins/sticker/module.go`.
  - Hapus `sticker.New()` dari `catalog.go`.
- [x] **Task 2.5 — `locks`**:
  - Buat `plugins/locks/module.go`.
  - Hapus `locks.New()` dari `catalog.go`.
- [x] **Task 2.6 — `profile`**:
  - Buat `plugins/profile/module.go`.
  - Hapus `profile.New()` dari `catalog.go`.
- [x] **Task 2.7 — `system`**:
  - Buat `plugins/system/module.go` (mengambil `rt.Metrics`).
  - Hapus `system.New()` dari `catalog.go`.
- [x] **Task 2.8**: Jalankan `go run ./tools/featuregen` dan verifikasi `go test ./...`.

---

### Phase 3: Persistent Features Migration (Decoupling Database)
*Tujuan: Memindahkan domain model, SQL, dan migrasi skema ke dalam package fitur terkait.*

- [x] **Task 3.1 — `notes`**:
  - Pindahkan struct `Note` dan interface `NotesRepository` ke `plugins/notes/repository.go`.
  - Pindahkan SQL implementasi dari `internal/database/notes.go` ke `plugins/notes/sqlite.go`.
  - Buat `plugins/notes/migration.go` (`notes.001` mengadopsi legacy versi 1).
  - Buat `plugins/notes/module.go` (mengimplementasikan `module.Module` dan `database.MigrationProvider`).
  - Buat `plugins/notes/migration_test.go` (uji fresh DB & adoption).
  - Hapus `internal/database/notes.go` dan hapus dari `catalog.go`.
- [x] **Task 3.2 — `afk`**:
  - Pindahkan struct `AFK` dan interface `AFKRepository` ke `plugins/afk/repository.go`.
  - Pindahkan SQL implementasi dari `internal/database/afk.go` ke `plugins/afk/sqlite.go`.
  - Buat `plugins/afk/migration.go` (`afk.001` mengadopsi legacy versi 1).
  - Buat `plugins/afk/module.go`.
  - Buat `plugins/afk/migration_test.go`.
  - Hapus `internal/database/afk.go` dan hapus dari `catalog.go`.
- [x] **Task 3.3 — `filters`**:
  - Pindahkan struct `Filter` dan interface `FiltersRepository` ke `plugins/filters/repository.go`.
  - Pindahkan SQL implementasi dari `internal/database/filters.go` ke `plugins/filters/sqlite.go`.
  - Buat `plugins/filters/migration.go` (`filters.001` mengadopsi legacy versi 1).
  - Buat `plugins/filters/module.go`.
  - Buat `plugins/filters/migration_test.go`.
  - Hapus `internal/database/filters.go` dan hapus dari `catalog.go`.
- [x] **Task 3.4 — `blacklist`**:
  - Pindahkan interface `BlacklistRepository` ke `plugins/blacklist/repository.go`.
  - Pindahkan SQL implementasi dari `internal/database/blacklist.go` ke `plugins/blacklist/sqlite.go`.
  - Buat `plugins/blacklist/migration.go` (`blacklist.001` mengadopsi legacy versi 1).
  - Buat `plugins/blacklist/module.go`.
  - Buat `plugins/blacklist/migration_test.go`.
  - Hapus `internal/database/blacklist.go` dan hapus dari `catalog.go`.
- [x] **Task 3.5 — `sudo`**:
  - Pindahkan struct `SudoUser` dan interface `SudoRepository` ke `plugins/sudo/repository.go`.
  - Pindahkan SQL implementasi dari `internal/database/sudo.go` ke `plugins/sudo/sqlite.go`.
  - Buat `plugins/sudo/migration.go` (`sudo.001` mengadopsi legacy versi 1).
  - Buat `plugins/sudo/module.go`.
  - Buat `plugins/sudo/migration_test.go`.
  - Hapus `internal/database/sudo.go` dan hapus dari `catalog.go`.
- [x] **Task 3.6**: Jalankan `go run ./tools/featuregen` dan verifikasi migrasi berjalan deterministik.

---

### Phase 4: Domain Subsystems Presentation Migration
*Tujuan: Memodularisasi plugin yang membungkus sub-sistem domain di `internal/services/`.*

- [x] **Task 4.1 — `admin`**: Buat `plugins/admin/module.go` (mengambil `rt.ModService`).
- [x] **Task 4.2 — `media`**: Buat `plugins/media/module.go` (mengambil `rt.MediaService`).
- [x] **Task 4.3 — `downloader`**: Buat `plugins/downloader/module.go` (mengambil `rt.DownloadRegistry`, `rt.Storage`).
- [x] **Task 4.4 — `broadcast`**: Buat `plugins/broadcast/module.go` (mengambil `rt.BroadcastService`).
- [x] **Task 4.5 — `userlog`**: Buat `plugins/userlog/module.go` (mengambil `rt.UserlogService`, `rt.EventBus`).
- [x] **Task 4.6 — `pmpermit`**: Buat `plugins/pmpermit/module.go` (mengambil `rt.PMPermitService`).
- [x] **Task 4.7 — `addon`**: Buat `plugins/addon/module.go` (mengambil `rt.AddonManager`).
- [x] **Task 4.8 — `help`**: Buat `plugins/help/module.go` (mengambil `rt.Router`, meregister ke `rt.Callbacks`).
- [x] **Task 4.9 — `settings`**: Buat `plugins/settings/module.go` (mengambil `rt.SettingsService`, meregister ke `rt.Callbacks`).
- [x] **Task 4.10 — `scheduler`**: Buat `plugins/scheduler/module.go` (mengambil `rt.SchedEngine`).
- [x] **Task 4.11**: Hapus seluruh plugin di atas dari `internal/app/catalog.go`.
- [x] **Task 4.12**: Jalankan `go run ./tools/featuregen`.

---

### Phase 5: Catalog Deprecation & Architecture Enforcement
*Tujuan: Mengunci boundary arsitektur dan membersihkan sisa kode legacy.*

- [x] **Task 5.1**: Kosongkan `defaultPluginCatalog` di `internal/app/catalog.go`. Seluruh modul kini mendaftar secara deklaratif melalui compile-time generator.
- [x] **Task 5.2**: Bersihkan agregasi interface di `internal/database/repository.go` yang sudah dipindahkan ke fitur (`notes`, `afk`, `filters`, `blacklist`, `sudo`).
- [x] **Task 5.3**: Perluas dan verifikasi `internal/architecture/imports_test.go`:
  - `internal/database` dan `internal/core` tidak mengimpor `plugins/*`.
  - Tidak ada modul di `plugins/*` yang mengimpor `internal/app/*`.
  - Tidak ada modul di `plugins/*` yang mengimpor fitur lain di `plugins/*`.
  - Platform packages (`database`, `core`) tetap terisolasi.
- [x] **Task 5.4**: Jalankan verifikasi dual-path database (Fresh Install vs Legacy Upgraded Database) di semua test suite migrasi fitur.
- [x] **Task 5.5**: Validasi seluruh test suite aplikasi (`go test ./...` dan `go build ./cmd/goultroid`).

---

## Definition of Done (DoD)

Sebuah fitur dinyatakan selesai dirework jika:
1. Memiliki `module.go` yang mendefinisikan `Manifest()` dan `Register()`.
2. Terdaftar otomatis di `internal/app/generated_modules.go` via `tools/featuregen`.
3. Tidak diinisialisasi secara manual di `internal/app/catalog.go`.
4. Jika memiliki persistensi, tabel SQL, repository interface, dan query diimplementasikan di folder fitur tersebut (`sqlite.go`).
5. Jika memiliki skema DB, migrasi terdaftar dengan format namespaced (`<feature>.001`) dan memiliki test adopsi legacy.
6. Lolos uji isolasi dependensi pada `internal/architecture/imports_test.go`.
7. Seluruh unit test lokal dan test integrasi runtime lulus tanpa regresi.
