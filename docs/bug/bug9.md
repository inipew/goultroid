Saya sudah audit ulang `main` terbaru GoUltroid, commit **`ca3410c` / v1.1.25**, dengan fokus pertama pada **seluruh subsystem plugin**, lalu saya tracing ke `Router → Dispatcher → Middleware → Context → Telegram service → DB → lifecycle`, karena banyak bug plugin sebenarnya berasal dari kontrak core, bukan dari handler plugin itu sendiri. Repository saat ini memiliki sekitar **25 plugin package** di `plugins/`, dan seluruh inventory plugin saya cocokkan dengan wiring di `App`.

Kesimpulan utama:

> **GoUltroid sudah jauh lebih matang daripada MVP, tetapi saya belum akan menyebut subsystem plugin production-grade/flawless.**
>
> Masalah terbesar sekarang bukan kekurangan command sederhana, melainkan **plugin architecture, lifecycle, peer resolution, raw message interceptor, caching, access_hash, HTML safety, dan consistency antar plugin**.

Sebagai pembanding, arsitektur plugin Ultroid sendiri memang memusatkan command/event handling melalui decorator seperti `ultroid_cmd`, `in_pattern`, dan `callback`, sehingga lifecycle/registration lebih terintegrasi dengan framework plugin. ([GitHub][1])

---

# 1. Temuan paling kritis

## P0 — Plugin lifecycle masih terpecah

Sekarang `Plugin` hanya punya:

```go
type Plugin interface {
    Name() string
    Commands() []core.Command
    Init() error
}
```

dan lifecycle tambahan hanya berupa optional `Shutdowner` / `ContextShutdowner`.

Masalahnya: **raw message handlers tidak dimiliki oleh Plugin Manager.**

Contoh di `App`:

```go
afkPlugin := afk.New(...)
dispatcher.AddPrioritizedMessageHandler(..., afkPlugin.HandleIncomingMessage)

filtersPlugin := filters.New(...)
dispatcher.AddPrioritizedMessageHandler(..., filtersPlugin.HandleIncomingMessage)

blacklistPlugin := blacklist.New(...)
dispatcher.AddPrioritizedMessageHandler(..., blacklistPlugin.HandleIncomingMessage)
```

sementara plugin command-nya sendiri baru kemudian didaftarkan melalui `Manager`.

Artinya lifecycle sebenarnya:

```text
PluginManager
   └── Commands

Dispatcher
   └── raw message handlers
```

bukan:

```text
PluginManager
   └── Plugin
       ├── Commands
       ├── Message handlers
       ├── Callback handlers
       ├── Inline handlers
       ├── Event handlers
       └── Lifecycle
```

### Dampaknya

Plugin bisa:

* sudah di-`Shutdown()`
* tetapi handler raw-nya masih terdaftar
* dispatcher masih punya reference ke handler
* handler bisa tetap dipanggil jika update masuk
* handler kemudian mengakses DB/service yang sedang shutdown

Dan `Dispatcher.Stop()` saat ini terutama menghentikan worker peer-cache; ia **tidak unregister hook Telegram / membuat dispatcher benar-benar quiescent**.

### Target

Saya sangat menyarankan:

```go
type Plugin interface {
    Metadata() Metadata
    Init(context.Context) error
    Commands() []core.Command

    Shutdown(context.Context) error
}
```

kemudian optional capability interfaces:

```go
type MessageHandlerPlugin interface {
    OnMessage(context.Context, MessageEvent) error
}

type CallbackPlugin interface {
    OnCallback(context.Context, CallbackEvent) error
}

type InlinePlugin interface {
    OnInline(context.Context, InlineEvent) error
}

type ReactionPlugin interface {
    OnReaction(context.Context, ReactionEvent) error
}
```

Manager yang melakukan seluruh registration.

**Jangan lagi `App` mengetahui handler internal masing-masing plugin.**

---

# 2. P0 — Dispatcher belum benar-benar quiesce

`App.Shutdown()` sekarang sudah lebih baik dan urutannya sudah:

```text
Dispatcher
Scheduler
Addon
Plugins
EventBus
Callback/Inline
RateLimiter
DB
Logger
```

yang secara dependency ordering jauh lebih benar.

Tetapi ada lubang:

`Dispatcher.Stop()` belum berarti:

> "tidak ada update baru yang bisa masuk."

`RegisterHooks()` masih terpasang ke `tg.UpdateDispatcher`, sedangkan `Stop()` terutama mengatur peer worker.

Jadi seharusnya ada state:

```go
type DispatcherState uint8

const (
    DispatcherRunning DispatcherState = iota
    DispatcherQuiescing
    DispatcherStopped
)
```

dan di awal `dispatch()`:

```go
if !d.acceptingUpdates.Load() {
    return nil
}
```

Kemudian:

```text
Running
   ↓
Quiescing
   ↓
stop accepting new update
   ↓
drain in-flight commands/interceptors
   ↓
stop workers
   ↓
Stopped
```

Ini sangat penting untuk plugin yang punya DB access.

---

# 3. P0 — Access hash masih menjadi sumber bug plugin

Ini adalah masalah terbesar kedua setelah lifecycle.

Banyak plugin masih membuat peer sendiri.

Contoh AFK:

```go
return &tg.InputPeerUser{
    UserID: p.UserID,
    AccessHash: 0,
}
```

jika entity tidak tersedia.

Blacklist melakukan hal yang sama.

Filters juga sama.

PMPermit juga mempunyai fallback:

```go
&tg.InputPeerUser{UserID: senderID}
```

ketika access hash tidak ditemukan.

Padahal core sudah mempunyai resolver.

### Ini harus dilarang.

Plugin seharusnya **tidak boleh membuat `InputPeerUser` / `InputPeerChannel` dari ID mentah** kecuali:

* `InputPeerSelf`
* `InputPeerChat`
* atau access hash memang diketahui valid.

Buat helper tunggal:

```go
ctx.ResolvePeer(...)
ctx.ResolveUser(...)
ctx.ResolveChat(...)
ctx.ResolveChannel(...)
```

dan:

```go
if accessHash == 0 {
    return ErrAccessHashMissing
}
```

**Jangan pernah mengembalikan `InputPeerUser{AccessHash: 0}` sebagai successful resolution.**

Audit core sebelumnya memang sudah menemukan masalah ini di resolver.

---

# 4. P0 — Basic Group vs Supergroup masih sangat sensitif

Resolver harus membedakan:

```text
-12345
```

dari:

```text
-1001234567890
```

karena:

```text
-12345        → basic group
-10012345...  → channel/supergroup
```

Kalau semua negative ID dianggap channel, plugin:

* ban
* mute
* pin
* purge
* schedule
* send message

bisa gagal dengan `CHANNEL_INVALID`.

Ini sudah dicatat dalam audit core terbaru dan tetap harus dianggap **dependency P0 untuk plugin**.

---

# 5. P0 — Blacklist + Filters melakukan DB query pada setiap message

Ini salah satu masalah performa terbesar.

Blacklist:

```go
words, err := p.db.ListBlacklists(ctx, chatID)
```

dipanggil untuk hampir setiap incoming message.

Filters:

```go
filters, err := p.db.ListFilters(ctx, chatID)
```

juga dipanggil untuk hampir setiap incoming message.

Pada grup aktif:

```text
100 msg/s
×
SQLite query
×
filters
×
blacklist
×
AFK
×
userlog
×
pmpermit
```

akan menjadi bottleneck.

## Solusi

Buat:

```go
type ChatAutomationCache struct {
    Filters
    Blacklist
    Version uint64
    ExpiresAt time.Time
}
```

dengan:

```text
DB
 ↓
cache
 ↓
incoming message
 ↓
O(1)/O(n-small)
```

Saat:

```text
.filter
.stop
.blacklist
.unblacklist
```

cache langsung di-invalidasi.

Lebih bagus lagi:

```text
chat_id
  ↓
atomic immutable snapshot
  ↓
filter matcher
```

Tidak perlu mutex berat pada hot path.

---

# 6. P1 — Regex cache tidak dibatasi

Blacklist:

```go
blacklistRegexCache = make(map[string]*regexp.Regexp)
```

Filters:

```go
filterRegexCache = make(map[string]*regexp.Regexp)
```

keduanya global dan tidak memiliki:

* max entries

* TTL

* eviction

* LRU

Jika user memasukkan ribuan keyword unik:

```text
keyword1
keyword2
...
keyword100000
```

cache akan terus tumbuh.

