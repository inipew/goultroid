Saya sudah **fetch ulang repo `inipew/goultroid` dari `main`** dan audit khusus area **logging**, baik:

1. **application/runtime logging** — log internal Go/Zap
2. **UserLog** — fitur `.setlog` / `.log`
3. **event logging** — mention/tag, PM, admin action
4. **error logging & observability**
5. **queue/asynchronous logging**
6. **shutdown/reliability**
7. **keamanan & privacy**
8. **perbandingan perilaku dengan Ultroid**

Repo saat ini memang sudah mempunyai fondasi logging yang cukup baik: aplikasi menggunakan `zap`, konfigurasi `LOG_LEVEL` tersedia, dan ada service `userlog` khusus yang dipasang ke dispatcher.

## Kesimpulan utama

**Logging internal:** sekitar **7.5/10**
**UserLog feature:** sekitar **5.5/10**
**Reliability:** **6/10**
**Security/privacy:** **6/10**
**Ultroid parity:** **4.5–5/10**

Fondasinya bagus, tetapi **UserLog saat ini belum bisa dianggap parity dengan Ultroid**.

Masalah paling serius yang saya temukan adalah **cara menyimpan/resolve ID destination log untuk channel/supergroup**. Selain itu, Goultroid masih terlalu sedikit dalam jenis event yang dicatat dibanding Ultroid.

---

# 1. Arsitektur logging saat ini

Sekarang ada dua sistem yang sebenarnya berbeda.

### A. Application logger

Goultroid menggunakan:

```go
go.uber.org/zap
```

dan logger dibuat ketika `app.New()`:

```go
zapCfg := zap.NewDevelopmentConfig()
zapCfg.Level = zap.NewAtomicLevelAt(zapLevel)
logger, err := zapCfg.Build()
```

Level yang tersedia:

* `debug`
* `info`
* `warn`
* `error`

default:

```text
info
```

Ini sudah benar sebagai fondasi application logging.

Konfigurasinya juga sudah tersedia melalui:

```text
LOG_LEVEL
```

dan default-nya `info`.

### B. UserLog

Ini berbeda.

Ada:

```text
internal/services/userlog
plugins/userlog
```

Service-nya menyimpan konfigurasi:

```text
log_chat_id
log_tags_enabled
log_pms_enabled
```

dan plugin menyediakan:

```text
.setlog
.setlogchat
.log
.logstatus
```

Jadi secara arsitektur pemisahan ini **bagus**:

```text
Application Logger
        │
        ├── runtime diagnostics
        ├── errors
        ├── scheduler
        ├── callback
        └── internal services

UserLog
        │
        ├── mentions
        ├── PM
        └── admin actions
```

Saya **tidak menyarankan menggabungkan keduanya**.

---

# 2. Masalah paling serius: destination channel salah

Ini yang paling perlu diperbaiki.

Saat `.setlog` dijalankan:

```go
case *tg.InputPeerChannel:
    chatID = -peer.ChannelID
```

Kemudian ketika mengirim:

```go
if chatID < 0 {
    peer = &tg.InputPeerChannel{
        ChannelID: -chatID,
    }
}
```

Ini bermasalah karena **Telegram peer ID dan Telegram channel ID bukan hal yang sama**.

Contoh konseptual:

```text
Peer ID:
-1001234567890

Channel ID:
1234567890
```

Kode sekarang menyimpan:

```text
-1001234567890
```

kemudian membuat:

```text
ChannelID = 1001234567890
```

Padahal yang dibutuhkan adalah:

```text
ChannelID = 1234567890
```

Akibatnya logging ke supergroup/channel dapat gagal.

### Lebih buruk lagi

`InputPeerChannel` secara MTProto juga membutuhkan **access hash** dalam banyak konteks.

Jadi desain:

```go
log_chat_id = int64
```

saja terlalu miskin.

---

# 3. Solusi yang benar

Jangan simpan destination sebagai `int64` saja.

Buat model seperti:

```go
type LogDestination struct {
    PeerType   LogPeerType
    PeerID     int64
    AccessHash *int64
}
```

atau lebih baik, simpan **Telegram peer identity yang sudah dinormalisasi**.

Contoh:

```text
log_destination
├── type = channel
├── id = 1234567890
└── access_hash = ...
```

Untuk basic group:

```text
type = chat
id = 12345
```

Untuk user:

```text
type = user
id = ...
access_hash = ...
```

