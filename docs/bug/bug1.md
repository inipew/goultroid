Saya sudah cek ulang `main` terbaru, dengan fokus lebih luas: **bug/correctness**, **gotd/td usage**, **MTProto peer/update lifecycle**, dan **fungsionalitas plugin secara keseluruhan**. Saya juga cross-check pola penggunaan terhadap dokumentasi gotd/td v0.161.0 yang saat ini dipakai repo. `go.mod` memang mengunci `github.com/gotd/td v0.161.0`.

Kesimpulan singkatnya:

> **GoUltroid sudah punya fondasi yang bagus, tetapi setelah audit lebih dalam saya menemukan beberapa bug fungsional yang nyata dan beberapa penggunaan gotd yang "benar secara API" tetapi belum optimal/aman secara semantic.**
>
> Yang paling penting bukan lagi scheduler saja. Sekarang perhatian terbesar saya justru **peer/access-hash lifecycle, command timeout semantics, Telegram entity handling, admin semantics, media handling, dan consistency error handling**.

---

# 1. Status keseluruhan

Saya akan menilai sekarang:

| Area                   |       Nilai |
| ---------------------- | ----------: |
| Arsitektur             |  **8.8/10** |
| Scheduler              |  **8.3/10** |
| gotd integration       |  **8.0/10** |
| Telegram peer handling |  **7.3/10** |
| Command execution      |  **8.2/10** |
| Plugin functionality   |  **7.8/10** |
| Error handling         |  **7.5/10** |
| Media                  |  **7.0/10** |
| Security               |  **8.0/10** |
| Production readiness   | **~8.0/10** |

Bukan karena arsitekturnya buruk — justru fondasinya sudah bagus — tetapi karena beberapa bug berada di layer yang **sangat terlihat ketika dipakai di Telegram sebenarnya**.

---

# 2. 🚨 BUG BESAR: access hash belum durable

Ini yang menurut saya sekarang menjadi masalah gotd paling penting.

Kamu sudah melakukan hal yang benar:

```go
peerManager := peers.Options{}.Build(raw.API())

gaps := updates.New(updates.Config{
    Handler:      tgDispatcher,
    AccessHasher: peerManager,
})
```

dan:

```go
updateHook = peerManager.UpdateHook(gaps)
```

Ini **sesuai pola resmi gotd**. Dokumentasi gotd memang menunjukkan pola `peers.Options{}.Build(client.API())`, kemudian `updates.New(... AccessHasher: peerManager)` dan `peerManager.UpdateHook(gaps)`. ([GotD][1])

Jadi **penggunaan API-nya benar**.

Masalahnya ada pada **storage**.

Sekarang kamu membuat:

```go
peers.Options{}.Build(raw.API())
```

tanpa persistent peer storage.

Artinya peer manager pada dasarnya hanya mempunyai state yang diketahui selama lifetime proses.

Dokumentasi gotd juga menjelaskan bahwa `peers.Manager` memang bertugas menyelesaikan peer dan melakukan caching access hash. ([GotD][1])

Dan gotd sendiri menyediakan `Storage` pada `peers.Options`; contoh aplikasi yang membutuhkan persistence menggunakan storage sendiri. Contoh lain di ekosistem gotd juga menyimpan peer cache terpisah karena session bukan entity database. ([GitExtract][2])

### Dampaknya

Kamu memang mencoba mengatasi dengan:

```go
MessagesGetDialogs(... Limit: 100)
```

Tetapi hanya preload **100 dialog**.

Jadi:

```text
Chat #A
Chat #B
Chat #C
...
Chat #500
```

Jika chat #500 tidak masuk 100 dialog pertama:

```text
restart
   ↓
peerManager kosong
   ↓
chat #500
   ↓
InputPeerChannel{ChannelID: ..., AccessHash: 0}
   ↓
RPC
   ↓
❌ CHANNEL_INVALID / ACCESS_HASH_INVALID
```

Resolver juga mempunyai fallback:

```go
return &tg.InputPeerChannel{ChannelID: channelID}, nil
```

yang berarti **mengembalikan peer yang belum usable untuk banyak raw MTProto operation**.

Ini bukan sekadar optimization.

### Saya kategorikan:

