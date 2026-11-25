# AI Coding Plan — MyXL Scheduled Token Maintenance & Telegram Schedule Management

## 1. Informasi Proyek

- Repository: `inipew/goultroid`
- Branch: `test-next`
- Feature: MyXL Scheduled Token Maintenance
- Target: Telegram Userbot
- Status: M1–M8 Complete (Closed & Verified on `test-next`)
- Scope: MyXL token lifecycle, scheduled maintenance, Telegram configuration, observability, persistence, dan resource management.

Dokumen ini menjadi acuan utama perencanaan, implementasi, pengujian, dan evaluasi fitur refresh token otomatis MyXL.

---

# 2. Latar Belakang

Plugin MyXL saat ini sudah memiliki mekanisme pengelolaan token CIAM, meliputi:

1. `EnsureFreshToken()` untuk memeriksa masa berlaku token sebelum request API.
2. `DefaultTokenRefreshSkew` sebagai toleransi sebelum token kedaluwarsa.
3. `refreshSingleflight()` untuk menghindari refresh bersamaan pada akun yang sama.
4. `refreshAfterUnauthorized()` untuk menangani HTTP 401.
5. `ForceRefreshToken()` untuk refresh secara manual.
6. `refreshAllTokens()` untuk memperbarui seluruh akun tersimpan secara manual.

Namun, mekanisme tersebut masih bergantung pada aktivitas request atau tindakan manual.

Akun yang jarang digunakan tidak mendapatkan refresh proaktif sebelum token kedaluwarsa.

Hal ini menimbulkan beberapa risiko:

- Token akun yang tidak aktif dapat kedaluwarsa.
- Refresh baru dilakukan ketika akun digunakan kembali.
- Pengguna tidak mempunyai kontrol jadwal refresh dari Telegram.
- Tidak tersedia pengelolaan interval maintenance yang terpusat.
- Tidak tersedia ringkasan keberhasilan dan kegagalan maintenance berkala.

## 2.1 Tujuan

Membangun mekanisme pemeliharaan token otomatis yang:

- Memperbarui token akun MyXL sebelum kedaluwarsa.
- Dapat dijadwalkan dan dikonfigurasi melalui Telegram.
- Mendukung seluruh akun tersimpan.
- Memiliki kontrol enable/disable.
- Mendukung interval dan waktu maintenance yang dapat diatur.
- Memiliki batas concurrency dan resource yang jelas.
- Tetap aman ketika terjadi restart Goultroid.
- Tidak mengganggu request MyXL normal.
- Tidak membuat runtime atau execution engine baru.
- Tidak memaksa refresh token yang sebenarnya masih cukup lama masa berlakunya.
- Memiliki observability yang memadai.

## 2.2 Non-Goals

Fitur ini TIDAK bertujuan untuk:

- Membuat scheduler baru di dalam plugin MyXL.
- Membuat background worker permanen.
- Membuat TaskEngine kedua.
- Membuat retry engine khusus MyXL.
- Mengubah protokol autentikasi CIAM.
- Melakukan login ulang otomatis menggunakan OTP.
- Menyimpan password atau OTP untuk autentikasi otomatis.
- Menjamin refresh token selalu berhasil.
- Memperbarui seluruh token pada setiap siklus tanpa memeriksa expiry.
- Mengubah sistem refresh token yang sudah digunakan request normal.
- Menambahkan layanan eksternal atau database tambahan.
- Melakukan refresh token terus-menerus hanya untuk menjaga token tetap baru.

---

# 3. Arsitektur yang Wajib Dipertahankan

Fitur wajib menggunakan infrastruktur yang sudah dimiliki Goultroid.

## 3.1 Arsitektur eksekusi

Alur yang diharapkan:

Telegram Configuration
        |
        v
MyXL Settings / Schedule Management
        |
        v
Persisted Schedule Configuration
        |
        v
Existing Scheduler
        |
        v
Existing Jobs / JobManager
        |
        v
Existing TaskEngine
        |
        v
MyXL Maintenance Handler
        |
        v
Token Expiry Evaluation
        |
        v
EnsureFreshToken / Shared Refresh Logic
        |
        v
Existing Managed HTTP Client (CIAM)
        |
        v
Persist Updated Tokens
        |
        v
Maintenance Result / Diagnostics

Diagram tersebut merupakan logical flow, bukan instruksi untuk membuat setiap komponen sebagai service baru.

## 3.2 Pembagian tanggung jawab

### MyXL Plugin

Bertanggung jawab atas:

- Membaca konfigurasi maintenance.
- Memilih akun yang memenuhi syarat.
- Memeriksa expiry token.
- Menjalankan refresh melalui client yang sudah tersedia.
- Mengumpulkan hasil maintenance.
- Menyediakan Telegram interface.
- Menyediakan status dan diagnostics.

### Scheduler

Bertanggung jawab atas:

- Memicu maintenance berdasarkan jadwal.
- Mengelola aktivasi dan perubahan jadwal.
- Menjaga lifecycle schedule.
- Menghindari pemicu ganda.
- Menangani penghentian dan perubahan konfigurasi.

### JobManager

Bertanggung jawab atas:

- Mengelola logical job yang durable.
- Menangani attempt dan status pekerjaan.
- Mendukung recovery sesuai mekanisme yang sudah tersedia.

### TaskEngine

Bertanggung jawab atas:

- Admission control.
- Concurrency.
- Fairness.
- Resource budgeting.
- Execution timeout.
- Cancellation.
- Pengaturan beban eksekusi.

### RPCExecutor

Bertanggung jawab atas:

- Eksekusi Telegram-independent HTTP/RPC yang memang menggunakan executor tersebut.
- FloodWait dan retry untuk Telegram RPC sesuai kebijakan yang sudah ada.

Telegram RPCExecutor hanya mengatur MTProto. Request CIAM MyXL menggunakan managed HTTP client dari network service; jangan menyebutnya atau mengarahkannya ke Telegram RPCExecutor. Audit timeout, cancellation, transport ownership, dan batas request HTTP yang benar-benar tersedia.

---

# 4. Aturan Fundamental Token Refresh

## 4.1 Jangan refresh semua akun secara paksa

Implementasi `refreshAllTokens()` yang ada menggunakan `ForceRefreshToken()` untuk seluruh akun.

Metode ini cocok untuk tindakan manual yang memang meminta refresh paksa.

Namun, scheduled maintenance harus berbeda.

Scheduled maintenance wajib mengevaluasi expiry terlebih dahulu.

Contoh:

| Kondisi token | Tindakan |
|---|---|
| Expiry masih 24 jam | Skip |
| Expiry masih 2 jam | Skip |
| Expiry masih 15 menit | Refresh jika masuk jendela maintenance |
| Expiry tinggal 1 menit | Prioritaskan refresh |
| Token sudah kedaluwarsa | Coba refresh |
| Expiry tidak diketahui | Evaluasi kebijakan fallback |
| Refresh token kosong | Skip dan laporkan |
| Akun tidak valid | Skip dan laporkan |