### Target

Gunakan bounded LRU:

```text
max 4096 regex
```

atau lebih bagus:

```text
per-chat compiled matcher
```

sehingga lifecycle cache mengikuti chat configuration.

---

# 7. P1 — Filter/Blacklist dapat bereaksi terhadap bot lain

`filters.HandleIncomingMessage()` hanya mengecek:

```go
if msg.Out {
    return nil
}
```

tetapi tidak memastikan sender bukan bot.

Blacklist juga demikian.

Ini membuka potensi:

```text
Bot A
 ↓
keyword
 ↓
GoUltroid
 ↓
auto reply
 ↓
Bot A
 ↓
keyword
 ↓
...
```

atau bot lain terus dihapus oleh blacklist.

Harus ada policy:

```go
if senderEntity.Bot {
    return nil
}
```

kecuali plugin secara eksplisit menginginkan bot messages.

---

# 8. P1 — Filter reply belum punya rate-limit

Filter bisa melakukan:

```go
SendMessage(...)
```

untuk setiap incoming message yang match.

Tidak ada:

* per-filter cooldown
* per-chat cooldown
* per-user cooldown
* deduplication

Contoh:

```text
"ping"
"ping"
"ping"
"ping"
"ping"
```

→ 5 RPC.

Lebih buruk jika keyword sangat umum:

```text
hi
```

### Target

Tambahkan:

```go
FilterPolicy {
    CooldownPerChat
    CooldownPerUser
    MaxRepliesPerMinute
}
```

dan default:

```text
1 reply / keyword / chat / 10 sec
```

---

# 9. P1 — Broadcast saat ini membangun peer tanpa access_hash

Broadcast:

```go
&tg.InputPeerUser{UserID: d.ID}
&tg.InputPeerChannel{ChannelID: d.ID}
```

tanpa access hash.

Ini sangat mungkin menyebabkan broadcast gagal untuk user/channel tertentu.

Selain itu:

```go
GetDialogs(ctx, 100)
```

berarti hanya mengambil bounded dialog set, bukan menjamin semua dialog.

### Target

Service harus mengembalikan:

```go
type DialogTarget struct {
    Peer tg.InputPeerClass
    Type DialogType
}
```

bukan hanya:

```go
ID
Type
Title
```

Kemudian broadcast cukup:

```go
targets = append(targets, dialog.Peer)
```

dan pagination harus dilakukan sampai:

```text
all dialogs
```

atau explicit user-configured limit.

---

# 10. P1 — Broadcast cancellation sudah bagus, tetapi progress architecture masih kurang

Service broadcast sekarang sudah jauh lebih baik:

* satu active broadcast
* cancellation context
* FloodWait handling
* retry
* progress callback

Tetapi UX bisa ditingkatkan menjadi:

```text
Broadcast
 ├── total
 ├── sent
 ├── failed
 ├── skipped
 ├── flood-wait
 ├── current target
 ├── ETA
 └── cancellation state
```

dan status disimpan sebagai task object.

---

# 11. P1 — `.help` mempunyai bug Unicode/HTML splitting

`splitMessage()`:

```go
if len(text) <= maxLen
```

dan:

```go
text[:cut]
```

menggunakan **byte length**, bukan rune boundary.

Ini bisa memotong:

```text
UTF-8 character
```

di tengah byte.

Lebih buruk:

```html
<b>something very long...
```

bisa terpotong di tengah HTML tag.

Akibatnya Telegram bisa menolak pesan atau markup rusak.

### Target

Buat:

```go
SplitTelegramHTML(text string, maxBytes int)
```

yang:

1. tokenize HTML
2. menjaga tag stack
3. split berdasarkan UTF-8 boundary
4. close tag pada chunk
5. reopen tag pada chunk berikutnya

Ini harus menjadi utility core, bukan helper plugin Help.

---

# 12. P1 — HTML escaping belum konsisten

Beberapa plugin sudah bagus:

Profile menggunakan `core.EscapeHTML()`.

Tetapi banyak plugin masih:

```go
fmt.Sprintf("<code>%s</code>", userInput)
```

Contoh:

### Notes

```go
noteName
```

langsung dimasukkan ke HTML.

### Blacklist

```go
word
```

langsung dimasukkan.

### Scheduler