**P1 correctness issue.**

---

# 3. Solusi access hash yang saya rekomendasikan

Jadikan database GoUltroid sebagai persistent peer cache.

Misalnya:

```text
telegram_peers
├── peer_type
├── peer_id
├── access_hash
├── username
├── title
├── updated_at
└── flags
```

Lalu implement storage adapter untuk:

```go
peers.Options{
    Storage: ...
}
```

Dengan demikian:

```text
Telegram
   ↓
gotd UpdateHook
   ↓
peers.Manager
   ↓
Persistent Peer Storage
   ↓
SQLite
```

Bukan:

```text
Telegram
   ↓
peers.Manager
   ↓
RAM
   ↓
restart
   ↓
gone
```

Ini juga membuat scheduler jauh lebih reliable.

---

# 4. Dispatcher masih melakukan manual peer reconstruction

Di dispatcher:

```go
peerInput = &tg.InputPeerUser{
    UserID:     p.UserID,
    AccessHash: accessHash,
}
```

dan:

```go
peerInput = &tg.InputPeerChannel{
    ChannelID: p.ChannelID,
    AccessHash: accessHash,
}
```

Ini bekerja jika access hash tersedia.

Tetapi kamu sebenarnya sudah memiliki:

```go
peers.Manager
```

dan `Resolver`.

Jadi arsitektur idealnya:

```text
Update
 ↓
entities
 ↓
PeerManager
 ↓
canonical InputPeer
 ↓
core.Context
```

bukan:

```text
Update
 ↓
manual switch PeerUser/PeerChannel
 ↓
manual access hash extraction
 ↓
fallback resolver
```

Karena semakin banyak tempat melakukan peer reconstruction, semakin besar kemungkinan behavior berbeda.

---

# 5. Resolver numeric ID masih terlalu permissive

Contoh:

```go
if uid, err := strconv.ParseInt(ref, 10, 64); err == nil && uid > 0 {
    ...
    return &tg.InputPeerUser{UserID: uid}, uid, nil
}
```

Ini menganggap:

```text
123456789
```

selalu bisa dibuat menjadi:

```go
InputPeerUser
```

Padahal Telegram memerlukan access hash untuk banyak operasi user.

Lebih buruk lagi:

```text
ResolveUser("123456789")
        ↓
peerManager gagal
        ↓
return InputPeerUser{UserID: 123456789}
        ↓
caller menganggap resolve sukses
```

Seharusnya resolver punya semantic:

> **"resolved" berarti peer siap dipakai**, bukan sekadar ID berhasil diparse.

Saya akan ubah menjadi:

```go
if numeric ID && access hash unavailable {
    return nil, 0, ErrPeerNotResolved
}
```

kecuali memang ada operation tertentu yang sah menggunakan bare ID.

---

# 6. gotd `updates.Manager`: implementasi dasarnya BENAR

Bagian ini justru saya beri nilai tinggi.

Kamu memakai:

```text
telegram.Client
    ↓
tg.UpdateDispatcher
    ↓
updates.Manager
    ↓
peerManager.UpdateHook
```

dan:

```go
return c.gaps.Run(
    ctx,
    c.raw.API(),
    me.ID,
    updates.AuthOptions{IsBot: me.Bot},
)
```

Ini mengikuti pola resmi gotd. Dokumentasi contoh gotd menggunakan `updates.Manager`, `AccessHasher`, `UpdateHook`, kemudian `gaps.Run(..., updates.AuthOptions{IsBot: isBot})`. ([Go Packages][3])

Jadi:

### ✅ Benar

* `tg.NewUpdateDispatcher()`
* `updates.New(...)`
* `AccessHasher: peerManager`
* `peerManager.UpdateHook(gaps)`
* `gaps.Run(...)`
* `AuthOptions{IsBot: me.Bot}`

Untuk userbot:

```text
me.Bot = false
```

sehingga ini juga benar.

---

# 7. Tetapi ada satu improvement penting pada gotd updates

Kamu menggunakan:

```go
updates.New(updates.Config{
    Handler:      tgDispatcher,
    AccessHasher: peerManager,
})
```

tanpa logger.

Bukan bug, tetapi saya akan menambahkan:

```go
Logger: logzap.New(logger.Named("updates")),
```

karena updates manager adalah salah satu komponen paling penting untuk diagnosis:

* gap
* difference
* channel updates
* reconnect
* pts/qts
* entity/access hash

gotd sendiri menunjukkan konfigurasi `Logger` untuk `updates.Manager` pada contoh resminya. ([Go Packages][3])

---

# 8. ⚠️ gotd punya beberapa issue upstream yang perlu kamu ketahui

Per 2026, repository gotd sendiri memiliki beberapa issue terbuka/tercatat terkait update handling dan networking, termasuk:

* update tertentu yang tidak diterima dalam kondisi tertentu,
* masalah `updates.Manager`,
* channel scaling,
* beberapa masalah transport/retry. ([GitHub][4])

Ini **bukan bug GoUltroid**.

Tetapi karena kamu menggunakan `updates.Manager`, saya sarankan jangan menulis asumsi:

> "gotd menjamin semua update pasti masuk."

Lebih tepat:

> gotd menyediakan mekanisme update recovery dan gap handling, tetapi aplikasi tetap harus idempotent terhadap update dan tahan terhadap reconnect/replay.

Ini penting untuk:

```text
AFK
filters
blacklist
scheduler
command execution
```

---

# 9. 🚨 BUG: Command timeout sekarang tidak sesuai dokumentasi plugin

Ini bug nyata.

`CommandExecutor`:

```go
TimeoutMiddleware(cmd, e.defaultTimeout)
```

dan default:

```go
30 * time.Second
```

Tetapi plugin system mendefinisikan:

```go
exec.Timeout = 65s
update.Timeout = 180s
extractaudio.Timeout = 3m
```

Middleware memang membaca:

```go
if cmd.Timeout > 0 {
    timeout = cmd.Timeout
}
```

jadi secara teori timeout command-specific **bekerja**.

Namun `system.handleExec()` sendiri membuat:

```go
context.WithTimeout(ctx.Ctx, 60*time.Second)
```

dan update:

```go
context.WithTimeout(ctx.Ctx, 120*time.Second)
```

Jadi timeout effective adalah:

```text
.exec
CommandExecutor = 65s
handler internal = 60s
→ 60s

.update
CommandExecutor = 180s
git fetch = 30s
git pull = 60s
build = 120s
→ pipeline total bisa >180s
```

Yang terakhir memang bisa timeout di executor.

Ini bukan fatal, tetapi timeout policy-nya **tersebar di dua layer**.

### Lebih baik:

CommandExecutor:

```text
command.Timeout
```

menjadi **single source of truth**.

Handler jangan membuat timeout kedua kecuali memang operasi internal membutuhkan deadline lebih pendek.

---

# 10. 🚨 `.update` punya bug semantic serius

Kode:

```go
git fetch
```

kemudian:

```go
git log HEAD..origin/main
```

Ini mengasumsikan:

```text
origin/main
```

ada.

Fallback:

```go
git log HEAD..@{u}
```

lumayan.

Tetapi `.update pull` langsung:

```go
git pull
```

Ini berbahaya untuk deployment.

Jika working tree berubah:

```text
git pull
```

bisa:

* merge
* conflict
* fail
* menghasilkan state setengah-update

Kemudian userbot sudah menjalankan:

```text
git pull
```

tetapi belum build/restart.

Lebih baik:

```text
git fetch
 ↓
check dirty
 ↓
check upstream
 ↓
fast-forward only
 ↓
build temporary binary
 ↓
atomic replace
 ↓
restart
```

Minimal:

```bash
git pull --ff-only
```

bukan:

```bash
git pull
```

---

# 11. 🚨 `.restart` berpotensi kehilangan restart state

Kamu melakukan:

```go
os.WriteFile(...)
```

lalu:

```go
syscall.Exec(...)
```

Secara normal bagus.

Tetapi kamu mengabaikan error:

```go
_ = os.MkdirAll(...)
_ = json.Marshal(...)
_ = os.WriteFile(...)
```

Kalau disk read-only/full:

```text
write restart state gagal
       ↓
syscall.Exec
       ↓
restart
       ↓
state hilang
```

Lebih aman:

```text
write temp
fsync
rename atomic
```

