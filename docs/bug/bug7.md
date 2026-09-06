Saya sudah lakukan research lintas **implementasi PMPermit Ultroid, implementasi PMPermit Goultroid saat ini, Telegram MTProto semantics, block/report/archive behavior, serta pola PM-permit userbot lain**. Kesimpulannya: **PMPermit Goultroid sudah punya fondasi yang cukup bagus, tetapi belum setara secara fitur maupun robustness dengan PMPermit Ultroid, dan masih ada beberapa bug desain P0/P1 yang perlu dibereskan.**

Telegram sendiri sebenarnya tidak menyediakan konsep “PMPermit” sebagai fitur native; ini adalah **application-level gate** di atas private-message updates. Telegram menyediakan primitive seperti `contacts.block/unblock`, `messages.reportSpam`, `messages.deleteMessages`, peer folders/archive, dan peer settings yang menjadi building block untuk implementasi seperti ini. ([Telegram][1])

---

# 1. Apa sebenarnya PMPermit?

PMPermit bukan sekadar:

> “Kalau user belum approved, kirim warning.”

Implementasi production-grade seharusnya menjadi **Private Message Access Control System**:

```text
                 Telegram Private Message
                           │
                           ▼
                  PMPermit Interceptor
                           │
              ┌────────────┼────────────┐
              │            │            │
          OWNER/SUDO     APPROVED     BLOCKED
              │            │            │
              ▼            ▼            ▼
            ALLOW        ALLOW       DROP/IGNORE
                           │
                     PENDING USER
                           │
                  warning / challenge
                           │
                 ┌─────────┴─────────┐
                 │                   │
              APPROVE             LIMIT
                 │                   │
                 ▼                   ▼
             ALLOW                BLOCK
```

Jadi ada **state machine**, bukan sekadar boolean.

Telegram message sendiri memiliki flag `out`, `peer_id`, `from_id`, media, entities, reply information, dan sebagainya, sehingga interceptor harus membedakan incoming/outgoing dan sumber pesan dengan benar. ([Telegram][2])

---

# 2. PMPermit Ultroid sebenarnya jauh lebih besar

Research terhadap `TeamUltroid/Ultroid/plugins/pmpermit.py` menunjukkan bahwa PMPermit Ultroid mencakup:

* approve
* disapprove
* block
* unblock
* unblock all
* list approved
* PM logging
* per-user PM logging exclusion
* archive PM otomatis
* clear archive
* PMPermit enable/disable
* configurable warning count
* custom PMPermit message
* PMPermit picture
* inline PMPermit
* auto-approve outgoing
* developer bypass
* bot bypass
* verified-user bypass
* self bypass
* warning counter
* warning-message replacement
* automatic block setelah threshold
* spam report
* log-channel notification
* inline approve/block buttons
* approved/disapproved status logging
* auto-approved logging

Command surface-nya sendiri secara eksplisit mencantumkan:

```text
.a / .approve
.da / .disapprove
.block
.unblock
.unblock all
.nologpm
.logpm
.startarchive
.stoparchive
.cleararchive
.listapproved
.pmpermit on/off
```

Bahkan Ultroid mempunyai `PMPIC`, `PM_TEXT`, `PMLOG`, `PMLOGGROUP`, `AUTOAPPROVE`, `INLINE_PM`, `PMWARNS`, dan `PMPERMIT` sebagai bagian dari state/configuration-nya.

---

# 3. Arsitektur PMPermit Ultroid

Secara konseptual Ultroid melakukan:

```text
Incoming PM
     │
     ▼
is PM?
     │
     ├── bot/self/verified/dev → bypass
     │
     ├── approved → allow
     │
     └── unapproved
             │
             ├── warning 1
             ├── warning 2
             ├── warning 3
             │
             └── warning N
                    │
                    ├── block
                    └── report spam
```

Dan secara terpisah:

```text
Outgoing PM
     │
     ▼
AUTOAPPROVE?
     │
     └── yes
          │
          ▼
       approve user
```

Ultroid memang memiliki handler outgoing khusus untuk auto-approve.

Ini penting karena **outgoing PM adalah intent signal**:

> “Saya sebagai owner memang memulai komunikasi dengan user ini.”

---

# 4. Goultroid saat ini

Goultroid sudah jauh lebih bagus dibanding implementasi MVP awal.

Current architecture:

```text
Telegram update
      │
      ▼
dispatcher
      │
      ▼
pmpermit.HandleIncomingMessage()
      │
      ├── outgoing
      │      └── AutoApproveOutgoing()
      │
      └── incoming
             │
             ▼
       HandleIncomingPM()
             │
             ├── owner/sudo
             ├── approved
             ├── pending
             └── blocked
```

Wiring-nya juga sudah benar secara arsitektur: PMPermit service dibuat sebagai service terpisah dan plugin didaftarkan ke dispatcher.

Database sudah durable:

```text
pm_permit_records
```

dengan:

```text
user_id
status
first_seen_at
last_seen_at
expires_at
reason
warn_count
warn_msg_ids
```

dan migration v12 sudah menambahkan tracking warning message IDs.

Ini merupakan foundation yang bagus.

---

# 5. State machine Goultroid saat ini

Current state:

```text
unknown
   │
   ▼
pending
   │
   ├───────────────┐
   │               │
   ▼               ▼
approved         blocked
   │
   ▼
expired → pending
```

Namun ada satu state yang mencurigakan:

```go
StatusDenied
```

yang saat ini tidak mempunyai lifecycle yang jelas.

Saya sarankan jangan mempunyai:

```text
approved
denied
blocked
```

secara ambigu.

Lebih baik:

```text
PENDING
APPROVED
BLOCKED
EXPIRED
```

dan `DENIED` hanya dipakai jika memang ada semantic berbeda dari `BLOCKED`.

