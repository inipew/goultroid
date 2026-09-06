Saya audit **current `main` branch** Goultroid secara lintas-layer, bukan hanya file yang namanya sesuai dengan kategori. Fokus saya mengikuti lima area yang Anda minta dan saya trace dependency-nya sampai ke Telegram/gotd, DB, dispatcher, scheduler, plugin, EventBus, addon runtime, dan error handling.

Repo saat ini memang sudah berkembang cukup jauh: sudah ada `PeerResolver`, persistent `PeerStorage`, explicit `ExecutionSource`, durable scheduler, EventBus bounded queue, addon capability broker, dan centralized Telegram service.

**Namun hasil audit menunjukkan ada beberapa masalah serius yang sifatnya cross-cutting. Yang paling kritis memang Peer/Entity Resolution. Ada juga lifecycle shutdown bug yang nyata dan reliability layer yang masih terlalu dangkal untuk disebut production-grade.**

---

# Executive Summary

| Area                           |      Nilai | Status                      |
| ------------------------------ | ---------: | --------------------------- |
| Peer / Entity Resolution       | **5.5/10** | 🔴 Critical                 |
| Lifecycle & Shutdown           | **5.5/10** | 🔴 Critical                 |
| Execution Context & Middleware | **7.5/10** | 🟡 Good foundation          |
| EventBus & Handler Pipeline    | **6.5/10** | 🟡 Needs redesign           |
| Addon Runtime / Sandbox        | **5.5/10** | 🔴 Security concern         |
| Telegram Reliability           |   **6/10** | 🟡 Partial                  |
| **Overall**                    | **6.0/10** | 🟠 Not yet production-grade |

Yang paling penting:

> **Jangan menambah banyak fitur dulu sebelum lima subsystem ini dibereskan.**

Karena error di layer bawah akan menyebabkan bug yang terlihat seperti bug feature.

Contoh:

```text
UserLog gagal kirim
PMPermit gagal block
Scheduler gagal send
Broadcast gagal
Restart notification gagal
Admin gagal ban
```

padahal akar masalahnya bisa sama:

```text
Peer → InputPeer → AccessHash → Telegram RPC
```

---

# 1. Peer / Entity Resolution — PALING KRITIS

## 1.1 Foundation-nya sebenarnya sudah bagus

Goultroid sekarang sudah mempunyai:

```text
Dispatcher
    ↓
PeerResolver
    ↓
peers.Manager
    ↓
PeerStorage
    ↓
SQLite
```

`Resolver` secara eksplisit mendukung:

* numeric ID
* username
* phone
* user
* chat
* channel
* access hash
* persistent storage

dan `PeerStorage` memang mengimplementasikan `peers.Storage` dari gotd.

Ini **arah arsitekturnya benar**.

Masalahnya adalah beberapa fallback justru merusak guarantee yang ditulis oleh interface.

---

# 1.2 BUG P0 — negative Telegram ID salah diklasifikasikan

Ini temuan paling serius.

Current:

```go
if id < 0 {
    channelID := id

    if strings.HasPrefix(str, "-100") {
        ...
    } else {
        channelID = -id
    }

    return &tg.InputPeerChannel{
        ChannelID: channelID,
    }
}
```

Artinya:

```text
-1001234567890
    ↓
channel
```

masih benar.

Tetapi:

```text
-12345
    ↓
channel 12345
```

**salah.**

Telegram legacy/basic group menggunakan ID negatif biasa, sedangkan supergroup/channel menggunakan namespace `-100...`.

Jadi:

```text
-12345
```

seharusnya:

```text
InputPeerChat{ChatID: 12345}
```

bukan:

```text
InputPeerChannel{ChannelID: 12345}
```

Ini bisa menyebabkan fitur:

```text
mute
ban
pin
send
schedule
delete
resolve
```

gagal secara silent.

### Correct classification

```text
positive
  ↓
User? / explicit user resolver

negative -100...
  ↓
Channel / Supergroup

negative non--100
  ↓
Basic Group
```

**Tetapi bahkan ini belum ideal**, karena string ID sendiri tidak cukup untuk semua context.

---

# 1.3 Jangan gunakan signed ID sebagai universal peer type

Saya sarankan **hapus paradigma**:

```text
int64 ID
```

sebagai representasi lengkap peer.

Gunakan:

```go
type PeerKind uint8

const (
    PeerUser PeerKind = iota
    PeerChat
    PeerChannel
)

type PeerRef struct {
    Kind       PeerKind
    ID         int64
    AccessHash *int64
    Username   string
}
```

Sehingga:

```text
PeerUser
  ID=123
  AccessHash=...

PeerChat
  ID=123

PeerChannel
  ID=123
  AccessHash=...
```

Tidak ada lagi:

```text
-123
-100123
```

sebagai semantic type.

---

# 1.4 BUG P0 — Resolver mengklaim "guaranteed access hashes", tetapi fallback tidak menjaminnya

Interface comment:

```go
// ... with guaranteed access hashes.
```

tetapi current implementation:

```go
return &tg.InputPeerUser{UserID: uid}, uid, nil
```

dan:

```go
return &tg.InputPeerChannel{ChannelID: channelID}, nil
```

Artinya:

```text
ResolveUser()
      ↓
success
      ↓
InputPeerUser{AccessHash: 0}
```

Ini **kontradiksi contract**.

Resolver seharusnya memilih:

### Option A — strict

```text
Resolve
 ↓
cannot obtain access hash
 ↓
ERROR
```

untuk peer yang membutuhkan access hash.

### Option B — typed confidence

```go
type ResolvedPeer struct {
    Peer       tg.InputPeerClass
    Kind       PeerKind
    ID         int64
    AccessHash *int64
    Source     ResolutionSource
    Confidence ResolutionConfidence
}
```

Tetapi jangan:

```text
err == nil
```

sementara peer sebenarnya tidak valid untuk operasi tertentu.

---

# 1.5 BUG P0 — ResolveUser numeric ID fallback terlalu permisif

Current:

```text
peerManager.ResolveUserID
    ↓ fail

storage.Find
    ↓ fail

InputPeerUser{UserID}
    ↓
SUCCESS
```

Ini berbahaya.

