Ya. Saya **fetch ulang branch `main` terbaru** dan kali ini saya fokus khusus pada **arsitektur, struktur package, ownership, dependency direction, lifecycle, serta gaya penulisan kode**—bukan lagi audit fitur Telegram/UI secara umum.

## Kesimpulan singkat

**Sekarang arsitekturnya sudah jauh lebih bagus dan sudah masuk kategori well-structured production-oriented Go codebase.**

Kalau saya beri skor khusus:

| Area                        |    Kondisi |
| --------------------------- | ---------: |
| Composition root            |   **9/10** |
| Dependency injection        | **8.5/10** |
| Package separation          | **8.5/10** |
| Domain/application boundary |   **8/10** |
| Telegram adapter isolation  | **8.5/10** |
| Dispatcher structure        |   **9/10** |
| Plugin architecture         |   **8/10** |
| Settings architecture       | **8.5/10** |
| Interaction architecture    |   **8/10** |
| Lifecycle                   | **7.5/10** |
| Repository architecture     |   **6/10** |
| Event architecture          | **6.5/10** |
| Error architecture          | **7.5/10** |
| Type safety                 |   **8/10** |
| Code style                  | **7.5/10** |
| Architectural enforcement   |   **8/10** |
| Test architecture           |   **8/10** |

**Overall: ~8/10.**

Jadi saya **tidak menyarankan rewrite besar-besaran**. Yang sekarang dibutuhkan adalah **architectural consolidation/refinement**, terutama pada repository, event semantics, lifecycle ownership, service boundaries, dan konsistensi coding style.

---

# 1. Struktur sekarang sudah jauh lebih benar

Dari struktur terbaru terlihat kira-kira:

```text
cmd/
  goultroid/
    main.go

internal/
  app/
  addon/
  assistant/
  config/
  core/
  database/
  plugin/
  scheduler/
  settings/
  telegram/
  ui/
  services/
    broadcast/
    callback/
    download/
    inline/
    interaction/
    localization/
    media/
    moderation/
    pmpermit/
    process/
    ratelimit/
    storage/
    userlog/

plugins/
```

Ini sudah jauh lebih sehat daripada model:

```text
plugin
 ↓
telegram
 ↓
database
 ↓
everything
```

Sekarang sudah terlihat pembagian:

```text
cmd
 ↓
app
 ↓
core / infrastructure / services
 ↓
plugins
```

dan `core` secara eksplisit dijaga supaya tidak mengimpor layer atas. Bahkan sudah ada architectural tests untuk memaksa aturan tersebut.

**Ini bagus.**

---

# 2. `cmd/goultroid/main.go` sekarang sangat sehat

`main.go` hanya melakukan:

```text
load env
    ↓
load config
    ↓
create signal context
    ↓
app.New()
    ↓
app.Run()
    ↓
app.Shutdown()
```

Itu tepat untuk Go application.

Saat ini `main.go` hanya sekitar fungsi bootstrap dan tidak menjadi tempat business logic.

### Target ideal

```go
func main() {
    cfg := mustLoadConfig()

    ctx, stop := signal.NotifyContext(...)
    defer stop()

    app := mustNewApp(cfg)

    if err := app.Run(ctx); err != nil {
        ...
    }

    shutdown(...)
}
```

Anda sudah sangat dekat dengan bentuk ini.

**Status: KEEP.**

---

# 3. `App` sudah menjadi Composition Root yang benar

Ini salah satu improvement terbesar.

`App` sekarang bertanggung jawab terhadap ownership:

```text
App
 ├── DB
 ├── Telegram client
 ├── Plugin manager
 ├── Router
 ├── Scheduler
 ├── EventBus
 ├── Assistant
 ├── RateLimiter
 ├── Addon manager
 ├── Callback state
 ├── Inline engine
 └── Settings service
```

`app.New()` juga sudah dipecah menjadi:

```text
buildCore()
buildTelegramRuntime()
buildDomainServices()
buildPlugins()
```

Commit terbaru memang secara eksplisit memindahkan bootstrap besar menjadi wiring modules.

### Ini architectural pattern yang benar:

```text
Composition Root
       │
       ├── construct
       ├── configure
       ├── connect
       └── own lifecycle
```

Plugin tidak seharusnya membuat database sendiri.

Service tidak seharusnya membuat Telegram client sendiri.

Dispatcher tidak seharusnya membuat EventBus sendiri.

Dan sekarang sebagian besar sudah mengikuti aturan tersebut.

---

# 4. Tetapi `App` masih terlalu mengetahui banyak concrete components

Ini bukan bug, tetapi architectural smell tingkat menengah.

Sekarang:

```go
type App struct {
    cfg             *config.Config
    logger          *zap.Logger
    db              *database.DB
    client          *telegram.Client
    plugins         *plugin.Manager
    router          *core.Router
    sched           *scheduler.Engine
    eventBus        *core.EventBus
    assistant       assistant.Client
    limiter         *ratelimit.Limiter
    addonMgr        *addon.Manager
    callbackStore   *callback.StateStore
    inlineEngine    *inline.Engine
    settingsService *settings.Service
}
```

Ini masih acceptable karena `App` memang composition root.

Tetapi target jangka panjang:

```text
App
 ├─ Runtime
 │    ├─ Telegram
 │    ├─ Dispatcher
 │    └─ Interaction
 │
 ├─ Domain
 │    ├─ Scheduler
 │    ├─ Settings
 │    ├─ Moderation
 │    └─ PMPermit
 │
 └─ Infrastructure
      ├─ DB
      ├─ EventBus
      └─ RateLimiter
```

