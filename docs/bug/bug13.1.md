Ada. Bahkan kalau targetnya **atomic, clean, robust, dan maintainable dalam jangka panjang**, pembahasan sebelumnya masih berada di level *arsitektur komponen*. Kita belum membedah beberapa **aturan mikro** yang justru sering menyebabkan codebase Go besar menjadi berantakan setelah fitur bertambah.

Saya akan membaginya menjadi **atomic engineering concerns**, supaya nanti bisa dijadikan checklist audit satu per satu.

## 1. Dependency ownership

Ini belum kita bahas cukup dalam.

Untuk setiap object harus jelas:

> siapa yang membuat, siapa yang memiliki, siapa yang boleh mengubah, dan siapa yang menghancurkan.

Contoh:

```text
App
 ├─ owns DB
 ├─ owns Telegram client
 ├─ owns Dispatcher
 └─ owns Scheduler
```

Jangan sampai:

```go
plugin.New(...)
    ↓
creates own DB
```

atau:

```go
service.New(...)
    ↓
creates Telegram client
```

Rule:

```text
Construction → composition root
Ownership    → parent component
Usage        → dependency injection
Shutdown     → owner
```

---

# 2. Mutability policy

Ini sering dilupakan.

Tentukan object mana yang:

```text
Immutable
Mutable
Runtime mutable
Concurrent mutable
```

Misalnya:

```go
type SettingDefinition struct {
    ...
}
```

seharusnya **immutable setelah registry initialization**.

Sedangkan:

```go
SettingState
```

memang mutable.

Jangan sampai:

```go
registry.Get("foo").Default = "bar"
```

bisa terjadi runtime.

Rule:

```text
Definition = immutable
Configuration = immutable snapshot
RuntimeState = mutable
SessionState = mutable
```

---

# 3. State ownership

Untuk setiap state harus bisa menjawab:

> state ini tinggal di mana?

Pisahkan secara eksplisit:

```text
Persistent State
    DB

Runtime State
    memory

Session State
    memory + TTL

Callback State
    short-lived store

Cache
    memory

Derived State
    jangan dipersist kalau bisa dihitung ulang
```

Ini sangat penting untuk menghindari:

```text
DB state ≠ memory state ≠ UI state
```

---

# 4. Source of Truth

Setiap data harus punya **satu canonical source**.

Contoh setting:

```text
DB = source of truth
cache = derived
UI = derived
runtime = projection
```

Jangan:

```text
DB
Memory setting
Plugin setting
UI setting
```

semuanya bisa menjadi sumber kebenaran.

Rule:

> **One authoritative state, many projections.**

---

# 5. Cache ownership & invalidation

Kita sebelumnya membahas cache resolver, tetapi belum atomic.

Untuk setiap cache tentukan:

```text
What?
Owner?
TTL?
Max size?
Eviction?
Invalidation?
Stale acceptable?
Refresh strategy?
```

Contoh:

```go
type PeerCache struct {
    ...
}
```

harus jelas:

```text
Write:
    resolver

Read:
    resolver

Invalidate:
    peer update

Eviction:
    LRU/TTL

Shutdown:
    App
```

Jangan punya cache tanpa invalidation strategy.

---

# 6. Concurrency ownership

Ini sangat penting di GoUltroid karena banyak goroutine.

Untuk setiap mutable object:

```text
Who writes?
Who reads?
What lock?
Can callbacks access concurrently?
Can shutdown race with operation?
```

Buat aturan:

```text
Mutex protects data
Channel coordinates events
Atomic protects counters/flags
Context controls lifecycle
```

Jangan menggunakan channel sebagai pengganti mutex hanya karena "Go style".

---

# 7. Lock ordering

Ini level atomic yang belum dibahas.

Kalau ada:

```text
App mutex
Settings mutex
Cache mutex
DB transaction
```

tentukan urutan:

