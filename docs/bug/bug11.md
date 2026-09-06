Ya. Saya baca ulang **state `main` terbaru** dan kali ini saya fokuskan audit dari **AFK sebagai sebuah subsystem**, lalu saya trace fitur-fitur yang bersinggungan dengannya: dispatcher/hook, PMPermit, filters, blacklist, peer/entity resolution, Telegram service, database, command execution, dan lifecycle.

Repo saat ini tree `main` berada pada SHA `80ba11...`, dan plugin AFK terakhir dimodifikasi 6 September 2026.

## Verdict utama

**AFK saat ini sudah berfungsi secara dasar, tetapi belum production-grade/flawless.**

Saya beri:

| Area                             |      Nilai |
| -------------------------------- | ---------: |
| Basic `.afk`                     |       8/10 |
| Persistent state                 |       8/10 |
| Auto-reply DM                    |     7.5/10 |
| Group reply detection            |       6/10 |
| Auto-unAFK                       |       6/10 |
| Anti-spam                        |       6/10 |
| Peer handling                    |     5.5/10 |
| Error handling                   |       5/10 |
| Performance                      |       5/10 |
| Concurrency                      |       5/10 |
| Integration dengan hook pipeline |       6/10 |
| **AFK overall**                  | **6.2/10** |

Dan ada **beberapa P0/P1 yang menurut saya harus diperbaiki sebelum AFK dianggap selesai.**

---

# 1. Apa yang AFK sekarang sebenarnya lakukan?

Implementasi sekarang mempunyai satu command:

```text
.afk [reason]
```

dan state disimpan:

```text
user_id
is_afk
reason
since
```

Database repository memang menyediakan:

```go
SetAFK(...)
GetAFK(...)
```

dan state AFK persistent di SQLite.

Flow-nya:

```text
.afk reason
      ↓
DB.SetAFK(owner, true, reason)
      ↓
AFK active

incoming message
      ↓
AFK hook
      ↓
GetAFK(owner)
      ↓
DM / reply / mention?
      ↓
SendMessage()

owner sends message
      ↓
GetAFK(owner)
      ↓
SetAFK(false)
      ↓
Welcome back
```

Secara konsep ini benar.

---

# 2. 🔴 P0: `.afk` tidak punya explicit OFF/toggle

Ini menurut saya kekurangan fungsional terbesar.

Command hanya:

```go
Name: "afk"
Usage: ".afk [reason]"
```

dan handler selalu:

```go
SetAFK(..., true, reason)
```

Artinya:

```text
.afk
```

→ ON

```text
.afk vacation
```

→ ON

tetapi tidak ada:

```text
.afk off
.afk disable
.afk stop
.afk toggle
```

Untuk mematikan, user harus mengirim **pesan outgoing lain**, yang kemudian auto-unAFK.

Ini bukan UX yang ideal.

### Seharusnya

```text
.afk
    → toggle

.afk on [reason]
    → enable

.afk off
    → disable

.afk status
    → status
```

Atau minimal:

```text
.afk [reason]
.afk off
```

Saya sangat merekomendasikan toggle semantics.

---

# 3. 🔴 P0: AFK melakukan DB query pada setiap incoming message

Ini jauh lebih serius dari sekadar style.

Setiap incoming message:

```go
status, err := p.db.GetAFK(ctx, ownerID)
```

dan outgoing message juga:

```go
status, err := p.db.GetAFK(ctx, ownerID)
```

Jadi misalnya user berada di:

```text
20 groups
10 msg/sec aggregate
```

AFK aktif:

```text
10 DB SELECT/sec
```

Kalau traffic tinggi:

```text
100 msg/sec
→ 100 SQLite queries/sec
```

Dan sebelumnya kita sudah menemukan database menggunakan SQLite dengan single connection.

### Ini tidak scalable.

AFK state sangat cocok untuk memory cache.

Target:

```go
type AFKState struct {
    Enabled bool
    Reason  string
    Since   time.Time
}
```

Plugin menyimpan:

```text
atomic / mutex protected state
```

DB hanya menjadi **durable source of truth** ketika state berubah.

### Flow baru

```text
.afk on
 ↓
DB.SetAFK
 ↓
memory state = active

message
 ↓
memory state
 ↓
NO DB
```

Startup:

```text
DB.GetAFK
 ↓
load memory state
```

Shutdown:

```text
memory → DB
```

Dengan begitu AFK hot path hampir gratis.

---

# 4. 🔴 P0: auto-unAFK tidak atomic

Sekarang:

```text
GetAFK()
 ↓
if IsAFK
 ↓
SetAFK(false)
```

Dua outgoing message dapat terjadi bersamaan:

```text
Message A
GetAFK → true

Message B
GetAFK → true

A → SetAFK(false)
B → SetAFK(false)

A → send welcome
B → send welcome
```

Akibatnya bisa:

```text
☀️ Welcome back!
☀️ Welcome back!
```

### Harus atomic state transition

Database idealnya punya:

```sql
UPDATE afk_status
SET is_afk = 0
WHERE user_id = ?
  AND is_afk = 1
```

dan memeriksa `RowsAffected()`.

Hanya caller yang mendapatkan:

```text
RowsAffected = 1
```

yang berhak melakukan welcome-back.

Lebih bagus lagi gunakan in-memory CAS/state machine.

---

# 5. 🔴 P1: cooldown AFK sebenarnya bukan "per user/chat"

Komentarnya:

```go
cooldown sync.Map // map[int64]time.Time
```

dan key-nya:

```go
senderID
```

Artinya:

```text
User A → Group 1
User A → Group 2
```

akan memakai cooldown yang sama.

Misalnya:

```text
10:00 Group A → reply
10:01 Group B → NO reply
```

Padahal user tersebut mungkin sedang menanyakan sesuatu di chat berbeda.

Komentar sendiri mengatakan:

> rate limit auto-replies per user/chat

tetapi implementasinya **per sender global**.

### Seharusnya key:

```go
type AFKReplyKey struct {
    ChatID   int64
    SenderID int64
}
```

atau lebih sederhana:

```text
peerID + senderID
```

---

# 6. 🔴 P1: cooldown check tidak atomic

Sekarang:

```text
Load(senderID)
 ↓
check
 ↓
Store(senderID)
```

Dua goroutine bisa melakukan:

```text
G1 Load → empty
G2 Load → empty

G1 Store
G2 Store

G1 Send
G2 Send
```

Jadi cooldown **tidak benar-benar concurrency safe secara semantic**, walaupun `sync.Map` sendiri aman.

### Solusi

Gunakan mutex + check/set atomically:

```go
mu.Lock()

if last, ok := replies[key]; ok && now.Sub(last) < cooldown {
    mu.Unlock()
    return
}

replies[key] = now
mu.Unlock()

send()
```

Atau sharded lock kalau mau optimasi.

---

# 7. 🔴 P1: `Cleanup()` dipanggil hanya ketika owner mengirim pesan

Saat owner mengirim pesan:

```go
p.Cleanup(0)
```

Masalahnya jika owner tidak mengirim pesan selama berhari-hari:

```text
cooldown map
 ↓
stale entries
 ↓
never cleaned
```

Memang ini relatif kecil, tetapi desain yang benar adalah:

```text
ticker
  ↓
periodic cleanup
```

misalnya:

```text
5 menit
```

atau gunakan TTL cache.

---

# 8. 🔴 P1: cooldown hanya membatasi reply, bukan request mahal sebelum reply

Ini sangat penting.

Untuk group reply-to-owner, AFK melakukan:

```go
svc.GetMessage(...)
```

**sebelum** cooldown diperiksa.

Flow:

```text
incoming
 ↓
GetAFK
 ↓
reply detection
 ↓
GetMessage Telegram RPC
 ↓
shouldReply
 ↓
cooldown
```

Jadi spammer bisa menghasilkan:

```text
100 messages
→ 100 Telegram GetMessage RPC
```

meskipun akhirnya:

```text
only 1 reply
```

Ini buruk.

### Harus:

```text
incoming
 ↓
AFK active?
 ↓
sender/chat cooldown eligibility?
 ↓
cheap detection
 ↓
only then expensive GetMessage
 ↓
send
```

Dan cooldown harus reserve slot sebelum RPC mahal.

---

# 9. 🔴 P1: reply-to-owner detection terlalu mahal

Saat:

```text
message.ReplyTo
```

plugin melakukan:

```go
svc.GetMessage(ctx, peer, h.ReplyToMsgID)
```

Ini bisa menghasilkan Telegram RPC pada **setiap message yang reply**.

Padahal gotd update/entity/context mungkin sudah menyediakan cukup informasi pada beberapa event.

Minimal:

```text
check local reply metadata first
```

dan hanya fetch jika:

```text
owner identity unknown
```

Lebih baik lagi dispatcher menyimpan normalized reply metadata:

```go
type ReplyContext struct {
    MsgID       int
    SenderID    int64
    IsOutgoing  bool
    Resolved    bool
}
```

Sehingga AFK tidak perlu RPC sendiri.

---

# 10. 🔴 P1: mention detection tidak lengkap

Sekarang hanya:

```go
tg.MessageEntityMentionName
```

yang diperiksa:

```go
if m, ok := ent.(*tg.MessageEntityMentionName); ok
```

Tetapi Telegram memiliki beberapa cara mention.

Minimal perlu membedakan:

```text
@username
text_mention
```

`MessageEntityMentionName` adalah text mention berdasarkan user ID.

Sedangkan:

```text
@username
```

merupakan `MessageEntityMention`.

Jadi:

```text
@dhimas
```

belum tentu terdeteksi oleh kode sekarang.

### Solusi

Resolver/context harus menyediakan:

```go
IsMentioningUser(msg, ownerID, ownerUsername)
```

yang menangani:

```text
MessageEntityMentionName
MessageEntityMention
```

dan validasi UTF-16 offsets Telegram dengan benar.

Jangan sekadar `strings.Contains("@username")`, karena entity offset Telegram penting.

---

# 11. 🔴 P1: private chat detection terlalu sederhana

Sekarang:

```go
if _, isUser := msg.PeerID.(*tg.PeerUser); isUser {
    shouldReply = true
}
```

Artinya semua private message dianggap harus dibalas.

Tetapi tidak dibedakan:

```text
normal user
bot
saved messages/self
service account
```

Sender ID memang diambil dari `FromID`, tetapi tidak ada explicit:

```text
ignore bot
ignore self
ignore service messages
```

Filters sudah melakukan bot-loop prevention.

AFK juga perlu memiliki policy yang sama.

### Minimal

```text
if sender.IsBot → ignore
if senderID == ownerID → ignore
if service message → ignore
```

Kalau userbot menerima bot PM ketika AFK:

```text
bot → userbot
userbot → bot
```

bisa menghasilkan automation loop.

---

# 12. 🔴 P1: auto-unAFK mengirim pesan ke chat yang sedang dipakai

Ini semantik yang menurut saya kurang tepat.

Ketika owner mengirim:

```text
"hello"
```

di group:

```text
Group A
```

plugin:

```go
svc.SendMessage(ctx, peer, "Welcome back...")
```

Maka:

```text
owner sends message
       ↓
AFK OFF
       ↓
bot sends "Welcome back"
       ↓
Group sees bot-like announcement
```

Padahal user mungkin hanya ingin kembali aktif.

Ini bisa sangat mengganggu di group.

### Better semantics

Default:

```text
.afk
...
owner speaks
→ silently disable
```

Optional:

```text
.afk off
→ explicit confirmation
```

atau hanya welcome-back jika owner mengirim di private chat.

Config:

```text
AFKWelcomeBack:
    private: true
    group: false
```

Saya lebih menyukai ini.

---

# 13. 🔴 P1: command `.afk` sendiri bypass auto-unAFK dengan string command

Ini:

```go
if isCommand && strings.EqualFold(cmdName, "afk") {
    return nil
}
```

benar untuk `.afk`.

Tetapi karena command lifecycle dan outgoing update bisa berjalan asynchronous, ada kemungkinan:

```text
.afk reason
 ↓
command execution
 ↓
outgoing update
```

terjadi sebelum DB state selesai di-set atau hook melihat state yang berbeda.

Jadi semantics sebaiknya tidak berdasarkan:

```text
cmdName == afk
```

tetapi command execution context:

```go
ExecutionSource
CommandName
MessageOrigin
```

dan AFK command sendiri melakukan explicit state transition.

---

# 14. 🔴 P1: error handling terlalu silent

Ada banyak:

```go
_ = p.db.SetAFK(...)
```

dan:

```go
_, _ = svc.SendMessage(...)
```

Ini buruk untuk observability.

Misalnya:

```text
AFK active
owner sends message
 ↓
DB.SetAFK(false) fails
 ↓
error ignored
 ↓
plugin sends Welcome back
 ↓
DB masih AFK
```

Pesan berikutnya:

```text
Welcome back lagi
```

Karena state sebenarnya belum mati.

### Harus:

```text
transition failed
→ state remains AFK
→ log error
→ no false success message
```

Dan SendMessage failure harus masuk metric/log:

```text
afk.auto_reply.failed
afk.auto_unafk.failed
```

---

# 15. 🔴 P1: peer resolution AFK belum mengikuti central resolver

AFK memiliki fungsi sendiri:

```go
extractPeerInput(...)
```

yang membangun:

```go
&tg.InputPeerUser{
    UserID: ...
    AccessHash: ...
}
```

dan channel juga demikian.

Ini masih melanggar arsitektur yang sedang kita bangun.

Jika entity tidak ada:

```go
return nil
```

Memang lebih aman daripada `AccessHash: 0`, tetapi tetap membuat AFK gagal hanya karena `tg.Entities` tidak membawa entity.

Seharusnya:

```text
AFK
 ↓
PeerResolver
 ↓
validated InputPeer
```

sehingga AFK tidak peduli apakah access hash berasal dari:

```text
update entities
peer cache
DB
gotd peer manager
Telegram resolve
```

---

# 16. Database AFK sendiri cukup bagus, tetapi `SetAFK(false)` mengubah `Since`

Ini subtle.

`SetAFK()`:

```sql
INSERT ...
ON CONFLICT DO UPDATE SET
    is_afk = excluded.is_afk,
    reason = excluded.reason,
    since = excluded.since
```

dan `excluded.since = time.Now()` setiap kali.

Ketika OFF:

```text
is_afk = false
since = now
reason = ""
```

Secara fungsional tidak fatal.

Tetapi secara data model lebih bersih:

```text
AFK started_at
AFK ended_at
```

atau:

```text
is_afk
reason
since
last_ended_at
```

Kalau ingin history/analytics, current schema kehilangan:

```text
berapa lama AFK terakhir?
kapan terakhir AFK?
```

---

# 17. AFK belum punya history

Kalau target GoUltroid ingin mendekati bot/userbot matang, AFK bisa punya:

```text
current state
+
history
```

Tetapi **tidak harus** kalau parity target Ultroid tidak membutuhkan itu.

Saya justru menjadikannya P3, bukan prioritas.

---

# 18. AFK belum mendukung konfigurasi

Saat ini hardcoded:

```go
const afkCooldown = 60 * time.Second
```

Tidak ada:

```text
AFK_COOLDOWN
AFK_REPLY_PRIVATE
AFK_REPLY_GROUP
AFK_REPLY_MENTION
AFK_REPLY_TO
AFK_AUTO_UNAFK
AFK_WELCOME_BACK
```

Untuk production userbot, sebaiknya behavior configurable.

---

# 19. AFK + Filters: ada potensi double-response

Sekarang hook priority:

```text
PMPermit  = 10
Filters   = 20
AFK       = 50
```

Filters sudah berjalan sebelum AFK.

Misalnya:

```text
User:
"ping"
```

dan:

```text
filter "ping" → "pong"
AFK → "owner is AFK"
```

Maka:

```text
filter reply
+
AFK reply
```

Ini bisa menjadi spam/UX buruk.

### Harus ada policy

Misalnya:

```text
Security
 ↓
PMPermit
 ↓
AFK gate
 ↓
Filters
```

atau AFK menjadi terminal automation:

```text
AFK replied
→ stop lower-priority automation
```

Namun jangan sembarang menghentikan seluruh pipeline karena command/moderation bisa tetap perlu jalan.

Saya lebih suka:

```go
AutomationDecision{
    SuppressFilters: true
}
```

daripada hard `return`.

---

# 20. AFK + PMPermit: interaksi juga perlu ditentukan

PMPermit berada pada priority 10, AFK 50.

Artinya PMPermit memproses PM terlebih dahulu.

Ini benar secara security.

Tetapi ada kasus:

```text
unknown user
 ↓
PMPermit sends warning
 ↓
AFK sees PM
 ↓
AFK sends "owner is AFK"
```

Jadi user yang seharusnya ditahan PMPermit bisa mendapat **dua automated responses**.

PMPermit sudah punya konsep `handled`:

```go
handled, err := p.svc.HandleIncomingPM(...)
```

tetapi AFK harus menghormati result itu melalui shared execution context.

### Target

```text
PMPermit
    ↓
Decision:
  Allow
  Block
  Challenge
  Handle
    ↓
AFK
```

Jika:

```text
PMPermit = Block/Challenge
```

AFK **tidak boleh reply**.

Ini penting.

---

# 21. AFK + Blacklist

Blacklist priority kemungkinan juga berada di moderation stage.

Jika user masuk blacklist:

```text
blacklist
 ↓
delete
```

AFK seharusnya **tidak** menjawab.

Kalau pipeline hanya:

```text
hook A returns nil
hook B returns nil
hook C returns nil
```

maka AFK tidak tahu bahwa message sudah diblokir.

### Ini masalah arsitektur umum

Hooks perlu memiliki hasil:

```go
type HookResult struct {
    Handled           bool
    StopPropagation   bool
    SuppressAutomation bool
}
```

Sehingga:

```text
Blacklist:
    SuppressAutomation = true
```

dan AFK tidak membalas.

---

# 22. AFK + command handling

AFK saat ini:

```text
if isCommand
```

hanya digunakan untuk outgoing `.afk`.

Tetapi incoming command dari orang lain:

```text
someone → .ping
```

tidak akan masuk AFK auto-reply karena:

```text
dispatcher kemungkinan menandai isCommand
```

Namun AFK sendiri **tidak memeriksa `isCommand` untuk incoming**.

Jadi behavior sangat bergantung pada apakah hook dipanggil untuk command message dan bagaimana dispatcher mengurutkannya.

Kalau AFK menerima:

```text
incoming ".help"
```

di DM:

```text
shouldReply = true
```

dan bisa mengirim:

> My owner is currently AFK

Itu sebenarnya mungkin diinginkan.

Tetapi perlu diputuskan secara eksplisit:

```text
AFK replies to commands? yes/no
```

Saya pilih:

```text
DM command → yes
group command → only if owner mentioned/replied
```

---

# 23. AFK + bot messages

Ini saya anggap bug nyata.

Filters sudah:

```go
if senderUser.Bot {
    return nil
}
```