Jadi bukan:

```text
App = list of everything
```

melainkan:

```text
App
 ├── Runtime
 ├── Services
 └── Infrastructure
```

**Priority: P2.**

Jangan refactor sekarang kalau belum ada pain nyata.

---

# 5. Dispatcher sekarang jauh lebih sehat

Ini improvement yang sangat bagus.

Sebelumnya dispatcher terlalu besar.

Sekarang:

```text
dispatcher.go
dispatcher_peer.go
dispatcher_handlers.go
dispatcher_accessors.go
dispatcher_callback.go
dispatcher_dispatch.go
```

dan `Dispatcher` sendiri sekitar 120 LOC untuk struktur utamanya.

Ini prinsip yang benar:

> **Split by responsibility, not arbitrary file size.**

Jadi:

```text
Dispatcher
 ├── lifecycle
 ├── message dispatch
 ├── callback
 ├── peer
 ├── handlers
 └── accessors
```

### Saya setuju dengan refactor ini.

Namun ada satu tahap berikutnya.

---

# 6. Dispatcher masih merupakan "orchestrator", bukan pure adapter

Ini masih sehat, tetapi harus dijaga.

Ideal:

```text
Telegram Update
      ↓
Dispatcher
      ↓
Execution Context
      ↓
Use Case
      ↓
Domain Service
      ↓
Repository
```

Jangan sampai berkembang menjadi:

```text
Dispatcher
 ├── authorization
 ├── DB
 ├── moderation logic
 ├── settings logic
 ├── Telegram API
 ├── formatting
 ├── retry
 └── business rules
```

Sekarang arahnya sudah benar, tetapi ini **architectural invariant yang harus dipertahankan**.

Architectural test Anda bahkan sudah secara eksplisit memeriksa agar `internal/telegram` tidak mengimpor `internal/settings`.

**KEEP THIS RULE.**

---

# 7. Router command sudah bagus, tetapi API-nya masih bisa diperdalam

`core.Router` sekarang:

```go
Register()
RegisterBatch()
Parse()
Find()
All()
```

dan registration batch sudah atomic:

```text
validate entire batch
        ↓
if conflict
        ↓
NO mutation
```

Ini sangat bagus.

`Parse()` juga sudah mempunyai tokenizer sendiri untuk:

```text
quoted args
single quotes
double quotes
escape
raw args
```

### Tetapi ada satu architectural concern:

`core.Router` masih memegang concrete:

```go
map[string]Command
```

dan command itu kemungkinan cukup kaya dengan handler runtime.

Jangka panjang saya lebih suka:

```text
CommandDefinition
CommandHandler
CommandRegistry
CommandParser
```

dipisahkan secara konseptual.

Misalnya:

```go
type CommandDefinition struct {
    Name        string
    Aliases     []string
    Description string
    Usage       string
    Flags       []Flag
}
```

dan:

```go
type CommandHandler interface {
    Execute(ctx *ExecutionContext, cmd ParsedCommand) error
}
```

Tidak harus langsung dibuat sekarang.

**Priority: P2.**

---

# 8. Plugin architecture sudah bagus, tetapi ada satu masalah penting

`plugin.Manager` sekarang melakukan:

```text
validate plugin
    ↓
Init plugin
    ↓
register commands
    ↓
register hooks
    ↓
metadata
    ↓
store plugin
```

Itu reasonable.

Namun:

```go
m.mu.Lock()
defer m.mu.Unlock()
...
p.Init()
...
m.router.RegisterBatch(...)
...
hook registration
```

Artinya **manager mutex ditahan selama initialization plugin**.

Ini tidak ideal.

Kalau plugin `Init()`:

```text
takes long
calls manager
calls another subsystem
waits for callback
```

bisa terjadi contention atau deadlock.

### Target:

```text
1. validate under lock
2. release lock
3. initialize plugin
4. register atomically
5. reacquire lock for commit
```

Lebih bagus lagi gunakan lifecycle state:

```text
discovered
 ↓
initializing
 ↓
registered
 ↓
running
 ↓
stopping
 ↓
stopped
```

Ini akan sangat membantu kalau nanti plugin hot-load/unload diperkenalkan.

**Priority: P1/P2.**

---

# 9. Plugin registration belum benar-benar transactional

Misalnya:

```text
plugin.Init()
    ↓ success

register commands
    ↓ success

register hooks
    ↓ success

metadata
    ↓
commit
```

Sekarang sebagian rollback sudah ada kalau command registration gagal.

Tetapi architectural target:

```text
Prepare
   ↓
Validate
   ↓
Initialize
   ↓
Register
   ↓
Commit
```

atau:

```go
registration := pluginManager.BeginRegistration(plugin)

registration.Commands(...)
registration.Hooks(...)
registration.Settings(...)
registration.Callbacks(...)

registration.Commit()
```

Ini nanti menjadi penting ketika plugin mempunyai:

```text
commands
callbacks
settings
event handlers
message hooks
scheduler jobs
resources
```

Jangan sampai plugin half-installed.

**Priority: P1 untuk plugin ecosystem jangka panjang.**

---

# 10. Settings sekarang sudah cukup matang

Ini salah satu bagian yang paling improved.

Sekarang ada:

```text
SettingDefinition
SettingScope
ScopeRef
SettingValue
Registry
Service
cache
hierarchical resolution
typed conversion
validation
canonicalization
import/export
outbox
```

`ScopeRef` juga sudah memperjelas:

```text
global → ID 0
chat   → non-zero chat ID
user   → non-zero user ID
```