`payload`, `LastError`, dan beberapa field lain juga dimasukkan ke HTML tanpa escape.

### Info

Username juga belum di-escape secara konsisten.

### Target

**Rule global:**

> Tidak boleh ada `fmt.Sprintf()` dengan user-controlled value di HTML response tanpa `core.EscapeHTML()`.

Buat API:

```go
ui.Code(value)
ui.Text(value)
ui.Bold(value)
ui.Italic(value)
ui.Link(...)
```

sehingga plugin tidak perlu mengingat escaping.

---

# 13. P1 — Locks punya failure mode berbahaya

`locks` mengambil current rights:

```go
fullChat, err := ctx.GetFullChat()
```

tetapi jika gagal:

```go
return tg.ChatBannedRights{}
```

Ini sangat berbahaya.

Misalnya:

```text
current:
messages = true
media = true
links = true
pin = true
```

RPC `GetFullChat()` gagal.

Kode menganggap:

```text
semua false
```

kemudian:

```text
.lock polls
```

dan mengirim rights baru yang bisa secara tidak sengaja **menghapus lock yang sebelumnya aktif**.

### Fix wajib

```go
func getCurrentRights(...) (tg.ChatBannedRights, error)
```

dan jika fetch gagal:

```go
return ..., err
```

**Never mutate Telegram state using a synthetic zero-value snapshot.**

---

# 14. P1 — Media conversion memiliki path traversal risk

Di transcoder:

```go
outPath := filepath.Join(
    tmpDir,
    fmt.Sprintf("output.%s", outExt),
)
```

sementara `TargetFormat` datang dari user:

```go
targetFormat = strings.ToLower(...)
```

Target format harus **allowlist**, misalnya:

```text
mp4
mp3
aac
m4a
ogg
opus
webm
gif
```

bukan arbitrary string.

Gunakan:

```go
type MediaFormat string
```

daripada raw string.

---

# 15. P1 — URL downloader SSRF protection masih perlu diperketat

Downloader sudah jauh lebih bagus:

* HTTP/HTTPS only
* blocked private IP
* redirect limit
* size limit
* timeout
* streaming limit

Tetapi transport menggunakan:

```go
Proxy: http.ProxyFromEnvironment
```

Artinya environment proxy dapat mengubah jalur request.

Untuk threat model bot/userbot production, SSRF policy harus mempertimbangkan:

```text
direct connection
HTTP proxy
HTTPS proxy
redirect
DNS rebinding
IPv4
IPv6
```

Kalau downloader memang harus SSRF-safe, lebih aman:

```go
Proxy: nil
```

atau proxy sendiri harus melalui destination policy.

---

# 16. P1 — Addon capability model masih belum benar-benar sandbox

Manifest mendefinisikan capability:

```text
telegram.read
telegram.send
telegram.delete
media.download
process.execute
storage.write
scheduler.create
scheduler.cancel
```

dan broker melakukan authorization.

Tetapi addon executable adalah **native external process**:

```go
exec.CommandContext(ctx, path)
```

Jadi addon tersebut sebenarnya tetap dapat melakukan:

```text
open()
read()
write()
network
fork
exec
environment access
filesystem traversal
```

langsung melalui OS.

Artinya capability system saat ini adalah:

> **API authorization**

bukan:

> **process sandbox**

Ini penting sekali.

Kalau addon executable tidak dipercaya 100%, capability gate tidak memberikan isolation nyata.

### Target production

Gunakan salah satu:

```text
bubblewrap
seccomp
landlock
namespaces
cgroup
restricted filesystem
network namespace
```

atau paling tidak:

```text
dedicated OS user
private runtime directory
no writable application directory
resource limits
no network by default
```

---

# 17. P1 — Plugin Manager tidak memvalidasi kualitas command

`Register()` menerima plugin, `Init()`, lalu `RegisterBatch(p.Commands())`.

Saya sarankan validasi:

```text
Name != ""
Commands != empty
Handler != nil
command name valid
alias valid
category valid
timeout sane
cooldown sane
permission valid
```

Saat ini router bisa menerima `Command` dengan `Handler == nil`; executor memang punya fallback no-op di middleware chain, sehingga plugin yang salah konfigurasi dapat terlihat "berhasil" tetapi command tidak melakukan apa-apa.

Itu harus menjadi startup error, bukan silent no-op.

