# Runbook: Execution Runtime Cutover, Data Migration, and Disaster Recovery (ADR 0006)

**Versi**: 1.0.0  
**Tanggal**: 15 September 2026  
**Status**: Authoritative Production Runbook  
**Acuan**: [ADR 0006](file:///home/dhimas/any/random/ultroid-go/docs/adr/0006-execution-runtime-redesign.md) dan [03-implementation-plan.md](file:///home/dhimas/any/random/ultroid-go/docs/design/execution-redesign/03-implementation-plan.md) §6 & §7.

---

## 1. Ikhtisar & Prinsip Keamanan

Cutover dari arsitektur runtime eksekusi lama (berbasis `workers.Manager` dan polling `scheduled_jobs`) ke arsitektur redesigned (berbasis single `TaskEngine`, model `JobDefinition`/`JobSchedule`/`JobOccurrence`/`JobAttempt`, dan SQLite store) wajib mengikuti prinsip zero data loss, zero ambiguous side effects, dan strict boundary rollback.

> [!CAUTION]
> **Pesan Telegram adalah Efek Samping Eksternal**:
> Rollback database tidak dapat menarik kembali pesan atau aksi Telegram yang sudah terkirim. Jika terjadi crash di antara side-effect Telegram dan commit database, attempt harus dinilai sebagai `RecoveryRequired` dan tidak boleh langsung di-replay secara membabi-buta.

---

## 2. Prosedur Cutover Standar (8 Langkah)

### Langkah 1: Buat Consistent Backup SQLite (Mendukung WAL)
Jangan menyalin file database utama (`.db`) secara langsung ketika proses bot sedang menulis ke WAL (`.db-wal`).
Gunakan SQLite backup API atau command CLI vacuum into:
```bash
sqlite3 data/goultroid.db ".backup 'data/backup_pre_cutover_$(date +%Y%m%d_%H%M%S).db'"
```
Verifikasi integritas file backup:
```bash
sqlite3 data/backup_pre_cutover_*.db "PRAGMA integrity_check;"
```

### Langkah 2: Inisialisasi Skema Redesigned (Additive Schema)
Skema baru bersifat additif dan tidak merusak tabel lama (`scheduled_jobs`, dsb.):
```bash
go run ./tools/jobmigrator -db data/goultroid.db -dry-run
```
Pastikan tabel `job_definitions`, `job_schedules`, `job_occurrences`, `job_attempts`, `job_outbox`, dan `job_migration_map` telah terbentuk.

### Langkah 3: Eksekusi Dry-Run Migrasi
Jalankan analisis data legacy tanpa memodifikasi tabel:
```bash
go run ./tools/jobmigrator -db data/goultroid.db -dry-run
```
Periksa laporan JSON:
- `total_legacy_rows`: Total baris di `scheduled_jobs`.
- `mapped_count`: Baris valid yang siap dimigrasikan.
- `blocked_count`: Baris dengan payload corrupt atau `action_type` tak dikenal. Baris blocked harus diinvestigasi sebelum melanjutkan.
- `active_lease_count`: Baris yang sedang diklaim oleh worker lama.

### Langkah 4: Freeze Writer & Drain Legacy Execution
1. Set bot ke mode pemeliharaan (*maintenance/drain mode*) atau hentikan proses bot lama:
   ```bash
   pkill -SIGINT goultroid
   ```
2. Tunggu hingga semua background workers selesai drain (periksa log shutdown bersih).

### Langkah 5: Eksekusi Migrasi Transaksional
Jalankan migrasi di bawah kondisi writer freeze:
```bash
go run ./tools/jobmigrator -db data/goultroid.db
```
Semua baris legacy valid akan disalin ke `job_definitions`, `job_schedules`, dan dicatat relasinya di `job_migration_map` dalam satu transaksi tunggal atomik.

### Langkah 6: Validasi Delta & Checksum
Validasi integritas data antara tabel lama dan baru:
```bash
go run ./tools/jobmigrator -db data/goultroid.db -validate
```
Output wajib menunjukkan `"checksum_match": true` dan `discrepancies: []`. Jika validasi gagal, jangan memulai bot baru.

### Langkah 7: Commit Cutover Marker & Nyalakan New Runtime
Nyalakan binary baru goultroid:
```bash
./goultroid
```
Periksa log startup:
- Pastikan komponen `taskengine` dan `jobs` terdaftar di runtime DAG.
- Pastikan `loaded active external addons` dan `registered plugins` aktif tanpa error capability.

### Langkah 8: Rekonsiliasi Lease & Pengawasan Pasca-Cutover
Awasi metrik telemetri selama 30 menit pertama:
- Periksa status `job_attempts`: tidak boleh ada attempt yang tertahan di status `leased` melebihi `lease_until`.
- Periksa `job_outbox`: pastikan event delivery outbox terkirim secara berkala.

---

## 3. Batas & Prosedur Rollback

Terdapat tiga batasan rollback sesuai siklus migrasi (ADR 0006 §6.3):

### Skenario A: Rollback SEBELUM Eksekusi Baru Dimulai
Jika cutover dibatalkan sebelum bot baru menulis data/efek samping:
1. Hentikan proses bot baru.
2. Skema baru bersifat additif sehingga tabel legacy `scheduled_jobs` masih 100% utuh dan konsisten.
3. Jalankan kembali binary bot versi sebelumnya.

### Skenario B: Rollback SETELAH Ada Eksekusi Baru (Rollback Window)
Jika bot baru sudah berjalan dan memperbarui jadwal (`next_due_at`) atau status jadwal:
1. Hentikan proses bot baru: `pkill -SIGTERM goultroid`.
2. Jalankan proyeksi balik (*Reverse Projection*) agar mutasi status/jadwal dari `job_schedules` dikembalikan ke `scheduled_jobs`:
   ```bash
   go run ./tools/jobmigrator -db data/goultroid.db -rollback
   ```
3. Verifikasi data legacy telah terbarui.
4. Jalankan kembali binary bot versi lama.

### Skenario C: Disaster Recovery (Data Corrupt / Bencana)
Jika database mengalami inkonsistensi parah:
1. Hentikan seluruh proses bot: `pkill -9 goultroid`.
2. Ganti database aktif dengan file backup snapshot dari Langkah 1:
   ```bash
   cp data/backup_pre_cutover_*.db data/goultroid.db
   ```
3. Lakukan verifikasi PRAGMA:
   ```bash
   sqlite3 data/goultroid.db "PRAGMA integrity_check;"
   ```
4. Jalankan bot.

---

## 4. Runbook Penanganan Insiden Operasional

### Insiden 1: Admission Overload (Capacity Exceeded / Error 429)
*   **Gejala**: Log menunjukkan `admission rejected: capacity exceeded` atau callback query menerima peringatan *"Bot sedang sibuk"*.
*   **Akar Masalah**: Antrean backlog pool terisi penuh melebihi `BacklogLimit` atau `ResultCapacity` jenuh akibat eksekusi lambat.
*   **Tindakan**:
    1. Periksa metrik pool mana yang jenuh (`general`, `scheduler`, dsb.).
    2. Jika pool `scheduler` jenuh, periksa apakah ada task berulang dengan interval terlalu rapat (< 5s).
    3. Naikkan batas `BacklogLimit` atau `Concurrency` pada konfigurasi pool jika kapasitas hardware mencukupi.

### Insiden 2: Commit Pending / DB Contention
*   **Gejala**: Log menunjukkan `fallback background commit` atau attempt tertahan dalam status `leased`.
*   **Akar Masalah**: SQLite lock contention tinggi atau `PersistencePump` penuh karena I/O disk lambat.
*   **Tindakan**:
    1. Pastikan SQLite berjalan dalam mode WAL (`PRAGMA journal_mode=WAL;`).
    2. Tingkatkan `busy_timeout` pada database connection string (minimal 5000ms).
    3. Fallback commit otomatis di `jobs.Manager` (timeout 10s) akan mencoba commit langsung. Pantau log error untuk kegagalan I/O disk.

### Insiden 3: Unknown Attempt / Crash Recovery
*   **Gejala**: Log menunjukkan `recovery required` atau attempt kedaluwarsa tanpa hasil `CommitAttemptResult`.
*   **Akar Masalah**: Node/proses bot mati tiba-tiba saat tugas Telegram sedang berjalan.
*   **Tindakan**:
    1. Jangan me-resubmit attempt secara otomatis jika tugas tersebut mengirim pesan ke Telegram (mencegah pesan spam ganda).
    2. Evaluasi kolom `last_error` dan `lease_epoch`.
    3. Jika handler bersifat idempoten, tandai attempt sebagai `aborted` dan jadwalkan occurrence baru.
