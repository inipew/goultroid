Sudah saya cek **kode terbaru `main` sekarang**, bukan versi audit sebelumnya. Per hari ini repo sudah masuk **v1.1.26**, commit `02764f4` pada 6 September 2026. Dibanding `v1.1.25`, commit terbaru memang membawa perubahan nyata ke dispatcher, plugin manager, peer handling, HTML helper, media, filters/blacklist, dan beberapa plugin lain—jadi ada progres yang cukup besar.

[GoUltroid repository](https://github.com/inipew/goultroid?utm_source=chatgpt.com)

## Kesimpulan cepat

**GoUltroid sekarang lebih baik secara arsitektur daripada audit sebelumnya.** Beberapa P0 yang saya temukan sebelumnya sudah mulai diperbaiki, terutama:

* Plugin Manager sekarang benar-benar mendaftarkan `MessageHookPlugin`.
* Hook punya unregister cleanup.
* Dispatcher sudah punya mekanisme `acceptingUpdates` + `inFlight`.
* Filters dan blacklist sudah menggunakan cache per-chat.
* Bot-message loop sudah dicegah.
* Regex cache sudah diberi batas 500 entry.
* Access-hash resolution sudah mulai dipusatkan di Telegram service.
* HTML utility sudah ditambahkan.
* Plugin registration sekarang memvalidasi command dan nil handler.
* Shutdown plugin sekarang detach hooks dahulu.

**Tetapi setelah tracing ulang seluruh jalur runtime, saya menemukan beberapa masalah performa dan concurrency yang justru sekarang menjadi lebih penting.**

### Nilai saya saat ini

| Area                    |      Nilai |
| ----------------------- | ---------: |
| Plugin architecture     | **8.0/10** |
| Dispatcher              | **7.5/10** |
| Peer/cache              | **7.5/10** |
| DB architecture         | **6.5/10** |
| Hot-path performance    | **6.5/10** |
| Concurrency             | **6.0/10** |
| Telegram RPC efficiency | **7.0/10** |
| Shutdown safety         | **7.5/10** |
| Memory behavior         | **7.0/10** |
| **Overall**             | **7.1/10** |

Belum flawless.

---

# 1. Perubahan terbaru: Plugin architecture sudah jauh lebih bagus

Ini improvement yang signifikan.

Sekarang plugin yang mengimplementasikan:

```go
type MessageHookPlugin interface {
    Plugin
    MessageHookPriority() int
    HandleIncomingMessage(...)
}
```

akan otomatis diregistrasikan ke Dispatcher oleh `PluginManager`.

Manager juga menyimpan cleanup function dan ketika shutdown:

```text
Shutdown
   ↓
detach hooks
   ↓
shutdown plugins reverse order
```

Ini jauh lebih benar daripada sebelumnya.

### Ini saya anggap berhasil memperbaiki P0 sebelumnya.

Namun belum selesai karena masih ada masalah concurrency di Dispatcher.

---

# 2. 🚨 P0 baru: `inFlight` tidak mencakup execution command

Ini temuan paling penting dari audit terbaru.

Dispatcher melakukan:

```go
d.inFlight.Add(1)
defer d.inFlight.Done()
```

di awal `dispatch()`.

Tetapi command dijalankan:

```go
go func() {
    defer cancel()
    _ = d.executor.Execute(coreCtx, cmd)
}()
```

Artinya `dispatch()` langsung selesai setelah membuat goroutine.

Jadi secara nyata:

```text
Telegram update
      ↓
dispatch()
      ↓
inFlight++
      ↓
create command goroutine
      ↓
dispatch() RETURN
      ↓
inFlight--
      ↓
shutdown thinks "no work"
```

padahal:

```text
                    command goroutine
                         ↓
                    masih running
                         ↓
                       DB
                         ↓
                     Telegram
```

Ini terlihat langsung pada implementation dispatcher terbaru.

### Dampaknya

Ini sangat serius.

Misalnya:

```text
T0  message masuk
T1  command goroutine dibuat
T2  dispatch selesai
T3  inFlight = 0
T4  shutdown
T5  DB.Close()
T6  command masih Execute()
T7  command akses DB
```

Potensi:

* `sql: database is closed`
* Telegram operation terputus
* partial command execution
* race saat shutdown
* goroutine leak
* state tidak konsisten

### Solusi

Jangan menganggap `dispatch()` selesai ketika command goroutine dibuat.

Buat **execution tracker** terpisah:

```go
type ExecutionTracker struct {
    wg sync.WaitGroup
}
```

Kemudian:

```go
tracker.Add(1)

go func() {
    defer tracker.Done()
    defer cancel()

    _ = executor.Execute(ctx, cmd)
}()
```

Shutdown:

```text
quiesce dispatcher
        ↓
stop accepting update
        ↓
wait interceptor
        ↓
wait command execution
        ↓
stop scheduler
        ↓
shutdown plugins
        ↓
close DB
```

Lebih bagus lagi gunakan `errgroup`/execution registry dengan limit concurrency.

---

# 3. 🚨 P0/P1: command execution sekarang unlimited goroutine

Setiap command:

```go
go func() {
    _ = d.executor.Execute(...)
}()
```

Tidak ada semaphore global.

Kalau Telegram menghasilkan burst:

```text
100 messages
→ 100 goroutines

1,000 messages
→ 1,000 goroutines

10,000 messages
→ 10,000 goroutines
```

Memang Go goroutine murah, tetapi command bukan pekerjaan murah.

Command dapat:

* DB
* Telegram RPC
* media processing
* download
* process execution
* filesystem
* scheduler
* addon
* network

Jadi bottleneck bukan goroutine-nya, tetapi resource di bawahnya.

### Target yang lebih sehat

Pisahkan:

```text
Message ingestion
       ↓
lightweight routing
       ↓
bounded execution queue
       ↓
N workers
```

Misalnya:

```text
interactive commands
    32 workers

background/heavy commands
     4 workers

media/process
     2 workers
```

Tetapi jangan membuat satu queue global untuk semuanya.

Gunakan **class-based concurrency**.

---

# 4. Dispatcher hot path sekarang masih cukup berat

Untuk setiap message, Dispatcher melakukan beberapa pekerjaan sebelum command:

```text
Parse command
↓
copy entities
↓
peer cache queue
↓
copy handlers
↓
run every interceptor
↓
extract core message
↓
album buffer
↓
resolve peer
↓
resolve sender
↓
create execution context
↓
spawn goroutine
```

Bagian yang paling saya soroti adalah peer cache.

---

# 5. Peer cache worker sekarang lebih baik, tetapi desain write-nya masih mahal

Sekarang ada queue:

```go
peerQueue chan peerUpdateJob
```

dengan:

```go
workers := 2
```

dan queue 1024. Ini jauh lebih baik daripada membuat goroutine tanpa batas untuk setiap entity.

Tetapi setiap entity melakukan **dua storage operation**:

```text
Save(access_hash)
SaveEntity(metadata)
```

Misalnya satu update memiliki:

```text
10 users
2 channels
3 chats
```

bisa menjadi:

```text
15 entities
× 2 writes
= 30 DB writes
```

Dan worker hanya 2.

Dengan SQLite `MaxOpenConns(1)`, semua tetap serialized.

### Ini menjadi bottleneck yang nyata.

Saya justru menyarankan:

```text
Telegram update
       ↓
entity batch
       ↓
bounded queue
       ↓
DB transaction
       ↓
bulk upsert
```

bukan:

```text
entity
 ↓
INSERT/UPDATE

entity
 ↓
INSERT/UPDATE
```

Target:

```go
SaveEntities(ctx, users, channels, chats)
```

dalam **satu transaction**.

Ini bisa mengurangi SQLite overhead secara drastis.

---

# 6. SQLite `MaxOpenConns(1)` bukan salah, tetapi harus diperlakukan sebagai serialized storage

Current:

```go
db.SetMaxOpenConns(1)
db.SetMaxIdleConns(1)
```

dengan:

```text
WAL
busy_timeout=5000
synchronous=NORMAL
```

Konfigurasinya sendiri masuk akal untuk SQLite.

Masalahnya adalah arsitektur aplikasi sekarang mulai mempunyai banyak subsystem:

```text
Dispatcher
Filters
Blacklist
PMPermit
Userlog
Scheduler
Peer resolver
Addon
Notes
Broadcast
...
```

Semua akhirnya bertemu pada satu serialized DB connection.

Jadi targetnya bukan sekadar:

> tambah connection

Karena SQLite write tetap punya constraint.

Lebih tepat:

### Read

gunakan read cache:

```text
message
 ↓
memory cache
```

### Write

gunakan:

```text
bounded write queue
 ↓
batch transaction
```

### Configuration mutation

langsung:

```text
DB write
 ↓
cache invalidation
```

Ini jauh lebih scalable.

---

# 7. Filters sekarang jauh lebih bagus

Ini improvement nyata.

Sekarang sudah ada:

```go
chatFilters map[int64][]database.Filter
```

sehingga DB tidak lagi dipanggil untuk setiap message.

Dan sudah ada:

* bot loop prevention
* cooldown
* regex cache
* bounded regex cache
* cache invalidation ketika `.filter`
* cache invalidation ketika `.stop`

Bagus.

### Tetapi ada masalah baru: cache bisa tumbuh berdasarkan jumlah chat

```go
chatFilters map[int64][]database.Filter
```

dan:

```go
chatBlacklist map[int64][]string
```

tidak mempunyai eviction.

Jika userbot berada di:

```text
10 chat       → kecil
1,000 chat    → masih oke
10,000 chat   → mulai besar
100,000 chat  → problem
```

Karena seluruh konfigurasi tetap hidup di memory.

### Target

Gunakan:

```text
LRU
TTL
max chat entries
```

atau lebih bagus:

```go
type ChatConfigSnapshot struct {
    Filters
    Blacklist
    Version uint64
    LoadedAt time.Time
}
```

dengan bounded cache.

---

# 8. Regex cache sekarang sudah diperbaiki, tetapi eviction-nya terlalu kasar

Sekarang:

```go
const maxRegexEntries = 500

if len(cache) >= maxRegexEntries {
    cache = make(map[string]*regexp.Regexp)
}
```

Ini bounded, bagus.

Tetapi ketika 500 tercapai:

```text
hapus SEMUA
```

bukan LRU.

Jadi burst keyword baru dapat menyebabkan:

```text
500 cached
 ↓
keyword 501
 ↓
clear all
 ↓
recompile
 ↓
500 cached
 ↓
...
```

### Target

LRU sederhana:

```text
max 512 / 1024
        ↓
evict oldest
```

Tetapi saya sebenarnya lebih suka **compiled matcher per chat**, karena filter memang scoped per chat.

---

# 9. Filter matching masih melakukan lowercase + regex untuk setiap filter

Sekarang:

```go
lowerText := strings.ToLower(text)
```

sekali, bagus.

Tetapi:

```go
for _, f := range filters {
    matchFilter(text, f.Keyword)
}
```

dan `matchFilter()` kembali melakukan:

```go
strings.ToLower(text)
```

jadi text di-lowercase ulang untuk setiap filter.

Ini unnecessary allocation/work.

Lebih baik:

```go
lowerText := strings.ToLower(text)

for _, matcher := range snapshot.Matchers {
    if matcher.Match(lowerText) {
        ...
    }
}
```

Lebih bagus lagi compile matcher ketika config berubah.

Target:

```text
.filter
 ↓
compile
 ↓
immutable matcher snapshot

message
 ↓
snapshot.Match(text)
```

bukan compile/matching infrastructure di hot path.

---

# 10. Blacklist juga sudah jauh lebih baik

Sekarang sudah:

* per-chat cache
* bot ignore
* regex limit
* cache invalidation
* HTML escaping

Ini bagus.

Tetapi masalah yang sama:

```text
chatBlacklist
```

tidak bounded.

Dan:

```go
matchBlacklist()
```

masih melakukan lowercase per keyword.

Jadi architecture target sebaiknya sama dengan Filters.

---

# 11. 🚨 Peer resolution masih belum benar-benar clean

Ini masih menjadi perhatian besar.

Dispatcher command resolution masih memiliki:

```go
peerInput = &tg.InputPeerUser{
    UserID: p.UserID,
    AccessHash: accessHash,
}
```

bahkan ketika `accessHash == 0`.

Memang sekarang ada resolver fallback sebelumnya.

Tetapi jika resolver gagal:

```text
accessHash = 0
        ↓
InputPeerUser{..., 0}
```

masih dibuat.

Ini bertentangan dengan invariant yang kita tetapkan sebelumnya.

### Harus menjadi:

```go
peerInput, err := resolver.ResolvePeer(...)
if err != nil {
    return typed peer-resolution error
}
```

bukan:

```go
best effort
→ zero access hash
→ continue
```

Telegram Service sekarang juga mencoba memperbaiki access hash lagi sebelum send.

Itu membuat:

```text
Dispatcher resolver
        ↓
Service resolver
        ↓
storage
        ↓
Telegram
```

terjadi **double resolution**.

Lebih baik:

```text
Central PeerResolver
        ↓
validated InputPeer
        ↓
Service
        ↓
RPC
```

Service tidak perlu melakukan resolver kedua kecuali memang recovery path.

---

# 12. 🚨 `callbackInputPeer()` memakai `context.Background()`

Ini saya anggap bug concurrency/lifecycle kecil tapi nyata.

Di callback resolver:

```go
resolver.ResolveUser(context.Background(), ...)
```

dan:

```go
resolver.ResolveChat(context.Background(), ...)
```

dipakai.

Artinya callback request yang sudah cancellation/shutdown bisa tetap melakukan pekerjaan karena context request dibuang.

Harus:

```go
resolver.ResolveUser(ctx, ...)
```

dan:

```go
resolver.ResolveChat(ctx, ...)
```

Ini juga penting untuk latency.

---

# 13. Callback/inline architecture sudah cukup bagus

Sekarang sudah ada:

```text
callback router
callback limiter
callback timeout 15s

inline engine
inline limiter
inline timeout 4s
```

dan dispatcher juga menolak event saat:

```go
!acceptingUpdates
```

Ini bagus.

Tetapi untuk inline, **4 detik masih terlalu panjang jika workload meningkat**.

Inline query sebaiknya:

```text
cache first
 ↓
fast path
 ↓
strict timeout
```

karena user Telegram merasakan latency langsung.

Target praktis:

```text
P50 < 100ms
P95 < 300ms
P99 < 1s
```

untuk inline result yang tidak membutuhkan network berat.

---

# 14. Telegram Service: access-hash recovery bagus, tetapi bisa menyebabkan RPC tambahan

Sekarang `SendMessage()`:

```text
ensureChannelAccessHash
ensureUserAccessHash
SendMessage
```

Jadi sebuah send bisa menjadi:

```text
storage/peer-manager lookup
+
Telegram RPC
```

kalau peer tidak lengkap.

Ini acceptable sebagai fallback.

Tetapi jangan sampai normal path selalu lewat resolver.

### Target

Normal:

```text
InputPeer sudah valid
 ↓
RPC
```

Recovery:

```text
InputPeer invalid/missing hash
 ↓
resolver
 ↓
retry RPC
```

Bukan:

```text
every send
 ↓
resolver
 ↓
RPC
```

---

# 15. FloodWait handling sekarang cukup baik

Current service mempunyai retry untuk FloodWait pendek:

```text
<= 5 seconds
```

dan RPC_CALL_FAIL dengan delay 300ms.

Ini bagus untuk transient failure.

Tetapi jangan retry FloodWait panjang secara otomatis di general service.

Untuk:

```text
FloodWait 60s
FloodWait 300s
FloodWait 3600s
```

lebih baik caller/service mengembalikan typed rate-limit error.

Broadcast/scheduler bisa melakukan policy khusus.

---

# 16. `recordBotSent` punya desain memory yang masih sederhana

Sekarang:

```go
map[int]time.Time
```

dibatasi sekitar 200 entry dan pruning 5 menit.

Ini kecil sehingga tidak menjadi problem besar.

Namun message ID Telegram bukan globally unique dalam semua konteks.

Kalau dipakai untuk anti-loop/identification, idealnya key:

```go
type MessageKey struct {
    PeerID int64
    MsgID  int
}
```

bukan hanya:

```go
msgID
```

---

# 17. 🚨 Raw message interceptors masih sequential

Dispatcher melakukan:

```text
Security
 ↓
Moderation
 ↓
Feature
 ↓
Observability
```

dan menjalankan satu per satu.

Ini benar secara semantics.

Tetapi setiap interceptor memiliki timeout 5 detik.

Worst case:

```text
4 interceptors
×
5 sec
=
20 sec
```

untuk satu message.

Memang context cancellation biasanya membuatnya lebih cepat, tetapi architecture ini memungkinkan satu interceptor memperlambat semuanya.

### Yang harus dilakukan

Security/moderation memang harus sequential.

Tetapi observability:

```text
UserLog
analytics
metrics
audit
```

seharusnya asynchronous.

Target:

```text
Security
 ↓
Moderation
 ↓
Feature gate
 ↓
command

        └── async observability
```

Jangan membuat UserLog menghambat command pipeline.

---

# 18. UserLog harus sangat diperhatikan

Karena userlog merupakan observability path, ia seharusnya:

```text
message
 ↓
non-blocking queue
 ↓
worker
 ↓
DB / Telegram
```

bukan:

```text
message
 ↓
UserLog
 ↓
DB
 ↓
command
```

Current priority `Observability = 90`, yang secara konsep sudah tepat. Dispatcher memang mendesain priority security → moderation → feature → observability.

Tetapi saya tetap akan menjadikan observability **fire-and-forget bounded queue**.

---

# 19. 🚨 Peer cache queue bisa drop data tanpa recovery

Dispatcher sengaja:

```text
queue full
 ↓
drop
```

Ini sebenarnya benar kalau peer cache hanya optimization.

Masalahnya harus benar-benar dipastikan bahwa:

> peer cache kehilangan satu update tidak pernah menyebabkan functional failure.

Saat ini beberapa resolver masih bergantung pada storage jika entity tidak tersedia.

Jadi:

```text
cache drop
 ↓
future command
 ↓
resolver
 ↓
DB tidak punya entity
 ↓
Telegram peer failure
```

Kalau cache hanya optimization, resolver harus tetap punya source-of-truth lain:

```text
gotd peer manager
Telegram entity
session cache
```

Bukan menganggap SQLite cache sebagai satu-satunya recovery.

---

# 20. App startup sekarang mulai terlalu berat

`App.New()` menginisialisasi banyak hal:

```text
DB
permissions
router
dispatcher
telegram
callbacks
inline
moderation
scheduler
storage
process runner
download registry
media
pmpermit
broadcast
userlog
addon
assistant
25 plugins
```

Semua dilakukan sebelum `Run()`.

Ini bagus untuk deterministic startup, tetapi latency startup bisa meningkat.

Yang lebih penting:

### plugin Init memakai `context.Background()`

Manager:

```go
ci.InitContext(context.Background())
```

Jadi startup tidak bisa dibatalkan oleh application context.

Ideal:

```go
manager.Initialize(ctx)
```

dengan root startup context.

---

# 21. Assistant / “GoUserBot” yang terbaru

Kalau yang Anda maksud dengan **GoUserBot** adalah subsystem `internal/assistant`, saya juga cek.

Saat ini assistant adalah bot MTProto terpisah yang dibuat melalui:

```go
telegram.NewClient(
    c.appID,
    c.appHash,
    telegram.Options{UpdateHandler: gaps},
)
```

dan hanya menangani:

```text
/start
/help
/ping
/alive
```

### Performa assistant saat ini sebenarnya ringan.

Pipeline:

```text
Update
 ↓
OnNewMessage
 ↓
type assertion
 ↓
command comparison
 ↓
extract sender
 ↓
send reply
```

Tidak ada DB query.

Tidak ada plugin router berat.

Tidak ada regex pipeline.

Jadi untuk 4 command tersebut performanya bagus.

---

# 22. Tetapi Assistant Bot punya masalah access hash yang sama

Di:

```go
peer := &tg.InputPeerUser{
    UserID: senderID,
}
```

kemudian jika entity tersedia:

```go
AccessHash = u.AccessHash
```

Jika entity tidak tersedia, tetap:

```text
InputPeerUser{UserID}
```

tanpa access hash.

Ini harus menggunakan canonical peer resolution.

Jadi problem yang sama muncul di **assistant**.

---

# 23. Assistant juga tidak menggunakan limiter

Userbot utama punya:

```text
command rate limiter
callback limiter
inline limiter
```

Tetapi assistant command surface:

```text
/start
/help
/ping
/alive
```

langsung:

```go
MessagesSendMessage
```

Tidak ada per-user rate limiter.

Kalau bot exposed ke publik:

```text
1 user
→ spam /ping 1000x
```

maka assistant bisa menghasilkan banyak outbound RPC.

### Minimal

```text
per-user:
    5 req / 10 sec

global:
    bounded outbound queue
```

Untuk `/ping` bahkan bisa:

```text
cache response
```

atau throttle.

---

# 24. Assistant menggunakan raw `tg.NewUpdateDispatcher`

Ini sebenarnya bagus untuk performa karena surface-nya kecil.

Tidak perlu memasukkan assistant ke seluruh userbot pipeline.

Saya justru **tidak menyarankan** assistant dipaksa menggunakan Dispatcher userbot utama.

Pisahkan:

```text
Userbot
  └── full plugin pipeline

Assistant Bot
  └── lightweight bot pipeline
```

Ini desain yang bagus.

---

# 25. Masalah terbesar performa GoUserBot sekarang: bukan CPU

Ini penting.

Saya tidak melihat indikasi bahwa GoUltroid akan berat karena CPU murni.

Bottleneck utamanya adalah:

### #1 SQLite serialization

```text
MaxOpenConns = 1
```

### #2 unnecessary DB writes

peer caching:

```text
entity
→ Save
→ SaveEntity
```

### #3 unbounded command goroutines

```text
message
→ goroutine
```

### #4 sequential interceptors

```text
security
→ moderation
→ feature
→ observability
```

### #5 repeated peer resolution

```text
dispatcher
→ resolver
→ service
→ resolver/storage
```

### #6 memory cache tanpa eviction

```text
chatFilters
chatBlacklist
```

### #7 regex matching

masih bisa dioptimalkan dengan compiled per-chat matcher.

---

# 26. Performance architecture yang saya rekomendasikan

Target final:

```text
                    Telegram Update
                           │
                           ▼
                    ┌──────────────┐
                    │  Dispatcher  │
                    └──────┬───────┘
                           │
                  lightweight normalize
                           │
                           ▼
                  ┌─────────────────┐
                  │ Security Gate   │
                  └───────┬─────────┘
                          │
                  ┌───────▼────────┐
                  │ Moderation     │
                  │ immutable      │
                  │ chat snapshot  │
                  └───────┬────────┘
                          │
                          ▼
                    Command Router
                          │
                          ▼
                Bounded Execution Pool
                 ┌────────┼────────┐
                 ▼        ▼        ▼
              normal    media    background
```

Sedangkan entity:

```text
Telegram entities
       │
       ▼
bounded entity queue
       │
       ▼
batcher
       │
       ▼
single SQLite transaction
       │
       ▼
peer/entity cache
```

Dan observability:

```text
message
   │
   ├──────────────► command
   │
   └──────────────► bounded event queue
                         │
                         ▼
                    UserLog/Metrics
```

---

# 27. Target angka yang sebaiknya dipakai

Untuk userbot production, saya akan menetapkan SLO internal seperti:

| Metric                  |             Target |
| ----------------------- | -----------------: |
| Dispatcher overhead     | **<1 ms** tanpa DB |
| Command routing         |          **<1 ms** |
| Cached filter lookup    |        **<100 µs** |
| Cached blacklist lookup |        **<100 µs** |
| P50 command start       |         **<10 ms** |
| P95 command start       |         **<50 ms** |
| P99 command start       |        **<200 ms** |
| Inline P95              |        **<300 ms** |
| Shutdown drain          |   **<10 s** normal |
| Goroutine growth        |        **bounded** |
| Entity queue            |        **bounded** |
| Regex cache             |        **bounded** |
| Chat config cache       |        **bounded** |
| SQLite writes           |        **batched** |

Telegram RPC tentu tidak bisa dipaksa mengikuti angka tersebut; yang kita ukur adalah **local processing latency**.

---

# 28. Prioritas fix terbaru

Saya akan mengubah roadmap dari audit sebelumnya menjadi:

### 🔴 P0

1. **Pisahkan `inFlight` update dan `inFlight` command execution**
2. **Bound command goroutines**
3. **Pastikan shutdown menunggu command execution**
4. **Hilangkan `InputPeerUser/Channel` zero-access-hash sebagai successful resolution**
5. **Hilangkan `context.Background()` dari resolver callback**
6. **Centralize peer resolution sepenuhnya**

### 🟠 P1

7. Batch peer/entity DB writes
8. Immutable compiled filter matcher
9. Immutable compiled blacklist matcher
10. LRU/TTL chat configuration cache
11. Async observability queue
12. Assistant per-user/global rate limit
13. Assistant central peer resolver
14. Avoid duplicate resolver layer
15. Add execution-class concurrency limits

### 🟡 P2

16. Inline result caching
17. adaptive worker pool
18. metrics per plugin
19. per-command latency histogram
20. peer cache hit/miss metrics
21. DB queue depth metrics
22. RPC latency metrics
23. memory/cache metrics
24. goroutine count monitoring

---

# 29. Hal yang **sudah berhasil diperbaiki** dari audit sebelumnya

Ini penting supaya kita tidak mengulang pekerjaan:

| Temuan lama                            | Status                              |
| -------------------------------------- | ----------------------------------- |
| Plugin raw hook tidak dikelola Manager | ✅ **fixed**                         |
| Hook unregister                        | ✅ **fixed**                         |
| Dispatcher quiesce                     | 🟢 **sebagian besar fixed**         |
| Filter DB query setiap message         | ✅ **fixed**                         |
| Blacklist DB query setiap message      | ✅ **fixed**                         |
| Bot loop                               | ✅ **fixed**                         |
| Regex unbounded                        | 🟢 **bounded, tapi eviction kasar** |
| Plugin command nil validation          | ✅ **fixed**                         |
| HTML helper                            | ✅ **ditambahkan**                   |
| Locks fail-closed                      | 🟢 **improved**                     |
| Media target validation                | 🟢 **improved**                     |
| Access-hash architecture               | 🟡 **belum tuntas**                 |
| Command lifecycle                      | 🔴 **masih ada race**               |
| DB write batching                      | ❌                                   |
| Bounded command execution              | ❌                                   |
| Chat cache eviction                    | ❌                                   |
| Full peer centralization               | ❌                                   |

Jadi **v1.1.26 memang bukan sekadar perubahan kosmetik**. Banyak rekomendasi audit sebelumnya sudah diterapkan. Namun audit terbaru menemukan bahwa setelah hook/lifecycle diperbaiki, **execution concurrency sekarang menjadi titik kritis berikutnya**.

### Verdict

**Saya tidak menyarankan menambah banyak fitur/plugin dulu.**

Fondasi sekarang sudah cukup bagus untuk masuk fase **performance/concurrency hardening**. Urutan paling bernilai adalah:

> **Execution lifecycle → bounded concurrency → peer resolver → DB batching → immutable hot-path cache → observability → benchmarking.**

Kalau keenam area itu dibereskan, saya memperkirakan GoUltroid bisa naik dari sekitar **7.1/10 menjadi 8.5–9/10 secara production architecture**, dan baru setelah itu masuk ke tahap parity fitur Ultroid secara agresif.
