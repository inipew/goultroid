Setuju. **Lebih baik kita stop menambal implementasi sekarang dan reset desain Assistant interaction layer dari nol**, tetapi tetap mempertahankan bagian transport/auth yang memang sudah benar.

Saya sudah cek ulang implementasi TeamUltroid di `assistant/`, terutama `start.py`, `__init__.py`, dan callback decorator. Ultroid sendiri sebenarnya cukup sederhana: menu dibangun sebagai `Button.inline`, callback diarahkan berdasarkan data, handler melakukan `event.edit()`, `event.answer()`, atau `event.delete()`. Contohnya `closeit` memang langsung `event.delete()`, sedangkan `mainmenu` mengedit pesan kembali ke menu utama.  Implementasi callback mereka juga memang menjadi abstraction tersendiri melalui decorator `@callback(...)`.

Yang penting: **kita jangan menyalin kelemahan Ultroid. Kita jadikan behavior Ultroid sebagai compatibility/reference layer, lalu desain GoUltroid lebih ketat.**

### Yang saya sarankan untuk reset

Bukan:

> `Handler -> switch action -> ctx.Edit/Delete`

Tetapi:

```text
Telegram Update
      │
      ▼
Assistant Update Normalizer
      │
      ▼
Callback Transaction
      │
      ├── Validate query
      ├── Validate target
      ├── Validate authorization
      ├── Resolve peer
      ├── Resolve message target
      ├── Answer callback
      │
      ▼
Assistant Action
      │
      ├── Render screen
      ├── Edit message
      ├── Delete message
      ├── Toast/Alert
      └── Send message
      │
      ▼
Telegram RPC
      │
      ▼
Outcome + structured error
```

### Prinsip utamanya

**1. Callback target harus menjadi first-class object**

Jangan lagi mengandalkan `ctx.Target.Peer` yang bisa saja salah/nil.

Misalnya:

```go
type Target struct {
    Kind        TargetKind
    Peer        tg.InputPeerClass
    MessageID   int
    InlineID    tg.InputBotInlineMessageIDClass
    ChatID      int64
    ChatInstance int64
}
```

Kemudian:

```go
type TargetKind uint8

const (
    TargetMessage TargetKind = iota
    TargetInline
)
```

Sehingga operasi:

```go
target.Edit(...)
target.Delete(...)
target.Answer(...)
```

selalu tahu **jenis target Telegram yang sebenarnya**.

---

### 2. Pisahkan Message Callback dan Inline Callback sejak awal

Ini sangat penting.

Ultroid bisa terlihat sederhana karena Telethon menyembunyikan banyak detail tersebut. Go + gotd tidak boleh menganggap keduanya sama.

Normal callback:

```text
UpdateBotCallbackQuery
        ↓
messages.editMessage
messages.deleteMessages
```

Inline callback:

```text
UpdateInlineBotCallbackQuery
        ↓
messages.editInlineBotMessage
```

Inline message **tidak punya peer/message ID biasa**.

Jadi jangan sampai abstraction kita memaksa keduanya mempunyai:

```go
Peer + MessageID
```

---

### 3. Peer resolution dibuat deterministic

Ini salah satu sumber masalah kita sekarang.

Kita akan punya satu resolver:

```go
type PeerResolver interface {
    Resolve(
        ctx context.Context,
        peer tg.PeerClass,
        entities tg.Entities,
    ) (tg.InputPeerClass, error)
}
```

Dengan aturan eksplisit:

```text
PeerUser
 ├─ entity access_hash
 ├─ cache
 └─ resolve

PeerChat
 └─ InputPeerChat

PeerChannel
 ├─ entity access_hash
 ├─ cache
 └─ InputPeerChannel

unknown
 └─ ErrUnsupportedPeer
```

Tidak ada fallback diam-diam.

---

### 4. Message identity tidak boleh diperlakukan sebagai global ID

Ini juga penting karena kita sudah menemukan indikasi `MESSAGE_ID_INVALID`.

`msg_id` Telegram itu **context-dependent**.

Jadi:

```go
MessagesGetMessages(InputMessageID{ID: msgID})
```

bukan abstraction yang cukup untuk semua jenis peer.

Kita harus membedakan:

```text
message in private/chat
message in channel
inline message
```

dan operasi masing-masing.

---

### 5. Callback lifecycle dibuat transactional

Saya ingin behavior seperti:

```text
receive callback
      │
      ├── validate
      │
      ├── answer callback
      │
      ├── execute action
      │
      └── record outcome
```

Dengan satu invariant:

> **Setiap callback query harus di-answer maksimal sekali.**

Jadi tidak ada lagi situasi:

```text
Router auto-answer
        +
Handler answer
        +
error handler answer
```

yang berpotensi menghasilkan race / double-answer.

---

### 6. `Close` tidak boleh menggunakan generic Delete yang ambigu

Untuk close:

```go
err := interaction.Acknowledge(ctx, ...)
if err != nil {
    return err
}

return interaction.DeleteMessage(ctx)
```

Tetapi `DeleteMessage()` sendiri harus tahu:

```text
Message target?
 ├── private/chat → messages.deleteMessages
 └── channel      → channels.deleteMessages

Inline target?
 └── bukan delete message biasa
```

Dengan begitu kita tidak perlu lagi membuat patch khusus seperti `deleteCallbackMessage()` hanya karena generic service ternyata tidak cukup.

---

