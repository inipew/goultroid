Ya — **kalau yang dimaksud perilaku AFK saat ini**, pemahamanmu benar: AFK pada dasarnya **baru mencatat state bahwa user sedang AFK**. Tetapi ada bagian penting: **kode AFK memang sudah memiliki jalur untuk mendeteksi pesan masuk dan membalas**, jadi bukan sekadar “set flag lalu tidak melakukan apa-apa”.

Dalam audit sebelumnya saya **sudah menganalisis bagian ini**, termasuk:

* saat user lain mengirim DM;
* saat user lain **mention/tag** user AFK;
* saat user lain **reply ke pesan user AFK**;
* cooldown balasan AFK;
* auto-unAFK ketika owner mengirim pesan;
* masalah `@username` vs `MessageEntityMentionName`;
* bot/self/service-message exclusion;
* interaksi AFK dengan PMPermit, blacklist, filters;
* dan masalah paling penting: pesan yang dikirim scheduler/broadcast/addon/automation bisa dianggap sebagai aktivitas owner.

Jadi ada perbedaan antara **“fitur AFK di database”** dan **“AFK responder”**.

### Kondisi yang saya pahami dari kode saat ini

Alurnya secara konsep:

```text
.afk [reason]
      ↓
SetAFK(true)
      ↓
User dianggap AFK
      ↓
Incoming message
      ↓
cek apakah DM / mention / reply-to-owner
      ↓
kalau cocok
      ↓
kirim pesan AFK
```

Artinya, **`.afk` sendiri tidak otomatis mengirim warning/pesan kepada siapa pun**. Pesan baru dikirim ketika ada incoming message yang memenuhi kondisi responder.

Contohnya:

```text
User A:
.afk lagi tidur

Database:
is_afk = true
reason = "lagi tidur"
```

Tidak ada pesan broadcast:

> “Saya sedang AFK.”

Lalu:

```text
User B → DM:
"Halo"
```

Baru AFK handler seharusnya merespons:

> “Saya sedang AFK: lagi tidur.”

Begitu pula di group:

```text
User B:
@username_user_afk halo
```

atau:

```text
User B:
(reply ke pesan user AFK)
"Bro?"
```

seharusnya memicu responder.

---

### Tetapi ada masalah besar yang saya temukan

Yang perlu dibedakan adalah:

> **“kode memiliki mekanisme responder”**

vs

> **“responder tersebut pasti berjalan benar pada semua kondisi.”**

Dan justru **bagian kedua yang saya audit cukup dalam**.

Saya menemukan beberapa titik yang bisa membuat user melihat gejala:

> “Saya sudah `.afk`, tapi ketika ditag tidak ada balasan.”

Terutama:

1. **Mention detection belum lengkap**
   Implementasi saat ini tidak menangani seluruh bentuk mention Telegram secara robust, terutama perbedaan `MessageEntityMentionName` dan `MessageEntityMention`.

2. **Reply detection melakukan RPC `GetMessage`**
   Kalau resolusi pesan/reply gagal, AFK bisa tidak merespons.

3. **AFK membuat `InputPeer` sendiri**
   Ini berhubungan langsung dengan masalah `access_hash` yang sebelumnya kita identifikasi sebagai salah satu masalah arsitektur GoUltroid. Seharusnya AFK memakai `PeerResolver` terpusat.

4. **Cooldown**
   Cooldown saat ini berbasis sender, bukan kombinasi:

```text
(chat_id, sender_id)
```

sehingga perilakunya bisa tidak sesuai ekspektasi.

5. **AFK + PMPermit / blacklist / filters belum mempunyai arbitration layer yang benar**
   Jadi beberapa automation bisa saling bertabrakan.

6. **Bot/service/automation messages**
   Ini sangat penting. Pesan yang dihasilkan scheduler, broadcast, addon, assistant, dsb. bisa dianggap sebagai aktivitas owner kalau hanya melihat `msg.Out`.

7. **Auto-unAFK belum atomic**
   Dua pesan outgoing bersamaan bisa sama-sama melihat user masih AFK dan sama-sama mencoba melakukan transition.

---

## Jadi koreksinya terhadap penjelasan saya sebelumnya

**Ya, bagian responder AFK sudah saya analisa.**

Namun kalau maksudmu:

> “Apakah audit sebelumnya sudah secara eksplisit memverifikasi bahwa AFK benar-benar memberikan warning ketika orang mention/chat saya?”

**Belum sampai level verifikasi end-to-end seperti itu.**

Audit sebelumnya lebih fokus pada **arsitektur dan correctness dari mekanisme responder**, bukan sekadar memastikan:

```text
.afk
→ orang DM
→ bot membalas
```

dengan integration test nyata dari dispatcher → hook → AFK → peer resolution → Telegram send.

Dan menurut saya **ini justru harus kita audit ulang secara khusus**, karena AFK adalah fitur event-driven dan sangat mudah terlihat “aktif di database” tetapi sebenarnya responder-nya tidak pernah sampai ke Telegram.

### Target behavior yang seharusnya

Saya akan anggap AFK production-grade kalau alurnya seperti ini:

```text
.afk tidur
    │
    ├── DB: AFK=true
    └── Memory: AFK=true
              │
              ▼
        Incoming message
              │
       ┌──────┴──────┐
       │             │
      DM           Group
       │             │
   respond       mention/reply?
                     │
              ┌──────┴──────┐
              │             │
             yes            no
              │             │
           respond        ignore
```

Dengan tambahan:

```text
Owner manual message
        ↓
atomic UnAFK
        ↓
AFK=false
```

tetapi:

```text
Scheduler message
Broadcast message
Addon message
AI/automation message
AFK-generated message
        ↓
JANGAN unAFK
```

Dan idealnya:

```text
AFK response
    ↓
cooldown(chat_id + sender_id)
    ↓
maksimal 1 response / periode
```

Jadi **jawaban singkatnya: bukan hanya set status AFK**. Kode sudah mempunyai mekanisme responder, dan mekanisme itu sudah saya masukkan ke audit sebelumnya. **Tetapi saya belum menganggap jalur responder tersebut terbukti end-to-end benar**, dan dari audit kode ada beberapa alasan kuat kenapa mention/DM bisa tidak menghasilkan pesan.

Kalau tujuanmu sekarang adalah mencari **“kenapa `.afk` saya set tetapi orang mention/chat saya tidak mendapatkan pesan AFK?”**, itu sebaiknya kita lakukan sebagai **audit execution path khusus AFK**, dari dispatcher → hook registration → event filtering → AFK handler → mention/reply detection → peer resolution → `SendMessage`, bukan hanya membaca `afk.go`.