Contoh:

```text
restart.json.tmp
    ↓
fsync
    ↓
rename
    ↓
restart.json
```

---

# 12. 🚨 `.exec` security sudah Owner-only, tetapi resource isolation belum cukup

Permission:

```text
Owner
```

bagus.

Tetapi:

```go
bash -c <user-controlled string>
```

memang secara desain memberikan arbitrary shell.

Karena ini userbot Owner-only, itu bukan vulnerability utama.

Yang lebih penting:

```text
CPU
RAM
process count
disk output
disk usage
fork bomb
```

Misalnya:

```bash
:(){ :|:& };:
```

Timeout context tidak selalu menjadi proteksi yang cukup terhadap child-process trees.

Saya akan menambahkan Linux resource isolation jika ingin benar-benar production:

```text
setrlimit
process group
kill process group
max output
max CPU
max file size
```

Minimal output limit sekarang ada secara Telegram presentation, tetapi **output tetap dikumpulkan seluruhnya oleh `CombinedOutput()` ke RAM**.

Jadi:

```bash
yes
```

dapat membuat memory pressure sebelum timeout.

Ini **P1 security/resource issue**.

---

# 13. 🚨 Media download size limit belum benar-benar resource-safe

`DownloadMedia()`:

```go
if media.Size > 500MB {
    reject
}
```

bagus.

Tetapi jika:

```text
media.Size == 0
```

atau ukuran Telegram tidak tersedia/unknown, download tetap berjalan.

Selain itu:

```go
DownloadFile(...).ToPath(...)
```

tidak terlihat ada:

* actual byte limit
* disk quota
* free-space check
* cancellation cleanup

Jadi:

```text
Telegram reports unknown size
        ↓
download allowed
        ↓
huge file
        ↓
disk fills
```

Saya sarankan downloader wrapper memakai hard byte limit.

---

# 14. 🚨 `mediainfo` bisa panic

Ini bug kecil tetapi nyata:

```go
mediaTypeDisplay := strings.ToUpper(media.Type[:1]) + media.Type[1:]
```

Jika:

```go
media.Type == ""
```

maka:

```text
panic: slice bounds out of range
```

Harus:

```go
mediaTypeDisplay := media.Type
if mediaTypeDisplay != "" {
    mediaTypeDisplay = strings.ToUpper(mediaTypeDisplay[:1]) + mediaTypeDisplay[1:]
}
```

---

# 15. 🚨 HTML escaping tidak konsisten

Di `media.go`:

```go
FileName
MimeType
```

langsung dimasukkan ke:

```html
<code>...</code>
```

tanpa escaping.

Misalnya filename:

```text
foo<bar>.mp4
```

akan menjadi Telegram HTML.

Hal serupa terjadi di beberapa plugin.

Kamu sudah punya:

```go
escapeHTML()
```

di system plugin.

Saya sarankan pindahkan ke shared core utility:

```go
core.EscapeHTML()
```

dan **semua generated Telegram HTML wajib melewatinya**.

---

# 16. 🚨 Admin `ban` menggunakan semua banned rights

Ini perlu diperhatikan.

Kode:

```go
ViewMessages: true,
SendMessages: true,
SendMedia: true,
SendStickers: true,
SendGifs: true,
SendGames: true,
SendInline: true,
EmbedLinks: true,
```

Ini memang menghasilkan efek ban yang kuat.

Tetapi Telegram permission model memiliki banyak field dan semantic yang dapat berubah/bertambah.

Lebih aman membuat helper:

```go
FullBanRights()
```

dan:

```go
FullMuteRights()
```

agar tidak ada perbedaan antara ban/mute.

---

# 17. 🚨 Kick implementation adalah hack dua langkah

Sekarang:

```text
ChannelsEditBanned
ViewMessages = true
UntilDate = now + 60s

        ↓

ChannelsEditBanned
BannedRights = {}
```

Ini digunakan untuk kick.

Secara praktis bisa bekerja:

```text
ban sementara
↓
user keluar
↓
unban
```

Tetapi ada race:

```text
ban
 ↓
unban terlalu cepat
 ↓
Telegram state timing
```

atau:

```text
user rejoins sebelum expected
```