Angka di atas adalah contoh kondisi, bukan nilai konfigurasi final.

## 4.2 Gunakan expiry sebagai dasar keputusan

Gunakan:

- `Account.TokenExpiresAt`
- `DefaultTokenRefreshSkew`
- `DefaultTokenExpiryFallback`

Jangan membuat perhitungan expiry kedua yang berbeda dari mekanisme client.

Buat satu kebijakan keputusan yang konsisten antara scheduled maintenance dan request normal.

Jika `TokenExpiresAt` tidak tersedia atau tidak dapat dipercaya:

1. Bedakan expiry yang benar-benar tidak diketahui dari expiry estimasi yang dibuat oleh `DefaultTokenExpiryFallback` ketika CIAM tidak mengirim `expires_in`. Nilai fallback 60 menit adalah estimasi lokal, bukan expiry otoritatif dari CIAM.
2. Jangan menganggap token pasti masih valid.
2. Jangan otomatis melakukan refresh paksa setiap siklus.
3. Audit asal dan arti expiry yang tidak diketahui.
4. Tentukan apakah pemeriksaan atau refresh diperlukan berdasarkan kontrak autentikasi.
5. Laporkan status yang tidak dapat dipastikan secara eksplisit.

## 4.3 Gunakan mekanisme refresh yang sudah ada

Prioritas:

1. Gunakan `EnsureFreshToken()` apabila perilakunya sesuai.
2. Gunakan `refreshSingleflight()` melalui API internal yang sesuai apabila diperlukan.
3. Hindari pemanggilan `ForceRefreshToken()` untuk maintenance normal.
4. Pertahankan recovery HTTP 401.
5. Pertahankan persistence token hasil refresh.

Jangan membuat mekanisme refresh paralel yang melewati singleflight.

## 4.4 Hindari refresh token storm

Satu siklus maintenance tidak boleh membuat seluruh akun melakukan refresh serentak.

Wajib tersedia:

- Batas concurrency.
- Pembatasan jumlah akun per batch bila diperlukan.
- Jeda atau admission yang mengikuti kebijakan resource Goultroid.
- Cancellation.
- Execution timeout.
- Batas waktu keseluruhan siklus.
- Pencegahan siklus tumpang tindih.

Jangan menggunakan `time.Sleep()` sebagai pengganti mekanisme scheduling atau admission control.

---

# 5. Konfigurasi Telegram

Pengguna harus dapat mengelola scheduled maintenance sepenuhnya dari Telegram.

Fitur ini harus tersedia melalui:

1. Native command MyXL.
2. Assistant/native menu MyXL yang sudah ada.
3. Inline interaction apabila sesuai dengan arsitektur a2.

Jangan membuat dua sistem konfigurasi terpisah untuk command dan menu.

Semua jalur wajib menggunakan satu configuration service dan satu sumber kebenaran.

## 5.1 Menu utama

Tambahkan menu:

MyXL
  |
  +-- Kelola Akun
  |
  +-- Paket dan Kuota
  |
  +-- Pembelian
  |
  +-- Token Maintenance
         |
         +-- Status
         +-- Aktifkan / Nonaktifkan
         +-- Atur Interval
         +-- Atur Waktu
         +-- Atur Jendela Refresh
         +-- Atur Akun
         +-- Jalankan Sekarang
         +-- Riwayat
         +-- Kembali

Menu harus mengikuti pola UI dan navigation MyXL yang sudah ada.

Jangan membuat callback state store atau session registry baru.

Gunakan mekanisme interaction/session canonical yang sudah tersedia.

## 5.2 Command Telegram

Usulan command:

### Status

`.myxl schedule`

Menampilkan:

- Status maintenance.
- Interval.
- Zona waktu.
- Jadwal berikutnya.
- Waktu maintenance terakhir.
- Hasil maintenance terakhir.
- Jumlah akun terdaftar.
- Jumlah akun yang membutuhkan refresh.
- Jumlah akun yang berhasil.
- Jumlah akun gagal.
- Jumlah akun yang dilewati.

### Enable

`.myxl schedule enable`

Mengaktifkan scheduled maintenance.

### Disable

`.myxl schedule disable`

Menonaktifkan scheduled maintenance tanpa menghapus konfigurasi.

### Interval

`.myxl schedule interval 6h`

Mengatur interval pemeriksaan.

Contoh nilai yang diperbolehkan:

- 1h
- 3h
- 6h
- 12h
- 24h

Nilai final wajib ditentukan setelah audit Scheduler dan batasan operasional.

### Waktu

`.myxl schedule time 03:00`

Mengatur anchor waktu lokal untuk menentukan `NextDueAt` awal/phase schedule. Karena `jobs.JobSchedule` saat ini hanya mendukung recurrence `once` dan `interval` (bukan cron/daily), `DailyTime` bukan jadwal harian terpisah. Contoh interval 6h dengan anchor 03:00 menghasilkan jadwal 03:00, 09:00, 15:00, dan 21:00 pada zona waktu yang dipilih.

Waktu harus mengikuti zona waktu yang ditentukan secara eksplisit. Perilaku perubahan DST perlu didefinisikan; jangan mengasumsikan interval 24h selalu sama dengan jam dinding harian.

Default zona waktu yang diusulkan:

`Asia/Jakarta`

Jangan mengasumsikan zona waktu server selalu sama dengan zona waktu pengguna.

### Jendela refresh

`.myxl schedule window 30m`

Mengatur jendela sebelum expiry ketika akun boleh dipertimbangkan untuk refresh.

Nilai harus dibatasi dan divalidasi.

### Akun

`.myxl schedule accounts all`

Memilih semua akun.

`.myxl schedule accounts active`

Memilih hanya akun aktif.

Jika diperlukan, tambahkan pemilihan akun individual melalui menu.

### Manual execution

`.myxl schedule run`

Meminta satu siklus maintenance segera.

Manual execution tidak boleh membuat jalur eksekusi baru.

Gunakan pipeline Scheduler/Jobs/TaskEngine yang sama, dengan pemicu manual.

Manual run harus tetap menghormati:

- Concurrency.
- Admission control.
- Expiry evaluation.
- Singleflight.
- Cancellation.
- Execution timeout.

Jangan mengartikan `run` sebagai refresh paksa semua token.

## 5.3 Validasi konfigurasi

Semua konfigurasi harus divalidasi sebelum disimpan.

Wajib menolak:

- Interval nol atau negatif.
- Durasi di luar batas yang diizinkan.
- Format waktu tidak valid.
- Zona waktu tidak valid.
- Akun yang tidak terdaftar.
- Konfigurasi yang melampaui batas resource.
- Nilai yang dapat menyebabkan polling terlalu sering.

Jika konfigurasi tidak valid, tampilkan alasan yang jelas.

Jangan diam-diam mengganti nilai yang salah dengan default.

---

# 6. Desain Konfigurasi dan Persistence

Konfigurasi schedule harus durable.

Restart Goultroid tidak boleh menghilangkan pengaturan pengguna.