```text
App
 ↓
Service
 ↓
Cache
 ↓
Repository
 ↓
DB
```

Jangan ada:

```text
goroutine A:
Settings → Cache

goroutine B:
Cache → Settings
```

karena deadlock.

Buat invariant:

> Never acquire locks in reverse order.

---

# 8. Critical section minimization

Jangan:

```go
mu.Lock()

telegramCall()

databaseCall()

networkCall()

mu.Unlock()
```

Harus:

```go
mu.Lock()
state := copyState()
mu.Unlock()

telegramCall(state)
```

External I/O tidak boleh dilakukan sambil memegang lock kecuali benar-benar diperlukan.

---

# 9. Context propagation

Kita sudah menemukan `context.Background()`, tetapi concern-nya lebih luas.

Setiap operation harus punya:

```text
request context
    ↓
service
    ↓
repository
    ↓
Telegram/network
```

Jangan:

```go
context.Background()
```

di tengah business logic.

Rule:

> Context dibuat di boundary, diteruskan ke bawah.

---

# 10. Timeout policy

Jangan semua operation bergantung pada parent context tanpa timeout.

Contoh:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
```

Tentukan timeout berdasarkan kategori:

```text
DB             2–5s
Telegram API   5–15s
HTTP           10–30s
callback       2–5s
shutdown       10–30s
```

Dan jangan menyebarkan angka magic ke seluruh codebase.

---

# 11. Cancellation semantics

Perlu dibedakan:

```text
success
failure
timeout
canceled
expired
shutdown
```

Jangan semua menjadi:

```go
if err != nil {
    return err
}
```

Misalnya:

```go
errors.Is(err, context.Canceled)
```

harus diperlakukan berbeda dari:

```text
Telegram API failure
```

---

# 12. Idempotency

Ini belum kita bahas cukup.

Semua operasi yang mungkin dieksekusi ulang harus punya policy:

```text
Safe
Idempotent
Non-idempotent
```

Contoh:

```text
Enable setting      → idempotent
Disable setting     → idempotent
Delete message      → mostly idempotent
Create scheduler    → NOT inherently idempotent
Ban user             → should be idempotent
Send message         → NOT idempotent
```

Callback retry + timeout bisa menyebabkan duplicate operation.

Jadi untuk operation tertentu:

```go
IdempotencyKey
```

harus tersedia.

---

# 13. Exactly-once vs at-least-once

Jangan mengasumsikan callback/event hanya dieksekusi sekali.

Realistically:

```text
Telegram update
    ↓
at least once
```

Maka design harus aman terhadap:

```text
duplicate update
duplicate callback
retry
reconnect
restart
```

Ini sangat penting untuk:

* scheduler
* broadcast
* moderation
* payments jika ada
* state-changing command

---

# 14. Transaction boundary

Kita membahas settings import, tetapi harus diperluas.

Setiap use-case harus jelas:

```text
Transaction starts
Transaction ends
```

Contoh:

```text
CreateFilter
 ├─ validate
 ├─ DB transaction
 │   ├─ filter
 │   └─ audit
 └─ commit
```

Bukan:

```text
validate
DB write
event
another DB write
telegram
```

tanpa boundary.

---

# 15. Repository contract

Repository harus fokus persistence.

Buruk:

```go
repo.SaveFilter(...)
    ↓
validate permission
    ↓
send Telegram
    ↓
render UI
```

Bagus:

```text
UseCase
 ├─ authorization
 ├─ validation
 ├─ transaction
 └─ repository
```

Repository:

```text
CRUD / query / transaction
```

---

# 16. Query responsibility

Jangan semua repository hanya punya:

```go
Get()
Set()
Delete()
```

yang kemudian application layer mengambil seluruh data dan filtering sendiri.

Misalnya:

```go
FindActiveJobs(...)
FindJobsByOwner(...)
FindExpiredPermits(...)
```

Query-specific methods sering lebih bersih dan lebih efisien.

---

# 17. N+1 query prevention

GoUltroid punya banyak:

```text
users
chats
peers
settings
messages
jobs
plugins
```

Harus audit:

```text
loop
  ↓