Contoh:

```text
.approve 123456
```

bisa menghasilkan:

```text
InputPeerUser{UserID:123456, AccessHash:0}
```

dan caller mengira resolution berhasil.

Saya sarankan:

```text
numeric user ID
 ↓
peerManager
 ↓
persistent storage
 ↓
Telegram resolution/recovery
 ↓
FAIL
```

bukan:

```text
FAIL → fabricate InputPeer
```

---

# 1.6 `ensureUserAccessHash()` lebih baik, tetapi masih menyembunyikan kegagalan

Telegram service sudah memiliki:

```go
ensureUserAccessHash()
ensureChannelAccessHash()
```

dan ini dipanggil sebelum send/edit.

Ini bagus.

Tetapi:

```text
ensure...
 ↓
gagal resolve
 ↓
return original peer
```

jadi:

```text
AccessHash = 0
```

tetap diteruskan ke Telegram.

Lebih benar:

```go
func ensureUserAccessHash(...) (tg.InputPeerClass, error)
```

kemudian:

```text
missing hash
   ↓
resolve
   ↓
success → return peer
failure → return typed error
```

---

# 1.7 Persistent PeerStorage bagus, tetapi cache bisa stale

Current cache:

```go
peers map[peers.Key]int64
```

dan:

```text
Find()
 ↓
cache hit
 ↓
return access hash
```

Tidak ada TTL/version/invalidating policy.

Access hash biasanya stable enough, tetapi entity information:

```text
username
phone
name
title
```

bisa berubah.

Jadi entity cache perlu:

```text
updated_at
```

dan refresh policy.

Minimal:

```text
hot cache
   ↓
persistent storage
   ↓
network
```

dengan stale detection.

---

# 1.8 BUG P1 — SaveEntity cache hanya berubah jika seluruh snapshot berubah

Ini benar secara correctness, tetapi tidak ada canonical normalization username.

Misalnya:

```text
Alice
alice
@alice
```

dapat menghasilkan duplicate lookup behavior.

Normalisasi:

```go
strings.TrimPrefix(username, "@")
strings.ToLower(...)
```

harus dilakukan **di storage boundary**.

---

# 1.9 BUG P1 — Dispatcher peer cache worker menelan semua error

Current:

```go
_ = r.storage.Save(...)
_ = r.storage.SaveEntity(...)
```

Kalau SQLite error:

```text
peer cache gagal
```

tetapi tidak ada:

```text
metric
log
retry
```

Akibatnya beberapa menit kemudian:

```text
feature gagal resolve access_hash
```

dan sulit mencari penyebabnya.

Minimal:

```text
peer_cache_save_failed_total
```

dan structured warning.

---

# 1.10 BUG P1 — peer worker bisa memproses stale entities setelah shutdown

`Dispatcher.Stop()` menutup queue dan menunggu worker. Ini bagus.

Tetapi handler producer masih bisa enqueue sebelum stop.

Kita perlu state:

```text
RUNNING
STOPPING
STOPPED
```

dan:

```text
enqueue()
```

harus menolak ketika `STOPPING`.

---

# 1.11 Entity resolution seharusnya menjadi subsystem tunggal

Saat ini berbagai bagian masih bisa membuat:

```go
&tg.InputPeerUser{}
&tg.InputPeerChat{}
&tg.InputPeerChannel{}
```

langsung.

Saya sarankan invariant:

> **Feature layer tidak boleh membuat InputPeer secara manual kecuali peer berasal langsung dari Telegram update context.**

Semua explicit target:

```text
user ID
username
chat ID
channel ID
phone
reply
```

harus melewati:

```text
PeerResolver
```

---

# 1.12 Peer source harus dipertahankan

Saya rekomendasikan:

```go
type ResolutionSource string

const (
    SourceCurrentUpdate
    SourcePeerManager
    SourcePersistentStorage
    SourceUsernameLookup
    SourcePhoneLookup
    SourceNetworkRecovery
)
```

Ini sangat berguna ketika debugging.

Log:

```text
resolve peer failed
kind=user
id=123
source=persistent_storage
reason=access_hash_missing
```

---

# 1.13 Score Peer Resolution

**5.5/10**

Foundation: **8.5**

Correctness current implementation: **5**

Persistence: **8**

Telegram semantics: **5**

Production reliability: **5**

---

# 2. Lifecycle & Shutdown

Ini area kedua yang sangat penting.

Current App shutdown:

```text
Scheduler
   ↓
Plugins
   ↓
EventBus
   ↓
Limiter
   ↓
DB
   ↓
Logger
```

Kelihatannya bagus.

Tetapi ada masalah besar.

---

# 2.1 BUG P0 — Dispatcher tidak pernah di-stop

`Dispatcher.Start()` membuat:

```text
2 peer workers
```

dan:

```text
peerQueue
```

Dispatcher juga mempunyai:

```go
func (d *Dispatcher) Stop(ctx context.Context) error
```

Tetapi `App.Shutdown()` **tidak memanggilnya**.

Jadi lifecycle aktual:

```text
App Shutdown
 ↓
Scheduler stop
 ↓
Plugins stop
 ↓
EventBus close
 ↓
DB close
```

tetapi:

```text
Dispatcher
  ↓
peer workers
```

tidak explicitly dihentikan.

Ini sangat serius.

---

# 2.2 Ini bisa menghasilkan use-after-close terhadap DB

Peer worker melakukan:

```go
r.storage.Save(...)
r.storage.SaveEntity(...)
```

yang menggunakan SQLite.

Shutdown:

```text
plugins
 ↓
eventbus
 ↓
DB.Close()
```

sementara peer worker bisa masih hidup.

Potensi:

```text
peer worker
    ↓
Save()
    ↓
DB already closed
```

atau lebih buruk:

```text
enqueue
 ↓
worker
 ↓
DB close race
```

**P0.**

---

# 2.3 Correct shutdown order

Saya rekomendasikan:

```text
Signal
  ↓
STOP ACCEPTING NEW WORK
  ↓
Telegram Update Dispatcher STOP
  ↓
wait update handlers
  ↓
Scheduler STOP
  ↓
Addon runtimes STOP
  ↓
Plugins STOP
  ↓
EventBus STOP / DRAIN
  ↓
Interaction stores/cache STOP
  ↓
RateLimiter STOP
  ↓
DB CLOSE
  ↓
Telegram client/session CLOSE
  ↓
Logger Sync
```