Contoh struktur konseptual:

MyXLTokenMaintenanceConfig (konseptual):
- Enabled
- Interval
- DailyTime (anchor untuk menentukan fase/jadwal pertama, bukan cron harian terpisah)
- Timezone
- RefreshWindow
- AccountScope
- LastRunAt (hanya jika tidak dapat diperoleh dari riwayat eksekusi yang sudah ada)
- UpdatedAt

`NextRunAt` tidak boleh menjadi sumber kebenaran kedua. Waktu due berikutnya dimiliki oleh durable `jobs.JobSchedule`; jangan menduplikasi dan menyinkronkan dua nilai `next_run_at` tanpa kebutuhan API yang terbukti.

Struktur ini bersifat konseptual.

Jangan langsung membuat tabel atau struct baru sebelum memeriksa:

- Existing scheduler persistence.
- Existing job schedule representation.
- Existing feature settings.
- Database migration conventions.
- Existing configuration versioning.

## 6.1 Aturan persistence

1. Konfigurasi schedule harus tersimpan secara durable.
2. Jangan menyimpan access token atau refresh token dalam konfigurasi schedule.
3. Token tetap menjadi milik repository MyXL.
4. Jangan membuat salinan token pada job payload.
5. Job payload hanya boleh membawa identifier dan metadata non-secret.
6. Jangan menyimpan seluruh account object ke durable job payload.
7. Pastikan perubahan konfigurasi bersifat atomic.
8. Perubahan schedule harus aman terhadap restart.
9. Hindari menyimpan nilai turunan yang dapat dihitung ulang tanpa alasan kuat.

## 6.2 Restart recovery

Ketika Goultroid dimulai:

- Baca konfigurasi maintenance.
- Periksa apakah fitur enabled.
- Pulihkan jadwal menggunakan Scheduler yang ada.
- Hindari membuat schedule duplikat.
- Jangan langsung menjalankan seluruh akun hanya karena aplikasi restart.
- Tentukan kebijakan missed execution.
- Jangan melakukan burst refresh setelah downtime.

Kebijakan missed execution yang diusulkan:

Jika jadwal terlewat ketika aplikasi mati, lakukan satu maintenance catch-up setelah runtime siap, bukan menjalankan seluruh jadwal yang terlewat satu per satu.

Catch-up tetap harus melewati admission control dan expiry evaluation.

---

# 7. Account Selection dan Maintenance Policy

## 7.1 Scope akun

Minimal mendukung:

- All accounts.
- Active account.
- Selected accounts jika mekanisme pemilihan dapat dibuat sederhana dan durable.

Default yang diusulkan: All accounts.

Namun, akun tanpa refresh token harus dilewati.

## 7.2 Prioritas akun

Urutan prioritas:

1. Token sudah expired.
2. Token mendekati expiry.
3. Token tidak diketahui masa berlakunya tetapi membutuhkan evaluasi.
4. Token masih valid dan jauh dari expiry: skip.

Jangan memproses seluruh akun dengan prioritas sama.

## 7.3 Hasil per akun

Setiap akun harus menghasilkan status yang eksplisit:

- Refreshed.
- Already Fresh.
- Skipped.
- Missing Refresh Token.
- Authentication Rejected.
- Network Error.
- Rate Limited.
- Persistence Error.
- Cancelled.
- Unknown Expiry.

Jangan menyamakan seluruh kegagalan menjadi satu status generik.

## 7.4 Kegagalan parsial

Kegagalan satu akun tidak boleh menghentikan seluruh batch kecuali:

- Context dibatalkan.
- Database mengalami kegagalan fatal.
- Runtime sedang shutdown.
- Batas keselamatan global tercapai.

Akun berikutnya harus tetap dapat diproses setelah kegagalan individual.

Jangan menghapus token lama hanya karena refresh gagal.

Jangan mengubah akun aktif ketika melakukan maintenance akun lain.

---

# 8. Resource dan Performance Requirements

Ini merupakan bagian wajib karena tujuan Goultroid adalah fast, lightweight, reliable, dan predictable.

## 8.1 Goroutine

DILARANG:

- Membuat goroutine permanen khusus MyXL scheduler.
- Membuat ticker per akun.
- Membuat satu worker per nomor telepon.
- Membuat background refresh loop yang tidak bounded.
- Menjalankan goroutine tanpa lifecycle dan cancellation.

Gunakan scheduler dan execution infrastructure yang tersedia.

## 8.2 Memory

DILARANG:

- Menyimpan seluruh token dalam cache baru.
- Membuat map akun tanpa batas.
- Menyimpan histori maintenance tanpa batas.
- Menyimpan duplicate account state.
- Membuat cache expiry yang tidak diperlukan.

Semua state tambahan harus memiliki batas yang jelas.

## 8.3 Database

- Hindari membaca seluruh akun berkali-kali dalam satu siklus.
- Gunakan query repository yang sudah tersedia.
- Hindari N+1 query apabila dapat dicegah.
- Jangan membuka koneksi database baru.
- Jangan membuat transaction panjang selama HTTP request.
- Jangan menahan database lock ketika menunggu CIAM.
- Jangan menulis konfigurasi setiap kali scheduler melakukan tick.
- Persist hasil secara efisien.
- Perhatikan SQLite busy/lock contention.

## 8.4 HTTP dan Network

- Gunakan HTTP client MyXL yang sudah ada.
- Gunakan network service dan ownership yang sesuai.
- Hormati timeout.
- Hormati cancellation.
- Hindari retry berlebihan.
- Jangan melakukan refresh paralel tanpa batas.
- Jangan menganggap HTTP 429 atau 5xx sebagai alasan refresh tanpa evaluasi.
- Jangan melakukan retry OTP otomatis.

## 8.5 Idle footprint

Saat maintenance disabled:

- Tidak boleh ada periodic work.
- Tidak boleh ada worker MyXL yang terus hidup.
- Tidak boleh ada polling database berkala.
- Tidak boleh ada network request.
- Tidak boleh ada cache maintenance yang terus bertambah.

Saat enabled tetapi belum waktunya:

- Tidak boleh ada aktivitas HTTP MyXL.
- Tidak boleh ada pemrosesan akun.
- Tidak boleh ada polling khusus yang bersaing dengan scheduler global.

---

# 9. Concurrency dan Idempotency

## 9.1 Singleflight

Tetap gunakan singleflight refresh yang ada.

Scheduled refresh tidak boleh membuat mekanisme singleflight kedua.

Jika request normal dan scheduled maintenance meminta refresh akun yang sama:

- Hanya satu refresh efektif yang dilakukan.
- Hasil refresh dapat digunakan oleh pemanggil lain sesuai semantik existing client.
- Token yang sudah diperbarui tidak boleh ditimpa dengan hasil refresh yang lebih lama.

## 9.2 Overlapping execution

Tidak boleh ada dua maintenance cycle aktif untuk konfigurasi dan scope yang sama.

Gunakan mekanisme admission/idempotency yang sudah ada.