Dan registry sudah immutable-ish dari sisi consumer karena `Get()` mengembalikan copy definition.

Ini bagus.

---

# 11. Tetapi Settings masih terlalu "stringly typed" di persistence boundary

Ini:

```go
Value string
ValueType string
```

masih valid untuk generic settings.

Tetapi:

```text
DB
 ↓
string
 ↓
SettingValue
 ↓
BoolE()
IntE()
DurationE()
```

berarti type safety baru terjadi **setelah persistence**.

Untuk generic settings ini memang acceptable.

Namun target ideal:

```go
type SettingValue struct {
    Type SettingType
    Raw  string
}
```

atau bahkan internal typed representation:

```go
type Value struct {
    Bool     *bool
    Int      *int64
    String   *string
    Duration *time.Duration
    Enum     *string
}
```

Tidak perlu mengubah DB.

Jadi:

```text
DB serialization = string
Domain representation = typed
```

Ini lebih clean.

---

# 12. Settings cache punya masalah arsitektur yang lebih penting

Saat ini cache:

```go
map[string]map[resolveCacheKey]string
```

dengan:

```text
namespace:key
    ↓
(userID, chatID)
    ↓
value
```

Ini workable.

Tetapi invalidation sekarang:

```text
setting changed
   ↓
delete entire namespace:key cache
```

Ini aman tetapi kasar.

Lebih ideal:

```text
cache entry:
(namespace,key,userID,chatID)
```

dan event membawa scope:

```text
scope_type
scope_id
```

sehingga bisa melakukan targeted invalidation.

Contoh:

```text
chat 123 setting changed
       ↓
invalidate:
(chat=123)
```

bukan:

```text
invalidate every resolved value of foo:bar
```

Kalau user/chats banyak, ini akan lebih scalable.

**Priority: P2.**

---

# 13. `EventBus` adalah area yang paling perlu Anda perhatikan sekarang

Ini menurut saya **weakest architectural area saat ini**.

EventBus sekarang sengaja:

```text
non-blocking
bounded queue
8 workers
drop when full
```

dan dokumentasinya memang mengatakan event observational adalah best-effort.

Untuk:

```text
metrics
logging
analytics
notifications
```

ini bagus.

Tetapi Anda sekarang memasukkan:

```text
SettingChangedEvent
```

ke mekanisme tersebut.

Padahal settings mempunyai:

```text
transactional outbox
```

yang secara konsep berarti:

> event ini penting dan tidak boleh hilang.

Di sinilah ada ketidaksesuaian arsitektur.

---

# 14. Ada potensi semantic conflict: durable outbox vs best-effort EventBus

Sekarang kira-kira:

```text
Set()
 ↓
DB transaction
 ↓
outbox
 ↓
EventBus.Publish()
```

lalu worker:

```text
outbox
 ↓
EventBus.Publish()
 ↓
MarkProcessed()
```

Masalahnya:

```text
EventBus.Publish()
```

**bisa drop** ketika queue penuh.

Tetapi outbox worker kemudian tetap:

```text
MarkOutboxProcessed()
```

Jadi:

```text
DB says event processed
        BUT
subscriber never received event
```

Ini bertentangan dengan semantics durable outbox.

Ini menurut saya **P1 architectural issue**.

### Harus dipisahkan:

```text
Durable Event
    ↓
durable delivery mechanism
```

dan:

```text
Observational Event
    ↓
best-effort EventBus
```

Misalnya:

```text
EventBus
 ├── PublishBestEffort()
 └── PublishDurable()
```

atau lebih bersih:

```text
EventBus
DurableEventDispatcher
```

---

# 15. Jangan membuat semua event menjadi durable

Ini penting.

Jangan:

```text
EVERYTHING → DB outbox
```

Itu malah overengineering.

Gunakan klasifikasi:

### Critical

```text
setting changed
scheduler state transition
financial state
security state
```

→ durable.

### Operational

```text
plugin started
plugin stopped
cache refreshed
```

→ maybe best-effort.

### Observational

```text
metrics
typing
analytics
debug
```

→ best-effort.

Jadi Event contract harus menyatakan delivery guarantee.

Misalnya:

```go
type DeliveryGuarantee int

const (
    BestEffort DeliveryGuarantee = iota
    Durable
)
```

---

# 16. Event ordering juga belum menjadi contract

EventBus menggunakan:

```text
8 workers
```

dan subscriber map.

Akibatnya:

```text
SettingChanged(foo=true)
SettingChanged(foo=false)
```

bisa diterima:

```text
false
true
```

oleh subscriber yang sama jika concurrency memungkinkan.

Untuk event tertentu ini tidak masalah.

Untuk:

```text
state transition
```

bisa fatal.

Target:

```text
Event class:
    Ordered
    Unordered
```

atau partition berdasarkan:

```text
aggregate key
```

misalnya:

```text
settings:global:core
settings:chat:-100123
scheduler:job:123
user:12345
```

lalu event dalam partition yang sama ordered.

**Priority: P1/P2.**

---

# 17. `Event` interface masih terlalu minimal

Sekarang:

```go
type Event interface {
    Type() EventType
    Timestamp() time.Time
}
```

Untuk sistem yang semakin kompleks, saya sarankan:

```go
type Event interface {
    Type() EventType
    Timestamp() time.Time
    EventID() string
    CorrelationID() string
    CausationID() string
}
```

Tidak semua harus dipakai sekarang.

Tetapi minimal:

```text
event_id
```

sangat berguna untuk deduplication.

Terutama karena Anda sudah mulai menggunakan durable outbox.