Yang penting:

> **DB harus menjadi salah satu resource terakhir.**

---

# 2.4 Telegram client sendiri tidak terlihat eksplisit dalam Shutdown

`App` mempunyai:

```go
client *telegram.Client
```

tetapi `Shutdown()` tidak mempunyai explicit:

```text
client.Disconnect()
```

Memang `client.Run(ctx)` menggunakan context cancellation dan gotd kemungkinan melakukan teardown ketika context selesai.

Tetapi architecture seharusnya tidak bergantung pada side-effect context cancellation tanpa lifecycle contract.

Saya sarankan:

```go
client.Stop(ctx)
```

atau:

```go
client.Close()
```

wrapper internal.

---

# 2.5 BUG P0 — Addon runtime tidak di-shutdown

App membuat:

```go
addonManager
```

dan manager mempunyai:

```go
ShutdownRuntimes()
```

Tetapi `App.Shutdown()` tidak memanggil:

```text
addonMgr.ShutdownRuntimes()
```

Jadi addon process bisa hidup ketika application shutdown.

Ini sangat jelas lifecycle leak.

---

# 2.6 Addon process bisa hidup setelah Go process mati

`ExternalRuntime` menggunakan:

```go
exec.CommandContext(ctx, path)
```

jadi jika context parent benar-benar cancel, process akan termination.

Tetapi runtime context bisa saja berasal dari context yang bukan lifecycle app.

Dan App tidak melakukan explicit:

```text
StopRuntime
```

pada shutdown.

Jadi jangan mengandalkan parent context saja.

---

# 2.7 BUG P1 — Addon manager tidak punya Shutdown state

Manager memiliki:

```text
runtimeMu
runtimes
```

tetapi tidak memiliki:

```text
shuttingDown bool
```

Akibatnya race:

```text
ShutdownRuntimes()
       ||
StartRuntime()
```

bisa terjadi.

Harus ada:

```text
ACTIVE
STOPPING
STOPPED
```

dan semua operation memeriksa state.

---

# 2.8 Plugin shutdown ordering sudah bagus

Plugin manager:

```text
reverse registration order
```

Ini benar.

Karena plugin yang terakhir didaftarkan sering bergantung pada plugin/service sebelumnya.

---

# 2.9 Tetapi lifecycle dependency belum explicit

Reverse registration order bukan dependency graph.

Misalnya:

```text
Plugin A → EventBus
Plugin B → A
Plugin C → DB
```

registration order bukan guarantee dependency order.

Production architecture lebih baik:

```go
type Lifecycle interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

dan dependency metadata:

```text
EventBus
DB
Telegram
Services
Plugins
```

---

# 2.10 EventBus shutdown sudah lumayan bagus

Current:

```text
closed = true
subscriber map cleared
close(queue)
workers.Wait()
```

dan worker:

```text
range queue
```

Ini membuat queued events drained sebelum worker exit.

Ini bagian yang bagus.

---

# 2.11 Tetapi EventBus close ordering masih salah

App:

```text
Plugins shutdown
 ↓
EventBus.Close()
```

Ini bagus jika plugin shutdown tidak membutuhkan EventBus.

Tetapi beberapa plugin justru dapat publish event saat shutdown.

Kalau:

```text
plugin shutdown
 ↓
Publish
```

masih bisa.

Setelah EventBus close baru ditolak.

Ini bukan bug besar, tetapi lifecycle contract harus:

```text
STOP PRODUCERS
 ↓
DRAIN EVENTBUS
 ↓
STOP CONSUMERS
```

atau jika plugin adalah producer+consumer:

```text
stop producers
 ↓
drain
 ↓
stop consumers
```

---

# 2.12 Callback/inline cache lifecycle

App `Run()` melakukan defer:

```text
callbackStore.Stop()
inlineCache.Stop()
inlineCache.Prune()
```

Ini cukup bagus.

Tetapi lifecycle-nya berada di `Run()` bukan `Shutdown()`.

Jika:

```text
Run()
```

tidak kembali normal karena fatal error / unexpected termination path, lifecycle contract menjadi tidak uniform.

Lebih baik semua long-lived resource punya centralized `Shutdown`.

---

# 2.13 Score Lifecycle

**5.5/10**

Plugin shutdown: **8**

Scheduler: **8**

EventBus: **7.5**

Dispatcher: **4**

Addon: **4**

Global dependency ordering: **5**

---

# 3. Execution Context & Middleware

Ini justru salah satu subsystem terbaik.

Current sudah mempunyai:

```go
type ExecutionSource uint8
```

dengan:

```text
Interactive
Scheduled
Assistant
System
```

dan `CommandExecution` sebagai canonical envelope.

Ini desain yang benar.

---

# 3.1 Good: scheduled execution tidak memalsukan Telegram message

Comment-nya secara eksplisit:

> TriggerMessage optional karena non-interactive source tidak memiliki real Telegram message.

Ini sangat bagus.

Scheduler kemudian dapat menggunakan:

```text
ExecutionScheduled
```

daripada:

```text
fake Telegram message
```

---

# 3.2 BUG P1 — ExecutionSource masih kurang granular

Sekarang:

```text
Interactive
Scheduled
Assistant
System
```

Tetapi project sudah mempunyai:

```text
Addon
Broadcast
Automation
PMPermit
UserLog
Callback
```

Untuk security policy, cukup:

```text
Interactive
Scheduled
Assistant
Addon
System
```

dan mungkin:

```text
Automation
```

Kalau tidak, semua non-interactive source bisa sulit dibedakan.

Ini terutama penting untuk PMPermit auto-approve.

---

# 3.3 BUG P0/P1 — Source bisa hilang ketika command berpindah layer

`CommandExecution` memiliki source.

Kemudian dibuat:

```go
Context{
    ...
}
```

dan `execute()` menerima source sebagai parameter.

Tetapi `Context` sendiri tidak memiliki:

```go
Source ExecutionSource
```

Artinya handler tidak dapat melihat source secara native.

Ini berpotensi menyebabkan:

```text
scheduler
  ↓
