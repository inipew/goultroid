Ya. Saya **fetch ulang `main` terbaru** lagi, bukan memakai snapshot audit sebelumnya. Kali ini ada improvement penting lagi: **persistent peer storage sudah benar-benar masuk**, scheduler sudah memakai **claim token/fencing**, lease default sudah dinaikkan menjadi **90 detik**, dan ada **job execution history**.

Saya juga cross-check dengan source/API gotd terbaru; pola `telegram.Client`, session storage, update handler, dan peer/update architecture yang digunakan memang sesuai dengan API gotd saat ini. ([GotD][1])

## Kesimpulan cepat

**Progress-nya nyata dan beberapa finding P0/P1 saya sebelumnya sudah diperbaiki.**

Sekarang saya nilai kira-kira:

> **8.6/10 — sudah mendekati production-grade, tetapi belum saya sebut fully production-grade.**

Dan ada beberapa hal yang **masih perlu diperbaiki sebelum saya nyaman menyebutnya production-ready**, terutama di scheduler, peer storage, lifecycle, dan Telegram service semantics.

---

# 1. Yang sudah FIX dan saya anggap bagus

### ✅ Persistent peer storage

Ini improvement paling penting.

Sekarang:

```text
gotd peers.Manager
       ↓
PeerStorage
       ↓
SQLite
```

`PeerStorage` sudah mengimplementasikan `peers.Storage` dan menyimpan `access_hash` secara persistent.

Ini jauh lebih benar daripada solusi sebelumnya yang hanya preload dialogs.

Dan penggunaannya:

```go
peerManager := peers.Options{
    Storage: peerStorage,
}.Build(raw.API())
```

adalah arah yang tepat. gotd memang menyediakan storage abstraction untuk peer manager.

**Status: FIXED ✅**

---

# 2. Scheduler sekarang jauh lebih kuat

Ini juga improvement besar.

Sekarang ada:

```text
claim_token
lease_until
attempt_count
max_attempts
claimed_at
last_started_at
last_finished_at
```

dan `ClaimDueScheduledJobs()` membuat token random setiap claim.

Kemudian completion:

```sql
WHERE id = ?
  AND status = 'running'
  AND claim_token = ?
```

dan failure juga menggunakan token yang sama.

Ini **sangat bagus**.

Sekarang scenario:

```text
Worker A
 claim token=A
 lease expired

Worker B
 claim token=B

Worker A selesai
       ↓
Complete(... token=A)
       ↓
❌ ErrJobLeaseLost
```

Worker A tidak bisa merusak state Worker B.

**Status: FIXED ✅**

---

# 3. Lease 90 detik juga sudah jauh lebih masuk akal

Sekarang scheduler:

```go
ClaimDueScheduledJobs(..., 90*time.Second)
```

dengan komentar:

> 3x default execution timeout

Ini jauh lebih aman daripada sebelumnya 30s vs timeout 30s.

Saya setuju dengan 90 detik untuk baseline.

Namun ada satu caveat:

### Kalau command bisa legitimately >90s

misalnya:

```text
media processing
large download
external API
.git update/build
```

lease tetap bisa expired.

Idealnya nanti ada:

```text
lease renewal / heartbeat
```

misalnya setiap 30 detik:

```text
lease = now + 90s
```

selama job masih hidup.

**Status: GOOD, tetapi belum sempurna 🟢**

---

# 4. Ada bug scheduler yang masih tersisa

Ini cukup penting.

`ClaimDueScheduledJobs()`:

```sql
WHERE
    (status = 'pending'
     OR (status = 'running' AND lease_until < ?))
    AND next_run_at <= ?
```

Artinya job yang:

```text
status = running
lease expired
next_run_at <= now
```

akan diambil kembali.

Itu benar.

Tetapi `FailScheduledJob()` ketika retry:

```sql
next_run_at = now + retryDelay
```

dan:

```text
status = pending
```

juga benar.

### Masalahnya:

**lease expiry dianggap sebagai execution failure, tetapi tidak ada history entry khusus untuk crash/reclaim.**

Misalnya:

```text
Worker A
started
↓
process killed
↓
90s
↓
Worker B reclaim
```

History hanya tahu:

```text
run A = ??? 
run B = success
```

Run A sebenarnya tidak pernah mendapat `Complete` atau `Fail`.

Saya sarankan saat reclaim:

```text
attempt_count++
history:
    outcome = abandoned/lease_expired
```

atau minimal:

```text
last_error = "previous execution lease expired"
```

Supaya debugging scheduler benar-benar forensic.

---

# 5. 🚨 `ClaimDueScheduledJobs()` masih punya masalah concurrency subtle

Ini bagian yang perlu diperhatikan.

Prosesnya:

```text
BEGIN
 ↓
SELECT jobs
 ↓
UPDATE jobs
 ↓
COMMIT
```

secara transaction memang atomic terhadap database.

Tetapi `UPDATE`-nya hanya:

```sql
WHERE id = ?
```

bukan:

```sql
WHERE id = ?
  AND (
      status = 'pending'
      OR expired lease
  )
```

Dalam SQLite dengan writer serialization, ini kemungkinan besar aman dalam praktik karena transaction writer lock.

Tetapi secara defensive correctness, saya lebih suka conditional update:

```sql
UPDATE scheduled_jobs
SET ...
WHERE id = ?
  AND (
      status = 'pending'
      OR (
          status = 'running'
          AND lease_until < ?
      )
  )
```

kemudian:

```go
RowsAffected() == 1
```

Baru job dimasukkan ke returned list.

Dengan begitu invariant-nya bukan hanya:

> "Saya melihat job tadi."

tetapi:

> **"Saya benar-benar berhasil memperoleh ownership atas job itu."**

Ini terutama penting jika nanti database backend berubah dari SQLite atau scheduler menjadi multi-process.

---

# 6. Persistent peer storage punya satu bug kecil

`peers_phones`:

```sql
access_hash INTEGER NOT NULL
```

tetapi `SavePhone()` menyimpan:

```sql
access_hash = 0
```

Kemudian `FindPhone()` mengambil access hash dari:

```sql
peers_storage
```

Jadi field:

```text
peers_phones.access_hash
```

sebenarnya redundant dan selalu 0.

Saya akan hapus field itu dari schema atau benar-benar populate.

Lebih bersih:

```text
peers_phones
--------------
phone
prefix
id
updated_at
```

dan access hash tetap authoritative di:

```text
peers_storage
```

---

# 7. 🚨 Peer storage belum menyimpan entity metadata

Saat ini `peers_storage` hanya:

```text
prefix
id
access_hash
updated_at
```

Itu cukup untuk access hash.

Tetapi kalau mau membuat peer subsystem benar-benar kuat, simpan juga:

```text
username
phone
title
first_name
last_name
peer_type
```

Kenapa?

Supaya resolver bisa:

```text
username
   ↓
local lookup
   ↓
peer
```

tanpa harus selalu:

```text
network RPC
```

Ini akan sangat meningkatkan:

* `.id`
* mention resolution
* scheduler
* admin commands
* restart
* username-based targeting

---

# 8. gotd usage sekarang saya naikkan nilainya

Setelah fetch terbaru:

### Client setup

```go
telegram.NewClient(...)
```

✅ benar.

### Session

```go
FileSessionStorage
```

✅ benar.

gotd sendiri memang mendesain session storage sebagai persistent secure storage dan `FileStorage` menggunakan mode `0600`. ([GotD][2])

### Update handler

```go
telegram.UpdateHandlerFunc(...)
```

✅ benar.

### peers

```go
peers.Options{
    Storage: peerStorage,
}.Build(raw.API())
```

✅ **sekarang benar-benar proper.**

### updates

```go
updates.New(...)
```

dengan:

```go
AccessHasher: peerManager
```

✅ benar.

### UpdateHook

```go
peerManager.UpdateHook(gaps)
```

✅ benar.

### Run

```go
gaps.Run(
    ctx,
    c.raw.API(),
    me.ID,
    updates.AuthOptions{IsBot: me.Bot},
)
```

✅ benar.

Jadi untuk pertanyaan:

> **"Apakah implementasi gotd-nya salah?"**

Sekarang jawaban saya:

**Tidak. Core integration-nya sudah benar.**

---

# 9. Tetapi saya masih ingin satu layer lagi: TelegramGateway

Saat ini `internal/telegram` masih terlalu banyak menjadi:

```text
gotd wrapper
+
business logic
+
peer resolution
+
message semantics
+
error translation
```

Saya lebih suka:

```text
gotd
 ↓
TelegramGateway
 ↓
TelegramServicer
 ↓
Core
```

Contoh:

```go
type TelegramGateway struct {
    client *tg.Client
    peers  *peers.Manager
}
```

Gateway menangani:

* InputPeer
* InputUser
* access hash
* RPC
* flood wait
* Telegram errors
* entity normalization

Kemudian `Service` menangani:

* send message
* delete
* ban
* forward
* media
* pin

Ini akan membuat gotd benar-benar terisolasi.

---

# 10. 🚨 Masalah terbesar yang masih saya lihat: `Message.ID` mutation

Ini masih salah secara design kalau belum diubah.

Konsep:

```text
ctx.Message
```

harus merepresentasikan **incoming update**.

Bukan:

```text
incoming message
        ↓
ctx.Reply()
        ↓
ctx.Message berubah menjadi outgoing message
```

Kalau:

```go
ctx.Reply()
```

mengubah:

```go
ctx.Message.ID
```

maka plugin yang melakukan:

```go
ctx.Reply()
ctx.Delete()
```

bisa menghapus **reply**, bukan message asal.

Saya tetap merekomendasikan:

```go
Message.ID          // immutable incoming
Message.ResponseID  // last generated response
```

atau:

```go
ctx.Response()
```

---

# 11. 🚨 Dispatcher architecture masih perlu audit khusus update semantics

Sekarang dispatcher fokus:

```text
UpdateNewMessage
UpdateNewChannelMessage
```

Ini cukup untuk command bot.

Tetapi GoUltroid sebagai framework/userbot seharusnya punya abstraction:

```text
MessageCreated
MessageEdited
MessagesDeleted
ChannelPost
Reaction
ParticipantChanged
```

Karena gotd sendiri memiliki banyak update types. Dokumentasi/generated API menunjukkan update types seperti `UpdateEditMessage`, `UpdateEditChannelMessage`, `UpdateDeleteMessages`, `UpdateDeleteChannelMessages`, `UpdateMessageReactions`, dan banyak lainnya. ([GotD][3])

Saya **tidak menyarankan langsung memasukkan semua update ke dispatcher**.

Lebih baik:

```text
gotd update
      ↓
UpdateNormalizer
      ↓
Domain Event
      ↓
Plugin subscribers
```

---

# 12. Functional gap terbesar: edit/delete events

Untuk plugin modern, ini penting.

Contoh:

```text
User:
hello

Bot:
auto reply
```

User edit:

```text
hello
→
hello admin
```

Sekarang framework perlu:

```text
MessageEdited
```

Kalau tidak:

* filter tidak dire-evaluate
* anti-spam tidak bereaksi
* logging tidak update
* automation tidak trigger

Begitu pula delete:

```text
MessageDeleted
```

berguna untuk:

* purge tracking
* logging
* anti-delete
* moderation
* message cache

---

# 13. Functional gap: album/grouped media

Ini masih saya anggap penting.

Telegram bisa mengirim:

```text
photo A
photo B
photo C
```

dengan:

```text
grouped_id
```

Framework seharusnya expose:

```go
type Message struct {
    ...
    GroupedID int64
}
```

dan optional:

```go
ctx.Album()
```

Tanpa itu downloader/media plugin akan cenderung memperlakukan album sebagai message individual.

---

# 14. Functional gap: callback/buttons

Untuk userbot/framework modern, perlu mempertimbangkan:

```text
UpdateBotCallbackQuery
UpdateInlineBotCallbackQuery
```

dan reply markup.

gotd API memang menyediakan tipe update tersebut. ([GotD][3])

Ini membuka:

```text
inline buttons
callback handler
interactive plugins
pagination
confirmation dialogs
```

Misalnya:

```text
Are you sure?

[ YES ] [ NO ]
```

Ini sangat berguna untuk:

* `.restart`
* `.update`
* `.delall`
* admin destructive actions.

---

# 15. Functional gap: message entities

Saya ingin ini dibenahi sebelum terlalu banyak plugin dibuat.

Telegram message bukan sekadar:

```go
Text string
```

tetapi:

```text
Message
 ├── Text
 ├── Entities
 │    ├── Bold
 │    ├── Italic
 │    ├── Code
 │    ├── URL
 │    ├── Mention
 │    └── CustomEmoji
```

Kalau parser framework hanya bergantung pada string:

```text
@user
https://...
```

banyak semantic Telegram hilang.

Sebaiknya Context expose:

```go
ctx.Entities()
ctx.Mentions()
ctx.URLs()
ctx.ReplyTo()
```

---

# 16. 🚨 Restart state masih perlu atomic write

Dari `client.go`, restart state sekarang masih:

```go
os.WriteFile(...)
```

dan `checkRestartState()` membaca:

```text
data/restart.json
```

Ini masih vulnerable terhadap:

```text
power loss
SIGKILL
disk full
partial write
```

Gunakan:

```text
restart.json.tmp
 ↓
Write
 ↓
Sync
 ↓
Rename
```

Ini kecil, tapi penting untuk reliability.

---

# 17. Scheduler shutdown masih punya semantic problem

Sekarang:

```go
StopWithTimeout(10s)
```

menunggu:

```go
wg.Wait()
```

Tetapi scheduled jobs diberi:

```text
ctx
```

yang berasal dari scheduler root.

Saat stop:

```go
e.cancel()
```

Jadi job aktif menerima cancellation.

Bagus.

Tetapi setelah 10 detik:

```text
Stop()
 ↓
timeout
 ↓
return error
```

goroutine job **masih mungkin hidup**.

Kalau kemudian:

```text
DB.Close()
```

dijalankan:

```text
job goroutine
 ↓
RecordJobRun()
 ↓
closed DB
```

Ini bisa menghasilkan error/race semantic.

### Solusi:

App shutdown harus:

```text
cancel root
 ↓
stop scheduler
 ↓
wait jobs
 ↓
if timeout:
    log active jobs
    force process shutdown
 ↓
close DB
```

Dan jangan menganggap `Stop()` timeout berarti scheduler sudah mati.

---

# 18. `RecordJobRun()` sebaiknya berada dalam transaction yang sama dengan completion?

Sekarang alurnya:

```text
execute
 ↓
RecordJobRun
 ↓
CompleteScheduledJob
```

Ada window:

```text
RecordJobRun SUCCESS
 ↓
process crash
 ↓
CompleteScheduledJob NEVER happens
```

atau:

```text
Complete SUCCESS
 ↓
history insert gagal
```

Untuk audit-grade scheduler, idealnya:

```text
transaction
 ├── job state transition
 └── execution history
commit
```

Dalam satu transaction.

Ini membuat:

```text
SUCCESS
```

dan:

```text
history SUCCESS
```

selalu konsisten.

Ini saya anggap **P1**.

---

# 19. Scheduler retry semantics sudah bagus, tetapi ada satu improvement

Sekarang:

```text
attempt 1 → 10s
attempt 2 → 20s
attempt 3 → failed
```

Bagus.

Tetapi untuk error seperti:

```text
CHAT_WRITE_FORBIDDEN
USER_BANNED
CHANNEL_PRIVATE
PEER_ID_INVALID
```

retry sebenarnya tidak berguna.

Seharusnya scheduler mengenali:

```text
PermanentError
```

dan langsung:

```text
DLQ / failed
```

sedangkan:

```text
FLOOD_WAIT
NETWORK
TIMEOUT
5xx
```

retry.

Ini bisa menghemat banyak RPC.

---

# 20. `FailScheduledJob()` perlu reset semantics untuk recurring

Sekarang successful recurring:

```text
attempt_count = 0
```

bagus.

Tetapi failure:

```text
attempt_count++
```

kemudian retry.

Saya sarankan dokumentasikan dengan jelas:

```text
attempt_count = attempts for current scheduled occurrence
```

bukan:

```text
lifetime attempts
```

Saat recurring job berhasil:

```text
reset → 0
```

sudah tepat.

---

# 21. Migration v6 history sudah bagus

Saya suka penambahan:

```text
scheduled_job_history
```

dengan:

```text
job_id
ran_at
duration_ms
success
error_msg
```

Ini membuat scheduler bisa menjadi observable.

Tetapi nanti tambahkan:

```text
attempt
claim_token
trigger_type
```

sehingga bisa membedakan:

```text
normal
retry
lease recovery
manual run
```

---

# 22. Migration system perlu satu improvement

`columnExists()` memakai:

```go
PRAGMA table_info(%s)
```

dengan interpolasi nama tabel/kolom.

Karena nama tersebut berasal dari internal migration constants, **tidak ada practical injection risk sekarang**.

Tetapi saya lebih suka tidak menggunakan generic dynamic SQL untuk migration helper.

Bisa dibuat:

```go
columnExists(tx, "scheduled_jobs", "claim_token")
```

dengan identifier whitelist.

Minor.

---

# 23. Ada satu hal yang jangan dilakukan: jangan lagi menambah workaround preload dialogs

Sekarang masih ada:

```go
MessagesGetDialogs(... Limit: 100)
```

untuk preload access hashes.

Dengan persistent `PeerStorage`, ini sebenarnya sudah menjadi **redundant optimization**.

Tidak salah.

Tetapi jangan bergantung padanya.

Lebih bersih:

```text
PeerStorage = authoritative cache
PeerManager = runtime cache
Dialogs      = optional warm-up
```

Komentarnya sebaiknya menjelaskan bahwa preload hanya optimization.

---

# 24. Saya akan mengubah startup sequence sedikit

Sekarang:

```text
Connect
 ↓
Auth
 ↓
Self
 ↓
PeerManager Init
 ↓
GetDialogs
 ↓
Gaps.Run
```

Saya lebih suka:

```text
Connect
 ↓
Auth
 ↓
Self
 ↓
PeerStorage ready
 ↓
PeerManager Init
 ↓
Gaps.Run
```

Kemudian warm-up dialogs **async**:

```text
Gaps.Run
 ↓
background peer warmup
```

Karena:

```text
MessagesGetDialogs
```

tidak seharusnya menjadi hard dependency startup.

Kalau Telegram RPC lambat:

```text
bot startup
```

tidak perlu tertunda hanya untuk warm cache.

---

# 25. Final assessment

Setelah fetch terbaru, saya **mengubah beberapa penilaian sebelumnya**.

### Yang sekarang saya anggap solid

```text
✅ gotd client initialization
✅ session persistence
✅ auth flow
✅ updates.Manager
✅ peers.Manager
✅ persistent peer access hashes
✅ UpdateHook
✅ dispatcher → executor
✅ scheduler claim
✅ fencing token
✅ retry
✅ recurring schedule
✅ migration versioning
✅ scheduler history
✅ graceful context architecture
```

### Yang masih saya anggap perlu diperbaiki

```text
🔴 Message.ID mutation
🔴 scheduler history/state atomicity
🟠 scheduler lease recovery observability
🟠 conditional claim UPDATE
🟠 scheduler permanent-error classification
🟠 shutdown timeout vs DB.Close
🟠 restart.json atomic persistence
🟠 error normalization consistency
🟠 peer metadata persistence
🟠 redundant peers_phones.access_hash
🟡 album/grouped messages
🟡 edited/deleted message events
🟡 callback/button events
🟡 Telegram entity abstraction
```

---

# Prioritas saya sekarang

Kalau langsung lanjut coding, saya **tidak akan menambah fitur baru dulu**.

Saya akan melakukan fase berikut:

```text
PHASE 1 — Correctness
────────────────────────────
1. Fix Message.ID immutability
2. Make scheduler state + history atomic
3. Conditional claim UPDATE
4. Lease-expiry history
5. Permanent vs transient Telegram errors
6. Shutdown hardening
7. Atomic restart state

PHASE 2 — Telegram abstraction
────────────────────────────
8. TelegramGateway
9. Central peer normalization
10. Central error normalization
11. Peer metadata cache
12. Entity abstraction

PHASE 3 — Update platform
────────────────────────────
13. MessageEdited
14. MessagesDeleted
15. Album/grouped messages
16. CallbackQuery
17. ReplyMarkup/buttons
18. Message entities

PHASE 4 — Functional expansion
────────────────────────────
19. Better media pipeline
20. Better admin/moderation
21. Cron/timezone scheduler
22. Job management/history UI
23. Plugin event system
```

**Jadi fetch terbaru ini hasilnya positif:** dua masalah yang sebelumnya paling saya khawatirkan — **access-hash persistence dan scheduler stale-worker overwrite** — sekarang sudah ditangani dengan desain yang jauh lebih proper. `PeerStorage` sekarang benar-benar terhubung ke `peers.Manager`, dan scheduler sudah memakai fencing token pada completion/failure.

Yang tersisa sekarang bukan rewrite besar. **GoUltroid sudah masuk fase hardening + semantic correctness + feature completeness.**

[1]: https://ref.gotd.dev/src/github.com/gotd/td/telegram/client.go.html?utm_source=chatgpt.com "Source: client.go in package github.com/gotd/td/telegram"
[2]: https://ref.gotd.dev/src/github.com/gotd/td/session/storage_file.go.html?utm_source=chatgpt.com "Source: storage_file.go in package github.com/gotd/td/session"
[3]: https://ref.gotd.dev/pkg/context.html?utm_source=chatgpt.com "Package: context"