---

# 6. BUG P0: AutoApproveOutgoing saat ini kontradiktif

Ini salah satu temuan terpenting.

Plugin saat ini melakukan:

```go
if p.svc.IsBlocked(ctx, targetID) {
    return nil
}
```

sebelum:

```go
p.svc.AutoApproveOutgoing(...)
```

Artinya:

```text
user blocked
      │
owner PM user
      │
      ▼
IsBlocked == true
      │
      ▼
return
      │
      X
tidak auto approve
```

Padahal `AutoApproveOutgoing()` sendiri mencoba:

```text
approve
delete warnings
reset warnings
unblock
```

Jadi dua layer memiliki policy yang bertentangan.

Ini juga bertentangan dengan tujuan dokumen `4.7` yang menyebut outgoing owner harus bisa mengaktifkan kembali akses user dan melakukan unblock.

### Seharusnya

```text
Owner sends outgoing PM
        │
        ▼
Is this genuine owner-originated PM?
        │
        ▼
YES
        │
        ├── APPROVE
        ├── RESET WARN
        ├── DELETE OLD WARNINGS
        └── UNBLOCK
```

Tidak boleh ada `IsBlocked -> return` di layer plugin.

---

# 7. BUG P0 yang lebih dalam: outgoing auto-approve terlalu agresif

Ini bahkan lebih penting.

Current logic kira-kira:

```text
msg.Out
  ↓
not command
  ↓
not PMPermit message
  ↓
private user
  ↓
AutoApprove
```

Masalahnya:

**tidak semua outgoing message berarti owner secara manual memulai percakapan.**

Misalnya:

```text
scheduler
broadcast
automation
addon
forward
media plugin
AI assistant
automatic reply
```

bisa mengirim pesan outgoing.

Maka:

```text
automation → send PM user
                 ↓
             AutoApprove
                 ↓
             security state changed
```

Itu salah secara semantic.

### Yang benar

Dispatcher harus membawa **execution origin/source**.

Misalnya:

```go
type MessageOrigin int

const (
    OriginUser
    OriginCommand
    OriginScheduler
    OriginAutomation
    OriginAddon
    OriginSystem
)
```

Kemudian:

```text
OriginUser + outgoing private
        ↓
AutoApprove
```

sedangkan:

```text
OriginScheduler
OriginAddon
OriginAutomation
OriginSystem
        ↓
NO AUTO APPROVE
```

Ini sangat cocok dengan arsitektur Goultroid yang sebelumnya sudah memiliki explicit execution source model.

**Ini saya kategorikan P0.**

---

# 8. Warning system sekarang

Current Goultroid:

```text
warn 1
warn 2
warn 3
warn 4
    ↓
blocked
```

Default:

```go
DefaultMaxWarns = 4
```

dan sudah configurable melalui:

```go
SetMaxWarns()
```

Bagus.

Tetapi konfigurasi ini **belum menjadi persistent user setting**.

Saat restart:

```text
SetMaxWarns()
```

tidak otomatis dipulihkan dari database/config.

Kalau ingin production-grade:

```text
pmpermit_config
```

atau global config:

```text
pmpermit.enabled
pmpermit.max_warns
pmpermit.auto_approve
pmpermit.archive
pmpermit.log
```

---

# 9. Warning message tracking

Ini improvement yang bagus.

Goultroid sekarang menyimpan:

```text
warn_msg_ids
```

dan membatasi maksimum 20.

Tujuannya:

```text
warning 1 → msg 101
warning 2 → msg 102
warning 3 → msg 103

approve
   ↓
delete 101,102,103
```

Ini benar secara UX.

Telegram memang menyediakan `messages.deleteMessages` untuk menghapus message IDs tertentu. ([Telegram][3])

---

# 10. Tetapi implementasi warning ID masih punya masalah concurrency

Current:

```go
ids, _ := db.GetWarnMsgIDs(...)
ids = append(ids, msgID)
UPDATE warn_msg_ids
```

Ini adalah:

```text
READ
 ↓
MODIFY
 ↓
WRITE
```

bukan atomic.

Jika dua warning diproses bersamaan:

```text
worker A → read [101]
worker B → read [101]

A → [101,102]
B → [101,103]

A write
B write
```

hasil:

```text
[101,103]
```

`102` hilang.

Untuk PMPermit mungkin tidak sering terjadi, tetapi Telegram update processing bisa concurrent.

### Lebih bagus

Gunakan transaction/serialized DB operation atau tabel:

```sql
pmpermit_warn_messages
----------------------
user_id
message_id
created_at
PRIMARY KEY(user_id, message_id)
```

Lalu:

```sql
DELETE FROM pmpermit_warn_messages
WHERE user_id = ?
```

Ini jauh lebih robust daripada JSON array.

Untuk maksimal 20 rows/user, overhead SQLite sangat kecil.

---

# 11. DB error terlalu banyak diabaikan

Ada beberapa pola:

```go
_ = s.db.AddWarnMsgID(...)
```

```go
_ = s.db.ClearWarnMsgIDs(...)
```

```go
_ = svc.DeleteMessage(...)
```

```go
_ = svc.UnblockUser(...)
```

```go
_ = svc.BlockUser(...)
```

Ini berbahaya.

Karena state bisa menjadi:

```text
SQLite:
BLOCKED

Telegram:
NOT BLOCKED
```

atau:

```text
SQLite:
APPROVED

Telegram:
STILL BLOCKED
```

PMPermit kemudian terlihat “benar” di database tetapi behavior Telegram salah.

### Harus ada state transition result

Misalnya:

```text
APPROVE_REQUESTED
      │
      ├── DB success
      │
      ├── Telegram unblock success
      │
      └── warning cleanup success
```

atau:

```text
APPROVAL_DEGRADED
```

Jika Telegram RPC gagal.

---

# 12. Block sekarang sudah benar secara konsep

Current service sekarang sudah melakukan:

```text
DB status = blocked
       +
Telegram BlockUser()
```

dan test juga memverifikasi `BlockUser` dipanggil.

Ini jauh lebih benar daripada hanya:

```text
SQLite status = blocked
```

Telegram memang menyediakan `contacts.block` untuk memasukkan peer ke main blocklist, dan user dalam main blocklist tidak dapat menulis kepada akun tersebut. ([Telegram][1])

---

# 13. Tetapi Block + Report Spam belum setara Ultroid

Ultroid melakukan:

```text
BlockRequest
+
ReportSpamRequest
```

Current Goultroid hanya:

```text
BlockUser
```

Jadi:

```text
BLOCK
```

sudah ada.

Tetapi:

```text
REPORT SPAM
```

belum.

Telegram mempunyai:

```text
messages.reportSpam
```

untuk melaporkan incoming chat sebagai spam, dengan syarat peer settings mengizinkannya. ([Telegram][4])

Ada juga:

```text
account.reportPeer
```

yang digunakan untuk report violation/spam. ([Telegram][5])

### Rekomendasi

Jangan otomatis melakukan `account.reportPeer` untuk semua threshold.

Lebih aman:

```text
warn >= max
      │
      ├── block
      │
      └── optional reportSpam
```

dengan config:

```text
pmpermit.auto_report_spam = false
```

default.

Karena **block dan report adalah dua tindakan berbeda**.

---

# 14. Blocked state saat ini masih bisa menghasilkan spam reply

Current:

```go
if rec.Status == StatusBlocked {
    SendMessage("You are blocked...")
    return true
}
```

Jadi apabila `contacts.block` gagal:

```text
user PM
 ↓
blocked state
 ↓
send "you are blocked"
 ↓
user PM again
 ↓
send "you are blocked"
 ↓
...
```

Ini bisa menciptakan loop.

### Lebih baik

Jika:

```text
StatusBlocked
```

maka default:

```text
DROP
```

tanpa reply.

Kecuali:

```text
block RPC belum sukses
```

dan kita ingin retry terbatas.

Contoh:

```text
BLOCK_PENDING
   ↓
retry block
   ↓
success → BLOCKED
   ↓
DROP
```

---

# 15. `Block` harus idempotent

Sekarang:

```text
Block()
```

dapat dipanggil berkali-kali.

Production design:

```text
if already BLOCKED:
    return nil
```

dan hanya melakukan RPC apabila Telegram state belum diketahui.

Ideal:

```text
DB status
+
Telegram block state cache
```

atau minimal:

```text
block operation idempotent
```

---

# 16. Approve juga harus idempotent

Current:

```text
Approve()
```

sudah relatif idempotent.

Tetapi ideal:

```text
if already approved:
    ensure unblock
    ensure warning cleanup
    return
```

Karena approve bukan sekadar:

```text
DB status = approved
```

tetapi harus menjadi invariant:

```text
APPROVED means:

DB = approved
warn_count = 0
warning messages = cleaned
Telegram block = false
```

---

# 17. Disapprove semantics belum ideal

Current:

```text
Disapprove
 ├── delete cache
 ├── clear warn IDs
 ├── reset warn
 └── status=pending
```

Tetapi tidak melakukan:

```text
UnblockUser
```

Ini bisa menciptakan:

```text
DB:
pending

Telegram:
blocked
```

Jika sebelumnya blocked.

Padahal semantics `.disapprove` harus didefinisikan jelas:

### Pilihan A

```text
DISAPPROVE
= kembali ke pending
= unblock
```

### Pilihan B

```text
DISAPPROVE
= revoke approval
= tetap blocked
```

Ultroid secara UX membedakan `disapprove` dan `block`, jadi untuk parity saya rekomendasikan **A**.

---

# 18. `Denied` sebaiknya dihapus

Saat ini ada:

```go
StatusDenied
```

tetapi flow tidak menggunakannya.

Jangan mempertahankan state yang tidak memiliki semantic jelas.

Gunakan:

```text
PENDING
APPROVED
BLOCKED
```

dan optional:

```text
EXPIRED
```

Jika benar-benar perlu:

```text
DENIED
```

harus memiliki behavior berbeda.

---

# 19. Expiration sudah bagus, tetapi belum lengkap

Goultroid mendukung:

```text
Approve(user, duration)
```

dan:

```text
ExpiresAt
```

Bagus.

Tetapi expiration saat ini terjadi lazy:

```text
IsApproved()
   ↓
expired?
   ↓
status = pending
```

Ini acceptable.

Tetapi perlu:

```text
approved_cache
```

expire sesuai `ExpiresAt`.

Sudah ada foundation itu.

Yang perlu diperbaiki:

```text
expiration event
```

harus tidak meninggalkan:

```text
Telegram unblocked
```

atau warning state yang stale.

---

# 20. Owner/Sudo bypass sudah benar

Current:

```go
if s.perms.IsSudo(userID) {
    return true
}
if userID == ownerID {
    return true
}
```

Ini bagus.

Ultroid juga memiliki bypass untuk developer/special users.

Tetapi harus dipastikan:

```text
Sudo
```

tidak pernah masuk:

```text
warning
blocked
```

bahkan jika database memiliki stale record.

Current `IsApproved()` melakukan bypass sebelum DB lookup, jadi ini benar.

---

# 21. Bot dan verified bypass belum ada di Goultroid

Ultroid explicitly melakukan:

```text
user.bot
user.is_self
user.verified
```

bypass.

Goultroid saat ini hanya bekerja berdasarkan:

```text
senderID
```

dan belum melakukan classification lengkap terhadap `tg.User`.