Jangan membuat distributed lock baru sebelum membuktikan bahwa Scheduler/JobManager tidak menyediakan mekanisme yang dibutuhkan.

## 9.3 Configuration changes

Jika pengguna mengubah interval ketika job sedang berjalan:

- Job yang sedang berjalan tidak boleh menggunakan konfigurasi yang berubah secara tidak konsisten.
- Definisikan snapshot konfigurasi per execution.
- Jadwal berikutnya harus menggunakan konfigurasi terbaru.
- Hindari membatalkan refresh yang sedang melakukan pertukaran token kecuali shutdown atau cancellation yang memang diwajibkan.

## 9.4 Manual run

Jika pengguna menjalankan `.myxl schedule run` ketika maintenance sedang berjalan:

- Jangan membuat batch duplikat.
- Berikan informasi bahwa maintenance sedang berjalan.
- Jangan membuat antrean manual tanpa batas.
- Terapkan kebijakan coalescing atau idempotent admission.

---

# 10. Security Requirements

Token CIAM merupakan data sensitif.

Wajib:

1. Tidak mencetak access token ke log.
2. Tidak mencetak ID token ke log.
3. Tidak mencetak refresh token ke log.
4. Tidak memasukkan token ke job payload.
5. Tidak menampilkan token lengkap pada Telegram.
6. Tidak menyimpan token di diagnostics.
7. Tidak mengirim token ke channel atau grup.
8. Memastikan hanya pengguna yang berwenang mengubah konfigurasi.
9. Memvalidasi callback/session ownership.
10. Tidak membocorkan MSISDN lengkap dalam group chat.

Perlu diperiksa kembali command dan menu yang sudah ada agar informasi sensitif dari hasil `.myxl refresh` tidak ikut ditampilkan secara tidak perlu.

Jangan memperluas hak akses hanya karena fitur berjalan otomatis.

---

# 11. Observability dan Diagnostics

Tambahkan diagnostics yang terintegrasi dengan observability Goultroid.

Minimal informasi:

- Schedule enabled.
- Interval.
- Timezone.
- Next run.
- Last run.
- Last duration.
- Accounts scanned.
- Accounts refreshed.
- Accounts already fresh.
- Accounts skipped.
- Accounts failed.
- Current execution state.

## 11.1 Metrics

Usulan metrics:

- `myxl_token_maintenance_runs_total`
- `myxl_token_maintenance_duration_seconds`
- `myxl_token_maintenance_accounts_scanned_total`
- `myxl_token_maintenance_accounts_refreshed_total`
- `myxl_token_maintenance_accounts_skipped_total`
- `myxl_token_maintenance_accounts_failed_total`

Nama final mengikuti konvensi observability yang sudah ada.

Jangan membuat metrics registry kedua.

## 11.2 Logging

Log hanya informasi yang diperlukan:

- Maintenance started.
- Maintenance completed.
- Maintenance cancelled.
- Account refresh outcome.
- Schedule changed.
- Schedule disabled.
- Schedule recovery.

Jangan mencatat setiap scheduler tick jika tidak ada pekerjaan.

Gunakan structured logging dan diagnostic classification yang konsisten.

---

# 12. Telegram Notification

Pengguna dapat mengatur notifikasi hasil maintenance.

Usulan:

- Notification enabled/disabled.
- Notify on failure.
- Notify on completion.
- Summary only.

Default yang direkomendasikan:

- Notifikasi keberhasilan: disabled.
- Notifikasi kegagalan: enabled.
- Summary: ringkasan agregat.
- Jangan mengirim satu pesan untuk setiap akun.

Notifikasi tidak boleh memperlambat maintenance.

Kegagalan pengiriman notifikasi tidak boleh mengubah hasil refresh token menjadi gagal.

Gunakan jalur pengiriman Telegram yang sudah ada.

Hindari menyimpan antrean notifikasi khusus MyXL.

---

# 13. Error Handling

| Kondisi | Tindakan |
|---|---|
| HTTP 401 | Gunakan recovery existing |
| HTTP 429 | Hormati retry policy yang sesuai |
| HTTP 5xx | Catat kegagalan, jangan retry tanpa batas |
| Network timeout | Tandai gagal dan lanjutkan sesuai kebijakan |
| Refresh token invalid | Tandai perlu login ulang |
| Refresh token kosong | Skip |
| Database failure | Tandai kegagalan persistence dan jangan menyatakan sukses |
| Context cancelled | Hentikan pekerjaan secara kooperatif |
| Runtime shutdown | Ikuti lifecycle cancellation |
| Schedule duplicate | Tolak atau coalesce |
| Config invalid | Tolak perubahan |
| Notification failure | Catat terpisah |

Perhatikan perbedaan antara:

- Token berhasil diperbarui di CIAM.
- Token berhasil disimpan ke database.
- Notifikasi berhasil dikirim.

Ketiganya bukan satu status yang sama.

Jangan menyatakan maintenance berhasil penuh jika token baru gagal dipersist.

---

# 14. Testing Requirements

Testing wajib mencakup unit test, integration test, lifecycle test, concurrency test, dan resource test.

## 14.1 Configuration

- Default configuration.
- Enable/disable.
- Interval validation.
- Time validation.
- Timezone validation.
- Invalid account scope.
- Persistence.
- Restart recovery.
- Configuration migration.

## 14.2 Token expiry

- Token masih valid.
- Token mendekati expiry.
- Token sudah expired.
- Token tanpa expiry.
- Token dengan fallback expiry.
- Token tanpa refresh token.
- Akun tidak terautentikasi.

## 14.3 Refresh

- Refresh berhasil.
- Refresh gagal.
- Refresh token ditolak.
- HTTP 401.
- HTTP 429.
- HTTP 5xx.
- Timeout.
- Persistence gagal.
- Refresh token rotation.
- Refresh tidak menimpa token lebih baru.

## 14.4 Concurrency

- Banyak akun diproses.
- Request normal bersamaan dengan maintenance.
- Dua maintenance trigger bersamaan.
- Manual run ketika maintenance berjalan.
- Disable ketika maintenance berjalan.
- Configuration update ketika maintenance berjalan.
- Cancellation ketika batch berlangsung.
- Singleflight lintas jalur request dan maintenance.

## 14.5 Scheduler lifecycle

- Enable.
- Disable.
- Restart.
- Recovery.
- Missed schedule.
- Interval update.
- Timezone update.
- Duplicate schedule prevention.
- Shutdown.
- Re-registration.

## 14.6 Resource

- Tidak ada goroutine permanen saat disabled.
- Tidak ada periodic DB polling khusus MyXL.
- Tidak ada network activity ketika belum waktunya.
- Memory tetap bounded.
- Tidak ada worker per akun.
- Tidak ada pertumbuhan histori tanpa batas.
- Tidak ada concurrency melebihi konfigurasi.

## 14.7 Race detector

Wajib:

`go test -race ./...`

Jika terlalu mahal selama iterasi, jalankan pengujian package terkait terlebih dahulu, kemudian full race test pada tahap verifikasi akhir.