executor
  ↓
handler
```

handler tidak tahu apakah ini:

```text
interactive
scheduled
system
```

kecuali metadata lain.

### Seharusnya

```go
type Context struct {
    ...
    Source ExecutionSource
}
```

Ini penting.

---

# 3.4 `CorrelationID` sudah benar arahnya

Current:

```text
CorrelationID
```

ada di:

```text
CommandExecution
Context
logging
```

dan executor mencatat:

```text
correlation_id
command
source
error
```

Ini sangat bagus.

Tetapi correlation ID harus **universal**, bukan hanya command.

Ideal:

```text
update
 ↓
correlation ID
 ↓
handler
 ↓
service
 ↓
Telegram RPC
 ↓
audit
```

---

# 3.5 BUG P1 — Legacy Execute menentukan source dari prefix string

Current:

```go
if strings.HasPrefix(ctx.CorrelationID, "sched-") {
    source = ExecutionScheduled
}
```

Ini fragile.

Security-sensitive metadata tidak boleh di-infer dari:

```text
string prefix
```

Kalau:

```text
correlationID = "sched-fake"
```

maka source dianggap scheduled.

Gunakan explicit field.

---

# 3.6 Permission middleware ordering

Current chain:

```text
Recovery
Correlation
Logging
Permission
Source Filter
Cooldown
Timeout
```

Secara umum bagus.

Tetapi saya akan ubah:

```text
Recovery
Correlation
Source/Auth context
Permission
Filter
RateLimit
Cooldown
Timeout
Logging/result
```

Logging harus bisa mencatat:

```text
permission denied
filter denied
rate limited
```

dengan outcome yang konsisten.

---

# 3.7 Rate limiter key hanya SenderID

Current:

```go
key := strconv.FormatInt(ctx.SenderID(), 10)
```

Ini bermasalah untuk non-interactive execution:

```text
scheduled
assistant
system
```

karena:

```text
SenderID = 0
```

semuanya menjadi:

```text
anonymous
```

Jadi scheduler jobs yang berbeda bisa berbagi bucket.

Seharusnya:

```text
interactive:
 user:<id>

scheduled:
 schedule:<job-id>

assistant:
 assistant:<session-id>

system:
 system:<component>
```

---

# 3.8 GroupOnly / PrivateOnly

Current filter architecture sudah ada.

Tetapi penting:

```text
scheduled execution
```

tidak memiliki real Telegram Chat.

Maka:

```text
GroupOnly
PrivateOnly
```

harus memiliki semantic jelas.

Jangan:

```text
Chat == nil
→ PrivateOnly true
```

Itu salah.

Harus:

```text
ChatContextUnknown
```

berbeda dengan:

```text
PrivateChat
```

---

# 3.9 Target Chat vs Execution Chat

`CommandExecution` memiliki:

```go
Chat *Chat
Target *Chat
```

Ini bagus, tetapi semantic harus ditegaskan:

```text
Chat
= origin context

Target
= action destination
```

Scheduled command:

```text
Chat=nil
Target=chat X
```

harus valid.

Ini penting untuk scheduler.

---

# 3.10 Score Execution

**7.5/10**

Architecture: **9**

Security metadata: **6.5**

Propagation: **7**

Scheduled execution: **8**

Middleware: **8**

---

# 4. EventBus & Handler Pipeline

Current EventBus:

```text
queue = 1024
workers = 8
```

dan publish bersifat non-blocking.

Ini punya kelebihan besar:

> observational event tidak boleh menghentikan Telegram update pipeline.

Good.

---

# 4.1 BUG P1 — event drop tidak observable

Current:

```go
default:
    // Observational events are best-effort by design.
```

Artinya:

```text
queue full
 ↓
event dropped
```

tanpa:

```text
metric
log
counter
```

Ini berbahaya.

Karena:

```text
UserLog
audit
analytics
edit tracking
metrics
```

bisa kehilangan event tanpa diketahui.

Harus:

```text
event_dropped_total{
    event_type="..."
}
```

---

# 4.2 Per-subscriber queue semantics

Current queue global:

```text
queue chan eventJob
```

dan setiap subscriber membuat satu job.

Bagus untuk isolation.

Tetapi satu subscriber yang lambat akan memenuhi queue bersama subscriber lain.

Contoh:

```text
UserLog subscriber lambat
    ↓
1000 events
    ↓
queue penuh
    ↓
Audit subscriber juga kehilangan event
```

Comment mengatakan:

> drops only that subscriber's event

Tetapi secara queue global, subscriber yang lambat tetap ikut memenuhi queue.

Jadi comment tersebut **tidak sepenuhnya benar secara architectural guarantee**.

---

# 4.3 Better design: subscriber-specific bounded queue

```text
EventBus
 ├── subscriber A → queue 256 → workers
 ├── subscriber B → queue 256 → workers
 └── subscriber C → queue 256 → workers
```

Maka:

```text
UserLog overload
```

tidak menyebabkan:

```text
Audit overload
```

---

# 4.4 Ordering tidak dijamin

Current:

```text
8 workers
```

dan map subscriber.

Akibatnya:

```text
event A
event B
event C
```

dapat diproses:

```text
B
A
C
```

Untuk:

```text
metrics
```

tidak masalah.

Untuk:

```text
edit log
audit
state transition
```

bisa bermasalah.

---

# 4.5 EventBus perlu ordering key

Saya rekomendasikan:

```go
type EventEnvelope struct {
    ID          string
    Type        EventType
    Timestamp   time.Time
    OrderingKey string
    Payload     Event
}
```

Contoh:

```text
chat:123
user:456
message:789
```

Lalu event dengan key sama diproses ordered.

---

# 4.6 Dedup belum ada

Tidak ada event ID / idempotency key.

Kalau Telegram update atau internal publisher mengirim duplicate:

```text
PMBlocked
PMBlocked
```

subscriber harus menangani sendiri.

Lebih bagus EventBus memberi:

```text
EventID
```

dan consumer dapat dedup.

---

# 4.7 Panic isolation bagus

Worker:

```go
defer func() {
    _ = recover()
}()
```

Bagus.

Tetapi panic hilang tanpa observability.

Seharusnya:

```text
event_handler_panic_total
logger.Error(...)
```

dengan:

```text
event_type
handler
panic
```

---

# 4.8 Handler error tidak bisa dikembalikan

`EventHandler`:

```go
type EventHandler func(event Event)
```

tidak memiliki error.

Untuk observational bus ini boleh.

Tetapi sebaiknya:

```go
type EventHandler func(context.Context, Event) error
```

kemudian EventBus dapat:

```text
error
 ↓
