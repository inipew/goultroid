Ya. Saya lanjut audit **`main` terbaru** `12df43152e4e08008e838e34e8ab003f081e5038`, dan kali ini saya sengaja **tidak menyentuh purge** dulu. Fokus saya ke bagian lain yang berpotensi menyebabkan bug runtime, security issue, race, lifecycle leak, atau klaim “production-ready” yang belum benar-benar terpenuhi.

## Hasil audit lanjutan

Saya menemukan beberapa hal yang cukup signifikan.

### 🔴 P0 — Voice subsystem masih memakai `MockBackend`

Di composition root:

```go
voiceBackend := voiceSvc.NewMockBackend()
voiceResolver := voiceSvc.NewResolver(mediaService, downloadRegistry)
voiceService := voiceSvc.NewService(voiceBackend, db, voiceResolver, logger)
```

Jadi seluruh command voice memang bisa terlihat “berfungsi” dari sisi state/internal API, tetapi **belum terhubung ke Telegram voice chat sungguhan**. Backend-nya hanya menyimpan state in-memory seperti `joinedChats`, `activeSource`, `paused`, dan `volumes`.

**Kesimpulan:**

> Voice Chat belum production-functional. Ini bukan sekadar improvement; ini missing implementation.

Kalau target GoUltroid adalah parity terhadap Ultroid, ini harus masuk daftar **P0/P1 parity gap**.

---

### 🔴 P0 — `PluginManager.ShutdownWithContext()` sebenarnya tidak benar-benar cancellable

Implementasinya membuat goroutine:

```go
go func(sh Shutdowner) { done <- sh.Shutdown() }(s)

select {
case <-ctx.Done():
    ...
case err := <-done:
    ...
}
```

Masalahnya: kalau `Shutdown()` plugin macet, context timeout **hanya membuat manager berhenti menunggu**. Goroutine shutdown tetap berjalan.

Lebih buruk lagi, setelah timeout manager bisa lanjut ke plugin berikutnya, dan akhirnya `App.Shutdown()` bisa melakukan:

```go
a.db.Close()
```

sementara goroutine plugin yang timeout masih mungkin menggunakan database.

Ini bertentangan dengan komentar:

> “respecting context budget”

karena `Shutdowner` sendiri tidak menerima context.

**Arsitektur yang benar:**

```go
type Shutdowner interface {
    Shutdown(ctx context.Context) error
}
```

atau paling tidak pisahkan:

```go
Shutdown() error
ShutdownContext(ctx context.Context) error
```

Kemudian semua service yang punya worker harus benar-benar menghentikan worker berdasarkan context.

**Prioritas: P0/P1.**

---

### 🔴 P1 — `PluginManager.Register()` tidak transactional

Saat register plugin:

1. `p.Init()`
2. register command satu per satu
3. kalau command ke-N gagal → return error.

Tetapi command 1 sampai N-1 **sudah masuk Router**.

Tidak ada rollback.

Akibatnya:

```text
Plugin A
 ├── command a  ✓
 ├── command b  ✓
 ├── command c  ✗
```

Plugin gagal didaftarkan, tetapi `a` dan `b` masih hidup di router.

Ini bisa menghasilkan state setengah-terpasang yang sangat sulit didiagnosis.

`Router` sendiri tidak memiliki `Unregister()` sehingga manager tidak bisa rollback saat ini.

**Fix yang benar:**

* tambahkan transactional registration;
* atau `Router.RegisterBatch()`;
* atau `Router.UnregisterBatch()` ketika plugin gagal;
* jangan memasukkan plugin ke state manager sebelum seluruh registration sukses.

---

### 🔴 P1 — Scheduler `RegisterPeriodicTask()` tidak masuk `WaitGroup`

Periodic task dibuat:

```go
go func() {
    ...
    task(taskCtx)
}()
```

tetapi tidak:

```go
e.wg.Add(1)
defer e.wg.Done()
```

Sedangkan `Stop()` menunggu:

```go
e.wg.Wait()
```

Akibatnya:

```text
Stop()
  ↓
wg.Wait()
  ↓
scheduler dianggap berhenti
  ↓
periodic task sebenarnya masih bisa berjalan
```