Jangan mengklaim keberhasilan test yang belum dijalankan.

---

# 15. Tahapan Implementasi

Pekerjaan wajib dilakukan secara bertahap.

Jangan langsung mengimplementasikan seluruh fitur sebelum audit arsitektur selesai.

## Phase M1 — Architecture Audit

**Status: Complete.** Hasil audit tersedia di `docs/design/myxl-token-maintenance-audit.md`. Jangan mengulang M1 kecuali ada perubahan implementasi yang membuat temuan audit stale.

Tujuan:

Memahami implementasi Scheduler, Jobs, TaskEngine, MyXL lifecycle, dan konfigurasi yang sudah tersedia.

Pekerjaan:

1. Verifikasi remote HEAD branch `test-next`.
2. `git pull --ff-only`.
3. Catat exact HEAD.
4. Periksa status working tree.
5. Audit Scheduler API.
6. Audit JobManager persistence dan schedule lifecycle.
7. Audit TaskEngine admission, timeout, fairness, dan resource budget.
8. Audit module registration/unregistration.
9. Audit MyXL Plugin lifecycle.
10. Audit token expiry dan refresh implementation.
11. Audit database schema dan migration.
12. Audit Telegram command/menu.
13. Audit observability.

Output:

`docs/design/myxl-token-maintenance-audit.md`

Dokumen audit harus berisi:

- Existing components.
- Existing APIs.
- Lifecycle.
- Persistence.
- Resource constraints.
- Integration points.
- Risks.
- Missing capabilities.
- Recommended implementation approach.

Tidak boleh mengubah production code pada fase ini.

## Phase M2 — Design & Contract

**Status: Complete.** Kontrak implementasi tersedia di `docs/design/myxl-token-maintenance-design.md` dan menjadi acuan utama implementasi M3–M7.

Tujuan:

Menentukan kontrak implementasi berdasarkan hasil M1.

Pekerjaan:

1. Menentukan schedule representation (`interval` + `AnchorTime` + `Timezone`, `MisfireRunOnce`, `OverlapForbid`).
2. Menentukan configuration schema (`myxl_token_maintenance_config`, tanpa duplikasi `NextRunAt` dan tanpa token).
3. Menentukan expiry policy (`EnsureFreshTokenWithin` dengan shared `refreshGroup` singleflight, pemisahan expiry aktual/estimasi/unknown).
4. Menentukan account selection (`all`, `active`, `selected`, batas `MaxAccountsPerCycle`).
5. Menentukan execution semantics (sequential processing di pool `scheduler` / `PriorityMaintenance` via `JobClient` & `ScheduleClient`).
6. Menentukan concurrency & resource limits.
7. Menentukan cancellation & lifecycle (`InitPlugin` & `CancelByOwner`).
8. Menentukan restart recovery.
9. Menentukan notification (best-effort owner notification).
10. Menentukan diagnostics & bounded run history (`myxl_token_maintenance_runs`, maks 20 baris).
11. Menentukan Telegram command/menu contract (satu `MaintenanceService` untuk `.myxl schedule` dan menu a2).

Output:

`docs/design/myxl-token-maintenance-design.md`

Acceptance Criteria M2:

- [x] Seluruh API yang dirujuk terverifikasi dari source code.
- [x] Sumber kebenaran konfigurasi dan jadwal jelas (tanpa duplikasi `NextRunAt`).
- [x] Semantik interval, anchor, timezone, misfire, dan overlap terdefinisi.
- [x] Expiry aktual, estimasi lokal, dan unknown dibedakan; singleflight dipertahankan.
- [x] Kontrak execution, resource, keamanan, dan testing matrix lengkap.

Tidak boleh membuat runtime atau scheduler baru.

Tidak boleh mengimplementasikan production code sebelum kontrak disetujui.

## Phase M3 — Token Maintenance Core

**Status: Complete.** Commit `caf2d7dfa20f8e5b2c2dde4f13078c16fc8a4e66` (`feat(myxl): implement M3 token maintenance persistence and core`).
Output: `plugins/myxl/migration.go` (`myxl.008`), `plugins/myxl/maintenance_types.go`, `plugins/myxl/maintenance_repo.go`, `plugins/myxl/maintenance_core.go`, `plugins/myxl/client.go` (`EnsureFreshTokenWithin` + classified refresh errors), `plugins/myxl/maintenance_core_test.go`.

Tujuan:

Membangun logika maintenance tanpa Telegram interface terlebih dahulu.

Pekerjaan:

1. Implementasi expiry evaluation.
2. Implementasi account eligibility.
3. Integrasi dengan existing refresh mechanism.
4. Implementasi hasil per akun.
5. Partial failure handling.
6. Idempotency.
7. Context cancellation.
8. Bounded processing.
9. Persistence consistency.

Testing:

- Expiry tests.
- Refresh tests.
- Partial failure tests.
- Singleflight tests.
- Persistence tests.

## Phase M4 — Scheduler Integration

**Status: Complete.** Commit `8a01ed78ccd748c6261020893de6f61ebbceeb27` (`feat(myxl): integrate token maintenance with scheduler`). Integrasi dengan `plugin.JobClient` (`CapJobs`) dan `plugin.ScheduleClient` (`CapScheduler`) selesai dan terverifikasi.

Implementasi & Keputusan Lifecycle:

- `plugins/myxl/module.go`: mendeklarasikan `plugin.CapJobs` dan `plugin.CapScheduler`.
- `plugins/myxl/myxl.go` & `plugins/myxl/maintenance_scheduler.go`: `InitPlugin(pctx)` mendaftarkan handler `"token_maintenance"` dan `JobDefinition` (`ID: "token_maintenance"`, scoped `plugin:myxl:token_maintenance`, `Pool: "scheduler"`, `Class: tasks.PriorityMaintenance`, `Timeout: 10m`, `RetryPolicy: MaxAttempts: 1`).
- Restart tidak menimpa `next_due_at` pada `job_schedules` ketika `Enabled == true` (menjaga `MisfireRunOnce` dan `OverlapForbid`). Jika `Enabled == false`, `InitPlugin` memanggil `DisableSchedule` secara defensif.
- `ApplyMaintenanceConfig` memvalidasi dan menyimpan konfigurasi lalu menyinkronkan ke `ScheduleClient.SaveSchedule` / `DisableSchedule` dengan rollback best-effort jika sinkronisasi jadwal gagal.
- `TriggerMaintenanceNow` memicu eksekusi melalui `JobClient.Trigger` -> `JobManager` -> `TaskEngine` dengan guard `maintRunning`.
- Handler meneruskan `ctx` job ke `runTokenMaintenanceCycle()` dan mengembalikan `ctx.Err()` / error persistensi tanpa menyamarkannya sebagai sukses.
- Verifikasi: `gofmt -l plugins/myxl/` bersih, `go vet ./plugins/myxl/...` lulus, `go test -race ./plugins/myxl/...` lulus, `go test ./internal/architecture/...` lulus, `git diff --check` bersih.
- Temuan terbuka untuk M5/M7: antarmuka `.myxl schedule` + menu a2 (`MaintenanceService` UI), serta perbaikan masking MSISDN & redaksi token pada `.myxl refresh` (`handleRefreshToken`).