DB query
```

Contoh buruk:

```go
for _, user := range users {
    repo.GetUser(user.ID)
}
```

Target:

```go
repo.GetUsers(ids)
```

Ini sangat atomic tetapi berdampak besar pada production.

---

# 18. Data normalization vs denormalization

Untuk setiap DB table:

```text
Why normalized?
Why duplicated?
Who updates duplicate?
How consistency maintained?
```

Misalnya peer metadata/access hash.

Jangan menyimpan data Telegram yang bisa berubah di banyak tempat tanpa reconciliation policy.

---

# 19. Serialization boundary

Sekarang kita membahas callback state yang memakai string.

Lebih luas:

Tentukan:

```text
Domain type
↓
serialization type
↓
DB representation
↓
Telegram representation
```

Jangan memakai satu type untuk semuanya.

Contoh:

```text
SettingScope
    ↓
DB: TEXT
    ↓
Telegram callback: encoded ID
```

Masing-masing punya adapter.

---

# 20. Validation layers

Validation sebaiknya tiga level:

```text
Transport validation
    ↓
Domain validation
    ↓
Persistence constraints
```

Contoh:

```text
callback:
    malformed?

application:
    action allowed?

domain:
    value valid?

DB:
    unique / FK / CHECK
```

Jangan mengandalkan UI validation saja.

---

# 21. Authorization vs authentication

Ini harus benar-benar dipisah:

```text
Authentication:
    Who are you?

Authorization:
    What may you do?
```

Callback:

```text
Telegram user authenticated
        ↓
permission check
        ↓
scope check
        ↓
resource ownership
```

Jangan hanya:

```go
if userID == state.UserID
```

karena admin permission bisa berubah setelah state dibuat.

---

# 22. TOCTOU

Ini masalah security/robustness yang sering terlewat.

Contoh:

```text
Check permission
       ↓
wait
       ↓
execute operation
```

Permission bisa berubah di antara keduanya.

Untuk destructive operation:

```text
authorize
+
execute
```

harus sedekat mungkin, dan jika perlu authorization dilakukan kembali di use-case.

---

# 23. Resource lifecycle

Untuk semua resource:

```text
DB
HTTP client
Telegram client
goroutine
ticker
timer
file
connection
cache worker
```

harus ada:

```text
Create
Use
Close
Owner
Shutdown behavior
```

Checklist:

> Kalau object ini dibuat, siapa yang memastikan object ini mati?

---

# 24. Goroutine leak prevention

Setiap:

```go
go func() {}
```

harus bisa menjawab:

```text
How does it stop?
```

Ideal:

```go
go worker(ctx)
```

bukan:

```go
go worker()
```

Kemudian audit:

```text
ticker.Stop()
timer.Stop()
channel close?
context cancellation?
WaitGroup?
```

---

# 25. Background worker supervision

Jangan hanya:

```go
go worker(ctx)
```

Kalau panic:

```text
worker mati
system tidak tahu
```

Untuk worker kritis:

```text
Supervisor
 ├─ start
 ├─ monitor
 ├─ recover
 ├─ restart policy
 └─ shutdown
```

Tetapi **jangan auto-restart semua worker**; beberapa panic harus fail-fast.

---

# 26. Panic policy

Tentukan boundary recovery.

Bukan:

```go
defer recover()
```

di mana-mana.

Ideal:

```text
Plugin callback boundary
        ↓
recover panic
        ↓
log + metrics
        ↓
operation failed
```

Tetapi panic pada invariant core:

```text
DB corruption
invalid internal state
```

mungkin harus menghentikan process.

---

# 27. Error taxonomy

Ini perlu menjadi standar global.

Misalnya:

```go
type ErrorKind int