---

# 18. Repository adalah architectural hotspot terbesar

Ini yang paling jelas dari fetch terbaru.

`internal/database/repository.go` sekarang sekitar **49 KB**. Struktur database sendiri sudah punya file domain seperti:

```text
addon.go
moderation.go
pmpermit.go
voice.go
repository.go
```

tetapi `repository.go` masih menjadi pusat besar untuk banyak domain.

Isi repository mencakup:

```text
sudo
notes
AFK
filters
scheduler
job history
peer metadata
blacklist
settings
...
```

dan interface `Repository` sendiri sangat besar.

Ini **God Repository**.

---

# 19. Target repository architecture

Daripada:

```go
type Repository interface {
    GetSudoUsers(...)
    SaveNote(...)
    GetAFK(...)
    SaveFilter(...)
    CreateScheduledJob(...)
    ...
    GetSetting(...)
    ...
    SaveVoiceSession(...)
}
```

lebih sehat:

```text
database/
    db.go
    migrations.go

    repositories/
        sudo.go
        notes.go
        afk.go
        filters.go
        scheduler.go
        peers.go
        blacklist.go
        settings.go
        pmpermit.go
        voice.go
        addon.go
```

Lalu interface per domain:

```go
type SettingsRepository interface {
    Get(...)
    Set(...)
    Delete(...)
    List(...)
}
```

```go
type SchedulerRepository interface {
    Create(...)
    Claim(...)
    Complete(...)
    Fail(...)
    RenewLease(...)
}
```

```go
type PeerRepository interface {
    Save(...)
    FindByUsername(...)
    ...
}
```

Ini jauh lebih atomic.

---

# 20. Jangan membuat satu `Repository` interface besar

Ini sangat penting.

Sekarang service cukup menerima:

```go
database.Repository
```

Akibatnya Settings Service mengetahui interface yang berisi:

```text
scheduler
notes
AFK
filters
voice
PMPermit
settings
...
```

padahal dia hanya butuh:

```go
SettingsRepository
```

Ini melanggar semangat **Interface Segregation Principle**.

Target:

```text
SettingsService
    ↓
SettingsRepository

SchedulerService
    ↓
SchedulerRepository

PMPermitService
    ↓
PMPermitRepository
```

dan implementasinya boleh berasal dari object DB yang sama:

```go
type DB struct {
    ...
}

func (db *DB) GetSetting(...)
func (db *DB) CreateScheduledJob(...)
```

Tidak perlu memecah `DB` menjadi banyak connection.

Yang dipecah adalah **contract/interface dan file responsibility**.

**Priority: P1.**

---

# 21. Ini juga akan membuat testing jauh lebih bagus

Sekarang test Settings harus mock:

```text
entire Repository
```

padahal cuma butuh:

```text
GetSetting
SetSetting
DeleteSetting
...
```

Setelah split:

```go
type fakeSettingsRepository struct {
    ...
}
```

Test menjadi jauh lebih kecil dan meaningful.

---

# 22. Interaction architecture sudah bergerak ke arah yang benar

Sekarang sudah ada:

```text
internal/services/interaction/
    navigation/
    wizard.go
```

dan UI hanya re-export/helper:

```go
type Navigator = navigation.Navigator
```

Ini justru **bagus**.

Stateful interaction tidak tinggal di `ui`.

`ui` hanya rendering/presentation.

`interaction` memiliki state/navigation.

Ini boundary yang saya rekomendasikan.

---

# 23. Tetapi Wizard masih terlalu generic dan stringly typed

Sekarang:

```go
Data map[string]string
```

Ini cocok untuk MVP.

Tetapi ketika wizard berkembang:

```text
scheduler wizard
moderation wizard
settings wizard
broadcast wizard
plugin setup wizard
```

semua data menjadi:

```go
map[string]string
```

Akhirnya:

```text
"chat_id"
"interval"
"action"
"confirm"
```

menjadi magic strings.

Target:

```go
type WizardData struct {
    values map[string]any
}
```

atau lebih bagus, wizard memiliki typed state sendiri:

```go
type Wizard[T any] struct {
    State T
}
```

Namun jangan buru-buru memakai generics kalau tidak memberi manfaat nyata.

Lebih sederhana:

```go
type Step interface {
    ID() string
    Render(...)
    Validate(...)
    Apply(...)
}
```

Wizard engine hanya mengelola lifecycle.

**Business data tetap milik wizard/use-case.**

---

# 24. Wizard saat ini juga belum sepenuhnya state machine

Walaupun komentar menyebut:

> multi-step interactive wizard sessions with state, validation, and lifecycle

implementasinya masih:

```text
Current++
Current--
SetData()
```

Belum:

```text
State A
 ├─ valid → State B
 └─ invalid → State A

State B
 ├─ next → State C
 ├─ back → State A
 └─ cancel → End
```

Target yang lebih benar:

```go
type Transition struct {
    From string
    Event string
    To   string
}
```

Tapi ini **bukan P0**.

Kalau wizard hanya dipakai untuk settings sederhana, current design cukup.

---

# 25. Navigator sudah benar secara ownership, tapi mutable tanpa encapsulation

Sekarang:

```go
type Navigator struct {
    Stack []ScreenState
}
```

dan public:

```go
Stack
```

Artinya caller bisa:

```go
navigator.Stack = nil
navigator.Stack[0] = ...
```

tanpa melalui invariant.

Lebih baik:

```go
type Navigator struct {
    stack []ScreenState
}
```

kemudian:

```go
Current()
Root()
Depth()
CanPop()
Push()
Pop()
Reset()
```