AFK tidak memiliki check semacam itu.

Jadi AFK harus menambahkan:

```text
sender is bot
→ ignore
```

Ini penting untuk mencegah automation loops.

---

# 24. AFK + Saved Messages

`PeerUser` juga dapat merepresentasikan self.

Jika owner mengirim ke:

```text
Saved Messages
```

itu outgoing message.

AFK akan:

```text
GetAFK
→ disable AFK
```

Padahal secara semantic:

> menulis sesuatu di Saved Messages

belum tentu berarti user ingin mengumumkan dirinya kembali.

Saya sarankan configurable:

```text
Saved Messages activity = does/doesn't clear AFK
```

Default saya:

**does clear AFK**, karena itu tetap aktivitas owner.

---

# 25. AFK + scheduled/broadcast/addon

Ini jauh lebih penting.

Kita sudah memiliki fitur:

```text
Scheduler
Broadcast
Addon
Assistant
Downloader
AI/automation
```

dan beberapa dapat menghasilkan **outgoing message**.

AFK saat ini hanya membedakan:

```go
msg.Out
```

Jadi:

```text
Scheduler sends message
 ↓
AFK sees msg.Out
 ↓
AFK OFF
```

Ini bug semantic serius.

Misalnya:

```text
Owner AFK

Scheduler:
09:00 → "Good morning everyone"
```

Scheduler mengirim.

AFK menganggap:

> owner sudah kembali

dan mematikan AFK.

Padahal owner tidak kembali sama sekali.

### Harus menggunakan `MessageOrigin`

Misalnya:

```go
type MessageOrigin uint8

const (
    OriginUser
    OriginCommand
    OriginScheduler
    OriginBroadcast
    OriginAddon
    OriginAssistant
    OriginAutomation
)
```

AFK auto-unAFK hanya boleh terjadi jika:

```text
OriginUser
```

atau command yang benar-benar berasal dari owner interaction.

Ini **P0 arsitektur** karena berhubungan dengan banyak fitur.

---

# 26. Ini juga berhubungan langsung dengan PMPermit

Menariknya PMPermit sudah mencoba menangani masalah ini:

```text
IsBotSent(msg.ID)
```

untuk membedakan outgoing programmatic messages.

AFK belum melakukan hal tersebut.

Jadi kita sekarang punya:

```text
PMPermit
    knows bot-generated outgoing

AFK
    doesn't
```

Solusinya jangan copy logic `IsBotSent()` ke AFK.

Buat:

```go
ExecutionContext.MessageOrigin
```

sebagai canonical metadata.

---

# 27. AFK + UserLog

UserLog kemungkinan merekam:

```text
incoming
outgoing
automation
```

Kalau AFK reply dianggap user activity, audit log akan menunjukkan:

```text
AFK reply
```

Tetapi kalau origin tidak dibedakan, sulit mengetahui:

```text
owner sent
vs
AFK sent
vs
scheduler sent
```

Jadi lagi-lagi:

**MessageOrigin wajib menjadi core primitive.**

---

# 28. AFK + dispatcher

Current AFK adalah `MessageHookPlugin`, ini sudah benar.

Priority:

```text
50
```

juga masuk akal untuk feature.

Tetapi hook interface saat ini hanya:

```text
error
```

tidak mengembalikan control-flow decision.

Ini membatasi integrasi AFK dengan:

* PMPermit
* blacklist
* filters
* anti-flood
* userlog
* automation

Saya rekomendasikan upgrade:

```go
type HookResult struct {
    StopPropagation bool

    SuppressReplyAutomation bool
    SuppressFilters         bool
    SuppressAFK             bool
}
```

Jangan membuat semua plugin saling tahu satu sama lain.

---

# 29. AFK tests masih terlalu dangkal

Test sekarang sudah mencakup:

* activate
* custom reason
* DB state
* DM reply
* cooldown
* `.afk` tidak mematikan
* outgoing message mematikan
* duration formatting
* cleanup