---

# 18. P1 — Plugin initialization belum context-aware

Sekarang:

```go
Init() error
```

seharusnya:

```go
Init(context.Context) error
```

Karena plugin production bisa memiliki:

* HTTP client
* goroutine
* cache
* worker
* timer
* subscription
* DB warmup
* external service

`Init()` tanpa context membuat startup cancellation dan dependency timeout sulit dikontrol.

---

# 19. P1 — Plugin Manager tidak punya dependency graph

Sekarang plugin hanya array:

```go
plugins := []plugin.Plugin{
    ping.New(),
    help.New(router),
    ...
}
```

Ini fragile.

Contoh:

```text
PMPermit
  depends on
Telegram + DB + EventBus + Permissions
```

UserLog:

```text
EventBus + DB + Telegram
```

Scheduler:

```text
DB + Telegram + Router + Permissions + Executor
```

Plugin manager seharusnya mendukung:

```go
Dependencies() []string
```

kemudian topological initialization.

---

# 20. P1 — Shutdown plugin reverse-order sudah benar, tetapi belum cukup

Manager memang shutdown reverse registration order.

Bagus.

Tetapi karena dependency graph belum ada, "reverse registration order" hanyalah approximation.

Target:

```text
Dependency graph
       ↓
topological startup
       ↓
reverse-topological shutdown
```

---

# 21. P1 — Scheduler output juga membutuhkan escaping dan validation

Scheduler sudah memiliki:

* scoped cancel
* job history
* recurring schedule
* action type
* access hash
* persistence

yang cukup bagus.

Tetapi payload adalah user-controlled:

```go
payload
```

dan ditampilkan sebagai HTML.

Selain itu command scheduling harus mempunyai **policy boundary**:

```text
.schedule .exec ...
.schedule .update ...
.schedule .restart ...
```

Walaupun scheduler command dibuat oleh Sudo, jangan sampai scheduler dapat mengubah privilege boundary secara tidak sengaja.

Saya menyarankan:

```text
scheduled command
    ↓
ExecutionSource = Scheduled
    ↓
SourcePolicy
    ↓
allowed command?
```

bukan hanya menjalankan command seperti interactive execution.

---

# 22. Ini yang paling saya rekomendasikan: Plugin Contract v2

Daripada terus menambah patch per plugin, saya akan mengubah arsitektur menjadi:

```text
                    ┌───────────────────┐
                    │   PluginManager   │
                    └─────────┬─────────┘
                              │
             ┌────────────────┼─────────────────┐
             ↓                ↓                 ↓
       Plugin Registry   Event Registry   Command Registry
             │                │                 │
             ↓                ↓                 ↓
         Lifecycle        Interceptors        Commands
```

Dengan:

```go
type Plugin interface {
    Metadata() Metadata

    Init(context.Context) error
    Commands() []core.Command

    Shutdown(context.Context) error
}
```

dan capability interfaces:

```go
type MessagePlugin interface {
    OnMessage(context.Context, *MessageEvent) error
}

type EditPlugin interface {
    OnEdit(context.Context, *EditEvent) error
}

type DeletePlugin interface {
    OnDelete(context.Context, *DeleteEvent) error
}

type CallbackPlugin interface {
    OnCallback(context.Context, *CallbackEvent) error
}

type InlinePlugin interface {
    OnInline(context.Context, *InlineEvent) error
}

type ReactionPlugin interface {
    OnReaction(context.Context, *ReactionEvent) error
}
```

---

# 23. Bahkan lebih bagus: deklaratif

Plugin:

```go
func (p *Plugin) Manifest() plugin.Manifest {
    return plugin.Manifest{
        Name: "filters",
        Version: "2.0.0",
        Category: plugin.Moderation,

        Hooks: []plugin.Hook{
            plugin.OnMessage,
        },

        Dependencies: []string{
            "database",
            "telegram",
        },
    }
}
```

Manager otomatis:

```text
discover
 ↓
validate
 ↓
dependency sort
 ↓
init
 ↓
register commands
 ↓
register hooks
 ↓
ready
```

Shutdown:

```text
quiesce
 ↓
stop hooks
 ↓
drain
 ↓
shutdown reverse dependency
```

---

# 24. Plugin execution pipeline yang saya targetkan

Saat ini:

```text
Telegram update
 ↓
parse
 ↓
raw interceptors
 ↓
command
```

Target:

```text
Telegram Update
       │
       ▼
 Normalize Update
       │
       ▼
 Resolve Peer / Entity
       │
       ▼
 Build ExecutionContext
       │
       ▼
 Security Pipeline
 ├── PMPermit
 ├── blacklist
 └── access policy
       │
       ▼
 Moderation Pipeline
 ├── filters
 ├── anti-flood
 └── automation
       │
       ▼
 Command Router
       │
       ▼
 Permission
       │
       ▼
 Source Policy
       │
       ▼
 Cooldown / Rate Limit
       │
       ▼
 Plugin Handler
       │
       ▼
 Observability
```

Ini akan jauh lebih deterministic.

---

# 25. Jangan biarkan plugin membuat Telegram primitive sendiri

Saya ingin menetapkan invariant:

### ❌ Tidak boleh

```go
tg.InputPeerUser{...}
tg.InputPeerChannel{...}
tg.InputUser{...}
```

langsung di plugin.

### ✅ Harus

```go
ctx.ResolveUser(...)
ctx.ResolvePeer(...)
ctx.ResolveChat(...)
ctx.Peer()
```

Core/Telegram layer yang bertanggung jawab terhadap:

* access hash
* peer type
* entity cache
* stale hash
* retry
* `PEER_ID_INVALID`
* `CHANNEL_INVALID`
* username normalization

Dengan demikian satu bug resolver tidak harus diperbaiki di 15 plugin.

---

# 26. Plugin test strategy sekarang masih kurang

Memang hampir setiap plugin memiliki `_test.go`, yang merupakan hal bagus. Tree saat ini menunjukkan coverage test yang cukup luas di plugin package.

Tetapi target test harus dinaikkan dari:

```text
handler unit test
```

menjadi:

```text
Plugin Contract Test
```

Setiap plugin harus dites minimal:

### Registration

```text
valid metadata
valid commands
no duplicate aliases
non-nil handlers
```

### Permission

```text
owner
sudo
normal user
anonymous
```

### Context

```text
private
basic group
supergroup
channel
Saved Messages
outgoing
incoming
```

### Peer

```text
user with access hash
user without access hash
stale access hash
basic group
supergroup
channel
```

### Lifecycle

```text
Init
Start
Message
Shutdown
Message after shutdown
```

### Concurrency

```text
1000 concurrent updates
shutdown while update arrives
DB unavailable
Telegram unavailable
```

---

# 27. Feature parity dengan Ultroid

Di level fitur, GoUltroid sudah mencakup banyak kategori penting:

```text
Admin
Moderation
Media
Downloader
Notes
Filters
AFK
Broadcast
Scheduler
PMPermit
UserLog
Profile
System
Voice
Addon
Inline
Callback
```

Tetapi **parity bukan hanya jumlah command**.

Ultroid secara arsitektural memiliki plugin/addon ecosystem dan handler untuk:

* message
* inline
* callback
* manager
* dual/bot/user modes

melalui plugin decorators/framework. ([GitHub][1])

Jadi target GoUltroid seharusnya:

> **feature parity + semantic parity + lifecycle parity + failure-behavior parity**

bukan sekadar:

```text
".ban exists" → parity
```

---

# 28. Prioritas implementasi yang saya sarankan

## Phase P0 — Plugin Foundation

**Jangan tambah plugin baru dulu.**

Kerjakan:

```text
1. Plugin Contract v2
2. Plugin lifecycle context-aware
3. Plugin-owned hooks
4. Dispatcher quiesce
5. Central Peer Resolver
6. No fabricated access_hash
7. BasicGroup/Supergroup distinction
8. Central HTML escaping
9. Source-aware execution
```

Target:

**Plugin subsystem 9/10**

---

## Phase P1 — Hot Path Optimization

```text
10. Filter cache
11. Blacklist cache
12. Regex LRU
13. Filter cooldown
14. Bot-message policy
15. UserLog queue metrics
16. Broadcast peer/access-hash fix
17. Dialog pagination
```

---

## Phase P1 — Safety

```text
18. Locks fail-closed
19. Media format allowlist
20. Downloader proxy/SSRF hardening
21. Scheduler source policy
22. HTML-safe rendering
23. Addon process isolation
```