Walaupun `.setlog` memang membatasi group/channel, internal representation tetap sebaiknya typed.

---

# 4. Jangan menggunakan tanda negatif sebagai type encoding

Saat ini:

```text
positive -> chat
negative -> channel
```

Ini fragile.

Telegram memiliki beberapa bentuk peer:

```text
PeerUser
PeerChat
PeerChannel
```

dan channel/supergroup punya ID semantics sendiri.

Lebih aman:

```go
type LogDestination struct {
    Kind LogDestinationKind
    ID   int64
}
```

dengan:

```go
const (
    DestinationChat DestinationKind = iota
    DestinationChannel
)
```

Kemudian resolver yang bertanggung jawab membuat:

```go
tg.InputPeerChat
```

atau:

```go
tg.InputPeerChannel
```

---

# 5. Access hash harus diperhatikan

Ini bahkan lebih penting untuk reliability.

Saat `.setlog` dilakukan, context sebenarnya mempunyai informasi peer yang lebih lengkap.

Daripada:

```go
chatID := ...
SetLogChat(chatID)
```

lebih baik:

```go
SetLogDestination(ctx, peer)
```

kemudian service menyimpan informasi yang diperlukan.

Dengan demikian:

```text
.setlog
   ↓
Context.PeerID
   ↓
normalize peer
   ↓
resolve entity/access hash
   ↓
persist destination
```

bukan:

```text
.setlog
   ↓
negative int64
   ↓
guess peer type
```

---

# 6. Error logging saat mengirim log terlalu lemah

Ini juga cukup serius.

`sendToLogChat()`:

```go
chatID, err := s.GetLogChat(ctx)
if err != nil || chatID == 0 {
    return nil
}
```

Artinya:

### Database error

Kalau database gagal:

```text
GetLogChat()
    ↓
error
    ↓
return nil
```

Logging dianggap sukses.

Ini buruk.

Karena:

```text
database error
```

bukan berarti:

```text
logging disabled
```

Seharusnya:

```go
if err != nil {
    s.logger.Error("failed to read log destination", zap.Error(err))
    return err
}
```

Sedangkan:

```go
chatID == 0
```

baru berarti:

```text
logging not configured
```

---

# 7. `logger` pada UserLog service praktis tidak digunakan

Service memiliki:

```go
logger *zap.Logger
```

dan bahkan `NewService()` menerima logger.

Tetapi error yang terjadi ketika `SendMessage()` gagal hanya:

```go
return err
```

Tidak ada:

```go
logger.Error(...)
```

Jadi application logger tidak mengetahui:

```text
UserLog delivery failed
```

Padahal ini salah satu event yang sangat penting untuk observability.

Seharusnya misalnya:

```text
ERROR userlog delivery failed
  destination=...
  category=mention
  error=...
```

---

# 8. `LogMention()` sebenarnya bukan logging message Telegram yang sebenarnya

Sekarang hasilnya diformat ulang:

```text
🔔 Tag / Mention Alert

• Chat: ...
• From: ...
• Message:
...
```

Ini cukup untuk basic notification.

Tetapi dibanding Ultroid, kehilangan banyak context.

Ultroid mencoba meneruskan **message asli**, termasuk:

* media
* reply
* sender
* chat link
* sender link
* message link
* buttons
* metadata
* edit tracking

Ultroid bahkan mempunyai fallback ketika `send_message()` gagal karena `MediaEmptyError`, kemudian mencoba mengambil message kembali dan mengirim media secara manual.

Goultroid sekarang hanya:

```text
message text
```

Jadi **feature parity belum dekat**.

---

# 9. Media mention belum ditangani

Misalnya seseorang mention userbot dengan:

```text
[photo]
caption: @username lihat ini
```

Goultroid mengambil:

```go
msg.Message
```

dan mengirim:

```text
Message:
caption
```

Media-nya tidak ikut.

Ultroid mencoba mengirim message asli terlebih dahulu dan mempunyai fallback media download ketika diperlukan.

### Target yang lebih benar

UserLog harus mendukung:

```text
text
photo
video
document
audio
voice
sticker
gif
album
reply
entities
caption
```

dan sebisa mungkin **preserve original message**.

---

# 10. Mention detection masih kurang lengkap

Saat ini:

```go
for _, ent := range msg.Entities {
    if m, ok := ent.(*tg.MessageEntityMentionName); ok && m.UserID == p.ownerID {
        isMentioned = true
    }
}
```