metrics
 ↓
log
```

tanpa mempengaruhi publisher.

---

# 4.9 Message pipeline

Dispatcher punya:

```text
raw Telegram handler
 ↓
messageHandlers
 ↓
command execution
```

PMPermit didaftarkan sebagai message handler, begitu pula:

```text
AFK
filters
blacklist
userlog
```

Ordering registration menjadi behavior.

Contoh current:

```text
AFK
filters
blacklist
...
pmpermit
...
userlog
```

Ini perlu **explicit priority**.

---

# 4.10 P1 — Handler ordering harus declarative

Jangan:

```go
append(handler)
```

dan bergantung pada urutan app.go.

Lebih baik:

```go
type HandlerPriority int

const (
    PrioritySecurity = 10
    PriorityModeration = 20
    PriorityFeature = 50
    PriorityObservability = 90
)
```

Kemudian:

```text
PMPermit = Security
Blacklist = Security
Filters = Moderation
AFK = Feature
UserLog = Observability
```

---

# 4.11 PMPermit seharusnya berada sebelum feature processing

Ideal:

```text
Telegram update
 ↓
dedup
 ↓
security interceptors
    ├── PMPermit
    ├── blacklist
    └── ...
 ↓
command parser
 ↓
feature
 ↓
observability
```

Kalau PMPermit terlambat:

```text
unapproved PM
 ↓
AFK
 ↓
reply
 ↓
PMPermit
```

bisa terjadi unwanted side effect.

---

# 4.12 Score EventBus

**6.5/10**

Isolation: **8**

Queue: **7**

Ordering: **4**

Dedup: **4**

Observability: **5**

Architecture: **7**

---

# 5. Addon Runtime / Sandbox

Ini area yang saya anggap **security-sensitive**.

Current design:

```text
Addon manifest
 ↓
capability gate
 ↓
external executable
 ↓
JSON-lines IPC
```

dan **tidak menggunakan Go plugin package**.

Itu keputusan bagus.

---

# 5.1 Good: external process isolation

Addon tidak dimuat sebagai shared Go process.

Artinya crash addon:

```text
addon panic
```

tidak otomatis:

```text
Goultroid crash
```

Ini bagus.

---

# 5.2 Good: SHA-256 verification

Current:

```go
VerifySHA256(...)
```

sebelum runtime start jika expected hash diberikan.

Ini bagus.

Tetapi masalah:

```text
expectedSHA256
```

bersifat optional.

Jadi executable bisa dijalankan tanpa integrity verification.

Untuk untrusted addon:

```text
signature/hash verification
```

seharusnya mandatory.

---

# 5.3 BUG P0 — "sandbox" sebenarnya belum sandbox

Current:

```go
exec.CommandContext(ctx, path)
cmd.Dir = filepath.Dir(path)
cmd.Env = ...
cmd.Stderr = os.Stderr
```

Tidak ada:

```text
seccomp
namespaces
chroot
container
AppArmor
SELinux policy
cgroup
RLIMIT
filesystem isolation
network isolation
```

Jadi addon masih memiliki permission OS user yang sama dengan Goultroid.

Kalau Goultroid berjalan sebagai user yang memiliki:

```text
DB
session file
SSH keys
filesystem
network
```

addon executable berpotensi mengakses semuanya.

Capability broker **tidak membatasi OS-level access**.

---

# 5.4 Ini harus disebut "process isolation", bukan sandbox

Terminologi yang benar:

Current:

```text
external process isolation + application capability gating
```

bukan:

```text
sandbox
```

Kalau ingin sandbox benar:

```text
unprivileged UID
+
private filesystem
+
no inherited secrets
+
network namespace
+
seccomp
+
cgroup
+
read-only rootfs
```

---

# 5.5 BUG P0 — addon executable directory menjadi working directory

Current:

```go
cmd.Dir = filepath.Dir(path)
```

Artinya addon berjalan dari directory tempat binary berada.

Kalau directory itu berisi:

```text
config
secret
manifest
other addon files
```

addon punya relative access ke semuanya.

Lebih aman:

```text
runtime directory:
data/addons/runtime/<name>/<instance>/
```

dan:

```text
cmd.Dir = isolatedDir
```

---

# 5.6 BUG P1 — Environment isolation belum cukup

Current:

```text
PATH
GOUTROID_ADDON_NAME
GOUTROID_ADDON_VERSION
```

Ini bagus.

Tetapi process tetap mewarisi:

```text
UID
GID
filesystem permissions
network
kernel capabilities
```

Environment bukan sandbox.

---

# 5.7 BUG P1 — stderr langsung ke host stderr

```go
cmd.Stderr = os.Stderr
```

Ini memungkinkan addon menulis output arbitrary ke application stdout/stderr.

Lebih bagus:

```text
addon log capture
 ↓
size limit
 ↓
structured logger
```

agar:

```text
addon=foo
level=...
message=...
```

dan tidak bisa spam terminal.

---

# 5.8 IPC concurrency cukup aman dari response interleaving

Current:

```go
callMu sync.Mutex
```

dan semua `Call()` serialized.

Ini membuat:

```text
request
response
request
response
```

lebih mudah dipasangkan.

Bagus.

---

# 5.9 BUG P1 — IPC read goroutine dapat leak

Current:

```go
go func() {
    r.stdout.ReadBytes('\n')
}()
```

kemudian caller:

```go
select {
case <-ctx.Done():
    return
case result := <-resultCh:
}
```

Kalau context timeout:

```text
caller returns
```

tetapi goroutine masih:

```text
ReadBytes()
```

menunggu addon.

Karena `stdout` tidak ditutup ketika call timeout, goroutine dapat tetap hidup.

Repeated timeout:

```text
100 calls
→ 100 reader goroutines
```

Potential goroutine leak.

---

# 5.10 IPC architecture seharusnya punya dedicated reader

Lebih bagus:

```text
Addon stdout
      ↓