---

## Phase P2 — Plugin Ecosystem

Baru setelah foundation selesai:

```text
24. Dynamic plugin enable/disable
25. Plugin health state
26. Plugin metrics
27. Plugin dependency graph
28. Plugin capabilities
29. Plugin config schema
30. Plugin migration hooks
31. Plugin versioning
32. Plugin compatibility checks
33. Plugin hot reload
```

---

# 29. Score saya saat ini

| Area                              |    Kondisi |
| --------------------------------- | ---------: |
| Command registration              | **8.0/10** |
| Permission model                  | **8.0/10** |
| Plugin lifecycle                  | **5.5/10** |
| Raw message hooks                 | **6.0/10** |
| Peer handling                     | **5.5/10** |
| Access-hash correctness           | **5.0/10** |
| Error handling                    | **7.0/10** |
| HTML/output safety                | **6.0/10** |
| Plugin performance                | **5.5/10** |
| Plugin concurrency                | **6.5/10** |
| Plugin tests                      | **7.5/10** |
| Plugin extensibility              | **6.0/10** |
| Feature breadth                   | **7.0/10** |
| Ultroid architectural parity      | **6.0/10** |
| Production readiness plugin layer | **6.3/10** |

**Bukan berarti GoUltroid jelek.** Justru fondasinya sudah cukup besar. Masalahnya adalah beberapa abstraction yang sekarang masih "cukup untuk bekerja" mulai menjadi bottleneck ketika jumlah plugin, message rate, dan failure scenario meningkat.

---

# 30. Verdict final

Saya **tidak menyarankan sekarang langsung menambah 20–50 plugin baru**.

Urutan yang paling benar:

```text
CURRENT
  │
  ├── 25 plugin
  ├── banyak command
  ├── tests cukup banyak
  └── core sudah cukup matang
          │
          ▼
PLUGIN CONTRACT V2
          │
          ▼
CENTRAL PEER / ENTITY
          │
          ▼
CENTRAL EVENT / HOOK SYSTEM
          │
          ▼
QUIESCE + LIFECYCLE
          │
          ▼
CACHE + HOT-PATH OPTIMIZATION
          │
          ▼
SECURITY / HTML / RESOURCE HARDENING
          │
          ▼
PLUGIN CONTRACT TEST SUITE
          │
          ▼
BARU TAMBAH FEATURE / PARITY
```

**Yang paling kritis dari audit ini adalah:** jangan memperbaiki bug `blacklist`, `filters`, `AFK`, `PMPermit`, `broadcast`, `scheduler`, dll satu per satu selamanya. Banyak bug tersebut berasal dari **kontrak plugin yang belum cukup kuat** dan **plugin masih ikut menangani Telegram peer/entity secara manual**.

Kalau kita melakukan **Plugin Contract V2 + central hook registration + central peer resolution** terlebih dahulu, implementasi seluruh plugin berikutnya akan jauh lebih sederhana, konsisten, cepat, dan jauh lebih sulit menghasilkan silent failure.

Dan dibanding Ultroid, ini juga akan membawa GoUltroid ke arah yang lebih tepat: bukan meniru Python/decorator-nya, tetapi membangun **equivalent Go-native plugin framework** dengan lifecycle, typed context, capability, source policy, callback/inline hooks, dan deterministic execution pipeline. Ultroid sendiri memang mendesain `ultroid_cmd`, `in_pattern`, dan `callback` sebagai bagian dari framework plugin, bukan sekadar fungsi yang ditempel manual ke application bootstrap. ([GitHub][1])

[Repository GoUltroid yang saya audit](https://github.com/inipew/goultroid?utm_source=chatgpt.com)

**Langkah berikut yang paling bernilai adalah melakukan audit/rekonstruksi `Plugin Contract V2` secara sangat detail, lalu saya petakan satu per satu ke 25 plugin yang sekarang—mana yang harus diubah interface-nya, hook mana yang dipindahkan ke Manager, state/cache apa yang harus dimiliki, test apa yang harus ditambahkan, dan urutan implementasi commit-per-commit.**

[1]: https://github.com/TeamUltroid/Ultroid/wiki/Creating-Plugins?utm_source=chatgpt.com "Creating Plugins · TeamUltroid/Ultroid Wiki · GitHub"