Acceptance Criteria M4:

- [x] Job & handler terdaftar melalui `pctx.Jobs()` (`plugin:myxl:token_maintenance`).
- [x] Schedule dikelola melalui `pctx.Schedules()` (`Recurrence: "interval"`, `MisfireRunOnce`, `OverlapForbid`).
- [x] Restart tidak mereset `next_due_at` pada `job_schedules`.
- [x] Enable/disable, update config, cancellation, dan partial failure teruji deterministik.
- [x] Tidak ada scheduler, ticker, worker pool, atau retry engine baru.

Tujuan:

Mengintegrasikan maintenance dengan Scheduler, Jobs, dan TaskEngine yang sudah ada.

Pekerjaan:

1. Schedule registration.
2. Durable schedule configuration.
3. Job admission.
4. Task execution profile.
5. Timeout.
6. Cancellation.
7. Restart recovery.
8. Duplicate prevention.
9. Manual trigger integration.
10. Lifecycle cleanup.

Tidak boleh:

- Membuat scheduler kedua.
- Membuat worker permanen baru.
- Membuat retry engine baru.
- Membuat job registry kedua.
- Menjalankan refresh langsung dari scheduler callback jika kontrak arsitektur mensyaratkan admission melalui Jobs dan TaskEngine.

## Phase M5 — Telegram Schedule Management

**Status: Complete.** Commit `585607c0c4e2420c975b6d33dee885329aab39b6` (`feat(myxl): add Telegram token maintenance management`). Pengelolaan jadwal maintenance melalui Telegram command (`.myxl schedule ...`) dan menu a2 (`MenuManager` + `assistant_interaction.go`) selesai dengan satu jalur `MaintenanceService`.

Output & Implementasi M5:

- `plugins/myxl/maintenance_service.go`: `MaintenanceService` (otoritas tunggal read/mutate/trigger/history, diserialisasi oleh `maintConfigMu`, memanggil `ApplyMaintenanceConfig` & `TriggerMaintenanceNow`, serta menangani `ErrScheduleSyncRollbackFailed`).
- `plugins/myxl/maintenance_telegram.go` & `plugins/myxl/myxl.go`: subcommand `.myxl schedule [status|enable|disable|interval|time|timezone|window|accounts|run|history]` dengan `PermissionOwner` dan format output jujur tanpa mengklaim `next_due_at`.
- `plugins/myxl/menu.go` & `plugins/myxl/assistant_interaction.go`: tombol dashboard `myxl:maintenance`, `BuildMaintenanceScreen`, `BuildMaintenanceHistoryScreen`, dan wizard input a2 (`maint_interval`, `maint_time`, `maint_tz`, `maint_window`, `maint_scope`).
- Security Fix: `handleRefreshToken` (`plugins/myxl/myxl.go`) tidak lagi menampilkan prefix `AccessToken`/`IDToken`/`RefreshToken` dan selalu me-masking MSISDN (`MaskMSISDN`). `token_refresh_all` (`plugins/myxl/assistant_interaction.go`) juga me-masking MSISDN.
- Verifikasi: `gofmt -l plugins/myxl/` bersih, `go vet ./plugins/myxl/...` lulus, `go test -race ./plugins/myxl/...` lulus (`13.981s`), `go test ./internal/architecture/...` lulus (`1.605s`), `git diff --check` bersih.
- Temuan terbuka untuk M6: structured logging, diagnostics counters, serta notifikasi owner opsional (`NotifyOnCompletion` / `NotifyOnFailure`).

Acceptance Criteria M5:

- [x] Command dan menu berbagi satu `MaintenanceService` melalui `ApplyMaintenanceConfig` dan `TriggerMaintenanceNow`.
- [x] `PermissionOwner` ditegakkan pada command dan sesi a2.
- [x] `.myxl refresh` dan `token_refresh_all` bebas kebocoran token dan me-masking MSISDN.
- [x] Tidak ada scheduler, runtime, atau protokol callback/session baru.
- [x] Seluruh test M3/M4/M5 dan architecture tests lulus.

Tujuan:

Membuat pengguna dapat mengatur fitur dari Telegram.

Pekerjaan:

1. Status schedule.
2. Enable/disable.
3. Interval.
4. Daily time.
5. Timezone.
6. Refresh window.
7. Account scope.
8. Manual run.
9. Riwayat.
10. Notification preferences.

Command dan menu wajib berbagi satu configuration service.

Semua interaction harus mengikuti canonical a2/session architecture.

Tidak boleh membuat callback protocol atau state registry kedua.

## Phase M6 — Observability & Diagnostics

**Status: Complete.** Commit `880f368e63efdefc8b7b02c506f28b5f09b3a7b5` (`feat(myxl): add token maintenance diagnostics`). Observability, structured logging, bounded diagnostics counters, dan best-effort owner notification selesai tanpa migration baru dan tanpa perubahan arsitektur.

Temuan Audit & Implementasi M6:

- **Structured Logging (`go.uber.org/zap`):** `rt.Logger` disuntikkan via `SetLogger` di `Module.Register`. Event lifecycle (`started`, `completed`, `partially failed`, `failed`, `cancelled`, `notification sent/failed`) menggunakan field terstruktur (`plugin`, `job_id`, `run_id`, `duration`, `account_count`, `processed_count`, `success_count`, `failure_count`, `skipped_count`, `outcome`, `error_class`, `notification_status`) tanpa raw error, token, atau MSISDN.
- **Diagnostics Counters (`MaintenanceDiagnostics`):** Counter atomik low-cardinality (`RunsTotal`, `RunsSucceeded`, `RunsPartialFailure`, `RunsFailed`, `RunsCancelled`, `RunsNoOp`, `AccountsExamined`, `AccountsRefreshed`, `AccountsFailed`, `AccountsSkipped`, `NotificationsAttempted`, `NotificationsSucceeded`, `NotificationsFailed`) diekspos melalui `Plugin.MaintenanceDiagnostics()`, tanpa label MSISDN/runID dan tanpa registry kedua.
- **Notification Preferences & Best-Effort Delivery:** Memanfaatkan kolom `notify_on_completion` (default `false`) dan `notify_on_failure` (default `true` sesuai kontrak M2/M3) di `myxl_token_maintenance_config` (tanpa migration baru), ditambah `.myxl schedule notify <completion|failure> <on|off>`. Notifikasi dikirim sinkron-terbatas (`5s` timeout) via `rt.MessageService` ke `rt.OwnerID` setelah run summary dipersistenkan; kegagalan notifikasi tidak mengubah outcome maintenance.
- **Resource & Security:** Nol goroutine baru, nol ticker/queue baru, sentinel credential & MSISDN terverifikasi tidak bocor di log maupun notifikasi.
- **Verifikasi:** `gofmt -l plugins/myxl/` bersih, `go vet ./plugins/myxl/...` lulus, `go test -race ./plugins/myxl/...` lulus (`15.540s`), `go test ./internal/architecture/...` lulus (`1.823s`), `git diff --check` bersih.