const (
    ErrValidation ErrorKind = iota
    ErrNotFound
    ErrUnauthorized
    ErrConflict
    ErrUnavailable
    ErrTimeout
    ErrCanceled
    ErrInternal
)
```

Lalu:

```text
domain error
     ↓
classification
     ├── retry?
     ├── user message?
     ├── alert?
     ├── metrics?
     └── log level?
```

---

# 28. Retry policy

Jangan:

```go
for i := 0; i < 3; i++ {
    ...
}
```

di banyak tempat.

Centralize:

```text
RetryPolicy
 ├── max attempts
 ├── backoff
 ├── jitter
 ├── retryable errors
 └── deadline
```

Dan **jangan retry non-idempotent operation sembarangan**.

---

# 29. Backoff + jitter

Kalau banyak goroutine melakukan retry bersamaan:

```text
1s
2s
4s
8s
```

bisa menjadi thundering herd.

Gunakan jitter:

```text
base backoff
+
random jitter
```

---

# 30. Rate limiting hierarchy

Jangan hanya satu rate limiter.

Bisa membutuhkan:

```text
Global
User
Chat
Command
Callback
Telegram API method
```

Misalnya:

```text
user A
 ├─ command rate
 └─ callback rate

global Telegram API rate
```

Harus jelas mana yang authoritative.

---

# 31. Queue ownership

Untuk asynchronous operation:

```text
Who queues?
Who consumes?
Who retries?
Who persists?
Who acknowledges?
```

Kalau tidak jelas, akan muncul duplicate worker/queue logic.

---

# 32. Event semantics

EventBus perlu convention.

Event harus menjelaskan:

```go
type Event struct {
    ID
    Type
    Timestamp
    Source
    CorrelationID
    ActorID
    Payload
}
```

Dan tentukan:

```text
Event = fact
Command = request
```

Jangan event bernama:

```text
"UpdateSetting"
```

yang sebenarnya command.

Lebih baik:

```text
SettingChanged
```

---

# 33. Event ordering

Kalau:

```text
SettingChanged
PluginEnabled
ChatUpdated
```

apakah order dijamin?

Harus ada policy:

```text
Per aggregate ordered
Global unordered
```

Kalau tidak, race subtle akan muncul.

---

# 34. Event delivery guarantee

Tentukan:

```text
Best effort
At-most-once
At-least-once
Durable
```

EventBus memory biasa:

```text
process crash
→ event lost
```

Untuk event penting, gunakan outbox.

---

# 35. Observability contract

Setiap operation penting harus punya:

```text
request ID
correlation ID
actor ID
chat ID
operation
duration
result
```

Misalnya:

```text
correlation=abc123
operation=settings.update
actor=123
chat=-100
key=antiflood.enabled
result=success
duration=18ms
```

Ini akan sangat membantu debugging.

---

# 36. Logging policy

Tentukan level:

```text
DEBUG
INFO
WARN
ERROR
```

Dan jangan:

```go
log.Printf(...)
fmt.Println(...)
```

secara random.

Semua logging melalui logger abstraction.

---

# 37. Secret handling

Ini belum kita bahas.

Audit:

```text
API ID
API hash
session
bot token
proxy credential
DB credential
cookies
authorization data
```

Rule:

```text
Never log secrets
Never put secrets in callback state
Never include secrets in errors
Never serialize secrets accidentally
```

---

# 38. Sensitive data lifecycle

Tidak cukup hanya "jangan log".

Tentukan:

```text
Where stored?
Encryption?
TTL?
Who can access?
Can export include it?
Can debug dump include it?
```

---

# 39. Configuration vs Settings

Ini harus benar-benar dipisahkan.

### Configuration

Deployment/runtime:

```text
DB path
Telegram API credentials
listen address
log level
```

### Settings

User/application behavior:

```text
prefix
antiflood
language
scheduler policy
```

Config biasanya:

```text
startup-time
```

Settings:

```text
runtime mutable
```

Jangan campur keduanya.

---

# 40. Environment/config precedence

Harus ada rule:

```text
CLI
↓
ENV
↓
config file
↓
defaults
```

atau yang dipilih project.

Jangan masing-masing subsystem punya precedence sendiri.

---

# 41. Feature flags

Ini juga berbeda dari settings.

```text
Feature flag:
    apakah feature tersedia?