Ini khususnya berbahaya karena task tersebut bisa melakukan IO/network/DB setelah scheduler dianggap sudah shutdown.

**P1.**

---

### 🔴 P1 — `SetMaxConcurrency()` dapat merusak semaphore scheduler ketika runtime

Sekarang:

```go
e.sem = make(chan struct{}, n)
```

setiap kali `SetMaxConcurrency()` dipanggil.

Jika scheduler sedang berjalan dan ada job yang memegang semaphore lama:

```text
old sem
 ├── job A
 ├── job B
 └── job C

SetMaxConcurrency(10)

new sem
```

Job yang sudah berjalan masih menggunakan semaphore lama, sementara job baru menggunakan semaphore baru.

Akibatnya batas concurrency menjadi tidak lagi global.

Ini harus dikonfigurasi **sebelum Start()**, atau gunakan semaphore yang tidak diganti saat runtime.

**P1.**

---

### 🟠 P1 — `Cancel()` scheduler tidak membatalkan job yang sedang berjalan

`Cancel()` sekarang hanya:

```go
return e.db.DeleteScheduledJob(ctx, jobID)
```

Tetapi execution job yang sudah di-claim menggunakan context scheduler dan tidak punya mapping:

```text
jobID → cancel function
```

Berarti:

```text
job running
   ↓
.Cancel
   ↓
DB row deleted
   ↓
job tetap menjalankan Telegram action
```

Kemudian saat completion:

```text
CompleteScheduledJob
       ↓
ErrJobLeaseLost
```

Jadi database memang akhirnya konsisten, tetapi **side effect sudah terjadi**.

Contoh:

```text
.schedule message
      ↓
job starts
      ↓
.cancel job
      ↓
message tetap terkirim
```

Untuk scheduler production-grade, cancellation harus punya semantics yang jelas:

* pending → cancel langsung;
* running → cancel execution context;
* side effect yang sudah tidak bisa dibatalkan → status `cancelling/cancelled_after_execution` atau sejenisnya.

---

### 🟠 P1 — `MisfireCatchUp` belum benar-benar diimplementasikan

Scheduler mendefinisikan:

```go
MisfireRunOnce
MisfireSkip
MisfireCatchUp
```

tetapi logic `executeJob()` yang terlihat hanya memiliki perlakuan khusus terhadap `MisfireSkip`.

Tidak ada mekanisme nyata untuk melakukan catch-up seluruh missed intervals.

Jadi enum API menjanjikan tiga behavior, sementara implementation belum setara.

Ini sebaiknya jangan disebut `MisfireCatchUp` sebelum benar-benar diimplementasikan.

---

### 🔴 P1 — Storage quota tidak benar-benar membatasi ukuran upload

`FileStorage.Put()` melakukan:

```go
currentSize >= maxQuota
```

sebelum menulis.

Tetapi tidak menghitung:

```text
currentSize + incomingSize
```

Karena `io.Reader` tidak selalu punya known size, hasilnya bisa:

```text
quota = 10 GB
current = 9.9 GB
incoming = 500 MB

check:
9.9 GB < 10 GB → OK

write:
10.4 GB
```

Jadi quota adalah **admission check**, bukan hard quota.

Lebih buruk lagi, `Put()` melakukan `io.Copy()` tanpa batas maksimum.

Untuk storage production:

* gunakan `io.LimitReader`;
* reservation jika ukuran diketahui;
* hitung bytes aktual;
* hapus partial asset bila quota terlampaui;
* jangan pernah memungkinkan asset melebihi quota.

---

### 🟠 P1 — File permission storage terlalu longgar

Storage menggunakan:

```go
os.MkdirAll(cleanDir, 0755)
```

dan file:

```go
0644
```

Untuk data media/session userbot, ini berarti user lain pada mesin yang sama berpotensi membaca file.

Saya lebih menyarankan:

```text
directory: 0700
file:      0600
```

Terutama karena aplikasi juga menyimpan session Telegram dan data yang mungkin sensitif.

---