Ini bukan salah gotd, tetapi semantic kick memang tricky.

Untuk supergroup, perlu diperlakukan sebagai operation dengan expected semantics, bukan sekadar dua RPC tanpa verification.

---

# 18. 🚨 `UnbanUser()` menganggap `USER_NOT_PARTICIPANT` sukses

Ini sebenarnya masuk kategori acceptable idempotency:

```go
USER_NOT_PARTICIPANT → nil
```

Saya justru setuju.

Tetapi dokumentasikan sebagai:

> operation is idempotent

Supaya developer berikutnya tidak menganggap error suppression sebagai bug.

Hal yang sama:

```text
CHAT_NOT_MODIFIED
RIGHTS_NOT_MODIFIED
```

sudah tepat diperlakukan sebagai success untuk operation tertentu.

---

# 19. 🚨 `GetMessage()` bisa mengembalikan `(nil, nil)`

Sekarang:

```go
return nil, nil
```

jika message tidak ditemukan.

Ini membuat caller harus membedakan:

```text
not found
```

dari:

```text
success
```

Lebih baik:

```go
return nil, core.ErrNotFound
```

atau explicit:

```go
(*tg.Message, bool, error)
```

Karena sekarang:

```text
GetReply()
   ↓
msg == nil
   ↓
nil, nil
```

dan caller bisa salah menganggap reply memang tidak ada.

---

# 20. 🚨 Forward belum melakukan peer normalization

```go
s.sender.To(toPeer).ForwardIDs(...)
```

Sedangkan operasi lain sudah melakukan:

```go
ensureChannelAccessHash()
ensureUserAccessHash()
```

Jadi ada inconsistency:

```text
SendMessage → peer helper
Delete → ensure hash
Pin → ensure hash
Ban → ensure hash
Forward → ❌ tidak
```

Ini harus disatukan.

Saya sarankan membuat:

```go
normalizePeer(ctx, peer)
```

dan setiap operation memanggilnya.

---

# 21. 🚨 Error mapping belum digunakan konsisten

Kamu sudah punya:

```go
mapTelegramError()
```

dan:

```go
ErrTelegram
ErrNotFound
ErrPermissionDenied
ErrRateLimit
```

Ini bagus.

Tetapi banyak method masih:

```go
return err
```

langsung.

Misalnya:

```go
React()
ForwardMessages()
DownloadFile()
GetMessage()
```

Akibatnya plugin mendapatkan dua dunia:

```text
core.ErrTelegram
```

vs:

```text
*telegram.Error
```

Saya sarankan **semua boundary `Service` → core harus normalize error**.

Ini akan sangat membantu:

```go
errors.Is(err, core.ErrPermissionDenied)
errors.Is(err, core.ErrNotFound)
errors.Is(err, core.ErrRateLimit)
```

secara konsisten.

---

# 22. 🚨 FloodWait handling masih terlalu sederhana

Sekarang:

```go
if wait <= 5s {
    sleep
    retry
}
```

Ini bagus untuk UX.

Tetapi ada masalah semantic:

```text
FloodWait = 4s
retry
FloodWait = 5s
return error
```

Selain itu retry dilakukan hanya sekali.

Saya lebih suka:

```go
RetryPolicy{
    MaxAttempts: 2,
    MaxFloodWait: 5 * time.Second,
}
```

dan jangan retry operation yang tidak idempotent tanpa pertimbangan.

Misalnya send message:

```text
request sebenarnya sukses
response hilang
retry
```

dapat menghasilkan duplicate message.

gotd memang menyediakan retry/transport machinery sendiri; application-level retry sebaiknya dibatasi pada error/operation yang aman. Dokumentasi client menunjukkan configurable retry interval/max retries di `telegram.Options`. ([Go Packages][5])

---

# 23. 🚨 `ctx.Reply()` mengubah `Message.ID`

Ini desain yang agak berbahaya:

```go
if sent != nil && c.Message != nil {
    c.Message.ID = sent.ID
}
```

Artinya:

```text
original incoming message
ID = 100

ctx.Reply()
   ↓
Message.ID = 101
```

Sekarang:

```go
ctx.Delete()
```

akan menghapus:

```text
101
```

bukan:

```text
100
```

Mungkin memang sengaja agar:

```text
Reply → Edit
```

bekerja.

Tetapi object model-nya menjadi ambigu.

Saya sarankan:

```go
Message.ID
```

tetap immutable.

Tambahkan:

```go
ResponseID int
```

atau:

```go
LastResponseID int
```

Sehingga:

```text
Message.ID       = incoming
ResponseID       = bot reply
ReplyToID        = target
```

Ini akan membuat API Context jauh lebih bersih.

---

# 24. Router punya dua edge-case bug

Parser:

```go
if r == '\\' {
    escaped = true
}
```

Kalau command berakhir:

```text
.cmd hello\
```

backslash terakhir hilang.

Selain itu:

```text
.cmd "hello world
```

unclosed quote tetap diterima.

Saya sarankan parser mengembalikan error:

```go
Parse(text) (*ParsedCommand, bool, error)
```

dengan:

```text
ErrUnclosedQuote
ErrTrailingEscape
```

Ini terutama penting untuk `.exec`.

---

# 25. `FilterMiddleware` outgoing bypass perlu ditinjau

Sekarang:

```go
if !isOutgoing {
    if cmd.GroupOnly ...
}
```

Jadi owner bisa melakukan:

```text
.outgoing GroupOnly command
```

di private/Saved Messages.

Saya memahami alasan desainnya.

Tetapi secara semantic:

> `GroupOnly` seharusnya berarti command hanya boleh dieksekusi dalam group.

Bukan:

> kecuali outgoing.

Lebih baik bedakan:

```go
cmd.GroupOnly
cmd.AllowOutgoing
```

Daripada middleware mengubah semantic `GroupOnly`.

---

# 26. Scheduler + CommandExecutor masih belum share executor

Ini saya perhatikan dari arsitektur terbaru.

Dispatcher membuat:

```go
executor := core.NewCommandExecutor(...)
```

Scheduler membuat executor sendiri lalu mempunyai:

```go
SetExecutor(...)
```

Tetapi dari `App.New()` yang kamu tunjukkan:

```go
schedEngine := scheduler.NewEngine(...)
```

tidak terlihat ada:

```go
schedEngine.SetExecutor(dispatcher.Executor())
```

Jadi ada kemungkinan:

```text
Incoming command
    ↓
Executor A
    ↓
Cooldown A

Scheduled command
    ↓
Executor B
    ↓
Cooldown B
```

Jika scheduler memang membuat executor internal dengan:

```go
cooldown = nil
```

maka scheduled commands tidak benar-benar identik dengan command biasa.

### Solusi

App harus membuat **satu executor**:

```text
App
 └── CommandExecutor
       ├── Dispatcher
       └── Scheduler
```

Bukan:

```text
Dispatcher → Executor A
Scheduler  → Executor B
```

Ini juga membuat observability lebih konsisten.

---

# 27. Permission object sendiri masih terlalu mutable

`Permissions` dipakai oleh:

```text
dispatcher
scheduler
sudo plugin
```

Kalau sudo berubah runtime:

```text
.addsudo
```

object permission berubah.

Itu bagus.

Tetapi scheduled execution perlu memastikan read/write thread safety.

Saya akan memastikan:

```go
CanRun()
AddSudo()
RemoveSudo()
```

semuanya protected oleh `RWMutex`.

Kalau belum, `go test -race` harus menjadi mandatory.

---

# 28. Functional audit: command coverage sudah lumayan luas

Daftar command sekarang sudah mencakup:

```text
Utility
Admin
Moderation
Notes
AFK
Filters
Media
System
Scheduler
Fun
Info
```

README juga sudah mendokumentasikan command-command tersebut.

Secara feature coverage, ini sudah jauh lebih dari sekadar skeleton.

---

# 29. Tetapi ada gap penting dibanding Userbot modern

Kalau targetmu benar-benar:

> "Go version of Ultroid"

maka masih ada banyak functional area yang belum ada.

Yang saya anggap **P1/P2 feature gap**:

### Telegram core

* inline queries
* callback queries
* edited message handling
* deleted-message handling
* channel post variants
* album/media groups
* service messages
* reactions/events
* join/leave events
* message entities
* buttons
* reply markup
* topics/thread operations