single reader goroutine
      ↓
response ID map
      ↓
pending request channels
```

seperti RPC multiplexer.

```text
ID=1 → chan A
ID=2 → chan B
ID=3 → chan C
```

Tidak perlu membuat reader goroutine per call.

---

# 5.11 IPC message size limit

Current:

```go
io.LimitReader(stdout, 8<<20)
```

8 MB lebih baik daripada unlimited.

Tetapi karena reader dibungkus sekali:

```text
bufio.Reader(io.LimitReader(...))
```

batasnya bersifat stream-level, bukan per-message.

Ideal:

```text
max line = 1MB
```

dan reject oversized frame.

---

# 5.12 Addon capability enforcement hanya jika caller memilih API yang benar

Manager memiliki:

```go
CallRuntime()
CallRuntimeWithCapability()
```

dan comment menyebut privileged calls harus menggunakan `WithCapability`.

Tetapi:

```go
CallRuntime()
```

sendiri tidak melakukan capability check.

Ini membuat enforcement bergantung pada developer discipline.

Lebih aman:

```text
Runtime.Call
```

tidak boleh direct untuk privileged host operations.

Hanya broker yang expose operation.

---

# 5.13 Score Addon

**5.5/10**

Architecture: **8**

Process isolation: **7**

Capability model: **7**

Actual sandbox: **2**

IPC robustness: **5**

Lifecycle: **4**

Security: **4**

---

# 6. Telegram Reliability

Current Telegram Service sudah memiliki:

```text
FloodWait detection
access hash preparation
Telegram error mapping
retry
```

Ini foundation yang bagus.

Tetapi masih jauh dari robust.

---

# 6.1 FloodWait handling terlalu sempit

Current:

```go
defaultFloodWaitRetryLimit = 5 * time.Second
```

dan:

```text
FloodWait <= 5 sec
→ wait
→ retry once
```

Masalah:

```text
FloodWait = 6s
```

langsung:

```text
error
```

Padahal aplikasi bisa saja menunggu 6 detik dengan aman.

Lebih baik policy:

```text
short wait:
 auto retry

medium:
 queue/defer

long:
 return rate-limited
```

---

# 6.2 Retry bukan generic retry

Current hanya retry FloodWait.

Tidak ada policy untuk:

```text
network reset
EOF
timeout
temporary unavailable
connection reset
transport error
```

Harus ada:

```text
Transient
Permanent
RateLimited
Auth
PeerInvalid
Permission
```

classification.

---

# 6.3 BUG P1 — no exponential backoff

Current:

```text
attempt
wait exact FloodWait
retry
```

Tidak ada generic:

```text
100ms
500ms
1s
2s
```

untuk network transient errors.

---

# 6.4 BUG P1 — retry safety / idempotency belum centralized

Contoh:

```text
SendMessage
```

timeout terjadi setelah Telegram menerima request:

```text
Telegram:
message sent

Client:
timeout
```

retry:

```text
message sent AGAIN
```

hasil:

```text
duplicate message
```

Jadi retry harus berdasarkan operation type.

### Safe-ish

```text
Get
Resolve
Read
```

### Potentially duplicated

```text
SendMessage
Forward
React
Ban
Unban
Delete
```

Harus memiliki idempotency strategy.

---

# 6.5 Telegram RPC tidak mempunyai universal idempotency key

Karena itu Goultroid perlu application-level dedup untuk operation tertentu.

Misalnya:

```go
type OperationKey struct {
    CorrelationID string
    Operation     string
    TargetID      int64
}
```

dan persistent/in-memory short TTL registry:

```text
operation started
operation success
```

---

# 6.6 Access hash recovery sudah bagus tetapi incomplete

`ensureChannelAccessHash()` dan `ensureUserAccessHash()`:

```text
peerManager
 ↓
storage
 ↓
return
```

Tetapi jika hash invalid/stale:

```text
CHANNEL_INVALID
PEER_ID_INVALID
```

tidak ada:

```text
invalidate cache
 ↓
re-resolve
 ↓
retry once
```

Itu harus ditambahkan.

---

# 6.7 Correct recovery flow

```text
RPC
 ↓
PEER_ID_INVALID / ACCESS_HASH_INVALID
 ↓
invalidate peer cache
 ↓
resolve fresh entity
 ↓
retry once
 ↓
success
```

Bukan:

```text
RPC error
 ↓
return error
```

---

# 6.8 BUG P1 — `mapTelegramError()` terlalu coarse

Current mapping:

```text
CHAT_ID_INVALID
PEER_ID_INVALID
USER_ID_INVALID
MESSAGE_ID_INVALID
→ ErrNotFound
```

Padahal:

```text
PEER_ID_INVALID
```

bisa berarti:

```text
stale access hash
```

bukan:

```text
entity doesn't exist
```

Jadi mapping harus lebih semantik:

```text
ErrPeerInvalid
ErrAccessHashInvalid
ErrEntityNotFound
ErrMessageNotFound
```

---

# 6.9 Permission errors juga terlalu broad

Current:

```text
CHAT_ADMIN_REQUIRED
RIGHTS_NOT_MODIFIED
CHAT_WRITE_FORBIDDEN
→ ErrPermissionDenied
```

`RIGHTS_NOT_MODIFIED` tidak selalu berarti permission denied.

Bisa:

```text
requested state already equals current state
```

yang merupakan:

```text
idempotent success
```

bukan failure.

Ini bisa menyebabkan command:

```text
mute
unmute
admin
```

melaporkan error padahal state sudah benar.

---

# 6.10 Rate limiting saat ini dua layer

Ada:

```text
CommandExecutor rate limiter
```

dan:

```text
Telegram operation FloodWait
```

Bagus.

Tetapi belum ada **destination-level limiter**.

Misalnya:

```text
broadcast → 1000 sends
```

semua melewati global limiter tetapi belum tentu punya:

```text
peer-specific send budget
```

---

# 6.11 Need Telegram operation limiter

Ideal:

```text
global
per-method
per-peer
per-user
```

contoh:

```text
SendMessage:
 global 30/sec
 per-peer 3/sec