Jangan expose internal mutable state.

**Priority: P1 kecil tetapi penting.**

---

# 26. `Screen` bagus, tetapi abstraction-nya masih sedikit redundant

Sekarang:

```go
Markup()
Text()
Render()
RenderScreen()
```

sementara:

```go
Render()
```

sudah menghasilkan:

```go
text, markup
```

kemudian:

```go
RenderScreen()
```

hanya membungkus kembali.

Ini bukan masalah besar, tetapi API bisa dibuat lebih sederhana:

```go
type RenderedScreen struct {
    Text   string
    Markup Markup
}

func (s Screen) Render() RenderedScreen
```

Satu canonical rendering method.

Jadi tidak perlu:

```text
Text()
Markup()
Render()
RenderScreen()
```

kecuali memang masing-masing dibutuhkan consumer.

**Priority: P2.**

---

# 27. `ui` sekarang boundary-nya sudah bagus

`internal/ui` sudah tidak langsung menjadi tempat business logic.

Architectural test bahkan melarang:

```text
ui → database
ui → scheduler
ui → plugins
```

Ini bagus sekali.

Target:

```text
UI
 ↓
Interaction / UseCase
 ↓
Domain
```

bukan:

```text
Button
 ↓
DB
```

---

# 28. Tetapi ada satu prinsip yang harus Anda kunci sekarang

Untuk setiap feature:

```text
Command
Button
Inline
Wizard
```

**jangan membuat empat business implementation.**

Harus:

```text
                 ┌── Command
Telegram input ──┼── Button
                 ├── Inline
                 └── Wizard
                        ↓
                    Use Case
                        ↓
                     Domain
                        ↓
                    Repository
```

Contoh:

```text
/settings prefix
```

dan:

```text
Settings UI
 → Prefix
 → Edit
```

harus sama-sama memanggil:

```go
settingsService.Set(...)
```

Bukan:

```text
command → direct DB
button  → settingsService
```

Ini salah satu architectural invariant terpenting GoUltroid.

---

# 29. Error handling sudah cukup bagus, tapi belum sepenuhnya unified

Sekarang sudah ada effort seperti:

```text
core errors
callback failures
MapUserErrorMessage
OperationResult
```

Ini bagus.

Tetapi target final sebaiknya punya taxonomy:

```text
Domain error
Application error
Infrastructure error
User-facing error
Cancellation
Timeout
Authorization
Validation
NotFound
Conflict
RateLimited
Unavailable
Internal
```

Misalnya:

```go
var (
    ErrNotFound
    ErrUnauthorized
    ErrForbidden
    ErrValidation
    ErrConflict
    ErrRateLimited
    ErrUnavailable
)
```

lalu wrapping:

```go
fmt.Errorf("load setting %s:%s: %w", key, ..., ErrNotFound)
```

UI kemudian mapping berdasarkan `errors.Is/As`.

Jangan mapping berdasarkan string error.

---

# 30. Ada satu code smell nyata di callback router

Pada `executeHandler`, sekarang ada:

```go
chain := Chain(
    final,
    TimeoutMiddleware(...),
    RecoverMiddleware(...),
)

_ = chain
```

kemudian handler masih dieksekusi dengan mekanisme manual.

Ini menunjukkan **refactor belum selesai sepenuhnya**.

Secara arsitektur:

```text
middleware chain exists
BUT
actual execution bypasses chain
```

Ini harus dibersihkan.

Pilih satu:

### Option A

```go
return chain.Handle(ctx, cbCtx)
```

### Option B

hapus abstraction middleware dan gunakan explicit execution.

Saya sangat menyarankan **A**.

Kalau tidak, codebase memiliki dua execution model:

```text
middleware pipeline
manual pipeline
```

yang nantinya mudah divergen.

**Priority: P1.**

---

# 31. Callback Router sendiri sudah cukup bagus

Saya suka struktur:

```text
Parse
 ↓
rate limit
 ↓
resolve state
 ↓
authorize scope
 ↓
consume single-use
 ↓
lookup handler
 ↓
timeout/recover
 ↓
execute
```

Ini sudah seperti pipeline yang benar.

Yang perlu dilakukan sekarang bukan rewrite, tetapi menjadikan pipeline tersebut **canonical**.

---

# 32. Context propagation sudah jauh lebih baik

Latest commit memang memperbaiki lifecycle root context dan wiring timeout.

`main` membuat signal context, lalu `App.Run(ctx)` meneruskannya.

Ini benar.

Tetapi saya masih akan audit seluruh repository untuk menemukan:

```go
context.Background()
context.TODO()
```

di:

```text
service
callback
plugin
scheduler
worker
repository
```

Rule:

```text
Background()
    hanya boundary/root

request context
    ↓
application
    ↓
service
    ↓
repository/API
```

---

# 33. Lifecycle sudah jauh lebih baik, tetapi masih belum benar-benar Supervisor-based

Sekarang:

```text
startBackgroundServices()
 ├── callback
 ├── inline
 ├── dispatcher
 ├── scheduler goroutine
 └── assistant goroutine
```

Ada beberapa `go func()` langsung.

Shutdown kemudian mencoba menghentikan component masing-masing.

Ini sudah functional.

Tetapi ideal:

```text
App Supervisor
 ├── Dispatcher
 ├── Scheduler
 ├── Assistant
 ├── EventBus
 ├── CallbackStore
 ├── InlineCache
 └── SettingsOutbox
```

dengan:

```go
type Component interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

Kemudian supervisor memiliki:

```text
start order
failure policy
stop order
wait
error propagation
```

Ini akan membuat lifecycle jauh lebih deterministic.

**Priority: P1/P2.**

---

# 34. Shutdown order sudah cukup bagus

Sekarang kira-kira:

```text
Dispatcher
 ↓
Scheduler
 ↓
Settings outbox
 ↓
Addon runtimes
 ↓
Plugins
 ↓
EventBus
 ↓
Interaction stores
 ↓
RateLimiter
 ↓
DB
 ↓
Logger
```

Secara konsep sudah benar:

> stop consumers before closing their dependencies.

Ini **KEEP**.

---

# 35. Tapi lifecycle ownership masih sedikit split

Contoh:

`callbackStore` dihentikan di:

```text
runLifecycle defer
```

dan juga:

```text
App.Shutdown()
```

Begitu juga inline cache.

Ini kemungkinan aman jika `Stop()` idempotent, tetapi ownership menjadi ambigu.

Pertanyaannya harus selalu punya jawaban tunggal:

> siapa yang owns Stop?

Saya sarankan:

```text
App.Shutdown()
    = ONLY lifecycle owner
```

`Run()` tidak melakukan resource shutdown.

Jadi:

```go
func Run(ctx) error {
    start()
    return client.Run(ctx)
}
```

dan:

```go
func Shutdown(ctx) error {
    ...
}
```

**Satu owner, satu shutdown path.**

---

# 36. Settings outbox worker juga punya lifecycle yang agak khusus

`settings.NewService()` langsung memulai goroutine apabila repository berupa concrete `*database.DB`.

Ini smell.

Service constructor seharusnya idealnya:

```text
NewService()
```

hanya membuat object.

Bukan:

```text
NewService()
    ↓
spawn goroutine
```

Target:

```go
service := settings.NewService(...)
service.Start(ctx)
```

Ini membuat ownership lifecycle eksplisit.

Sama dengan:

```text
EventBus
CallbackStore
InlineCache
Scheduler
```

**Construct != Start.**

Ini prinsip yang saya sangat rekomendasikan untuk GoUltroid.

---

# 37. Constructor semantics secara keseluruhan perlu distandarkan

Sekarang terdapat dua gaya:

```text
NewX()
    ↓
object ready

NewX()
    ↓
object + background worker
```

Ini berbahaya.

Saya sarankan aturan:

### `NewX`

Tidak:

```text
goroutine
network connection
background worker
external side effect
```

kecuali benar-benar immutable/synchronous initialization.

### `Start`

Melakukan:

```text
goroutine
subscriptions
timers
workers
network loops
```

### `Stop`

Melakukan:

```text
cancel
close
wait
```

Ini akan membuat lifecycle jauh lebih predictable.

---

# 38. Database migration architecture bagus

Database sudah mempunyai:

```text
migrations.go
versioning
checksums
transactional migration
```

dan banyak test.

Itu sudah bagus.

Tetapi struktur repository belum mengikuti struktur domain.

Jadi:

```text
Migration architecture = good
Repository architecture = needs refactor
```

Ini penting dibedakan.

---

# 39. Core package sekarang bagus, tetapi mulai terlalu besar

`internal/core` sekarang memiliki banyak:

```text
context
events
errors
router
command
cooldown
album
permissions
peer
...
```

Masalahnya bukan file besar.

Masalahnya adalah `core` mulai menjadi:

> "hal-hal yang tidak tahu harus ditaruh di mana"

Ini klasik Go smell.

Anda harus mencegah:

```text
core/
  util.go
  helper.go
  misc.go
  common.go
  ...
```

### Target:

`core` hanya berisi **stable primitives/contracts**.

Kalau sesuatu hanya digunakan oleh scheduler:

```text
scheduler/
```

Kalau hanya callback:

```text
callback/
```

Kalau hanya settings:

```text
settings/
```

Kalau hanya Telegram:

```text
telegram/
```

---

# 40. `core.EventBus` mungkin seharusnya bukan core selamanya

Saat ini `core` mengimpor:

```go
github.com/gotd/td/tg
```

karena event models memiliki:

```go
tg.InputPeerClass
tg.InputBotInlineMessageIDClass
```

Ini berarti `core` sebenarnya **tidak benar-benar Telegram-independent**.

Padahal architectural test menganggap `core` sebagai layer yang pure dan rendah.

Ini kontradiksi kecil.

Sekarang:

```text
core
 ↓
gotd/tg
```

Ideal:

```text
telegram adapter
 ↓
domain event
```

misalnya:

```go
type PeerRef struct {
    ID         int64
    AccessHash int64
    Type       PeerType
}
```

bukan:

```go
tg.InputPeerClass
```

di core.

**Ini salah satu improvement arsitektur paling penting berikutnya.**

Priority: **P1**.

---

# 41. Stable identity vs Telegram representation harus dipisahkan

Contoh sekarang:

```go
CallbackTarget {
    Peer tg.InputPeerClass
    InlineID tg.InputBotInlineMessageIDClass
}
```

Ini membuat domain model bergantung pada Telegram SDK.

Lebih sehat:

```text
Domain
    PeerRef
       ↓
Telegram adapter
    tg.InputPeerClass
```

Jadi:

```text
domain knows:
    user ID
    chat ID
    channel ID
    access hash
    peer kind

adapter knows:
    tg.InputPeerUser
    tg.InputPeerChat
    tg.InputPeerChannel