Ini berarti:

```text
verified user
```

dapat terkena PMPermit.

Begitu pula:

```text
bot
```

dapat terkena PMPermit.

### Recommendation

Classifier harus mendapatkan entity:

```go
type PMActor struct {
    ID       int64
    IsBot    bool
    IsSelf   bool
    Verified bool
    IsSudo   bool
}
```

Policy:

```text
self      → ALLOW
owner     → ALLOW
sudo      → ALLOW
bot       → configurable
verified  → configurable
approved  → ALLOW
blocked   → DROP
pending   → CHALLENGE
```

Default parity:

```text
bot      → bypass
verified → bypass
```

---

# 22. Mention of media

Ultroid punya behavior:

```text
incoming media from unapproved user
```

yang bisa di-delete sebelum warning dikirim, tergantung setting `DISABLE_PMDEL`.

Goultroid tidak mempunyai equivalent.

Saat ini PMPermit tidak membedakan:

```text
text
photo
video
document
voice
sticker
GIF
album
```

Semua dihitung:

```text
warn++
```

Ini bisa dipertahankan sebagai security policy, tetapi harus eksplisit.

Saya justru menyarankan:

```text
default:
unapproved media → delete original immediately
```

agar spammer tidak bisa dump:

```text
20 photos
20 videos
large documents
```

ke PM sebelum block.

---

# 23. PMPermit message sebaiknya bukan hardcoded English

Current warning:

```text
I haven't approved you...
```

hardcoded di service.

Ultroid memiliki localization:

```text
pmperm_1
pmperm_2
pmperm_3
```

dan tersedia banyak bahasa termasuk Indonesia.

Goultroid sebaiknya:

```text
pmpermit.message
pmpermit.limit_message
pmpermit.blocked_message
pmpermit.approved_message
```

melalui localizer.

---

# 24. Custom PM text

Ultroid memiliki:

```text
PM_TEXT
```

yang menggantikan default PMPermit message.

Goultroid belum.

Sebaiknya ada:

```text
.pmpermit text <...>
```

atau setting:

```text
pmpermit.message
```

Tetapi jangan membuat command terlalu kompleks.

Lebih baik:

```text
.pmpermit settings
```

→ interactive callback UI.

---

# 25. PM picture

Ultroid mendukung:

```text
PMPIC
```

dan warning dapat dikirim sebagai media/caption.

Goultroid belum.

Ini bukan P0.

Prioritas:

```text
P2
```

karena tidak berhubungan langsung dengan security correctness.

---

# 26. Inline PMPermit

Ultroid juga memiliki:

```text
INLINE_PM
```

sehingga warning dapat ditampilkan melalui inline bot.

Dengan callback/inline engine Goultroid yang sekarang sudah jauh lebih matang, fitur ini justru sangat feasible.

Contoh:

```text
User PM
   ↓
PMPermit
   ↓
inline result
   ↓
Approve / Block
```

Tetapi saya tidak akan menjadikan inline sebagai core security mechanism.

Core tetap:

```text
PMPermit Service
```

Inline hanya UI.

---

# 27. Approve/Block buttons

Ini salah satu UX terbaik Ultroid.

Log channel dapat menerima:

```text
Incoming PM from @user

[ Approve PM ] [ Block PM ]
```

Ultroid memang membuat callback buttons untuk approval/block.

Goultroid sekarang memiliki callback/inline infrastructure yang cukup matang, sehingga ini seharusnya menjadi **P1**.

Flow:

```text
incoming PM
     ↓
warning
     ↓
owner notification
     ↓
┌───────────────────────────────┐
│ PM from @username             │
│ ID: 123456                    │
│ Warnings: 2/4                 │
│                               │
│ [ Approve ] [ Block ]         │
└───────────────────────────────┘
```

Callback harus authorization-aware:

```text
owner only
```

dan callback state harus one-shot/idempotent.

---

# 28. `listapproved` belum ada

Ultroid memiliki:

```text
.listapproved
```

Current Goultroid tidak.

Padahal DB sudah memiliki semua informasi yang diperlukan.

Implementasi:

```text
.listapproved
```

→

```text
Approved PMs

1. @alice       123
2. @bob         456
3. @charlie     789

Total: 3
```

Harus pagination jika banyak.

---

# 29. Unblock belum menjadi command

Current:

```text
.approve
.disapprove
.blockpm
.pmpermit
```

Belum ada:

```text
.unblock
.unblock all
```

Ini cukup penting.

Karena:

```text
Block
```

sekarang benar-benar masuk Telegram blocklist.

Maka harus ada recovery mechanism.

Saya sarankan:

```text
.pmpermit unblock <user>
.pmpermit unblock all
```

daripada command global `.unblock`, agar semantic lebih jelas.

---

# 30. PM logging

Ultroid mempunyai PM logging terintegrasi:

```text
PM
 ↓
log channel
```

dengan exclusion per-user:

```text
.logpm
.nologpm
```

dan tidak melakukan log terhadap:

```text
bot
self
verified
excluded user
```

Goultroid sudah memiliki **UserLog system terpisah**, jadi jangan membuat PMPermit logging kedua.

Lebih baik:

```text
PMPermit
    │
    └── emits typed PM event
             │
             ▼
         UserLog
```

Ini sangat penting untuk architecture.

---

# 31. PMPermit dan UserLog harus dipisahkan

Saya sarankan:

```text
PMPermit
= authorization/security

UserLog
= observability/audit
```

Jangan:

```text
PMPermit → langsung send log Telegram
```

Lebih bagus:

```text
PMPermit decision
      │
      ▼
AuditEvent
      │
      ▼
UserLog
```

Contoh:

```go
type PMPermitEvent struct {
    UserID      int64
    Action      PMPermitAction
    Previous    Status
    Current     Status
    WarnCount   int
    Reason      string
    Timestamp   time.Time
}
```

Actions:

```text
PM_RECEIVED
PM_WARNING_SENT
PM_APPROVED
PM_DISAPPROVED
PM_BLOCKED
PM_UNBLOCKED
PM_AUTO_APPROVED
PM_EXPIRED
PM_REPORT_SPAM
```

---

# 32. Archive support

Ultroid memiliki:

```text
.startarchive
.stoparchive
.cleararchive
```

dan menggunakan Telegram peer folder dengan `folder_id=1` untuk archive.  ([Telegram][6])

Telegram memang secara resmi menggunakan:

```text
folder_id = 1
```

untuk archived chats. ([Telegram][6])

Goultroid belum punya.

Saya sarankan:

```text
pmpermit.archive = false
```

default.

Jika enabled:

```text
incoming unknown PM
       ↓
move peer → archive
```

tetapi jangan menganggap archive sebagai security mechanism.

Archive hanya UX.

---

# 33. Important: jangan memakai archive sebagai block

Ini harus jelas:

```text
archive ≠ block
```

Archive hanya memindahkan dialog ke folder.

Block benar-benar mencegah user menulis kepada kita. Telegram mendefinisikan main blocklist sebagai mekanisme yang mencegah peer menulis kepada akun. ([Telegram][7])

---

# 34. `messages.reportSpam` juga bukan pengganti block

Jangan:

```text
report spam
```

lalu menganggap user blocked.

Telegram memisahkan:

```text
contacts.block
messages.reportSpam
account.reportPeer
```

masing-masing memiliki semantics sendiri. ([Telegram][1])

Recommended:

```text
PMPermit policy

threshold
   │
   ├── block = primary enforcement
   │
   └── report = optional moderation action
```

---

# 35. Current `resolvePeer()` terlalu lemah

Current service:

```go
func (s *Service) resolvePeer(userID int64) tg.InputPeerClass {
    return &tg.InputPeerUser{UserID: userID}
}
```

Ini tidak membawa:

```text
AccessHash
```

Padahal `tg.User` dapat memiliki `access_hash`, dan peer resolution di MTProto perlu menggunakan data peer yang valid. Telegram sendiri menjelaskan bahwa user entity memiliki optional access hash. ([Telegram][8])

Goultroid bahkan sudah mempunyai persistent peer storage.

Jadi PMPermit seharusnya memanfaatkan:

```text
peers.Storage
```

untuk:

```text
UserID
 +
AccessHash
```

Bukan membuat:

```text
InputPeerUser{UserID}
```

secara blind.

---

# 36. Ini penting khususnya untuk restart

Sekarang:

```text
incoming event
→ e.Users
→ AccessHash tersedia
```

sering bekerja.

Tetapi:

```text
.approve 123456
```

setelah restart:

```text
resolvePeer(123456)
→ InputPeerUser{123456}
```

bisa tidak cukup untuk RPC tertentu.

Lebih baik:

```text
resolveUserPeer()
    │
    ├── in-memory peer cache
    ├── persistent peers_storage
    ├── entity cache
    └── Telegram resolve fallback
```

---

# 37. Duplicate warning / idempotency

Ini belum cukup.

Current service menerima:

```go
HandleIncomingPM(ctx, peer, senderID)
```

Tidak menerima:

```text
messageID
```

Akibatnya service tidak tahu:

```text
apakah ini PM yang sama?
```

Ideal:

```go
HandleIncomingPM(
    ctx,
    peer,
    senderID,
    messageID,
    messageMeta,
)
```

Kemudian:

```text
(user_id, message_id)
```

menjadi idempotency key.

Misalnya:

```sql
pm_permit_seen_messages
-----------------------
user_id
message_id
processed_at
```

Dengan TTL/cleanup.

Ini penting untuk production.

---

# 38. Warning harus punya reason

Jangan hanya:

```text
warn_count++
```

Simpan:

```text
message_id
message_type
received_at
reason
```

Misalnya:

```text
1 → text
2 → photo
3 → document
4 → voice
```

Ini membantu audit.

---

# 39. PMPermit perlu rate limiting

Current:

```text
user → PM
user → PM
user → PM
```

langsung menghasilkan:

```text
SendMessage
SendMessage
SendMessage
```

bahkan jika threshold hanya 4.

Ini dapat menyebabkan:

```text
Telegram FloodWait
```

dan workload yang tidak perlu.

Lebih baik:

```text
warning delivery rate limit
```

contoh:

```text
max 1 PMPermit response / 2 sec / user
```

Jika user mengirim 10 pesan:

```text
warning 1
drop 2-10
```

bukan:

```text
warning 1
warning 2
warning 3
block
```

secara burst.

---

# 40. FloodWait harus ditangani sebagai first-class state

Telegram dapat memberikan FloodWait.

PMPermit tidak boleh:

```text
send warning
→ error
→ ignore
```

Lebih baik:

```text
DeliveryFailure
    │
    ├── transient
    │      ↓
    │    retry
    │
    └── permanent
           ↓
        degraded
```

Terutama untuk:

```text
Block
Unblock
DeleteMessage
SendMessage
ReportSpam
```

---

# 41. Warning send error sekarang swallowed

Current:

```go
msg, _ := svc.SendMessage(...)
```

Kalau gagal:

```text
warn_count tetap bertambah
```

tetapi:

```text
user tidak menerima warning
```

Jadi:

```text
warning counter = 3
user melihat = 0 warning
```

Lalu tiba-tiba:

```text
blocked
```

Ini UX/security inconsistency.

Lebih baik:

```text
Increment warning
   ↓
attempt send
   ↓
success → commit delivery
   ↓
failure → record failure
```