Edit:
 global 20/sec

Delete:
 global 50/sec
```

Tentunya angka harus configurable dan mengikuti Telegram behavior.

---

# 6.12 Telegram connection readiness bagus

Client sekarang mempunyai:

```go
Ready()
IsReady()
signalReady()
```

dan scheduler baru start setelah Telegram ready.

Ini **excellent improvement**.

Scheduler tidak lagi mengklaim job sebelum Telegram siap.

---

# 6.13 Tetapi background dialog warmup error ditelan

Current:

```text
MessagesGetDialogs
 ↓
Apply
```

dan error hanya log warning.

Itu acceptable karena warmup adalah optimization.

Tetapi jangan sampai feature menganggap:

```text
peer cache complete
```

padahal warmup gagal.

Harus ada readiness distinction:

```text
TelegramReady
PeerCacheWarm
```

dua state berbeda.

---

# 6.14 Score Telegram Reliability

**6/10**

Access hash: **7**

FloodWait: **6**

Network retry: **4**

Error classification: **5**

Idempotency: **4**

Rate limiting: **6**

Readiness: **8**

---

# 7. Cross-cutting Problem: semua ini sebenarnya satu masalah

Kelima area ini saling berhubungan.

Contoh nyata:

```text
Scheduler
   ↓
ExecutionSource=Scheduled
   ↓
Command
   ↓
PeerResolver
   ↓
InputPeer
   ↓
AccessHash
   ↓
Telegram Service
   ↓
FloodWait
   ↓
Retry
   ↓
EventBus
   ↓
UserLog
```

Kalau salah satu layer melakukan:

```text
fallback
ignore error
fabricate peer
drop event
```

hasil akhirnya:

```text
feature "randomly fails"
```

---

# 8. Architecture yang saya rekomendasikan

Saya akan upgrade menjadi:

```text
                         Telegram
                            │
                            ▼
                     Update Dispatcher
                            │
                ┌───────────┴───────────┐
                │                       │
             Dedup                 Entity Cache
                │                       │
                ▼                       ▼
          Security Layer          Peer Resolver
                │                       │
                │                 ┌─────┴─────┐
                │                 │           │
                │              Memory       SQLite
                │                 │           │
                │                 └─────┬─────┘
                │                       │
                ▼                       ▼
          Execution Context       ResolvedPeer
                │
                ▼
        Middleware Pipeline
                │
                ├── Auth
                ├── Permission
                ├── Source Policy
                ├── Chat Policy
                ├── Rate Limit
                ├── Cooldown
                └── Timeout
                │
                ▼
             Executor
                │
                ▼
         Telegram Service
                │
         ┌──────┼──────┐
         │      │      │
       Retry  Flood  Recovery
         │      │      │
         └──────┼──────┘
                │
                ▼
             EventBus
                │
       ┌────────┼─────────┐
       ▼        ▼         ▼
    UserLog   Metrics    Audit
```

---

# 9. Peer subsystem target

Saya sangat menyarankan membuat:

```go
type PeerRef struct {
    Kind       PeerKind
    ID         int64
    AccessHash *int64
}

type ResolvedPeer struct {
    Ref        PeerRef
    Input      tg.InputPeerClass
    Entity     tg.PeerClass
    Source     ResolutionSource
}
```

dan satu API:

```go
ResolvePeer(ctx, PeerReference) (ResolvedPeer, error)
```

Dengan:

```text
PeerReference
 ├── UserID
 ├── ChatID
 ├── ChannelID
 ├── Username
 ├── Phone
 ├── Reply
 └── CurrentPeer
```

Semua feature menggunakan API ini.

---

# 10. Error taxonomy target

Jangan hanya:

```text
ErrNotFound
ErrPermissionDenied
ErrTelegram
ErrRateLimit
```

Tambahkan:

```text
ErrPeerNotFound
ErrPeerTypeMismatch
ErrAccessHashMissing
ErrAccessHashInvalid
ErrPeerInvalid
ErrMessageNotFound
ErrFloodWait
ErrTransientTelegram
ErrPermanentTelegram
ErrAlreadyApplied
ErrNotModified
```

Dengan demikian:

```text
PEER_ID_INVALID
```

bisa triggering recovery.

Sedangkan:

```text
CHAT_WRITE_FORBIDDEN
```

tidak.

---

# 11. Lifecycle target

Saya ingin lifecycle eksplisit:

```text
NEW
 ↓
STARTING
 ↓
RUNNING
 ↓
QUIESCING
 ↓
STOPPING
 ↓
STOPPED
```

Dan setiap subsystem:

```text
Dispatcher
Scheduler
PluginManager
EventBus
AddonManager
TelegramClient
```

mengikuti state yang sama.

---

# 12. Shutdown target

Final order:

```text
1. Set App = QUIESCING

2. Stop accepting:
   - Telegram updates
   - new commands
   - addon calls
   - scheduler claims

3. Dispatcher.Stop()

4. Scheduler.Stop()

5. AddonManager.ShutdownRuntimes()

6. PluginManager.Shutdown()

7. EventBus.Close()
   drain

8. Callback/Inline stores

9. Rate limiter

10. Telegram client/session

11. DB.Close()

12. Logger.Sync()
```

Dengan satu global deadline:

```text
30 sec
```

dan tiap subsystem menerima child context.

---

# 13. EventBus target

Gunakan:

```go
type EventEnvelope struct {
    ID           string
    Type         EventType
    Timestamp    time.Time
    Correlation  string
    OrderingKey  string
    Payload      Event
}
```

dan:

```text
subscriber-specific queues
```

dengan:

```text
dropped counter
panic counter
processing latency
queue depth
```

---

# 14. Addon target

Kalau ingin benar-benar production-grade:

```text
Addon
 ↓
manifest
 ↓
signature/hash
 ↓
capability policy
 ↓
isolated UID
 ↓
isolated directory
 ↓
resource limits
 ↓
network policy
 ↓
seccomp/AppArmor
 ↓
