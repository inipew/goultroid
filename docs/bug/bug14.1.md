Sudah saya **fetch ulang kode `main` terbaru** dan cek jalur yang relevan. Hasilnya sekarang jauh lebih jelas:

## Kesimpulan utama

**Ya, `BOT_TOKEN` memang diperlukan untuk menu Assistant Bot.**

Tetapi **kode GoUltroid sekarang sebenarnya SUDAH mengimplementasikan Assistant Bot + menu + inline button + callback handler.** Jadi masalahnya bukan lagi "belum ada implementasi bot".

Masalahnya adalah:

> **Kalau `BOT_TOKEN` belum diisi, seluruh Assistant Bot tidak dibuat sama sekali.**

Di `config.Load()`:

```text
BOT_TOKEN → cfg.BotToken
```

dan mode otomatis menjadi:

```text
BOT_TOKEN ada    → userbot+assistant
BOT_TOKEN kosong → userbot
```

Lalu di `wiring_telegram.go`, Assistant hanya dibuat kalau:

```go
if cfg.BotToken != "" {
    bot := assistant.NewBotClient(...)
    ...
    assistantClient = bot
}
```

Kalau token kosong, `assistantClient` tetap `nil`.

Kemudian lifecycle hanya menjalankan Assistant kalau:

```go
if a.assistant != nil {
    go a.assistant.Start(ctx)
}
```

Jadi tanpa token:

```text
BOT_TOKEN kosong
      ↓
assistantClient = nil
      ↓
assistant.Start() tidak pernah dipanggil
      ↓
tidak ada Bot account
      ↓
tidak ada /start Assistant
      ↓
tidak ada menu/button
```

---

# Tapi ada hal yang lebih penting

Saya sebelumnya mengatakan kemungkinan "belum ada layer pembuat menu".

**Setelah fetch ulang, itu ternyata sudah ada.**

Sekarang kode memiliki:

```text
internal/assistant/
├── client.go
├── handler.go
├── menu.go
├── bridge.go
└── service_adapter.go
```

Dan `menu.go` benar-benar membuat menu dengan button.

Contohnya `/start` menghasilkan:

```text
⚙️ Settings
📚 Help / Modules

📊 System Status
🏓 Ping

🔒 Close Menu
```

dengan callback data masing-masing.

Jadi **menu-nya sudah ada.**

---

# Bahkan Bot Client-nya sudah lengkap

`assistant.BotClient.Start()` sekarang sudah:

### 1. Membuat Telegram client khusus bot

```text
telegram.NewClient(
    appID,
    appHash,
    ...
)
```

### 2. Authenticate menggunakan BOT_TOKEN

```text
client.Auth().Bot(ctx, c.botToken)
```

### 3. Register `/start`, `/help`, `/ping`, `/alive`, `/status`

### 4. Register callback:

```text
OnBotCallbackQuery
```

### 5. Register inline callback:

```text
OnInlineBotCallbackQuery
```

### 6. Register inline query:

```text
OnBotInlineQuery
```

### 7. Register inline-send feedback

Semua itu memang ada di `assistant/client.go`.

Jadi implementasinya sudah **jauh lebih maju daripada yang saya simpulkan sebelumnya.**

---

# Jalur `/start` sekarang

Kalau `BOT_TOKEN` ada, jalurnya sebenarnya sudah:

```text
.env
 │
 ├── APP_ID
 ├── APP_HASH
 ├── PHONE
 └── BOT_TOKEN
       │
       ▼
 config.Load()
       │
       ▼
 cfg.BotToken != ""
       │
       ▼
 NewBotClient()
       │
       ▼
 App.startBackgroundServices()
       │
       ▼
 assistant.Start()
       │
       ▼
 Auth().Bot(BOT_TOKEN)
       │
       ▼
 Bot authenticated
       │
       ▼
 Telegram user sends /start
       │
       ▼
 handleBotCommand()
       │
       ▼
 RenderStartMenu()
       │
       ▼
 render.ToTelegram()
       │
       ▼
 SendMessageWithMarkup()
       │
       ▼
 ┌───────────────────────────┐
 │ 🤖 GoUltroid Assistant    │
 │                           │
 │ [⚙️ Settings] [📚 Help]  │
 │ [📊 Status]   [🏓 Ping]  │
 │ [🔒 Close Menu]           │
 └───────────────────────────┘
```

**Jadi secara desain, memang sudah seharusnya muncul.**

---

# Ada satu hal yang harus kita cek berikutnya

Saya menemukan sesuatu yang justru lebih mencurigakan.

Di `wiring_telegram.go`, Bot dibuat dan callback handler didaftarkan:

```go
assistantHandler := assistant.NewHandler(bot, bot.StartTime())

core.callbackRouter.Register(assistantHandler)
```

Dan handler memang menangani:

```text
assistant:start
assistant:status
assistant:ping
assistant:close
```

**Tetapi menu `Settings` dan `Help / Modules` menggunakan namespace berbeda:**

```text
settings
help
```

dari `RenderStartMenu()`.

Sedangkan yang secara eksplisit kita lihat baru diregister:

```text
assistant
```

Ini berarti:

```text
[⚙️ Settings]
       ↓
v1:settings:nav:noop
       ↓
callback router
       ↓
handler namespace = settings
       ↓
???
```

Dan:

```text
[📚 Help / Modules]
       ↓
v1:help:home:noop
       ↓
handler namespace = help
       ↓
???
```

**Ini sangat mungkin merupakan bug berikutnya.**

Button bisa muncul, tetapi ketika ditekan bisa mendapatkan:

```text
HANDLER_NOT_FOUND
```

karena handler `settings` / `help` belum diregister.

---

# Ada bug lain yang juga langsung terlihat

`EncodeCallbackData()` digunakan seperti:

```go
callback.EncodeCallbackData(
    "settings",
    callback.ActionNav,
    callback.ActionNoop,
)
```

Artinya:

```text
v1:settings:nav:noop
```

Tetapi `noop` di sini diperlakukan sebagai `opaqueID`.

Sementara state mechanism callback didesain untuk opaque ID dan router mempunyai state resolution. `ParseCallbackData()` sendiri menerima payload tersebut sebagai opaque ID.

Ini memang sengaja kelihatannya diberi special-case `noop`, tetapi kita perlu audit router bagian dispatch setelah ini untuk memastikan:

```text
noop
```

tidak dianggap state ID yang harus ada di StateStore.

---

# Jadi untuk pertanyaan Anda sekarang

### Apakah tidak muncul menu karena BOT_TOKEN belum ada?

**Kalau Anda belum mengisi `BOT_TOKEN`: YA, pasti.**

Karena:

```text
BOT_TOKEN kosong
→ BotClient tidak dibuat
→ Assistant tidak Start
→ /start Assistant tidak ada
→ menu tidak mungkin muncul
```

Ini bukan dugaan lagi; kode sekarang memang melakukan itu.

### Kalau BOT_TOKEN sudah ada tetapi menu tetap tidak muncul?

Maka **bukan lagi masalah callback router**.

Kita harus cek:

1. `BOT_TOKEN` benar-benar masuk environment.
2. `Auth().Bot()` berhasil.
3. Bot `Self()` berhasil.
4. Bot menerima `UpdateNewMessage`.
5. `/start` masuk `handleBotCommand()`.
6. `extractSenderID()` berhasil.
7. `e.Users[senderID]` punya `AccessHash`.
8. `SendMessageWithMarkup()` berhasil.
9. `render.ToTelegram()` menghasilkan markup.
10. Bot account memang sudah bisa menerima DM dari user.

Dan saya melihat **potensi masalah #7 yang cukup serius**:

```go
if u, ok := e.Users[senderID]; ok && u != nil && u.AccessHash != 0 {
    peer = &tg.InputPeerUser{...}
}

if peer == nil {
    logger.Warn("assistant: sender user access hash missing, command ignored")
    return nil
}
```

Jadi kalau `tg.Entities.Users` tidak menyediakan user/access hash pada update `/start`, **command akan diam-diam diabaikan**. Ini sangat layak kita uji/fix.

---

## Jadi jangan bikin ulang framework dulu

Setelah fetch terbaru ini, saya ubah rekomendasi saya:

**Phase 1 — Fix existing implementation dulu:**

```text
[1] BOT_TOKEN / Assistant startup
        ↓
[2] /start delivery
        ↓
[3] keyboard rendering
        ↓
[4] callback delivery
        ↓
[5] callback routing
        ↓
[6] Settings / Help handler
        ↓
[7] state / opaque ID
        ↓
[8] inline query
        ↓
[9] baru audit UX/parity Ultroid
```

Dan **belum perlu membuat `internal/interaction/` baru**. Kita harus memaksimalkan dan memperbaiki `internal/assistant`, `callback`, `inline`, dan wiring yang sudah ada.

Kalau Anda **sudah mengisi `BOT_TOKEN` tetapi `/start` tetap tidak menampilkan menu**, saya sarankan langkah berikutnya saya fetch ulang **`assistant/client.go` bagian lengkap + `service_adapter.go` + `ui/render` + dispatcher/event wiring + callback Dispatch**, lalu kita trace **exactly dari `/start` sampai `MessagesSendMessage`** untuk menemukan titik putusnya.