Atau warning counter memang merupakan **policy count**, bukan delivered count; kalau begitu harus jelas.

---

# 42. Recommended model: separate attempt vs warning

Saya rekomendasikan:

```text
message_count
warning_count
```

berbeda.

Misalnya:

```text
message_count = 7
warning_count = 3
```

karena beberapa messages di-drop akibat rate limit.

---

# 43. PMPermit should classify messages

Jangan semua:

```text
UpdateNewMessage
```

langsung dianggap PM.

Classifier:

```text
Message
 │
 ├── PeerUser
 │      │
 │      ├── incoming
 │      └── outgoing
 │
 ├── PeerChat
 ├── PeerChannel
 └── other
```

Telegram `peerUser` memang secara eksplisit merepresentasikan chat partner. ([Telegram][9])

Goultroid saat ini sudah membatasi:

```go
msg.PeerID.(*tg.PeerUser)
```

yang bagus.

---

# 44. PMPermit harus handle `FromID` dengan benar

Current:

```go
senderID := peerUser.UserID

if msg.FromID != nil {
    if u, ok := msg.FromID.(*tg.PeerUser); ok {
        senderID = u.UserID
    }
}
```

Ini perlu dipertahankan, tetapi jangan hanya mengandalkan `PeerID`.

Untuk incoming private chat:

```text
peer = user X
from = user X
```

normal.

Tetapi entity resolution harus tetap dilakukan berdasarkan `from_id` jika tersedia.

---

# 45. Self-generated PMPermit message filtering

Current memiliki:

```go
IsPMPermitMessage()
```

dengan string signatures.

Ini fragile.

Misalnya:

```text
translation
custom PM text
localization
user-similar text
```

bisa menyebabkan false positive/negative.

Lebih bagus menggunakan internal metadata:

```go
type OutgoingMessageMetadata struct {
    SystemGenerated bool
    Component       string
    Purpose         string
}
```

Contoh:

```text
Purpose = "pmpermit_warning"
```

Jadi tidak perlu:

```go
strings.Contains(...)
```

---

# 46. Ini juga berkaitan dengan auto-approve

Current:

```text
IsPMPermitMessage()
```

adalah workaround untuk membedakan system-generated outgoing messages.

Tetapi ini tidak scalable.

Lebih bagus:

```text
message origin
```

seperti:

```text
OriginPMPermit
OriginUser
OriginScheduler
OriginBroadcast
OriginAddon
```

Ini akan menyelesaikan dua masalah sekaligus:

```text
1. PMPermit warning tidak auto-approve
2. automation tidak auto-approve
```

---

# 47. Security policy yang saya rekomendasikan

Final policy:

```text
                   Incoming PM
                       │
                       ▼
                Is PMPermit enabled?
                  /          \
                no            yes
                │              │
              ALLOW            │
                               ▼
                         classify actor
                               │
          ┌────────────┬───────┼────────────┐
          │            │       │            │
        owner        sudo     bot        verified
          │            │       │            │
        ALLOW        ALLOW   policy       policy
                                  │
                                  ▼
                            approved?
                           /         \
                         yes          no
                         │             │
                       ALLOW        blocked?
                                    /     \
                                  yes      no
                                  │         │
                                 DROP    pending
                                           │
                                      rate limit
                                           │
                                      warning
                                           │
                                    threshold?
                                      /     \
                                    no       yes
                                    │         │
                                  WAIT      BLOCK
                                              │
                                      optional report
```

---

# 48. Recommended state machine final

Saya lebih menyarankan:

```text
                    ┌─────────────┐
                    │   UNKNOWN   │
                    └──────┬──────┘
                           │
                           ▼
                    ┌─────────────┐
                    │   PENDING   │
                    └──┬───────┬──┘
                       │       │
                 approve       │ threshold
                       │       │
                       ▼       ▼
                 ┌─────────┐ ┌─────────┐
                 │APPROVED │ │ BLOCKED │
                 └────┬────┘ └────┬────┘
                      │           │
                  expires       unblock
                      │           │
                      ▼           │
                  PENDING ◄───────┘
```

---

# 49. Event model yang ideal

Saya sarankan membuat:

```go
type PMPermitEventType string

const (
    PMReceived       PMPermitEventType = "pm_received"
    PMAllowed        PMPermitEventType = "pm_allowed"
    PMWarning        PMPermitEventType = "pm_warning"
    PMApproved       PMPermitEventType = "pm_approved"
    PMDisapproved    PMPermitEventType = "pm_disapproved"
    PMBlocked        PMPermitEventType = "pm_blocked"
    PMUnblocked      PMPermitEventType = "pm_unblocked"
    PMAutoApproved   PMPermitEventType = "pm_auto_approved"
    PMExpired        PMPermitEventType = "pm_expired"
    PMReportSpam     PMPermitEventType = "pm_report_spam"
)
```

Kemudian:

```text
PMPermit
   ↓
event
   ├── UserLog
   ├── metrics
   ├── notification
   └── audit
```

---

# 50. Metrics

Production PMPermit seharusnya punya:

```text
pmpermit_received_total
pmpermit_allowed_total
pmpermit_pending_total
pmpermit_warning_total
pmpermit_blocked_total
pmpermit_unblocked_total
pmpermit_approved_total
pmpermit_autoapproved_total
pmpermit_expired_total

pmpermit_send_failures_total
pmpermit_block_failures_total
pmpermit_unblock_failures_total
pmpermit_delete_failures_total

pmpermit_active_pending
pmpermit_active_approved
pmpermit_active_blocked
```

Per-user metrics jangan sampai menghasilkan cardinality explosion.

---

# 51. Audit log

Contoh:

```json
{
  "event": "pm_blocked",
  "user_id": 123456,
  "warn_count": 4,
  "reason": "exceeded_warning_threshold",
  "telegram_block": "success",
  "report_spam": "disabled",
  "timestamp": "..."
}
```

**Jangan** memasukkan isi PM mentah ke application log.

---

# 52. Privacy

PMPermit berhubungan langsung dengan private messages.

Jangan log:

```text
message content
phone number
media
session
access hash
```

ke application log.

Audit cukup:

```text
user_id
message_id
status
action
reason
timestamp
```

Jika PM logging diaktifkan, itu adalah fitur terpisah dengan explicit user consent/config.

---

# 53. Telegram privacy modern

Ada perubahan penting di Telegram modern yang perlu diperhitungkan.

Telegram sekarang mempunyai:

```text
contact_require_premium
```

dan global privacy setting:

```text
new_noncontact_peers_require_premium
```

yang dapat membuat non-contact users tidak bisa mengirim private message kecuali memenuhi kondisi tertentu. Telegram juga menyediakan `users.getRequirementsToContact` untuk mengetahui requirement tersebut. ([Telegram][10])

Artinya:

**PMPermit bukan satu-satunya PM security layer.**

Architecture harus menganggap:

```text
Telegram native restrictions
+
PMPermit policy
```

sebagai dua layer berbeda.

---

# 54. Paid messages

Telegram juga sekarang mendukung paid messages/Stars untuk PM tertentu melalui global privacy settings. ([Telegram][11])

Jadi PMPermit tidak boleh mengasumsikan:

```text
"kalau user tidak bisa PM, pasti karena PMPermit."
```

Error Telegram seperti:

```text
PRIVACY_PREMIUM_REQUIRED
```

atau paid-message requirements harus tetap diperlakukan sebagai Telegram-level policy.

---

# 55. Archive design

Jika nanti ditambahkan:

```text
.pmpermit archive on
```

implementasinya harus menggunakan:

```text
folders.editPeerFolders
folder_id = 1
```

bukan sekadar local database flag. Telegram secara resmi mendefinisikan folder 1 sebagai archive. ([Telegram][6])

---

# 56. UX command yang saya rekomendasikan

Daripada menambah 15 command terpisah seperti Ultroid, Goultroid bisa dibuat lebih konsisten:

```text
.pmpermit
.pmpermit on
.pmpermit off
.pmpermit status

.pmpermit approve <user>
.pmpermit disapprove <user>
.pmpermit block <user>
.pmpermit unblock <user>

.pmpermit list approved
.pmpermit list blocked
.pmpermit list pending

.pmpermit settings
.pmpermit test
```

Alias:

```text
.approve
.disapprove
.blockpm
```

tetap bisa dipertahankan untuk parity.

---

# 57. `.pmpermit status`

Harus menunjukkan:

```text
🛡 PMPermit

Status: ENABLED
Max warnings: 4

Auto approve: ON
Archive: OFF
Auto report spam: OFF

Pending: 7
Approved: 42
Blocked: 13

Last block:
@username
2 minutes ago
```

---

# 58. `.pmpermit test`

Sangat penting.

Harus memvalidasi:

```text
database
peer resolution
Telegram SendMessage
Telegram BlockUser
Telegram UnblockUser
DeleteMessage
callback/logging integration
```

Tanpa harus menunggu spammer.

---

# 59. Admin interaction dari group

Research komunitas userbot menunjukkan PMPermit modern sering memungkinkan approve/block user **dari group**, bukan hanya dari PM. Catuserbot bahkan mencatat fitur approve/block PM dari group. ([Telegram][12])

Maka Goultroid sebaiknya mendukung:

```text
.approve @user
.blockpm @user
```

dari:

```text
PM
group
reply
user ID
username
```

Current resolver sudah sebagian mengarah ke sana, tetapi perlu diperkuat dengan entity resolution.

---

# 60. Test matrix yang jauh lebih lengkap

Minimal:

### Basic

```text
owner PM
sudo PM
approved PM
pending PM
blocked PM
bot PM
verified PM
unknown PM
```

### Warning

```text
1st message
2nd message
3rd message
threshold
threshold+1
```

### State

```text
approve
double approve
disapprove
double disapprove
block
double block
unblock
double unblock
expiration
```

### Outgoing

```text
manual outgoing
command outgoing
scheduler outgoing
addon outgoing
system outgoing
PMPermit warning outgoing
automation outgoing
```

### Telegram

```text
missing access hash
invalid peer
user deleted
user blocked
FloodWait
network timeout
RPC error
```

### Persistence

```text
restart
warning IDs survive restart
approval survives restart
block survives restart
expiration survives restart
```

### Concurrency

```text
2 incoming simultaneously
10 incoming burst
duplicate message
approve while incoming arrives
block while incoming arrives
```

### UX

```text
media
text
voice
sticker
album
reply
long message
unicode
```

---

# 61. Current Goultroid score

Setelah research total ini, saya akan menilai current PMPermit:

| Area                    |      Score |
| ----------------------- | ---------: |
| Basic PM gate           | **8.5/10** |
| Persistent state        |   **8/10** |
| Warning system          | **7.5/10** |
| Approval                | **7.5/10** |
| Blocking                | **7.5/10** |
| Telegram peer handling  |   **6/10** |
| Outgoing auto-approve   |   **5/10** |
| Security semantics      |   **6/10** |
| Error/retry handling    | **4.5/10** |
| Concurrency/idempotency |   **5/10** |
| UX                      |   **5/10** |
| Ultroid feature parity  | **4.5/10** |
| Test coverage           |   **7/10** |
| Architecture            |   **8/10** |

### Overall

**≈ 6.3/10**

Bukan karena foundation jelek. Justru architecture-nya cukup bagus.

Masalahnya adalah:

> **security policy belum sepenuhnya deterministic dan Telegram-side state belum diperlakukan sebagai bagian dari state machine.**

---

# 62. Priority final

## P0 — wajib

### 1. Perbaiki outgoing auto-approve

Hapus logic:

```text
IsBlocked → return
```

dan ganti dengan policy yang benar.

### 2. Bedakan human outgoing vs automation

Gunakan:

```text
MessageOrigin / ExecutionSource
```

### 3. Peer resolver dengan AccessHash

Gunakan persistent peer storage.

### 4. Jangan swallow RPC errors

Minimal:

```text
Block failure
Unblock failure
Delete failure
Send failure
```

harus terukur dan tercatat.

### 5. Block state harus reliable

```text
DB BLOCKED
+
Telegram BLOCKED
```

bukan hanya salah satunya.

### 6. Idempotency berdasarkan message ID

```text
user_id + message_id
```

### 7. Race-safe warning storage

Jangan JSON read-modify-write jika bisa menggunakan tabel normalized.

---

# 63. P1 — parity + production

```text
bot bypass
verified bypass

.pmpermit unblock
.pmpermit list
.pmpermit test
.pmpermit status

approve/block callback buttons

PM logging integration

auto report spam optional

archive

custom message

localization

media deletion

rate limiting

FloodWait handling

retry policy

health state

metrics
```

---

# 64. P2 — advanced

```text
PMPermit picture
inline PMPermit
rich owner dashboard
approval expiration UI
per-user custom policy
trusted users
temporary approval
one-message approval
challenge buttons
anti-burst scoring
reputation
```

---

# 65. Target architecture yang saya rekomendasikan

Saya **tidak menyarankan rewrite** PMPermit.

Upgrade menjadi:

```text
                 Telegram Update
                        │
                        ▼
              ┌──────────────────┐
              │ PM Classifier     │
              └────────┬─────────┘
                       │
              PMContext
                       │
                       ▼
             ┌──────────────────┐
             │ PMPermit Engine   │
             └────────┬─────────┘
                      │
          ┌───────────┼─────────────┐
          │           │             │
       Policy       State         Delivery
          │           │             │
          ▼           ▼             ▼
       ALLOW       SQLite        Telegram RPC
       WARN        Cache         Retry
       BLOCK       History       RateLimit
       DROP
          │
          ▼
       PM Event
          │
    ┌─────┼──────┐
    ▼     ▼      ▼
 UserLog Metrics Callback
```

Ini jauh lebih cocok dengan arsitektur Goultroid daripada sekadar menyalin `pmpermit.py` Ultroid.

---

# 66. Kesimpulan paling penting

**Ultroid PMPermit bukan benchmark yang harus dicopy 1:1.**

Yang harus diambil dari Ultroid:

```text
feature parity
UX
commands
approval workflow
auto approve
logging
archive
inline UI
spam handling
```

Tetapi Goultroid harus membuat versi yang lebih kuat:

```text
typed state machine
durable state
Telegram state reconciliation
idempotency
execution-origin awareness
peer/access-hash resolution
retry
rate limit
metrics
audit event
```

Jadi target akhirnya bukan:

> “Goultroid punya `.pmpermit` seperti Ultroid.”

Melainkan:

> **“Goultroid mempunyai production-grade Private Message Access Control subsystem yang secara fungsional kompatibel dengan PMPermit Ultroid tetapi arsitekturnya lebih deterministic, durable, testable, dan aman.”**

Dan dari audit sekarang, **bug paling kritis yang harus diperbaiki terlebih dahulu adalah outgoing auto-approve + distinction antara human-originated outgoing message dan automated outgoing message**. Kalau itu tidak diperbaiki, automation lain di Goultroid berpotensi secara tidak sengaja mengubah security state PMPermit user.

Telegram sendiri memang memisahkan konsep outgoing message melalui `message.out`, jadi distinction tersebut sebaiknya dimodelkan di level event/dispatcher Goultroid, bukan ditebak dari string pesan. ([Telegram][2])

**Research ini juga menunjukkan bahwa dokumen `docs/4.7_pmpermit_fix_2026-09-06.md` yang sekarang sudah ada baru menyelesaikan sebagian bug P0; belum merupakan blueprint PMPermit lengkap.**

[TeamUltroid/Ultroid — PMPermit source](https://github.com/TeamUltroid/Ultroid/blob/main/plugins/pmpermit.py?utm_source=chatgpt.com)
[Goultroid repository](https://github.com/inipew/goultroid?utm_source=chatgpt.com)

[1]: https://core.telegram.org/method/contacts.block?utm_source=chatgpt.com "contacts.block"
[2]: https://core.telegram.org/constructor/message?utm_source=chatgpt.com "message"
[3]: https://core.telegram.org/method/messages.deleteMessages?utm_source=chatgpt.com "messages.deleteMessages"
[4]: https://core.telegram.org/method/messages.reportSpam?utm_source=chatgpt.com "messages.reportSpam"
[5]: https://core.telegram.org/method/account.reportPeer?utm_source=chatgpt.com "account.reportPeer"
[6]: https://core.telegram.org/api/folders?utm_source=chatgpt.com "Dialog folders"
[7]: https://core.telegram.org/api/block?utm_source=chatgpt.com "Blocked users"
[8]: https://core.telegram.org/constructor/user?utm_source=chatgpt.com "user"
[9]: https://core.telegram.org/constructor/peerUser?utm_source=chatgpt.com "peerUser"
[10]: https://core.telegram.org/api/privacy?utm_source=chatgpt.com "Privacy settings"
[11]: https://core.telegram.org/api/paid-messages?utm_source=chatgpt.com "Paid messages"
[12]: https://t.me/s/catuserbot17?before=351&utm_source=chatgpt.com "Catuserbot – Telegram"