Setting:
    bagaimana feature bekerja?
```

Contoh:

```text
FEATURE_NEW_SETTINGS_UI=true
```

bukan:

```text
settings.new_ui=true
```

kalau sebenarnya itu deployment feature flag.

---

# 42. Plugin isolation

Plugin jangan bisa sembarang:

```text
os.Exit()
panic()
DB schema migration
global state mutation
Telegram client replacement
```

Buat capability boundary:

```text
PluginContext
 ├── Commands
 ├── Settings
 ├── UI
 ├── Telegram
 ├── Storage
 ├── Logger
 └── Events
```

---

# 43. Plugin dependency graph

Kalau plugin A membutuhkan plugin B:

```text
A → B
```

harus ada:

```text
dependency declaration
version compatibility
startup ordering
failure behavior
```

Jangan mengandalkan registration order.

---

# 44. Registration determinism

Urutan plugin registration harus deterministic.

Jangan:

```text
range map
```

untuk registration yang bergantung order.

Gunakan:

```text
explicit priority
dependency graph
stable sorting
```

---

# 45. Initialization phase separation

Saya sangat merekomendasikan:

```text
Phase 1 — Construct
Phase 2 — Register
Phase 3 — Validate
Phase 4 — Wire
Phase 5 — Start
Phase 6 — Serve
Phase 7 — Stop
```

Bukan constructor melakukan:

```text
create
register
start goroutine
connect Telegram
load plugin
```

sekaligus.

---

# 46. Startup failure semantics

Kalau:

```text
plugin A gagal
```

apakah:

```text
whole app fails
```

atau:

```text
plugin disabled
```

Harus explicit.

Contoh:

```go
type FailurePolicy int

const (
    FailStartup FailurePolicy = iota
    DisableComponent
    Degrade
)
```

---

# 47. Shutdown semantics

Harus ada:

```text
Stop accepting work
        ↓
cancel context
        ↓
drain queues
        ↓
stop workers
        ↓
flush events
        ↓
close Telegram
        ↓
close DB
```

Dan **DB jangan ditutup sebelum worker berhenti**.

Ini sangat penting.

---

# 48. Drain vs abort

Tidak semua component harus drain.

Contoh:

```text
Callback UI      → abort
Scheduler        → drain/lease
Broadcast        → durable resume
Metrics          → flush
Cache cleanup    → abort
```

Jadi shutdown harus per-component policy.

---

# 49. Atomicity UI

Button action harus diperlakukan seperti API endpoint.

Misalnya:

```text
button:
Enable AntiFlood
```

jangan:

```text
update UI
→ DB update
```

tetapi:

```text
DB update
→ runtime update
→ UI update
```

Kalau gagal:

```text
UI tetap menunjukkan old state
```

---

# 50. Optimistic UI policy

Tentukan kapan UI boleh optimistic.

Untuk:

```text
simple toggle
```

bisa optimistic.

Untuk:

```text
ban
delete
scheduler
settings import
```

lebih baik:

```text
pending
→ success
```

---

# 51. UI state vs domain state

Ini harus menjadi invariant:

```text
UI state:
    page
    selected tab
    modal open
    temporary input

Domain:
    enabled
    filter
    schedule
    permission
```

UI state **tidak boleh menjadi source of truth domain**.

---

# 52. Callback lifecycle

Callback harus memiliki:

```text
created
active
expired
consumed
invalidated
```

State machine eksplisit:

```text
ACTIVE
  │
  ├── consume → CONSUMED
  ├── TTL     → EXPIRED
  └── revoke  → INVALIDATED