Bagus.

Tetapi belum menguji kasus yang justru paling berbahaya:

### Wajib ditambahkan

```text
concurrent incoming messages
concurrent owner outgoing messages
```

### Peer

```text
missing access_hash
missing entity
channel
basic group
supergroup
```

### Identity

```text
bot sender
self sender
service message
anonymous admin
```

### Mention

```text
@username
text mention
multiple entities
Unicode
```

### Reply

```text
reply to owner
reply to someone else
reply target unavailable
GetMessage failure
```

### Integration

```text
PMPermit blocked → no AFK reply
Blacklist matched → no AFK reply
Filter matched → expected AFK behavior
Scheduler outgoing → does not clear AFK
Broadcast outgoing → does not clear AFK
Addon outgoing → does not clear AFK
```

### Lifecycle

```text
shutdown while AFK reply running
context cancellation
service unavailable
DB unavailable
```

### Race

Wajib:

```bash
go test -race ./plugins/afk ./plugins/pmpermit ./plugins/filters ./plugins/blacklist
```

---

# 30. Saya menemukan satu desain yang jauh lebih penting daripada sekadar memperbaiki AFK

AFK memperlihatkan bahwa GoUltroid sekarang membutuhkan **Automation Arbitration Layer**.

Karena sekarang ada:

```text
PMPermit
Blacklist
Filters
AFK
Scheduler
Broadcast
Addon
UserLog
```

semuanya dapat bereaksi terhadap message.

Kalau masing-masing hanya:

```text
HandleMessage() error
```

maka hasilnya bisa:

```text
message
 ├─ PMPermit reply
 ├─ AFK reply
 ├─ Filter reply
 ├─ Blacklist action
 └─ UserLog
```

Tanpa koordinasi.

### Target architecture

```text
Telegram Update
       │
       ▼
Normalize
       │
       ▼
Security Decision
       │
       ├── blocked
       ├── challenged
       └── allowed
       │
       ▼
Automation Arbitration
       │
       ├── AFK
       ├── Filter
       ├── Notes
       ├── Auto-reply
       └── other automation
       │
       ▼
Command Router
       │
       ▼
Execution
```

Dengan context:

```go
type MessageDecision struct {
    Allowed              bool
    Handled              bool
    SuppressAutomation   bool
    SuppressAFK          bool
    SuppressFilters      bool
    SuppressCommands     bool
    Origin               MessageOrigin
}
```

Ini akan menyelesaikan banyak bug yang tidak bisa diselesaikan dengan patch AFK saja.

---

# 31. Desain AFK yang saya rekomendasikan

Final architecture:

```text
                    ┌──────────────┐
                    │ Telegram     │
                    │ Update       │
                    └──────┬───────┘
                           │
                           ▼
                    NormalizeMessage
                           │
                           ▼
                  MessageExecutionContext
                           │
          ┌────────────────┼─────────────────┐
          │                │                 │
          ▼                ▼                 ▼
      PeerResolver     MessageOrigin     Security
                                             │
                                      PMPermit/Blacklist
                                             │
                                             ▼
                                  Automation Arbitration
                                             │
                                             ▼
                                            AFK
```

AFK sendiri:

```text
AFK State
 ├── Enabled
 ├── Reason
 ├── Since
 └── Version

Memory
   ↑
   │
SQLite
```

Hot path:

```text
message
 ↓
atomic AFK state
 ↓
bot/self check
 ↓
chat/sender cooldown
 ↓
local mention/reply metadata
 ↓
SendMessage
```

**Tidak ada DB query normal path.**

---

# 32. Perilaku AFK final yang saya sarankan

### `.afk`

```text
.afk
```

toggle:

```text
OFF → ON
ON  → OFF
```

### `.afk reason`

```text
.afk sedang tidur
```

→ ON + reason.

### `.afk off`

→ OFF.