```

Ini juga sangat cocok dengan audit Anda sebelumnya mengenai **Peer / Entity Resolution**.

---

# 42. `database` juga masih terlalu mengetahui Telegram/domain semantics

Contoh:

```go
ScheduledJob {
    PeerType
    AccessHash
}
```

Ini workable.

Tetapi DB model sekarang mencampur:

```text
persistence representation
domain concepts
Telegram resolution data
```

Jangka panjang:

```text
database row
    ↓
mapper
    ↓
domain model
```

Contoh:

```go
database.ScheduledJobRow
```

dan:

```go
scheduler.Job
```

Tidak harus langsung dilakukan seluruhnya.

Tetapi untuk domain penting:

```text
scheduler
settings
peer
pmpermit
moderation
```

saya rekomendasikan.

---

# 43. Naming masih bisa diperbaiki

Ada beberapa API yang secara semantic belum konsisten.

Misalnya:

```text
Get
Find
Resolve
Load
List
All
Fetch
```

Harus punya aturan.

Saya sarankan:

```text
Get
    object wajib ada / error NotFound

Find
    object boleh tidak ada → nil,false

List
    collection

Resolve
    melakukan fallback/derivation

Load
    load lifecycle/persistent state

Save
    create/update

Create
    hanya create

Update
    hanya update

Delete
    remove
```

Ini kecil tetapi sangat membantu codebase besar.

---

# 44. `bool` semantics juga perlu distandarkan

Hindari:

```go
IsNotAllowed
NotEnabled
DisableX
```

yang menyebabkan:

```go
if !cfg.NotEnabled
```

lebih sulit dibaca.

Prefer:

```go
Enabled
Allowed
Active
CanExecute
HasPermission
```

dan:

```go
if cfg.Enabled
if !policy.Allowed
```

Ini masuk code-quality level, bukan architectural emergency.

---

# 45. Banyak one-line Go statement sebaiknya dibersihkan

Contoh dari router:

```go
if prefix == "" { prefix = "." }
```

atau:

```go
func (r *Router) Prefix() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.prefix }
```

Secara syntactic valid.

Tetapi untuk codebase sebesar GoUltroid saya lebih memilih:

```go
func (r *Router) Prefix() string {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.prefix
}
```

Kenapa?

Karena sekarang codebase sudah cukup kompleks sehingga **vertical readability** lebih penting daripada menghemat 2 baris.

`gofmt` tidak akan menyelesaikan semantic readability ini.

---

# 46. `types.go` Settings sudah bagus tetapi terlalu gemuk

`internal/settings/types.go` sekarang mencampur:

```text
SettingType
Scope
Category
Widget
Definition
validation
canonicalization
ScopeRef
SettingValue
typed conversion
```

~10 KB.

Lebih atomic:

```text
settings/
    types.go
    definition.go
    scope.go
    value.go
    validation.go
    registry.go
    service.go
```

Ini bukan berarti setiap 50 LOC harus dibuat file baru.

Rule saya:

> Pisahkan berdasarkan **semantic cohesion**, bukan ukuran file.

**Priority: P2.**

---

# 47. Test architecture sekarang justru sudah sangat bagus

Ini salah satu hal yang saya ingin pertahankan.

Anda sudah punya:

```text
arch_test.go
contract_test.go
settings_test.go
callback tests
race tests
```

CI juga menjalankan:

```text
gofmt
go vet
go test -race
go build
```

Dan `.golangci.yml` sudah mengaktifkan:

```text
govet
staticcheck
unused
ineffassign
errcheck
misspell
revive
gocritic
gofmt
goimports
```

Ini sudah bagus.

---

# 48. Tetapi CI belum menjalankan golangci-lint

Ini agak ironis.

Anda punya:

```text
.golangci.yml
```

tetapi CI yang saya fetch hanya melakukan:

```text
gofmt
go vet
go test -race
go build
```

Jadi konfigurasi lint tersebut **belum menjadi enforced CI gate**.

Saya sarankan:

```yaml
- name: GolangCI-Lint
  uses: golangci/golangci-lint-action@v...
```

atau menjalankan binary secara eksplisit.

**Priority: P1.**

---

# 49. Tambahkan static architectural checks yang lebih formal

Sekarang `arch_test.go` menggunakan parser dan string matching.

Bagus untuk awal.

Tetapi target lebih kuat:

```text
internal/core
    cannot import:
        app
        telegram
        database
        ui
        plugins
        services

internal/settings
    cannot import:
        telegram
        plugins
        app

internal/domain
    cannot import:
        database
        gotd/tg
        ui

plugins
    cannot import:
        database.DB
```

Dan kalau bisa gunakan package dependency graph tooling.

Tujuannya:

> Architecture should fail compilation/CI, not depend on developer discipline.

---

# 50. Saya akan membuat aturan dependency final seperti ini

```text
                    ┌───────────────┐
                    │      cmd      │
                    └───────┬───────┘
                            ↓
                    ┌───────────────┐
                    │      app      │
                    └───────┬───────┘
                            ↓
       ┌────────────────────┼────────────────────┐
       ↓                    ↓                    ↓
   adapters              services              domain
       ↓                    ↓                    ↓
   Telegram             use cases            entities
       ↓                    ↓                    ↓
       └───────────────→ repositories ←─────────┘
                              ↓
                              DB
```

Dengan:

```text
domain
  ❌ telegram
  ❌ database
  ❌ ui
  ❌ plugin

service
  ❌ UI rendering
  ❌ direct Telegram update parsing

telegram adapter
  ❌ business rules

UI
  ❌ DB

plugin
  ❌ raw DB
  ❌ lifecycle ownership of global resources