```

Lebih baik daripada boolean:

```go
Consumed bool
```

saja.

---

# 53. Session lifecycle

Wizard juga:

```text
Created
Active
WaitingInput
Validating
Completed
Canceled
Expired
Failed
```

Ini jauh lebih robust daripada:

```go
CurrentStep int
```

---

# 54. Schema migration discipline

Setiap migration harus:

```text
forward
atomic
versioned
checksum
```

Dan idealnya:

```text
migration compatibility
rollback strategy
backup consideration
```

Serta jangan schema migration tersebar di plugin business code tanpa policy.

---

# 55. DB constraints sebagai final guard

Jangan hanya:

```go
if exists(...)
```

kemudian insert.

DB juga harus punya:

```sql
UNIQUE
FOREIGN KEY
CHECK
NOT NULL
```

Karena concurrency bisa membuat application-level check gagal.

---

# 56. Time handling

Ini sering menjadi sumber bug.

Tentukan:

```text
UTC internal
localization only at UI
```

Untuk scheduler:

```text
timestamp = UTC
timezone = explicit
```

Jangan menyimpan waktu lokal tanpa timezone context.

---

# 57. ID semantics

Telegram memiliki:

```text
User ID
Chat ID
Channel ID
Message ID
Access hash
Peer
InputPeer
```

Masing-masing jangan direpresentasikan sembarang sebagai:

```go
int64
```

di seluruh domain.

Minimal gunakan typed aliases/value objects pada boundary penting.

---

# 58. Nil semantics

GoUltroid banyak memakai pointer/optional values.

Harus ada convention:

```text
nil = unknown?
nil = absent?
nil = not loaded?
nil = default?
```

Jangan satu `nil` punya empat arti.

---

# 59. Collection ownership

Slice/map yang dikembalikan API jangan sembarangan dibocorkan.

Buruk:

```go
func (r *Registry) Items() map[string]Item {
    return r.items
}
```

Caller bisa mutate internal state.

Lebih aman:

```go
func (r *Registry) Items() []Item {
    copy(...)
}
```

atau immutable view.

---

# 60. API surface minimization

Setiap exported:

```go
type
func
const
var
```

harus ditanya:

> Apakah ini benar-benar public API?

Kalau tidak:

```go
lowercase
```

Semakin kecil API surface, semakin kecil coupling.

---

# 61. Constructor complexity

Kalau constructor menerima:

```go
NewX(
    a,
    b,
    c,
    d,
    e,
    f,
    g,
    h,
    i,
)
```

gunakan:

```go
type Dependencies struct {...}
```

tetapi jangan menjadikan `Dependencies` sebagai dump semua dependency.

Harus tetap grouped berdasarkan responsibility.

---

# 62. Options pattern jangan berlebihan

Go codebase sering jatuh ke:

```go
WithFoo()
WithBar()
WithBaz()
WithQux()
```

padahal semuanya required.

Kalau required:

```go
Dependencies
```

Kalau optional benar-benar optional:

```go
Option
```

---

# 63. Avoid "util" package

Saya sangat menyarankan:

```text
internal/util
internal/helpers
internal/common
```

dihindari.

Karena biasanya berubah menjadi:

```text
miscellaneous dumping ground
```

Lebih baik function tinggal dekat domain pemakainya.

---

# 64. Avoid generic `common` types

Jangan:

```go
common.Result
common.Context
common.State
common.Config
```

karena akhirnya semua package bergantung ke `common`.

Buat:

```text
settings.Result
callback.State
scheduler.Job
interaction.Session
```

---

# 65. Avoid premature generic abstraction

Go generics jangan digunakan hanya untuk terlihat generic.

Buat generic kalau memang ada:

```text
same algorithm
same invariants
different types
```

Bukan:

```go
GenericManager[T]
```

hanya untuk mengurangi 10 lines.

---

# 66. Duplication policy

Tidak semua duplicate code harus dihapus.

Bedakan:

```text
Accidental duplication → hapus