### 🔴 P1 — `SanitizeEnv()` berpotensi merusak environment child process

`OSRunner` melakukan:

```go
if isSensitive {
    sanitized = append(sanitized, parts[0]+"=[REDACTED]")
}
```

Artinya bukan sekadar **tidak menampilkan secret**.

Ia benar-benar memberikan child process:

```text
API_TOKEN=[REDACTED]
```

bukan token asli.

Ini bisa membuat executable yang membutuhkan environment credential gagal secara misterius.

Yang benar adalah membedakan:

```text
environment passed to process
vs
environment shown in logs
```

Jangan redact environment yang benar-benar akan dipakai process.

Untuk security, lebih baik:

* allowlist environment;
* secret tetap diteruskan bila diperlukan;
* secret **tidak pernah masuk logging**.

---

### 🔴 P1 — Shell execution masih terlalu powerful

`OSRunner` mendukung:

```go
Shell: true
```

dan kemudian:

```go
bash -c <fullCmd>
```

Ini memang berguna untuk system plugin, tetapi berarti layer `process.Runner` sekarang memiliki capability:

> arbitrary shell execution.

Kalau nantinya ada input user yang sampai ke:

```go
Request{
    Shell: true,
    Command: userControlled,
}
```

maka menjadi command injection.

Saya sarankan secara arsitektur:

```text
ProcessRunner
 ├── Exec(command, args...)      ← default
 └── Shell(command)              ← explicit privileged capability
```

Dan hanya service tertentu yang boleh menggunakan Shell.

---

### 🟠 P1 — EventBus bisa kehilangan subscriber secara tidak adil

Implementasi:

```go
for _, h := range handlers {
    select {
    case b.queue <- eventJob{...}:
    default:
        return
    }
}
```

Kalau queue penuh pada subscriber ke-2:

```text
subscriber A → queued
subscriber B → queue full
             ↓
            return
subscriber C → tidak pernah mendapat event
```

Padahal komentar mengatakan event boleh drop karena observational.

Yang benar adalah drop **per event/job**, bukan menghentikan dispatch seluruh subscriber.

Selain itu EventBus tidak punya `Close()` yang menghentikan 8 worker goroutine.

Jadi ada dua issue:

1. unfair subscriber starvation ketika queue penuh;
2. worker lifecycle tidak dikelola.

---

### 🟠 P1 — EventBus unsubscribe meninggalkan slot `nil`

`Subscribe()`:

```go
handlers[idx] = nil
```

tetapi slice tidak pernah compact.

Jika plugin repeatedly subscribe/unsubscribe:

```text
[handler1, nil, nil, nil, nil, handlerN]
```

akan terus membesar.

Bukan immediate memory leak besar, tapi lifecycle registry kurang sehat.

---

### 🔴 P1 — Downloader SSRF protection masih perlu diperketat

Ada hal bagus:

* HTTP/HTTPS saja;
* private IPv4 diblok;
* loopback diblok;
* IPv6 ULA/link-local/multicast diblok;
* redirect divalidasi;
* response size dibatasi.

Tetapi transport masih:

```go
Proxy: http.ProxyFromEnvironment
```

Ini membuat SSRF policy bergantung pada proxy environment.

Selain itu desain SSRF idealnya memiliki satu policy resolver yang memvalidasi **destination IP aktual** setiap koneksi, termasuk redirect dan DNS resolution.

Saya akan jadikan ini security hardening P1.

---

### 🟠 P1 — `Config.cleanPhone()` menerima input garbage

Misalnya konsep input:

```text
abc123xyz
```

akan dibersihkan menjadi angka dan akhirnya bisa menjadi:

```text
+123
```

Padahal input awal invalid.

Sebaiknya validasi hasil akhirnya sebagai E.164-ish:

```text
+ + 8..15 digits
```

dan reject karakter non-digit selain separator yang memang diizinkan.

---

### 🟠 P1 — `OwnerID` tidak diwajibkan

Config bisa menghasilkan:

```go
OwnerID: 0
```

dan aplikasi tetap berjalan.

Permissions memang secara aman tidak menganggap ID `0` sebagai owner.