### 7. Render dan behavior dipisahkan

Misalnya:

```go
type Screen struct {
    ID      ScreenID
    Text    string
    Buttons [][]Button
}
```

Lalu:

```go
type MenuController struct {
    ...
}

func (m *MenuController) Start(...)
func (m *MenuController) Settings(...)
func (m *MenuController) Help(...)
func (m *MenuController) Status(...)
func (m *MenuController) Close(...)
```

Handler Telegram hanya menjadi adapter:

```text
Telegram callback
       ↓
decode action
       ↓
MenuController
       ↓
Screen
       ↓
Interaction
```

Jadi menu tidak bergantung pada gotd secara langsung.

---

### 8. Action tidak lagi `switch` besar

Daripada:

```go
switch ctx.Action {
case "start":
case "settings":
case "help":
case "status":
case "ping":
case "close":
}
```

lebih baik:

```go
type ActionHandler func(context.Context, *Interaction) error

handlers := map[string]ActionHandler{
    "start":    ...
    "settings": ...,
    "help":     ...,
    "status":   ...,
    "ping":     ...,
    "close":    ...,
}
```

Kemudian validasi action dilakukan sebelum handler.

Ini jauh lebih scalable ketika nanti menu menjadi:

```text
main
 ├── settings
 │    ├── API
 │    ├── PM Bot
 │    ├── Alive
 │    ├── Features
 │    └── ...
 ├── status
 ├── help
 └── close
```

Ultroid sendiri sudah menunjukkan pola menu bertingkat seperti Settings → API Keys / PM Bot / Alive / PMPermit / Features / VC Song Bot dan Back.

---

## Yang akan kita pertahankan dari Ultroid

Kita **tidak perlu meng-copy source Ultroid**, cukup behavior contract-nya:

* `/start`
* menu utama
* inline keyboard
* callback navigation
* Back
* Settings
* Status
* Close
* callback toast/alert
* owner-only actions
* DM-only behavior
* menu state

Ultroid memang menggunakan inline callback sebagai mekanisme utama untuk Assistant UI. Dokumentasinya juga secara eksplisit mendefinisikan `@callback` untuk callback events dan `in_pattern` untuk inline assistant updates. ([GitHub][1])

## Yang akan kita buat lebih baik

Target GoUltroid:

```text
Assistant
├── transport/
│   ├── client.go
│   ├── updates.go
│   └── auth.go
│
├── interaction/
│   ├── interaction.go
│   ├── target.go
│   ├── callback.go
│   ├── answer.go
│   ├── edit.go
│   ├── delete.go
│   └── errors.go
│
├── peer/
│   ├── resolver.go
│   ├── cache.go
│   └── errors.go
│
├── menu/
│   ├── controller.go
│   ├── screen.go
│   ├── actions.go
│   └── buttons.go
│
├── callback/
│   ├── router.go
│   ├── authorization.go
│   ├── lifecycle.go
│   └── validation.go
│
└── commands/
    ├── start.go
    ├── help.go
    ├── ping.go
    ├── alive.go
    └── status.go
```

Dengan boundary:

```text
gotd
 ↓
transport
 ↓
normalizer
 ↓
interaction
 ↓
router
 ↓
menu/controller
 ↓
renderer
```

**Tidak boleh ada menu handler yang langsung memanggil raw MTProto RPC.**

---

## Dan yang paling penting: kita test dari failure case dulu

Sebelum implementasi dianggap selesai, kita harus punya test untuk:

* normal callback
* inline callback
* private chat
* basic group
* channel
* missing access hash
* stale access hash
* unknown peer
* invalid message ID
* deleted message
* expired callback
* duplicate callback
* double answer
* edit gagal
* delete gagal
* callback timeout
* `/start` berulang
* Close setelah message sudah terhapus
* Close pada message yang tidak bisa dihapus
* Back dari setiap submenu
* user non-owner mencoba owner action
* callback data malformed
* callback data terlalu panjang
* concurrent callback pada message yang sama

Dan terutama:

> **error Telegram tidak boleh menyebabkan router/event loop mati.**

---

### Kesimpulan

Jadi saya setuju dengan keputusan Anda: **jangan lanjut patch `handler.go`/`delete.go` sedikit-sedikit.**

Kita jadikan implementasi sekarang sebagai **reference terhadap apa yang sudah ada**, lalu rebuild Assistant interaction subsystem dengan kontrak yang benar.

Ultroid kita gunakan sebagai **behavioral reference**, bukan sebagai architectural template. Source Ultroid sendiri memang memiliki `assistant/start.py` dengan menu/callback sederhana dan `event.delete()` untuk Close.

Dan dari kode GoUltroid sekarang, saya sudah melihat boundary yang memang perlu dibongkar: terutama **callback target resolution, peer/access-hash resolution, message-vs-inline distinction, generic delete/edit abstraction, dan callback lifecycle**.

**Langkah berikutnya sebaiknya bukan langsung coding.** Saya akan audit seluruh `assistant/` GoUltroid + seluruh `assistant/` Ultroid yang relevan, petakan behavior satu-per-satu, lalu kita buat **design/implementation spec Assistant v2** yang menjadi baseline sebelum satu baris kode lama diganti.

[1]: https://github.com/TeamUltroid/Ultroid/wiki/Creating-Plugins?utm_source=chatgpt.com "Creating Plugins · TeamUltroid/Ultroid Wiki · GitHub"