```

---

# 51. Arsitektur ideal GoUltroid menurut saya sekarang

Kalau kita refine codebase tanpa overengineering:

```text
cmd/
└── goultroid/
    └── main.go

internal/
├── app/
│   ├── app.go
│   ├── bootstrap.go
│   ├── lifecycle.go
│   └── shutdown.go
│
├── core/
│   ├── command/
│   ├── event/
│   ├── execution/
│   ├── permission/
│   ├── error/
│   └── ...
│
├── domain/
│   ├── settings/
│   ├── scheduler/
│   ├── moderation/
│   ├── peer/
│   └── ...
│
├── services/
│   ├── callback/
│   ├── interaction/
│   ├── moderation/
│   ├── pmpermit/
│   ├── scheduler/
│   └── ...
│
├── telegram/
│   ├── client/
│   ├── dispatcher/
│   ├── resolver/
│   └── adapter/
│
├── database/
│   ├── db.go
│   ├── migrations.go
│   └── repositories/
│
├── plugin/
├── ui/
└── settings/
```

**Tapi jangan langsung memindahkan semuanya.**

Struktur sekarang sudah cukup baik. Lakukan hanya extraction yang mengurangi coupling.

---

# 52. Prioritas fix yang saya sarankan sekarang

Saya akan mengurutkannya begini:

### P0 — jangan tambah fitur besar sebelum ini

**1. Selesaikan callback middleware pipeline**

Hilangkan:

```go
chain := ...
_ = chain
```

dan gunakan chain sungguhan.

---

**2. Benahi EventBus semantics**

Pisahkan:

```text
best-effort
```

dengan:

```text
durable
```

Terutama `SettingChangedEvent`.

---

**3. Pastikan outbox tidak dianggap processed sebelum delivery guarantee terpenuhi.**

Ini paling penting.

---

### P1 — architectural cleanup

**4. Pecah `database.Repository` menjadi domain-specific interfaces.**

```text
SettingsRepository
SchedulerRepository
PeerRepository
PMPermitRepository
...
```

---

**5. Pecah `repository.go` berdasarkan domain.**

Target:

```text
repository/
    settings.go
    scheduler.go
    peers.go
    notes.go
    moderation.go
    pmpermit.go
    voice.go
```

---

**6. Hilangkan Telegram `tg.*` dari domain/core event model bila memungkinkan.**

Buat:

```go
PeerRef
```

atau:

```go
TelegramPeerRef
```

di adapter boundary.

---

**7. Standardisasi lifecycle**

```text
New()
Start()
Stop()
```

Jangan constructor diam-diam membuat goroutine.

---

**8. Plugin registration transaction/state machine.**

---

**9. Jalankan golangci-lint di CI.**

---

### P2 — code quality

**10. Pecah `settings/types.go`.**

**11. Encapsulate `Navigator.Stack`.**

**12. Simplify `Screen.Render()` API.**

**13. Standardisasi naming `Get/Find/Resolve/List`.**

**14. Bersihkan one-line functions/statements.**

**15. Pisahkan domain model dari DB row model secara bertahap.**

---

# 53. Yang TIDAK saya sarankan

Saya **tidak** menyarankan sekarang melakukan:

```text
❌ rewrite seluruh core
❌ Clean Architecture 5–7 layers
❌ repository interface untuk setiap method
❌ generic repository[T]
❌ CQRS penuh
❌ event sourcing
❌ dependency injection framework
❌ microservices
❌ plugin process isolation
❌ pindahkan semua struct ke domain/
```

Untuk GoUltroid itu akan menjadi **overengineering**.

Yang dibutuhkan adalah:

```text
clear ownership
+
stable boundaries
+
small interfaces
+
deterministic lifecycle
+
typed domain state
+
explicit event semantics
```

---

# 54. Penilaian akhir saya

Dibanding audit sebelumnya, perubahan terbaru **memang signifikan**, bukan sekadar kosmetik.

Commit terbaru sendiri secara eksplisit melakukan:

```text
God Object remediation
Dispatcher split
Typed settings
Callback pipeline
Outbox
UI isolation
Interaction/navigation
Architecture tests
Contract tests
```

Dan setelah saya fetch ulang, saya setuju bahwa banyak masalah yang kita bahas sebelumnya **sudah benar-benar diperbaiki**.

### Kondisi sekarang:

```text
                    BEFORE          NOW
Architecture          5/10          8/10
Dispatcher             4/10          9/10
Settings               5/10          8.5/10
Callback               5/10          8.5/10
UI boundary            5/10          8/10
Lifecycle               5/10          7.5/10
Plugin                  6/10          8/10
Repository              5/10          6/10
Event semantics         5/10          6.5/10
Testing architecture    6/10          8/10
```

**Masalah utama sekarang bukan lagi "arsitekturnya buruk".**

Masalahnya sudah naik level menjadi:

> **"Bagaimana membuat arsitektur yang sudah bagus ini tetap deterministic, strongly bounded, dan tidak kembali menjadi God Object setelah fitur Ultroid bertambah?"**

Dan tiga area yang paling saya prioritaskan sekarang adalah:

```text
1. Repository boundary
   ↓
2. EventBus / durable event semantics
   ↓
3. Lifecycle / component supervision
```

Setelah tiga itu dibereskan, menurut saya GoUltroid sudah memiliki **fondasi arsitektur yang sangat solid untuk masuk fase feature parity dan plugin expansion**, tanpa perlu refactor besar lagi.

[1]: https://github.com/imtaqin/telegram-cli/blob/main/README.md?utm_source=chatgpt.com "telegram-cli/README.md at main · imtaqin/telegram-cli · GitHub"