Tetapi untuk userbot yang command administratifnya penting, production deployment sebaiknya:

```text
OWNER_ID wajib
```

atau fallback otomatis dari Telegram `Self()` setelah login.

Saat ini ada kemungkinan deployment berjalan tanpa owner yang dikonfigurasi dan semua command owner-only menjadi tidak dapat digunakan.

---

## Ada satu temuan arsitektural yang lebih besar

### 🔴 Dispatcher masih melakukan DB write dari goroutine per update

Bagian ini:

```go
go func() {
    for _, user := range e.Users {
        ...
        r.storage.Save(...)
        r.storage.SaveEntity(...)
    }
    ...
}()
```

Ini memang menghilangkan latency dari update path, tetapi sekarang tidak ada:

* bounded queue;
* worker pool;
* shutdown synchronization;
* backpressure;
* error reporting;
* deduplication lintas goroutine.

Jadi pada burst Telegram besar:

```text
1000 updates
   ↓
1000 goroutines
   ↓
SQLite single connection
   ↓
serialized DB writes
```

Peer cache sudah punya dirty cache lokal, tetapi queueing layer-nya tetap belum bounded.

Ini sebaiknya diubah menjadi:

```text
Telegram updates
       ↓
bounded peer-update queue
       ↓
2–4 workers
       ↓
PeerStorage
       ↓
SQLite
```

dan worker masuk `App` lifecycle sehingga shutdown benar-benar menunggunya.

---

# Prioritas keseluruhan sekarang

Setelah audit tambahan ini, saya akan mengubah penilaian repo menjadi:

| Area                  | Status                                                 |
| --------------------- | ------------------------------------------------------ |
| Core command routing  | 🟢 cukup kuat                                          |
| Permission model      | 🟢 cukup baik                                          |
| Response lifecycle    | 🟢 jauh membaik                                        |
| PMPermit              | 🟢 cukup kuat                                          |
| Event system          | 🟠 perlu lifecycle/backpressure                        |
| SQLite                | 🟢 dasar bagus, operational hardening perlu            |
| Scheduler persistence | 🟢 desain bagus                                        |
| Scheduler lifecycle   | 🔴 masih ada hole                                      |
| Downloader SSRF       | 🟠 bagus tapi belum hardened penuh                     |
| Process execution     | 🔴 capability terlalu powerful                         |
| File storage          | 🔴 quota belum hard                                    |
| Plugin lifecycle      | 🔴 rollback + shutdown issue                           |
| Media pipeline        | 🟢 cukup bagus                                         |
| Voice                 | 🔴 **mock only**                                       |
| Assistant bot         | 🟠 functional, lifecycle perlu diperketat              |
| Telegram dispatcher   | 🟠 concurrency/queue perlu dibenahi                    |
| Purge                 | 🔴 **masih belum selesai**                             |
| Ultroid parity        | 🟠 banyak bagian sudah ada, beberapa masih facade/mock |

## Urutan fix yang saya sarankan

Jangan langsung menambah fitur baru. Saya akan lanjut dengan urutan:

**1. Plugin lifecycle + EventBus lifecycle**
**2. Scheduler cancellation/concurrency/periodic-task lifecycle**
**3. Bounded peer-storage worker**
**4. Storage hard quota + permission**
**5. Process runner security model**
**6. Downloader SSRF hardening**
**7. Config validation**
**8. Voice backend real implementation**
**9. Baru kembali ke purge dan test runtime**

Yang paling penting: **jangan menganggap repo sudah production-grade hanya karena test coverage terlihat besar**. Dari audit kode terbaru ini masih ada beberapa bug lifecycle/concurrency yang test biasa sangat mudah lewatkan. Struktur dasarnya sudah jauh lebih matang, tetapi sekarang masalahnya bergeser dari “fitur belum ada” menjadi **durability, lifecycle, concurrency, security boundary, dan real-world Telegram semantics**.

Kalau lanjut eksekusi, saya sarankan kita mulai dari **Plugin Manager + EventBus + scheduler lifecycle** karena tiga area ini saling berhubungan dengan shutdown dan goroutine leak.
