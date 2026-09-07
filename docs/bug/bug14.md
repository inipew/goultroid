Betul. **Jangan langsung membangun framework menu baru. Kita harus memastikan dulu kenapa implementasi callback/inline yang sekarang tidak menghasilkan menu/button sama sekali.**

Dan dari struktur kode yang saya cek, **bot token sangat mungkin merupakan salah satu penyebab utama**, tetapi ada masalah yang lebih fundamental:

> **Callback service yang sekarang pada dasarnya sudah punya mesin untuk MEMPROSES callback, tetapi belum terlihat adanya layer yang benar-benar MEMBUAT dan MENGIRIM menu Assistant Bot seperti Ultroid.**

Contohnya, `callback.Router` memang sudah menyediakan routing namespace, state, authorization, rate limit, dan dispatch callback.  `CallbackContext` juga sudah memiliki `Edit`, `EditMarkup`, `Delete`, serta pemisahan normal-message vs inline-message.

Jadi masalahnya kemungkinan bukan sekadar "button callback rusak".

### Yang kemungkinan terjadi sekarang

Arsitekturnya kira-kira:

```text
Telegram Update
      │
      ▼
CallbackQuery
      │
      ▼
callback.Router
      │
      ├── parse callback
      ├── resolve state
      ├── auth
      ├── rate limit
      └── invoke handler
```

Tetapi yang kita butuhkan untuk UX Ultroid:

```text
/start
   │
   ▼
Assistant Bot
   │
   ▼
SendMessage
   │
   ├── text
   └── InlineKeyboard
             │
             ▼
       user tekan button
             │
             ▼
       CallbackQuery
             │
             ▼
       callback.Router
             │
             ▼
       handler
             │
             ▼
       edit menu
```

**Kalau bagian `Assistant Bot → SendMessage + InlineKeyboard` belum ada, wajar tidak ada menu yang muncul.**

Dan ini berbeda dengan inline-query engine. Inline query bukan mekanisme yang otomatis menampilkan menu `/start`.

### Soal Bot Token

Ya, kita harus cek ini **sebelum melakukan perubahan arsitektur**.

Ultroid Assistant yang kita lihat sebelumnya memang menggunakan **bot account**, bukan user account yang menjalankan userbot. Polanya adalah:

```text
User account / GoUltroid
          │
          │ memiliki / mengontrol
          ▼
     Assistant Bot
          │
          ▼
   /start + inline keyboard
          │
          ▼
    callback queries
```

Kalau GoUltroid belum:

* memiliki konfigurasi bot token,
* membuat bot client,
* login/authenticate bot,
* menjalankan bot update loop,
* menghubungkan bot update ke dispatcher,
* mendaftarkan callback router ke dispatcher,
* dan mengirim `/start` response,

maka **callback implementation sehebat apa pun tidak akan membuat tombol muncul.**

---

## Jadi urutan yang benar sekarang

Saya justru menyarankan kita **stop dulu desain framework baru**.

Audit/fix harus dimulai dari:

### 1. Apakah GoUltroid sekarang punya Bot Token?

Cari:

```text
BOT_TOKEN
BOT_TOKEN
assistant bot
bot token
NewBot
AuthBot
tg.Bot
```

dan lihat config → startup → Telegram client.

### 2. Apakah Bot Client benar-benar dibuat?

Harus ada sesuatu secara konseptual seperti:

```go
botClient := ...
botClient.Run(...)
```

bukan hanya:

```go
userClient := ...
userClient.Run(...)
```

Karena MTProto user session dan bot session adalah **dua identitas/account yang berbeda**.

### 3. Apakah bot menerima `/start`?

Harus ada jalur:

```text
Bot Update
   ↓
NewMessage
   ↓
/start handler
   ↓
Assistant.Start()
   ↓
SendMessage()
```

Kalau `/start` sendiri tidak menghasilkan apa-apa, kita belum perlu menyentuh callback router.

### 4. Apakah `InlineKeyboardMarkup` benar-benar dibuat?

Harus ada sesuatu seperti:

```go
&tg.ReplyInlineMarkup{
    Rows: [][]tg.KeyboardButtonClass{
        ...
    },
}
```

atau abstraction yang akhirnya menghasilkan object Telegram tersebut.

### 5. Apakah callback data cocok dengan parser sekarang?

Kode sekarang secara eksplisit mengharapkan format:

```text
v1:<namespace>:<action>:<opaque-id>
```

dan membatasi payload sampai 64 byte.

Jadi kalau menu dibuat dengan:

```text
settings
```

atau:

```text
settings_api
```

tetapi router mengharapkan:

```text
v1:settings:nav:<opaque-id>
```

button akan muncul tetapi callback-nya tidak bekerja.

### 6. Apakah callback update dari BOT masuk ke EventBus?

Ini juga krusial.

Kita harus memastikan:

```text
Telegram Bot
     ↓
Update dispatcher
     ↓
core.CallbackQueryEvent
     ↓
EventBus
     ↓
callback.Router
```

Bukan:

```text
User client → EventBus
Bot client  → tidak terhubung
```

Kalau begitu menu bisa muncul, tetapi ketika button ditekan tidak terjadi apa-apa.

---

# Dan ada satu indikasi penting dari kode sekarang

Implementasi callback **sudah cukup jauh**.

`CallbackContext` sekarang sudah menangani perbedaan:

```text
normal message
      ↓
EditMessageMarkup

inline message
      ↓
EditInlineBotMessage
```

serta mempunyai `Target`, `Origin`, state, answer, delete, dan lain-lain.

Router juga sudah mempunyai:

```text
namespace handler
state lookup
expiration
authorization
chat scope
message scope
single-use
rate limiting
timeout
```

Artinya **saya tidak akan membuang subsystem ini dan mulai dari nol**.

Yang perlu kita cari sekarang adalah:

> **"Siapa yang seharusnya membuat menu dan mengirim message pertama?"**

Karena callback router tidak bertanggung jawab untuk membuat menu pertama.

---

## Jadi jawaban pendeknya

**Ya, benar: perbaiki yang ada dulu.**

Dan:

**Ya, tidak adanya Bot Token/Assistant Bot implementation sangat mungkin menjadi alasan menu Ultroid-style tidak muncul.**

Tetapi saya belum mau menyimpulkan "100% karena bot token" sebelum memeriksa jalur lengkap:

```text
config
 ↓
bot token
 ↓
bot client
 ↓
bot authentication
 ↓
bot update loop
 ↓
dispatcher
 ↓
/start
 ↓
assistant/menu sender
 ↓
InlineKeyboard
 ↓
CallbackQuery
 ↓
EventBus
 ↓
callback.Router
```

Karena dari kode callback yang sudah kita lihat, **engine callback-nya sendiri bukan bagian yang pertama kali harus dicurigai**.

Kalau Anda setuju, langkah berikutnya paling tepat adalah **saya audit langsung jalur Bot Token → Bot Client → Dispatcher → `/start` → keyboard → Callback Router di `goultroid`**, lalu kita buat daftar konkret:

**[BROKEN] / [MISSING] / [PARTIAL] / [WORKING]**

dan baru setelah itu kita fix fondasinya.