Ini hanya menangani:

```text
MessageEntityMentionName
```

Tetapi mention biasa:

```text
@username
```

direpresentasikan sebagai entity mention.

Ultroid sendiri menggunakan:

```python
event.mentioned
```

dan kemudian melakukan filtering sender.

Jadi Goultroid berpotensi **tidak mencatat mention biasa ke username**.

Ini P1/P0 tergantung tujuan UserLog.

Seharusnya:

```text
MessageEntityMention
        +
MessageEntityMentionName
```

dan untuk `@username`, dibandingkan dengan username akun sendiri secara case-insensitive.

---

# 11. Bot/verified user filtering belum ada

Ultroid secara eksplisit:

```python
if isinstance(x, User) and (x.bot or x.verified):
    return
```

Goultroid tidak melakukan filtering tersebut.

Akibatnya bot bisa menghasilkan:

```text
mention log
PM log
```

yang mungkin tidak diinginkan.

Ini bisa menyebabkan spam pada log channel.

Sebaiknya ada policy:

```text
IgnoreBots
IgnoreVerified
IgnoreSelf
IgnoreLogDestination
```

yang configurable.

---

# 12. Self/log-channel exclusion

Ultroid secara eksplisit:

```python
if e.chat_id == NEEDTOLOG:
    return
```

Goultroid tidak mempunyai check yang ekuivalen sebelum mengirim UserLog.

Ini bisa menjadi masalah jika:

```text
log channel
   ↓
message
   ↓
dispatcher
   ↓
userlog
   ↓
send to log channel
```

Memang `msg.Out` akan mencegah message yang dikirim oleh userbot sendiri, jadi ada mitigasi:

```go
if msg.Out {
    return nil
}
```

Tetapi jangan bergantung hanya pada `Out`.

Lebih baik:

```go
if IsLogDestination(msg.PeerID) {
    return nil
}
```

---

# 13. PM logging terlalu sederhana

Sekarang:

```text
📩 New Private Message

• Sender: ...
• Message:
...
```

Masalahnya:

* tidak ada message ID
* tidak ada timestamp
* tidak ada message link bila tersedia
* tidak ada reply context
* tidak ada media
* tidak ada entities
* tidak ada username
* tidak ada account/client identity
* tidak ada edit tracking

Untuk userbot logging, ini terlalu minimal.

---

# 14. Admin Action logging belum benar-benar terintegrasi

Ada:

```go
LogAction(...)
```

Tetapi dari struktur yang saya audit, `LogAction()` masih terlihat sebagai **API service**, bukan sistem audit event yang otomatis terhubung ke seluruh moderation operation.

Artinya idealnya:

```text
.ban
 ↓
moderation
 ↓
Telegram API
 ↓
success
 ↓
UserLog.LogAction(...)
```

dan bukan:

```text
plugin lain harus ingat memanggil LogAction()
```

Kalau tidak, log akan tidak lengkap.

---

# 15. LogAction juga kekurangan informasi

Sekarang:

```text
Action
Target
Reason
```

Minimal production-grade audit sebaiknya:

```text
Action
Actor
Target
Chat
Reason
Timestamp
Message ID
Command
Success/Failure
Duration
```

Misalnya:

```text
⚖️ ADMIN ACTION

Action: BAN
Actor: @admin (123)
Target: @user (456)
Chat: My Group
Reason: spam
Result: SUCCESS
Time: 2026-09-06 11:20:31
```

---

# 16. Tidak ada success/failure audit

Ini penting.

Sekarang:

```go
LogAction(...)
```

tidak membawa hasil operasi.

Padahal:

```text
BAN requested
```

berbeda dengan:

```text
BAN succeeded
```

dan:

```text
BAN failed
```

Audit log harus mencatat **hasil aktual**, bukan sekadar intent.

Saya sarankan:

```go
type ActionResult string

const (
    ActionSuccess ActionResult = "success"
    ActionFailure ActionResult = "failure"
)
```

---

# 17. Async queue: idenya bagus

Goultroid memakai:

```go
const asyncLogWorkers = 32
```

dan worker pool.

Ini keputusan yang bagus.

Logging tidak boleh membuat command utama:

```text
user message
 ↓
send log Telegram
 ↓
wait
 ↓
command selesai
```

Karena Telegram API lambat/error/rate-limit.

Model sekarang:

```text
Incoming event
      │
      ▼
enqueue
      │
      ├──── command selesai
      │
      ▼
 worker
      │
      ▼
 Telegram log
```

**Saya pertahankan desain ini.**

---

# 18. Tetapi queue sekarang terlalu kecil

Ada:

```go
queue: make(chan func(), asyncLogWorkers)
```

Dengan:

```text
32 workers
32 queue capacity
```

Jadi total buffering sangat kecil.

Pada burst:

```text
100 mentions
```

bisa terjadi:

```text
32 running
32 queued
36 dropped
```

Dan drop ini silent.

Kode:

```go
default:
    // Logging is observational and must never become command backpressure.
```

Prinsipnya benar, tetapi observability-nya kurang.

Minimal:

```text
userlog.queue.dropped++
```

dan:

```text
WARN userlog queue overloaded
```

dengan rate limit agar tidak spam.

---

# 19. Dropped log harus observable

Karena UserLog memang **best effort**, drop boleh terjadi.

Tetapi harus diketahui.

Minimal metrics:

```text
userlog_enqueued_total
userlog_delivered_total
userlog_failed_total
userlog_dropped_total
userlog_queue_depth
userlog_delivery_latency
```

Ini akan membuat debugging jauh lebih mudah.

---

# 20. Tidak ada retry delivery

Sekarang:

```text
SendMessage()
   ↓
error
   ↓
done
```

Tidak ada retry.

Padahal error transient seperti:

```text
timeout
connection reset
flood wait
temporary Telegram RPC error
```

bisa pulih.

Saya sarankan:

```text
attempt 1
   ↓
100ms
   ↓
attempt 2
   ↓
500ms
   ↓
attempt 3
   ↓
drop + metric + application log
```

Tetapi **jangan retry permanent error** seperti:

```text
ChatWriteForbidden
ChannelPrivate
PeerIdInvalid
```

---

# 21. Queue job menggunakan `p.ctx`, bukan event context

Contoh:

```go
name, id, text, logCtx := senderName, senderID, msg.Message, p.ctx
p.enqueue(func() {
    _ = p.svc.LogPM(logCtx, name, id, text)
})
```

Ini sengaja dilakukan agar context event tidak mati sebelum worker selesai, dan secara prinsip masuk akal.

Tetapi:

```go
p.ctx
```

adalah background context yang tidak mempunyai deadline per delivery.

Jadi worker bisa menunggu Telegram RPC terlalu lama.

Lebih baik:

```text
plugin context
    +
per-job timeout
```

misalnya:

```go
jobCtx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
```

---

# 22. Shutdown UserLog sudah jauh lebih baik

Ini bagian yang saya nilai positif.

Plugin sudah:

```go
ContextShutdowner
```

dan:

```go
ShutdownContext()
```

menutup queue lalu menunggu worker drain.

Plugin manager juga melakukan shutdown reverse-order.

Ini sudah cukup bagus.

Namun ada satu catatan:

comment menyebut:

> dispatcher is stopped before plugin shutdown

tetapi `App.Shutdown()` yang terlihat sekarang **tidak secara eksplisit memanggil dispatcher shutdown sebelum plugin shutdown**. Ia langsung:

```text
scheduler
↓
plugins
↓
event bus
↓
limiter
↓
database
↓
logger
```

Jadi asumsi lifecycle tersebut harus diverifikasi/diperbaiki.

---

# 23. Race condition potensial saat queue ditutup

`enqueue()`:

```go
select {
case p.queue <- job:
default:
}
```

sedangkan shutdown:

```go
close(p.queue)
```

Jika ada goroutine yang masih memanggil `enqueue()` bersamaan dengan shutdown, bisa terjadi:

```text
send on closed channel
```

Comment mengasumsikan dispatcher sudah berhenti, tetapi lifecycle harus benar-benar menjamin itu.

Solusi production-grade:

```text
dispatcher stop
    ↓
wait for handlers
    ↓
stop accepting UserLog
    ↓
close queue
    ↓
drain workers
```

atau gunakan state:

```go
atomic.Bool accepting
```

sebelum close.

---

# 24. Unicode truncation

Sekarang:

```go
if len(snippet) > 200 {
    snippet = snippet[:200] + "..."
}
```

`len()` adalah **byte length**, bukan Unicode rune count.

Untuk UTF-8 bisa memotong karakter di tengah.

Contoh:

```text
😀😀😀...
```