### Media

* upload progress
* download progress
* thumbnails
* video conversion
* image conversion
* sticker → image
* video → sticker
* animated sticker
* GIF
* voice ↔ audio
* document metadata
* media streaming

### Admin

* admin rights inspection
* participant listing
* admin listing
* ban duration parsing
* temporary restrictions
* warning system
* anti-spam
* flood control

### Userbot

* `.id`
* `.me`
* `.profile`
* `.setbio`
* `.setname`
* `.setpic`
* `.delphoto`
* `.block`
* `.unblock`
* `.contacts`
* `.dialogs`

### Automation

* richer scheduler
* cron
* timezone
* misfire policy
* job enable/disable
* job retry configuration
* job history
* execution result

---

# 30. Ada satu hal yang menurut saya sangat penting: Album / grouped media

Dispatcher sekarang menangani:

```text
UpdateNewMessage
UpdateNewChannelMessage
```

tetapi belum terlihat ada abstraction untuk:

```text
grouped_id
```

Telegram media album:

```text
photo 1
photo 2
photo 3
```

datang sebagai beberapa messages.

Plugin yang mengharapkan:

```text
"reply to this media"
```

atau downloader dapat memproses hanya satu message.

Kalau ingin mendekati behavior Ultroid, sebaiknya ada:

```go
Message.GroupedID int64
```

dan optional album collector.

---

# 31. Forum Topics sudah mulai bagus, tetapi perlu konsistensi

Kamu sudah menangani:

```go
header.ForumTopic
header.ReplyToTopID
```

dan:

```go
TopicID
```

Ini bagus.

Tetapi topic awareness harus diterapkan secara konsisten ke:

```text
purge
filters
blacklist
scheduler
auto replies
message search
delete
```

Kalau hanya `purge` yang topic-aware, behavior akan terasa tidak konsisten.

---

# 32. `tg.UpdateDispatcher` belum tentu cukup untuk seluruh userbot lifecycle

Sekarang hanya:

```go
OnNewMessage
OnNewChannelMessage
```

Untuk command-oriented userbot ini cukup.

Tetapi kalau ingin platform-level:

```text
OnEditMessage
OnDeleteMessages
OnChannelUpdate
OnUserStatus
```

perlu ditambahkan event layer sendiri.

Saya sarankan jangan membuat plugin langsung bergantung pada `tg.UpdateDispatcher`.

Buat:

```text
gotd Update
      ↓
UpdateNormalizer
      ↓
Domain Event
      ↓
Plugin/Event Bus
```

Contoh:

```go
MessageCreated
MessageEdited
MessagesDeleted
UserUpdated
ChatUpdated
ReactionUpdated
```

Ini akan membuat GoUltroid jauh lebih extensible.

---

# 33. Arsitektur gotd yang saya rekomendasikan

Sekarang:

```text
                  gotd
                   │
        ┌──────────┴───────────┐
        ▼                      ▼
  UpdateDispatcher       Peer Manager
        │                      │
        ▼                      ▼
    Dispatcher             Resolver
        │
        ▼
 CommandExecutor
```

Saya akan evolusikan menjadi:

```text
                         gotd/td
                            │
                 ┌──────────┴──────────┐
                 ▼                     ▼
          updates.Manager         peers.Manager
                 │                     │
                 └──────────┬──────────┘
                            ▼
                    Telegram Gateway
                            │
                  ┌─────────┴─────────┐
                  ▼                   ▼
             Update Bus          Peer Service
                  │
        ┌─────────┼─────────┐
        ▼         ▼         ▼
   Interceptor  Command   Event Handler
        │         │
        │         ▼
        │    CommandExecutor
        │         │
        └─────────┤
                  ▼
              Plugins
```

Dengan:

```text
SQLite
 ├── scheduler
 ├── sudo
 ├── peer cache
 ├── settings
 └── plugin state
```

---

# 34. Priority fix saya sekarang

Kalau saya yang maintain repo ini, urutannya:

### 🔴 P0 — correctness

1. **Persistent peer/access-hash storage**
2. **Claim token scheduler**
3. **Lease > command timeout**
4. **Scheduled principal resolution**
5. **Shared CommandExecutor**
6. **Normalize all Telegram errors**
7. **Fix `Context.Message.ID` mutation**