### `.afk status`

→ status.

---

### DM

Ketika AFK:

```text
User → DM
```

→ reply maksimal sekali / user / cooldown.

Bot:

```text
Bot → DM
```

→ ignore.

---

### Group

Reply hanya jika:

```text
reply-to-owner
OR
mention-owner
```

Tidak perlu reply untuk semua message.

---

### Owner activity

```text
manual outgoing
```

→ clear AFK.

Tetapi:

```text
scheduler
broadcast
addon
AI
automation
AFK reply
```

→ **tidak clear AFK**.

---

### Welcome back

Default:

```text
OFF silently
```

Optional:

```text
private only
```

Saya **tidak menyarankan** otomatis mengirim "Welcome back" ke group.

---

# 33. Prioritas implementasi

Saya akan membuat roadmap AFK seperti ini:

### 🔴 P0

1. **AFK in-memory state/cache**
2. **atomic ON/OFF transition**
3. **explicit `.afk off` / toggle**
4. **MessageOrigin**
5. **central PeerResolver**
6. **bot/self/service-message exclusion**
7. **scheduler/broadcast/addon outgoing tidak boleh mematikan AFK**

### 🟠 P1

8. cooldown key = `chat + sender`
9. atomic cooldown
10. mention detection lengkap
11. reply detection tanpa unnecessary RPC
12. check cooldown sebelum expensive Telegram RPC
13. PMPermit/Blacklist arbitration
14. Filters/AFK arbitration
15. proper error handling/logging
16. periodic cleanup

### 🟡 P2

17. AFK configuration
18. AFK status command
19. optional welcome-back
20. metrics
21. AFK history
22. advanced policy

---

# 34. Hubungan dengan Ultroid

Untuk parity, saya tidak akan sekadar meniru nama command Python. Arsitektur Ultroid memang menggunakan centralized handler mechanism untuk message updates melalui `ultroid_cmd`, sementara inline/callback juga punya handler abstraction sendiri. ([GitHub][1])

Untuk GoUltroid, pendekatan yang lebih bagus adalah mempertahankan kelebihan Go:

```text
typed execution context
+
central peer resolver
+
central policy
+
bounded concurrency
+
durable state
```

daripada membuat port satu-per-satu dari Python.

---

## Kesimpulan akhir

**AFK sekarang bukan implementasi buruk.** Basic feature-nya sudah benar dan test dasarnya juga ada. Tetapi setelah saya trace dengan fitur-fitur yang sekarang sudah ada di GoUltroid, saya justru melihat masalah yang lebih fundamental:

> **AFK belum mengetahui perbedaan antara “aktivitas owner” dan “pesan yang dikirim automation”.**

Itu bisa membuat:

```text
Scheduler
Broadcast
Addon
AI
Downloader
```

secara tidak sengaja **mematikan AFK**.

Dan problem yang sama akan terus muncul pada fitur automation lain kalau belum ada `MessageOrigin` + `AutomationDecision`.

Jadi saya akan menempatkan **AFK + MessageOrigin + Automation Arbitration + central PeerResolver** sebagai satu pekerjaan fondasi, bukan memperbaiki `afk.go` sendirian.

Setelah empat hal itu selesai, AFK bisa dibuat sangat ringan:

```text
incoming message
   ↓
atomic cached state
   ↓
policy
   ↓
cooldown
   ↓
reply
```

tanpa query SQLite per message dan tanpa RPC yang tidak perlu.

**Status AFK saat ini: 6.2/10 — usable, tetapi belum layak disebut production-grade.** Yang paling mendesak bukan menambah fitur AFK, melainkan memperbaiki **state transition, message origin, arbitration antar automation, dan peer resolution**.

[1]: https://github.com/TeamUltroid/Ultroid/wiki/Creating-Plugins?utm_source=chatgpt.com "Creating Plugins · TeamUltroid/Ultroid Wiki · GitHub"