Structural similarity → boleh

Independent business rules → jangan dipaksa share

Stable shared algorithm → extract
```

Ini sangat penting.

**DRY bukan berarti semua kode yang mirip harus menjadi satu function.**

---

# 67. Abstraction extraction threshold

Rule praktis:

```text
1 occurrence → inline
2 occurrences → evaluate
3+ occurrences → consider abstraction
```

Tetapi lihat semantic similarity, bukan sekadar bentuk syntax.

---

# 68. Temporal coupling

Ini masalah besar yang belum kita bahas.

Kalau API harus dipanggil:

```text
A()
B()
C()
```

dengan urutan tertentu agar valid, berarti ada temporal coupling.

Lebih bagus:

```go
Initialize(...)
```

atau:

```go
Start(...)
```

yang menjamin invariant internal.

---

# 69. Hidden initialization

Hindari:

```go
func (x *X) Foo() {
    if x.cache == nil {
        x.cache = ...
    }
}
```

karena object lifecycle menjadi tidak jelas dan concurrency rawan.

Lebih baik initialize saat construction.

---

# 70. Zero-value usability

Untuk setiap struct:

> apakah zero value valid?

Kalau ya, manfaatkan Go idiom.

Kalau tidak, jangan pura-pura zero value valid.

Misalnya:

```go
var Router Router
router.Dispatch(...)
```

harus jelas apakah valid atau panic.

---

# 71. Invariant enforcement

Setiap subsystem harus punya invariants.

Contoh Settings:

```text
key != ""
namespace != ""
scope valid
value conforms definition
```

Callback:

```text
state exists
not expired
user matches
chat matches
namespace matches
```

Scheduler:

```text
lease owner valid
fencing token monotonic
status transition valid
```

Invariant harus dicek di boundary yang tepat.

---

# 72. State transition legality

Jangan membiarkan:

```text
Completed → Running
Expired → Active
Canceled → Running
```

kalau tidak valid.

Gunakan explicit transition:

```go
func (j *Job) Transition(to Status) error
```

daripada:

```go
j.Status = StatusRunning
```

di mana-mana.

---

# 73. Domain methods vs field mutation

Ini bagian kecil tetapi sangat penting.

Buruk:

```go
job.Status = Running
job.Lease = ...
job.Attempts++
```

tersebar.

Lebih baik:

```go
job.Claim(...)
job.MarkRunning(...)
job.MarkFailed(...)
job.Complete(...)
```

Sehingga invariant hanya ada di satu tempat.

---

# 74. Magic strings / magic numbers

Audit seluruh:

```text
"settings"
"toggle"
"noop"
"global"
"private"
"admin"
3
5
10
30
60
```

Centralize jika memiliki semantic meaning.

Tapi jangan membuat constant untuk angka yang cuma digunakan sekali dan jelas.

---

# 75. Stringly typed architecture

Ini salah satu concern utama GoUltroid saat ini.

Cari:

```go
map[string]any
map[string]string
string action
string scope
string status
string type
```

yang sebenarnya enum/domain object.

Target:

```go
type Action uint8
type Status uint8
type Scope uint8
type SettingType uint8
```

Serialize hanya di boundary.

---

# 76. `any` usage

`any` sangat berguna untuk framework, tetapi harus dibatasi.

Kalau:

```go
map[string]any
```

mulai sering melakukan:

```go
v.(string)
v.(int)
v.(bool)
```

berarti type system sedang dilewati.

---

# 77. Reflection usage

Kalau ada reflection untuk:

```text
settings
plugin registration
UI generation
```

pastikan reflection hanya berada di infrastructure/framework layer.

Domain jangan bergantung reflection.

---

# 78. Compile-time safety

Semakin banyak hal yang bisa dipindahkan dari runtime ke compile time, semakin bagus.

Misalnya:

```text
typed settings
typed callback actions
typed scopes
typed plugin capabilities
typed event types
```

daripada string.

---

# 79. API compatibility

Karena GoUltroid berkembang cepat, tentukan:

```text
internal API
plugin API
external API
```

Kalau plugin API dianggap stable, jangan sering memecahkan interface.

---

# 80. Documentation as architecture constraint

Setiap subsystem besar sebaiknya punya:

```text
README / package doc
Architecture
Invariants
Lifecycle
Concurrency
Error semantics
```

Bukan dokumentasi panjang untuk setiap function.

---

# Yang paling penting: buat **Atomic Architecture Checklist**

Kalau saya audit GoUltroid berikutnya, saya tidak akan lagi memakai kategori besar seperti "Settings bagus/tidak".

Saya akan pecah menjadi:

```text
ARCHITECTURE
├── dependency ownership
├── dependency direction
├── package boundary
├── API surface
├── lifecycle
├── initialization
├── shutdown
├── state ownership
├── source of truth
├── mutability
├── cache
├── concurrency
├── locking
├── context
├── cancellation
├── timeout
├── retry
├── idempotency
├── transaction
├── consistency
├── event semantics
├── queue semantics
├── plugin isolation
└── failure policy