### 🟠 P1 — functional reliability

8. `ForwardMessages()` peer normalization
9. resolver jangan return unusable bare peer
10. `.update --ff-only` + atomic binary replacement
11. `.exec` process/resource isolation
12. media hard byte limit
13. `GetMessage()` proper NotFound
14. media HTML escaping
15. router malformed quote handling
16. recurring scheduler misfire policy

### 🟡 P2 — platform quality

17. album/grouped media
18. edit/delete updates
19. domain event bus
20. richer peer model
21. timezone-aware scheduler
22. scheduler history
23. plugin capability model
24. metrics/health
25. structured Telegram RPC error taxonomy

---

# 35. Penilaian akhir saya terhadap gotd

**Apakah gotd/td digunakan dengan benar?**

### Ya, secara fundamental: **YA.**

Bagian ini sudah bagus:

* `telegram.NewClient`
* `FileSessionStorage`
* `tg.UpdateDispatcher`
* `updates.Manager`
* `AccessHasher`
* `peers.Manager`
* `UpdateHook`
* `gaps.Run`
* `auth.NewFlow`
* `Self()`
* `message.Sender`
* `downloader`
* `uploader`

Pola `peers.Manager + updates.Manager + UpdateHook` yang kamu gunakan memang sejalan dengan pola resmi gotd. ([GotD][1])

### Tetapi:

**pemanfaatannya belum maksimal.**

Terutama:

```text
❌ peer storage belum durable
❌ terlalu banyak manual InputPeer construction
❌ entity lifecycle belum dijadikan first-class concern
❌ error normalization tidak konsisten
❌ update event abstraction masih tipis
❌ album/edit/delete/update events belum digarap
```

Jadi bukan:

> "gotd implementation salah."

Lebih tepat:

> **"gotd implementation-nya benar, tetapi GoUltroid masih belum memanfaatkan gotd sebagai Telegram abstraction layer secara penuh."**

---

## Kesimpulan paling penting

Saya justru sekarang melihat **arah project-nya sudah benar**.

Masalah terbesar bukan rewrite architecture.

**Jangan rewrite.**

Pertahankan:

```text
gotd
 ↓
updates.Manager
 ↓
Dispatcher
 ↓
CommandExecutor
 ↓
Context
 ↓
Plugins
```

Lalu perkuat boundary:

```text
             Telegram Gateway
                    │
       ┌────────────┴────────────┐
       ▼                         ▼
 Persistent Peer Store      Update Normalizer
       │                         │
       └────────────┬────────────┘
                    ▼
               Core Context
                    │
             CommandExecutor
                    │
             ┌──────┴──────┐
             ▼             ▼
          Plugins       Scheduler
```

Kalau saya harus memilih **tiga pekerjaan paling penting setelah audit ini**, saya pilih:

> **1. Persistent peer/access-hash subsystem**
> **2. Unified executor + principal resolver**
> **3. Telegram service/error/peer normalization**

Baru setelah itu lanjut ke scheduler claim-token dan feature expansion.

Karena setelah tiga boundary tersebut benar, hampir semua plugin akan otomatis menjadi **lebih reliable**, dan implementasi gotd kamu tidak lagi hanya "bisa connect Telegram", tetapi benar-benar menjadi **Telegram runtime layer yang proper untuk userbot**. ([GotD][1])

[1]: https://gotd.dev/docs/helpers/peers/?utm_source=chatgpt.com "Peers and resolution | gotd"
[2]: https://gitextract.com/iyear/tdl?utm_source=chatgpt.com "Full Code of iyear/tdl for AI - Complete Repository Source | GitExtract"
[3]: https://pkg.go.dev/github.com/gotd/td/telegram/peers?utm_source=chatgpt.com "peers package - github.com/gotd/td/telegram/peers - Go Packages"
[4]: https://github.com/gotd/td/issues?utm_source=chatgpt.com "Issues · gotd/td · GitHub"
[5]: https://pkg.go.dev/github.com/gotd/td/telegram?utm_source=chatgpt.com "telegram package - github.com/gotd/td/telegram - Go Packages"