Tujuan:

Memberikan gambaran yang jelas mengenai kondisi maintenance.

Pekerjaan:

1. Schedule diagnostics.
2. Last execution.
3. Next execution.
4. Account counters.
5. Failure classification.
6. Duration.
7. Structured logging.
8. Metrics.
9. Telegram summary.

Hindari log berlebihan.

## Phase M7 — Hardening & Performance

**Status: Complete.** Commit `ec6b10cd00ddadf812a436ed80f3e06f9fe2bcbd` (`test(myxl): harden token maintenance lifecycle`). Hardening konkurensi, SQLite contention, token rotation, scheduler lifecycle, dan verifikasi benchmark/race selesai.

Temuan & Perbaikan M7:

- **SQLite WAL Snapshot Contention (`plugins/myxl/repository.go`):** `SQLiteRepository.Save` sebelumnya memulai transaksi `DEFERRED` lalu `SELECT` sebelum `UPDATE`, yang dapat memicu `SQLITE_BUSY_SNAPSHOT (517)` saat concurrent writers berjalan. Diperbaiki dengan write-intent `UPDATE myxl_accounts SET updated_at = updated_at WHERE 0` di awal transaksi agar `busy_timeout(5000)` menserialisasi writer secara bersih.
- **Konkurensi, Rotasi & Lifecycle (`plugins/myxl/maintenance_hardening_test.go`):**
  - `TestM7_SQLiteContention_ConfigRunsAndTokens`: 6 worker paralel × 10 iterasi pada file-backed SQLite WAL DB lulus tanpa error lock, retensi riwayat tetap `<= 20`.
  - `TestM7_TokenRotationAndSharedSingleflightAcrossNormalAndMaintenance`: rotasi penuh `AccessToken`/`IDToken`/`RefreshToken`/`TokenExpiresAt` + shared singleflight lintas `EnsureFreshToken` dan `EnsureFreshTokenWithin`.
  - `TestM7_ConcurrentTriggerGuardAndConfigChangeDuringRun`: guard `ErrMaintenanceAlreadyRunning`, perubahan config mid-flight aman, dan `runtime.NumGoroutine()` kembali ke baseline.
- **Benchmark (`go test -bench=BenchmarkMaintenanceCycle -benchmem ./plugins/myxl/...`):**
  - `1Account`: `577,996 ns/op`, `29,440 B/op`, `462 allocs/op`
  - `10Accounts` (5 due): `2,185,039 ns/op`, `135,632 B/op`, `2,072 allocs/op`
  - `50Accounts` (10 due, 40 fresh): `4,486,936 ns/op`, `288,677 B/op`, `4,429 allocs/op`
- **Verifikasi Penuh:** `go test -race ./...` (seluruh package repo lulus), `go vet ./...` lulus, `go test ./internal/architecture/...` lulus, `gofmt -l plugins/myxl/` bersih, `git diff --check` bersih.

Tujuan:

Memastikan fitur aman untuk penggunaan jangka panjang.

Pekerjaan:

1. Race testing.
2. Cancellation testing.
3. Restart testing.
4. Duplicate execution testing.
5. Resource profiling.
6. SQLite contention testing.
7. Network failure testing.
8. Token rotation testing.
9. Security review.
10. Regression testing.

## Phase M8 — Documentation Closure

**Status: Complete.** Seluruh fase M1–M8 telah selesai, diaudit terhadap implementasi aktual di `plugins/myxl/`, dan diverifikasi. Tidak ada pekerjaan lanjutan yang tertunda.

Ringkasan Commit Fase:

- **M1 (Audit & Plan):** `83498855f45b055433b21d54f8bf4a0873741686`, `da6b6e083b40a1b9b8ec6c1e3536a156c8b0f347`, `e8312f1bd70e30a336d19c835c2df553b862ca35`
- **M2 (Design & Contract):** `f9170fbbccd835f136fa7df86d2b968963c699cb`
- **M3 (Persistence & Core):** `caf2d7dfa20f8e5b2c2dde4f13078c16fc8a4e66`
- **M4 (Scheduler Integration):** `8a01ed78ccd748c6261020893de6f61ebbceeb27`
- **M5 (Telegram Management):** `585607c0c4e2420c975b6d33dee885329aab39b6`
- **M6 (Observability & Diagnostics):** `880f368e63efdefc8b7b02c506f28b5f09b3a7b5`
- **M7 (Hardening & Performance):** `ec6b10cd00ddadf812a436ed80f3e06f9fe2bcbd`

Keputusan Arsitektur Final:

- `jobs.JobSchedule` (`job_schedules`) adalah satu-satunya otoritas `next_due_at`, `MisfireRunOnce`, dan `OverlapForbid`; tidak ada `next_run_at` duplikat di `myxl_token_maintenance_config`.
- Eksekusi berjalan melalui `JobManager` dan `TaskEngine` (pool `"scheduler"`, `PriorityMaintenance`), menggunakan `Client.EnsureFreshTokenWithin` dengan shared `refreshGroup` singleflight dan write-intent SQLite di `SQLiteRepository.Save`.
- Pemrosesan bersifat sequential (maks `50` akun/siklus), riwayat bounded maks `20` baris, notifikasi owner best-effort (`NotifyOnCompletion=false`, `NotifyOnFailure=true`), dan nol goroutine/ticker/worker pool milik MyXL.

Tujuan:

Menutup implementasi dengan dokumentasi dan bukti verifikasi.

Dokumen:

- Architecture audit.
- Design.
- Configuration reference.
- Operational behavior.
- Troubleshooting.
- Test report.
- Final implementation report.

Update roadmap utama Goultroid jika diperlukan.

Jangan mengubah roadmap yang tidak berkaitan dengan pekerjaan ini.

---

# 16. Project Work Rules

Aturan berikut WAJIB dipatuhi oleh AI coding.

## 16.1 Git Rules

Sebelum setiap fase:

1. Periksa remote HEAD `test-next`.
2. Jalankan `git pull --ff-only`.
3. Catat exact HEAD sebelum perubahan.
4. Periksa working tree.
5. Jangan menimpa perubahan pengguna.
6. Jangan melakukan reset atau force push.
7. Jangan mengubah branch tanpa izin.

Setelah setiap fase:

- Periksa diff.
- Periksa status.
- Catat commit.
- Catat exact HEAD hasil pekerjaan.

## 16.2 Formatting

Sebelum commit:

- Jalankan `gofmt -w` pada seluruh file Go yang berubah.
- Jalankan pemeriksaan `gofmt -l`.
- Pastikan test files ikut diformat.

## 16.3 Testing

Setiap perubahan logic, API, atau function wajib diikuti pembaruan test.

Jangan menghapus test hanya untuk membuat test suite lulus.