bisa menghasilkan invalid UTF-8.

Gunakan:

```go
utf8.RuneCountInString()
```

atau lebih baik truncate berdasarkan Telegram text semantics.

---

# 25. HTML escaping sudah benar

Ini justru bagus.

Kode menggunakan:

```go
core.EscapeHTML(...)
```

untuk:

* chat title
* sender
* message
* action
* reason

Ini penting karena log menggunakan:

```html
<b>
<i>
<code>
```

dan data Telegram adalah untrusted input.

**Pertahankan.**

---

# 26. Tetapi error text jangan langsung diteruskan ke user

Contoh:

```go
return ctx.EditOrReply(fmt.Sprintf(
    "❌ Failed to set log chat: %v",
    err,
))
```

Ini berpotensi membocorkan detail internal.

Lebih baik:

```text
❌ Failed to configure log destination.
Check application logs for details.
```

Application logger:

```text
ERROR userlog configuration failed
error=...
```

---

# 27. `.log` command terlalu basic

Sekarang:

```text
.log
.log tags on
.log tags off
.log pms on
.log pms off
```

Untuk MVP oke.

Tapi target parity sebaiknya:

```text
.log
.log status
.log set
.log test
.log disable
.log tags on/off
.log pms on/off
.log actions on/off
.log joins on/off
.log edits on/off
.log media on/off
.log clear
```

Dan idealnya menggunakan inline buttons.

---

# 28. `.setlog` membutuhkan verification

Sekarang:

```text
.setlog
```

langsung menyimpan destination.

Seharusnya:

```text
.setlog
   ↓
validate peer
   ↓
check can send
   ↓
send test message
   ↓
success
   ↓
persist destination
```

Ini jauh lebih aman.

Kalau bot/userbot tidak punya permission:

```text
ChatWriteForbidden
```

lebih baik `.setlog` langsung gagal daripada konfigurasi tersimpan tetapi semua log berikutnya gagal.

Ultroid sendiri mempunyai handling khusus untuk kondisi `ChatWriteForbiddenError`, `PeerIdInvalidError`, dan membership issues.

---

# 29. Tidak ada `.log test`

Ini sangat dibutuhkan.

Contoh:

```text
.log test
```

menghasilkan:

```text
✅ UserLog test successful

Destination:
My Log Channel

Tags: enabled
PMs: enabled
```

Ini langsung menjawab:

> "Apakah log saya benar-benar bekerja?"

Tanpa harus menunggu seseorang mention userbot.

---

# 30. Tidak ada delivery health state

Saya sarankan UserLog mempunyai state:

```text
UNCONFIGURED
HEALTHY
DEGRADED
FAILED
```

Contoh:

```text
HEALTHY
  last_success: 11:20:31
  failures: 0
```

atau:

```text
DEGRADED
  last_success: 11:18:21
  consecutive_failures: 4
```

Kemudian `.log` dapat menampilkan health.

---

# 31. Tidak ada edit tracking

Ini salah satu gap besar terhadap Ultroid.

Ultroid menyimpan:

```python
TAG_EDITS
```

dan ketika message yang sudah dilog diedit:

```text
original
   ↓
edited
   ↓
update log message
```

bahkan membatasi jumlah edit yang dicatat.

Goultroid tidak mempunyai equivalent.

Untuk parity:

```text
mention detected
    ↓
send log
    ↓
save mapping
    ↓
original chat/message → log message
```

kemudian:

```text
MessageEdited
    ↓
lookup mapping
    ↓
update log
```

Saya sarankan persistent mapping, bukan hanya memory.

---

# 32. Tidak ada reply bridge

Ultroid mempunyai:

```python
who_tag(...)
```

dan reply dari log channel dapat diteruskan kembali ke message original.

Goultroid belum mempunyai konsep:

```text
Log message
     ↓ reply
original message
```

Ini fitur UX yang sangat bagus.

Target:

```text
UserLog message
       ↑
       │ reply
       ↓
Original chat/message
```

---

# 33. Tidak ada join/add log

Ultroid mencatat:

```text
#ADD_LOG
#APPROVAL_LOG
#JOIN_LOG
```

dan memberikan button untuk meninggalkan chat.

Goultroid saat ini tidak memiliki equivalent di UserLog.

Ini gap feature yang cukup besar.

---

# 34. Tidak ada log channel recovery/error notification

Ultroid ketika destination invalid/forbidden memiliki mekanisme:

```text
destination invalid
      ↓
notify fallback LOG_CHANNEL
```

dan cache supaya tidak spam.

Goultroid hanya:

```text
SendMessage error
    ↓
return error
```

dan plugin async bahkan membuang error:

```go
_ = p.svc.LogPM(...)
```

atau:

```go
_ = p.svc.LogMention(...)
```

Ini berarti kalau logging rusak, operator bisa **tidak tahu sama sekali**.

---

# 35. Application logging juga perlu dibenahi

Zap sudah benar, tetapi saat ini saya melihat pola yang cukup sederhana:

```text
logger.Info(...)
logger.Warn(...)
logger.Error(...)
```

Belum terlihat sebagai centralized structured logging policy.

Target seharusnya setiap subsystem memiliki fields konsisten:

```text
component
operation
event
user_id
chat_id
message_id
duration
error
```

Misalnya:

```text
ERROR
component=userlog
operation=send
category=mention
chat_id=-100...
sender_id=...
duration_ms=...
error=...
```

---

# 36. Jangan log message content ke application log

Ini penting.

Application logger seharusnya **tidak** melakukan:

```text
logger.Info("PM received",
    zap.String("message", msg.Message),
)
```

Karena application logs bisa:

* masuk journal
* file
* Docker logs
* monitoring
* CI
* centralized logging

Sedangkan PM/tag content bisa private.

UserLog Telegram adalah tempat yang memang sengaja dikonfigurasi untuk menerima konten.

Jadi:

```text
Application log:
metadata only

UserLog:
message content
```

Ini separation yang saya rekomendasikan.

---

# 37. Privacy model belum jelas

UserLog menyimpan/mengirim:

```text
private message content
```

Ini data sensitif.

Harus ada dokumentasi jelas:

```text
PM logging is disabled/enabled
```

dan default policy sebaiknya dipikirkan kembali.

Sekarang:

```go
if val == "" {
    return true
}
```

artinya feature default **enabled**.

Untuk PM logging, saya justru lebih menyarankan:

```text
PM logging default = OFF
```

sedangkan mention logging bisa:

```text
default = ON
```

karena mention biasanya memang tujuan utama UserLog.

---

# 38. `IsFeatureEnabled()` error diabaikan

Misalnya:

```go
enabled, _ := s.IsFeatureEnabled(...)
```

Kalau database error:

```text
enabled = false
```

dan error dibuang.

Ini membuat logging gagal secara diam-diam.

Seharusnya:

```go
enabled, err := ...
if err != nil {
    logger.Error(...)
    return err
}
```

---

# 39. `sync.RWMutex` service tidak digunakan

`Service` mempunyai:

```go
mu sync.RWMutex
```

tetapi method yang terlihat tidak menggunakan mutex tersebut.

Ini indikasi desain yang belum selesai.

Jika DB menjadi source of truth, mutex mungkin tidak diperlukan.

Kalau ada cache:

```go
destinationCache
```

baru mutex/atomic dibutuhkan.

Jadi saya sarankan **hapus mutex** kalau memang tidak diperlukan.

---

# 40. Target arsitektur logging yang saya sarankan

Untuk Goultroid saya tidak akan membuatnya seperti Ultroid secara mentah.

Saya akan membuat Go-native architecture:

```text
                 ┌─────────────────────┐
                 │    Telegram Events   │
                 └──────────┬──────────┘
                            │
                            ▼
                 ┌─────────────────────┐
                 │   Event Classifier   │
                 └──────────┬──────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
           Mention          PM          Action
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                 ┌─────────────────────┐
                 │   UserLog Pipeline  │
                 └──────────┬──────────┘
                            │
                       bounded queue
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Delivery Worker(s)  │
                 └──────────┬──────────┘
                            │
                            ▼
                  Telegram Log Chat
```

Sementara application logging terpisah:

```text
Services
   │
   ▼
zap.Logger
   │
   ├── stdout/stderr
   └── structured runtime logs
```

---

# 41. Model event yang lebih baik

Daripada:

```go
LogMention(...)
LogPM(...)
LogAction(...)
```

buat typed event:

```go
type UserLogEvent struct {
    Type      EventType
    Timestamp time.Time

    Actor     PeerRef
    Target    PeerRef
    Chat      PeerRef

    Message   *MessageRef
    Action    string
    Reason    string

    Result    ActionResult
}
```

Contoh:

```go
type UserLogEventType string

const (
    EventMention       = "mention"
    EventPM            = "pm"
    EventEditedMention = "edited_mention"
    EventJoin          = "join"
    EventAdd           = "add"
    EventApproval      = "approval"
    EventAdminAction   = "admin_action"
)
```

Dengan ini fitur baru jauh lebih mudah.

---

# 42. Recommendation priority

### P0 — wajib

1. **Fix channel/supergroup peer resolution**
2. Jangan encode peer type menggunakan negative ID
3. Persist peer/access-hash information
4. Jangan swallow DB errors
5. Jangan swallow delivery failures
6. Perbaiki mention detection `@username`
7. Pastikan shutdown queue tidak race
8. Tambahkan UserLog delivery metrics
9. Tambahkan `.log test`
10. Validasi destination saat `.setlog`

### P1

11. Retry transient Telegram errors
12. Message/media forwarding
13. Reply context
14. message links
15. edit tracking
16. join/add/approval logs
17. action success/failure
18. bot/self/log-channel filtering
19. PM logging privacy policy
20. structured UserLog events

### P2

21. reply bridge
22. inline buttons
23. log health dashboard/status
24. configurable event categories
25. persistent mention→log mapping
26. album handling
27. log delivery fallback
28. rate-limited failure notifications

---

# 43. Dibanding Ultroid

| Capability                  | Goultroid sekarang |                             Ultroid |
| --------------------------- | -----------------: | ----------------------------------: |
| Dedicated log destination   |                  ✅ |                                   ✅ |
| Mention logging             |                 ⚠️ |                                   ✅ |
| `@username` mention         |                 ⚠️ |                                   ✅ |
| PM logging                  |                  ✅ |                       lebih lengkap |
| Media forwarding            |                  ❌ |                                   ✅ |
| Message entities            |                 ⚠️ |                                   ✅ |
| Message links               |                  ❌ |                                   ✅ |
| Sender links                |                  ❌ |                                   ✅ |
| Reply context               |                  ❌ |                                   ✅ |
| Edit tracking               |                  ❌ |                                   ✅ |
| Join logging                |                  ❌ |                                   ✅ |
| Add logging                 |                  ❌ |                                   ✅ |
| Approval logging            |                  ❌ |                                   ✅ |
| Admin action logging        |                 ⚠️ |                          lebih luas |
| Reply bridge                |                  ❌ |                                   ✅ |
| Destination validation      |                  ❌ |                        lebih matang |
| Error recovery              |                 ⚠️ |                                   ✅ |
| Async delivery              |                  ✅ |                   async event-based |
| Bounded queue               |                  ✅ |                framework/event loop |
| Retry                       |                  ❌ | sebagian melalui exception handling |
| Delivery metrics            |                  ❌ |                       logging-based |
| Structured runtime logger   |              ✅ Zap |                              `LOGS` |
| Configurable log categories |                 ⚠️ |                          lebih luas |
| Privacy controls            |                 ⚠️ |                        configurable |

Ultroid sendiri menangani media fallback, invalid peer, write permission, edited mentions, join/add events, callback leave-chat, reply bridge, dan metadata message yang jauh lebih lengkap.

---

# 44. Penilaian akhir

Menurut saya **jangan rewrite UserLog**. Struktur dasarnya sudah benar:

```text
Plugin
  ↓
Service
  ↓
DB
  ↓
async worker
  ↓
Telegram
```

Tetapi implementasinya masih terlalu **MVP notification logger**, belum **production-grade event/audit logging system**.

Yang paling saya khawatirkan sekarang bukan jumlah fitur, melainkan:

> **destination peer representation + error handling + observability + lifecycle**

Khusus bug channel ID, itu saya anggap **P0** karena dapat membuat `.setlog` terlihat berhasil tetapi pengiriman log ke channel/supergroup sebenarnya gagal.

Repo saat ini memang sudah memasang `userlog.Service` dan plugin ke application lifecycle, serta sudah mempunyai graceful plugin shutdown; jadi fondasi infrastrukturnya tidak perlu dibongkar.

**Rekomendasi saya berikutnya:** buat **`docs/4.9_userlog_logging_audit_2026-09-06.md`** yang berisi audit ini secara formal + desain target + state machine + data model + daftar file/function yang harus diubah + urutan implementasi P0/P1/P2 + test matrix, lalu setelah itu baru implementasi langsung ke kode.