CODE STRUCTURE
├── package naming
├── file responsibility
├── type responsibility
├── function size
├── constructor complexity
├── exported API
├── interface size
├── dependency count
├── cyclic dependency risk
├── util/common dumping ground
├── abstraction duplication
├── temporal coupling
└── hidden initialization

CODE QUALITY
├── naming
├── magic strings
├── magic numbers
├── stringly typing
├── any usage
├── nil semantics
├── error wrapping
├── error taxonomy
├── ignored errors
├── panic policy
├── comments
├── documentation
├── unreachable code
├── dead code
└── duplicated logic

DATA
├── schema ownership
├── transaction boundaries
├── constraints
├── migration
├── serialization
├── IDs
├── timestamps
├── normalization
├── caching
└── consistency

TELEGRAM
├── peer identity
├── InputPeer
├── access_hash
├── update lifecycle
├── callback
├── command
├── inline
├── permissions
├── rate limits
├── retries
└── API errors

INTERACTION/UI
├── screen
├── component
├── navigation
├── session
├── wizard
├── form
├── selector
├── callback state
├── UI/domain separation
├── optimistic update
└── error/loading/progress

PLUGIN
├── registration
├── dependency graph
├── lifecycle
├── capability
├── isolation
├── settings
├── commands
├── callbacks
└── failure policy

OBSERVABILITY
├── logging
├── metrics
├── tracing
├── correlation ID
├── audit
└── sensitive-data redaction

TESTING
├── unit
├── integration
├── race
├── lifecycle
├── failure injection
├── property
├── migration
├── architecture
└── concurrency
```

**Ini yang menurut saya belum kita bahas secara atomic sebelumnya.**

Dan ada satu prinsip yang menurut saya harus menjadi **aturan utama GoUltroid ke depannya**:

> **Setiap state, behavior, invariant, dan responsibility harus mempunyai satu owner yang jelas.**

Kalau kita menemukan:

```text
dua tempat menyimpan state yang sama
dua tempat melakukan validation yang sama
dua tempat melakukan parsing yang sama
dua tempat menentukan permission
dua tempat menentukan retry
dua tempat melakukan rendering
dua tempat menentukan lifecycle
```

itu harus dianggap sebagai **architecture smell**, bukan sekadar duplicate code.

Dengan pendekatan ini, audit berikutnya bisa dibuat jauh lebih tajam: **bukan hanya "fitur apa yang kurang", tetapi setiap package/type/function bisa diperiksa berdasarkan ownership, invariant, dependency, lifecycle, concurrency, error semantics, dan duplication secara atomic.**