IPC broker
```

Kalau belum mau implement OS sandbox, jangan menyebutnya sandbox.

Sebut:

> **External Addon Runtime with Capability-Gated IPC**

Itu jauh lebih akurat.

---

# 15. Telegram reliability target

Buat satu central operation wrapper:

```go
func (s *Service) ExecuteRPC[T any](
    ctx context.Context,
    operation Operation,
    peer PeerRef,
    fn func() (T, error),
) (T, error)
```

yang menangani:

```text
classification
 ↓
FloodWait
 ↓
transient retry
 ↓
access hash recovery
 ↓
idempotency
 ↓
metrics
 ↓
structured logging
```

Sehingga tidak setiap:

```text
SendMessage
EditMessage
DeleteMessage
Block
Ban
Forward
```

membuat logic retry sendiri.

---

# 16. Priority implementation

## 🔴 P0

Saya akan melakukan ini **sebelum fitur baru**:

### Peer

1. Fix `-123` basic-group resolution.
2. Stop returning successful peers with missing access hash.
3. Introduce typed `PeerRef`.
4. Centralize `InputPeer` construction.
5. Access-hash invalidation + re-resolution.
6. Typed Telegram peer errors.

### Lifecycle

7. `App.Shutdown()` → `Dispatcher.Stop()`.
8. `App.Shutdown()` → `AddonManager.ShutdownRuntimes()`.
9. Prevent new work during shutdown.
10. Ensure DB closes only after all DB workers exit.

### Execution

11. Put `ExecutionSource` inside `Context`.
12. Remove source inference from `"sched-"` correlation prefix.
13. Add explicit source propagation.

### EventBus

14. Make drops observable.
15. Make panic observable.
16. Define ordering policy.

### Addon

17. Rename "sandbox" semantics to process isolation unless OS sandbox is added.
18. Fix IPC reader goroutine lifecycle.
19. Add runtime shutdown state.
20. Prevent runtime calls during shutdown.

### Telegram

21. Centralize retry/recovery.
22. Distinguish stale access hash from not-found.
23. Add transient network retry.
24. Handle ambiguous send operations carefully.

---

# 17. P1

Setelah P0:

```text
25. Peer entity TTL/refresh
26. Entity normalization
27. Peer resolution metrics
28. subscriber-specific EventBus queues
29. event IDs
30. event ordering keys
31. dispatcher handler priorities
32. source-aware rate limiting
33. ChatContextUnknown semantics
34. addon resource limits
35. addon log capture
36. addon signature verification mandatory
37. Telegram operation-specific retry policy
38. destination-level rate limiting
39. idempotency registry
40. peer resolution tracing
```

---

# 18. P2

```text
41. distributed-style durable event outbox
42. persistent RPC operation journal
43. OS-level addon sandbox
44. peer reconciliation background job
45. automatic peer cache repair
46. telemetry dashboard
47. lifecycle diagnostics
48. dependency graph lifecycle
```

---

# 19. Kesimpulan

Yang menarik dari audit ini adalah **Goultroid sebenarnya tidak membutuhkan rewrite besar**.

Fondasi yang sudah dibuat cukup bagus:

* `PeerResolver`
* `PeerStorage`
* `peers.Manager`
* `ExecutionSource`
* `CommandExecution`
* scheduler lease
* bounded EventBus
* capability broker
* external addon process
* centralized Telegram service
* FloodWait handling
* Telegram readiness gate

Contohnya scheduler sekarang memang sudah menunggu Telegram readiness sebelum mulai sehingga tidak mengklaim scheduled jobs terlalu dini.

Tetapi ada beberapa **kontradiksi antara abstraction contract dan implementasi aktual**.

Yang paling penting:

### 🔴 #1 PeerResolver

```text
"guaranteed access hash"
```

tetapi masih bisa mengembalikan:

```text
AccessHash=0
```

dan bahkan salah mengubah:

```text
basic group -123
```

menjadi:

```text
channel 123
```

Ini harus diperbaiki pertama.

### 🔴 #2 Shutdown

`Dispatcher.Stop()` sudah dibuat, tetapi tidak dipanggil oleh `App.Shutdown()`. Addon `ShutdownRuntimes()` juga tersedia tetapi tidak dipanggil.

Ini berarti **shutdown architecture saat ini belum benar-benar closed-loop**.

### 🔴 #3 Addon "sandbox"

Saat ini sebenarnya:

```text
external process + capability broker
```

bukan OS sandbox.

Addon masih berjalan dengan privilege OS yang sama.

### 🟠 #4 EventBus

Sudah aman dari blocking dan panic propagation, tetapi:

```text
drop
ordering
dedup
subscriber isolation
```

belum production-grade.

### 🟠 #5 Telegram reliability

Sudah ada FloodWait dan access-hash preparation, tetapi belum ada:

```text
stale hash recovery
generic transient retry
idempotency
operation classification
```

yang sangat penting untuk userbot.

---

## Prioritas keseluruhan saya

```text
                    CURRENT
                       │
                       ▼
          ┌────────────────────────┐
          │  1. Peer Subsystem     │ ← FIRST
          └────────────┬───────────┘
                       │
          ┌────────────▼───────────┐
          │  2. Lifecycle          │
          └────────────┬───────────┘
                       │
          ┌────────────▼───────────┐
          │  3. Execution Context  │
          └────────────┬───────────┘
                       │
          ┌────────────▼───────────┐
          │  4. Event Pipeline     │
          └────────────┬───────────┘
                       │
          ┌────────────▼───────────┐
          │  5. Telegram RPC       │
          └────────────┬───────────┘
                       │
          ┌────────────▼───────────┐
          │  6. Addon Hardening    │
          └────────────┬───────────┘
                       │
                       ▼
              PRODUCTION-GRADE
```

**Kalau saya yang melanjutkan repo ini, saya akan berhenti menambah feature parity dulu dan melakukan satu fase khusus “Core Reliability / Infrastructure Hardening”.** Lima subsystem di atas adalah dependency horizontal untuk hampir semua plugin. Memperbaikinya sekarang akan sekaligus memperbaiki PMPermit, UserLog, Scheduler, Broadcast, Admin, Notes, callbacks, addon, dan fitur-fitur lain yang menggunakan peer/Telegram operations.