Jangan melemahkan assertion concurrency atau determinism.

Jangan menggunakan arbitrary sleep untuk menyamarkan race condition.

Gunakan synchronization primitive dan deterministic test fixtures.

## 16.4 CI

Jangan memeriksa atau melakukan polling CI kecuali pengguna secara eksplisit meminta.

Local verification tetap wajib sesuai cakupan fase.

## 16.5 Architecture

DILARANG membuat:

- Second Scheduler.
- Second TaskEngine.
- Second JobManager.
- New RPC executor.
- New callback protocol.
- New background worker framework.
- New retry framework.
- Per-account goroutine.
- Unbounded cache.
- Unbounded queue.
- Separate MyXL runtime.

Gunakan komponen yang sudah ada.

Jika ternyata existing infrastructure tidak memiliki kemampuan yang dibutuhkan:

1. Dokumentasikan gap.
2. Jelaskan mengapa API yang ada tidak mencukupi.
3. Evaluasi perluasan komponen existing.
4. Berikan alternatif dengan dampak resource.
5. Jangan membuat komponen baru tanpa persetujuan pengguna.

## 16.6 Documentation First

Setiap pekerjaan nontrivial harus dimulai dengan audit dan desain.

Jangan langsung melakukan perubahan production code sebelum desain fase terkait selesai.

Setiap fase harus memiliki:

- Goal.
- Scope.
- Non-goals.
- Files affected.
- Implementation steps.
- Constraints.
- Test requirements.
- Acceptance criteria.
- Risks.

---

# 17. Acceptance Criteria

Fitur dianggap selesai hanya apabila seluruh kriteria berikut terpenuhi.

### Functional

- [x] Scheduled maintenance dapat diaktifkan dari Telegram.
- [x] Scheduled maintenance dapat dinonaktifkan dari Telegram.
- [x] Interval dapat diubah.
- [x] Waktu harian dapat diubah.
- [x] Timezone dapat diatur.
- [x] Refresh window dapat diatur.
- [x] Account scope dapat diatur.
- [x] Manual run tersedia.
- [x] Status dan riwayat tersedia.
- [x] Maintenance hanya memperbarui token yang membutuhkan refresh.
- [x] Akun gagal tidak menghentikan seluruh batch.
- [x] Konfigurasi tetap tersedia setelah restart.

### Architecture

- [x] Tidak ada scheduler baru.
- [x] Tidak ada TaskEngine baru.
- [x] Tidak ada JobManager baru.
- [x] Tidak ada worker permanen khusus MyXL.
- [x] Tidak ada refresh goroutine per akun.
- [x] Menggunakan existing singleflight.
- [x] Menggunakan existing token persistence.
- [x] Menggunakan canonical interaction/session architecture.
- [x] Mengikuti lifecycle runtime.

### Reliability

- [x] Tidak ada overlapping maintenance cycle.
- [x] Cancellation berfungsi.
- [x] Restart recovery berfungsi.
- [x] Token lama tidak terhapus ketika refresh gagal.
- [x] Token baru tidak tertimpa hasil refresh stale.
- [x] HTTP failure diklasifikasikan dengan benar.
- [x] SQLite contention ditangani.

### Performance

- [x] Disabled state tidak menghasilkan periodic work.
- [x] Idle network activity tidak bertambah.
- [x] Goroutine tetap bounded.
- [x] Memory tetap bounded.
- [x] Concurrency dibatasi.
- [x] Database access efisien.
- [x] Tidak ada retry storm.

### Security

- [x] Token tidak masuk log.
- [x] Token tidak masuk job payload.
- [x] Token tidak ditampilkan lengkap di Telegram.
- [x] Authorization Telegram benar.
- [x] MSISDN dilindungi pada group chat.

### Verification

- [x] Unit tests lulus.
- [x] Integration tests lulus.
- [x] Lifecycle tests lulus.
- [x] Concurrency tests lulus.
- [x] Race tests lulus.
- [x] Full `go test -race ./...` lulus.
- [x] Seluruh file Go sudah diformat.
- [x] Dokumentasi diperbarui.
- [x] Implementasi di-commit.

---

# 18. Final Design Principles

Prinsip yang tidak boleh dikompromikan:

1. **Expiry-driven, bukan interval-driven forced refresh.**

   Interval menentukan kapan pemeriksaan dilakukan. Expiry menentukan apakah token perlu diperbarui.

2. **One execution architecture.**

   Scheduler, Jobs, dan TaskEngine tetap menjadi satu jalur eksekusi.

3. **One token authority.**

   Client dan repository MyXL tetap menjadi sumber kebenaran token.

4. **One configuration authority.**

   Command dan Telegram menu menggunakan konfigurasi yang sama.

5. **Bounded by design.**

   Goroutine, concurrency, queue, memory, database access, dan histori harus mempunyai batas.

6. **No unnecessary work.**

   Akun yang tokennya masih valid tidak boleh melakukan HTTP refresh hanya karena jadwal tiba.

7. **Durable configuration, bounded history.**

   Konfigurasi bertahan setelah restart, sedangkan histori tidak boleh tumbuh tanpa batas.

8. **Failure must be explicit.**

   Jangan menyembunyikan kegagalan refresh, persistence, atau notifikasi.

9. **Runtime lifecycle is authoritative.**

   Maintenance harus mengikuti startup, shutdown, cancellation, dan reconfiguration runtime.

10. **Preserve existing behavior.**

    Request normal, refresh manual, HTTP 401 recovery, pembelian, kuota, dan seluruh fitur MyXL lainnya tidak boleh mengalami regresi.

---

# 19. Instruksi Awal untuk AI Coding Berikutnya

Mulai pekerjaan dari Phase M1 — Architecture Audit.

Jangan langsung mengimplementasikan scheduled refresh.

Urutan wajib:

1. Refresh remote HEAD branch `test-next`.
2. Catat exact HEAD.
3. Audit existing Scheduler.
4. Audit Jobs dan TaskEngine.
5. Audit MyXL plugin lifecycle.
6. Audit seluruh token refresh path.
7. Audit persistence dan expiry semantics.
8. Audit command dan menu MyXL.
9. Identifikasi API yang dapat digunakan tanpa membuat komponen baru.
10. Buat `docs/design/myxl-token-maintenance-audit.md`.

Pada fase M1:

- Jangan mengubah production code.
- Jangan membuat migration.
- Jangan membuat scheduler registration.
- Jangan membuat worker.
- Jangan mengubah token refresh behavior.
- Jangan memeriksa CI.
- Jangan menganggap API yang belum diperiksa tersedia.

Laporan akhir M1 harus menjelaskan secara konkret bagaimana fitur akan diintegrasikan dengan arsitektur Goultroid saat ini.

Jika terdapat konflik antara dokumen ini dan implementasi aktual, dokumentasikan konflik terlebih dahulu. Jangan mengubah arsitektur fundamental secara sepihak.

**Phase M2 dan seterusnya hanya boleh dimulai setelah hasil audit dievaluasi.**

End of document.
