Native Go Telegram Userbot Framework

Revised Architecture & Technical Blueprint

Status: Architecture / Technical Design
Target: Native Go Telegram Userbot Framework
Protocol: Telegram MTProto
Approach: Architecture-first, runtime-first, plugin-last
Primary Goal: Robust, controllable, extensible, observable, and resource-safe runtime

---

1. Executive Summary

Framework ini dirancang sebagai native Go Telegram Userbot Runtime yang menggunakan Telegram MTProto sebagai transport dan protocol boundary.

Framework bukan sekadar kumpulan command Telegram.

Framework harus menyediakan runtime yang mampu mengelola:

- application lifecycle
- Telegram connection
- MTProto updates
- event bus
- command system
- queue
- worker
- task
- scheduler
- job
- storage
- services
- permissions
- entity / identity
- plugin runtime
- feature lifecycle
- capability system
- resource ownership
- cleanup
- filesystem
- process execution
- network / HTTP
- secret management
- middleware
- observability
- diagnostics
- health
- failure isolation
- rate limiting
- flood-wait handling
- idempotency
- deduplication
- state management

Tujuan utamanya adalah membuat framework yang:

- stabil
- predictable
- mudah dikembangkan
- mudah diuji
- mudah di-debug
- resource-safe
- tidak mudah mengalami goroutine leak
- tidak mengalami uncontrolled concurrency
- tidak memiliki plugin yang dapat merusak runtime global
- mampu melakukan graceful shutdown
- mampu melakukan plugin enable/disable secara aman
- mampu menangani workload ringan dan berat secara berbeda
- mampu mengontrol akses network dan external process
- mampu mendeteksi unhealthy subsystem
- mampu mencegah duplicate execution
- mampu mengisolasi resource setiap plugin

Arsitektur utama:

UI / CLI
   │
   ▼
Application
   │
   ▼
Plugin Runtime
   │
   ▼
Features
   │
   ▼
Services
   │
   ▼
Runtime Infrastructure
   │
   ├── EventBus
   ├── Queue
   ├── Worker
   ├── Task Manager
   ├── Scheduler
   ├── Job Manager
   ├── Resource Manager
   ├── Middleware
   ├── Rate Limiter
   ├── Idempotency
   ├── Health / Diagnostics
   └── Configuration
   │
   ▼
Telegram Boundary
   │
   ├── MTProto
   ├── Entity / Identity
   ├── Flood-Wait
   ├── Telegram Rate Limit
   └── File Transfer
   │
   ▼
External Systems
   ├── Telegram
   ├── Internet / HTTP
   ├── Filesystem
   └── External Processes

Feature seperti:

AFK
PM Permit
Admin
Notes
Filters
Welcome
AntiSpam
Auto Reply
Downloader
Reminder

bukan bagian dari core runtime.

Feature tersebut berjalan melalui plugin.

---

2. Design Philosophy

2.1 Runtime First

Framework tidak dibangun dengan urutan:

buat command
buat AFK
buat PM Permit
buat Admin
baru memikirkan runtime

Tetapi:

Architecture
    ↓
Runtime
    ↓
Lifecycle
    ↓
Context
    ↓
Resource Ownership
    ↓
Event
    ↓
Queue
    ↓
Worker
    ↓
Task
    ↓
Scheduler
    ↓
Job
    ↓
Storage
    ↓
Telegram Boundary
    ↓
Services
    ↓
Plugin Runtime
    ↓
Features

Feature baru dibuat setelah runtime cukup stabil.

---

2.2 Infrastructure First

Infrastructure harus memiliki lifecycle yang jelas.

Contoh:

Database
Scheduler
Worker Manager
Task Manager
EventBus
Queue
Telegram Client
Network Service
Filesystem Manager
Process Manager
Resource Manager
Diagnostics

tidak boleh dibuat secara ad-hoc oleh feature.

---

2.3 Plugins Consume Capabilities

Plugin tidak memiliki runtime.

Plugin menggunakan runtime.

Runtime
 ├── EventBus
 ├── Storage
 ├── Scheduler
 ├── Workers
 ├── Tasks
 ├── Telegram
 ├── Network
 ├── Filesystem
 ├── Process
 ├── Services
 ├── Resource Manager
 └── Diagnostics

Plugin
 └── consume capabilities

Prinsip:

«Plugins consume capabilities; they do not own the runtime.»

---

2.4 Every Long-Lived Resource Has an Owner

Setiap resource yang hidup lebih lama dari satu function invocation harus memiliki:

Owner
Resource ID
Creation Time
Context
Lifecycle State
Cancellation Path
Cleanup Path
Diagnostics

Contoh:

Event Subscription → plugin:afk
Scheduler Job      → plugin:reminder
Worker Task        → plugin:downloader
Temp File          → plugin:media
HTTP Connection    → plugin:api-client
WebSocket          → plugin:watcher
Managed Goroutine  → plugin:watcher
Child Process      → plugin:media

Tidak boleh ada resource anonim yang tidak diketahui siapa pemiliknya.

---

3. Layer Architecture

Framework dibagi menjadi beberapa boundary.

┌─────────────────────────────────────┐
│ UI / CLI                            │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ Application Layer                   │
│ Use Cases / Policies                │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ Plugin Runtime                      │
│ Lifecycle / Scope / Capability      │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ Feature / Plugin                    │
│ AFK / Admin / PM Permit / etc.      │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ Services                            │
│ Message / Permission / Media / etc. │
│ Network / Entity / Cache / etc.     │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ Runtime Infrastructure              │
│ Event / Queue / Worker / Task       │
│ Scheduler / Job / Storage           │
│ Resource / Middleware / RateLimit   │
│ Health / Diagnostics / Config        │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│ External Boundaries                 │
│ Telegram / Internet / Filesystem    │
│ External Processes                  │
└─────────────────────────────────────┘

---

4. Core Architectural Rule

Gunakan aturan berikut:

Runtime
    = bagaimana sistem berjalan

Service
    = capability yang dapat digunakan banyak feature

Application
    = use case dan policy

Feature
    = business behavior

Plugin
    = packaging + lifecycle boundary untuk feature

Boundary
    = komunikasi dengan external system

Contoh:

AFK
 ├── Feature
 └── Plugin

Storage
 └── Runtime Capability

MessageService
 └── Reusable Service

Telegram Client
 └── Infrastructure

NetworkService
 └── Infrastructure / Service Boundary

Filesystem
 └── Infrastructure

ProcessManager
 └── Infrastructure

MTProto
 └── External Protocol

---

5. Dependency Direction

Dependency harus mengarah ke dalam.

UI
 ↓
Application
 ↓
Plugin Runtime
 ↓
Feature
 ↓
Service
 ↓
Infrastructure
 ↓
External Boundary

Feature tidak boleh langsung bergantung pada implementation detail.

Contoh yang tidak diinginkan:

plugin
    ↓
*sql.DB

atau:

plugin
    ↓
*telegram.Client

atau:

plugin
    ↓
go cron.New()

atau:

plugin
    ↓
http.Client{}

atau:

plugin
    ↓
exec.Command(...)

Yang diinginkan:

plugin
    ↓
ctx.Storage()
ctx.Scheduler()
ctx.Workers()
ctx.Tasks()
ctx.Telegram()
ctx.HTTP()
ctx.Files()
ctx.Process()

---

6. Repository Structure

Struktur awal:

cmd/
    userbot/
        main.go

internal/
    application/

    runtime/
    lifecycle/
    events/
    queue/
    workers/
    tasks/
    scheduler/
    jobs/
    registry/

    middleware/
    ratelimit/
    idempotency/
    locks/

    services/
        message/
        permission/
        moderation/
        entity/
        media/
        downloader/
        cache/
        network/
        notification/

    plugins/

    telegram/

    storage/

    filesystem/
    process/
    secrets/

    config/
    logging/
    diagnostics/
    health/
    metrics/
    ui/

pkg/
    telegram/

plugins/

    afk/
    pmpermit/
    admin/
    notes/
    filters/
    welcome/
    antispam/
    autoreply/
    downloader/
    reminder/

migrations/

configs/

docs/
    architecture/
    adr/

tests/

Feature berada di:

plugins/

sedangkan reusable capability berada di:

internal/services/

Runtime primitives berada di:

internal/runtime/
internal/events/
internal/queue/
internal/workers/
internal/tasks/
internal/scheduler/
internal/jobs/
internal/middleware/
internal/ratelimit/
internal/idempotency/

Infrastructure berada di:

internal/storage/
internal/telegram/
internal/filesystem/
internal/process/
internal/secrets/

---

7. "cmd/"

"cmd/" hanya menjadi entry point.

Contoh:

cmd/userbot/main.go

Tanggung jawab:

load config
    ↓
create application
    ↓
start runtime
    ↓
wait signal
    ↓
shutdown

"main.go" tidak boleh berisi:

Telegram handler
Database logic
AFK logic
Scheduler logic
Worker logic
Plugin logic
HTTP logic
Process management

---

8. Runtime

Runtime adalah pusat lifecycle sistem.

Runtime memiliki:

Context
Config
Logger
Diagnostics
Health

Telegram
EventBus
Queue
Worker Manager
Task Manager
Scheduler
Job Manager
Storage

Resource Manager
Middleware
Rate Limiter
Idempotency Manager

Service Registry
Plugin Runtime

Runtime bertanggung jawab atas:

- startup
- shutdown
- lifecycle
- dependency initialization
- resource ownership
- cancellation
- health
- failure handling
- observability

Runtime bukan tempat business logic feature.

---

9. Runtime State Machine

Runtime:

Created
   ↓
Initializing
   ↓
Starting
   ↓
Running
   ↓
Stopping
   ↓
Stopped

Failure:

Initializing ──→ Failed
Starting     ──→ Failed
Running      ──→ Failed

State harus observable.

Contoh:

type RuntimeState string

const (
    StateCreated      RuntimeState = "created"
    StateInitializing RuntimeState = "initializing"
    StateStarting     RuntimeState = "starting"
    StateRunning      RuntimeState = "running"
    StateStopping     RuntimeState = "stopping"
    StateStopped      RuntimeState = "stopped"
    StateFailed       RuntimeState = "failed"
)

Runtime state menjadi salah satu input utama Health/Diagnostics.

---

10. Startup Order

Startup harus deterministic.

Config
  ↓
Logger
  ↓
Diagnostics
  ↓
Health
  ↓
Storage
  ↓
Migrations
  ↓
Secrets
  ↓
Filesystem
  ↓
Process Manager
  ↓
Telegram
  ↓
Entity Manager
  ↓
Rate Limiter
  ↓
Idempotency
  ↓
EventBus
  ↓
Queue
  ↓
Worker Manager
  ↓
Task Manager
  ↓
Scheduler
  ↓
Job Manager
  ↓
Services
  ↓
Middleware
  ↓
Plugin Runtime
  ↓
Discover Plugins
  ↓
Validate Plugins
  ↓
Resolve Dependencies
  ↓
Initialize Plugins
  ↓
Telegram Update Receiver
  ↓
Workers
  ↓
Scheduler
  ↓
Plugins
  ↓
Running

Plugin tidak boleh start sebelum capability yang dibutuhkannya tersedia.

---

11. Shutdown Order

Shutdown harus membatasi intake terlebih dahulu.

Stop accepting new work
        ↓
Stop plugin intake
        ↓
Stop Scheduler
        ↓
Stop Job Triggering
        ↓
Cancel / Drain Queue
        ↓
Stop new Tasks
        ↓
Stop Workers
        ↓
Stop Plugins
        ↓
Stop Network Connections
        ↓
Stop Telegram Update Receiver
        ↓
Stop Telegram
        ↓
Stop External Processes
        ↓
Cleanup Temporary Files
        ↓
Flush logs / metrics / state
        ↓
Close Storage
        ↓
Final Health / Diagnostics Snapshot
        ↓
Stopped

Tujuan:

«Jangan shutdown infrastructure sebelum consumer berhenti menggunakannya.»

---

12. Context Is Mandatory

Semua operation yang berpotensi hidup lama harus menerima:

context.Context

Contoh:

func (s *Service) Run(ctx context.Context) error

Task:

func(ctx context.Context) error

HTTP:

func(ctx context.Context, req Request) (...)

Telegram:

func(ctx context.Context, ...) (...)

Process:

func(ctx context.Context, command Command) (...)

Plugin:

ctx.Go(func(ctx context.Context) {
    ...
})

Hindari:

go func() {
    for {
        ...
    }
}()

tanpa lifecycle owner.

---

13. Resource Ownership

Runtime memiliki global resources.

Runtime owns:

Database
Telegram connection
EventBus
Global Scheduler
Worker Manager
Queue
Logger
Network Manager
Filesystem Manager
Process Manager
Rate Limiter
Diagnostics

Plugin memiliki scoped resources.

Plugin owns:

Commands
Event subscriptions
Jobs
Tasks
Temporary files
Plugin state
Plugin configuration
HTTP sessions
WebSockets
Plugin processes

Plugin tidak memiliki:

Global DB connection
Global Telegram client
Global scheduler
Global worker manager
Global lifecycle
Global network pool
Global process manager

---

13.1 Resource Manager

Resource Manager adalah subsystem yang memastikan semua long-lived resource memiliki ownership yang jelas.

Setiap resource minimal memiliki:

type Resource struct {
    ID        string
    Owner     string
    Type      string
    CreatedAt time.Time
    State     ResourceState
}

Contoh owner:

plugin:afk
plugin:reminder
plugin:downloader
runtime:telegram
runtime:scheduler
runtime:network

Resource yang dapat dilacak:

Event Subscription
Scheduler Job
Task
Worker
Goroutine
HTTP Session
WebSocket
Temporary File
External Process
Cache Entry
Lock

Resource Manager harus mampu menjawab:

resource apa yang aktif?
siapa pemiliknya?
berapa resource plugin tertentu?
resource mana yang belum cleanup?
resource mana yang stuck?

Contoh:

Plugin: downloader

subscriptions: 3
jobs:          2
tasks:         4
goroutines:    1
temp_files:    6
processes:     1
websockets:    0

Resource Manager juga menjadi dasar:

- quota
- leak detection
- diagnostics
- plugin isolation
- graceful shutdown

---

14. Plugin Runtime

Plugin Runtime adalah subsystem yang mengelola plugin.

Komponen:

Plugin Manager
Plugin Manifest
Plugin Loader
Plugin Validator
Dependency Resolver
Capability Manager
Plugin Context
Plugin Scope
Resource Manager
Plugin Configuration
Plugin Lifecycle
Failure Isolation
Plugin Diagnostics
Plugin API Version
Plugin Migration
Conflict Resolver

Plugin Runtime menjawab:

Plugin ini valid?
Dependency tersedia?
Capability boleh?
API version kompatibel?
Bagaimana start?
Apa resource miliknya?
Bagaimana stop?
Apakah ada leak?
Apakah plugin conflict dengan plugin lain?

---

15. Plugin Lifecycle

Lifecycle:

Discovered
    ↓
Loaded
    ↓
Validated
    ↓
Initialized
    ↓
Enabled
    ↓
Running
    ↓
Stopping
    ↓
Disabled
    ↓
Unloaded

Failure:

Loaded ─────→ Failed
Validated ──→ Failed
Initialized → Failed
Running ────→ Failed

Disable plugin harus menghasilkan:

no new plugin work
no active subscription
no active scheduler job
no unmanaged goroutine
no transient task
no active plugin-owned process
no orphan temporary file

Persistent data tetap boleh ada.

---

16. Plugin Manifest

Contoh:

type Manifest struct {
    ID           string
    Name         string
    Version      string
    Description  string

    Dependencies []Dependency
    Capabilities []Capability

    ConfigSchema Schema
}

Manifest menjadi deklarasi formal plugin.

Plugin tidak boleh diam-diam menggunakan capability yang tidak dideklarasikan.

Manifest juga dapat mendeklarasikan:

API Version
Required Services
Optional Services
Conflicts
Network Quota
Worker Quota
Storage Namespace
Migration Version

---

17. Plugin Capability

Capability adalah hak plugin untuk menggunakan runtime resource.

Contoh:

telegram.read
telegram.send_message
telegram.edit_message
telegram.delete_message

storage.read
storage.write

scheduler
worker
events
tasks

http
websocket

cache

filesystem.temp
filesystem.data

media.download
media.process

process.execute

secret.read

telegram.raw

Capability harus eksplisit.

Contoh:

AFK:
    telegram.read
    telegram.send_message
    storage.read
    storage.write
    events

Reminder:
    storage.read
    storage.write
    scheduler
    telegram.send_message

Downloader:
    http
    media.download
    filesystem.temp
    process.execute

"telegram.raw", "process.execute", dan akses secret sebaiknya menjadi capability privileged.

---

18. Capability vs User Permission

Keduanya berbeda.

Plugin Capability

Menentukan:

«Apa yang plugin boleh gunakan dari runtime?»

User Permission

Menentukan:

«Siapa yang boleh melakukan action?»

Contoh:

Plugin Admin

capability:
    telegram.send_message
    telegram.delete_message
    storage
    events

User:

Owner
Sudo
Admin
Authorized
Everyone

Model authorization:

Actor
  ↓
Permission
  ↓
Policy
  ↓
Decision
  ↓
Action

Jangan hard-code:

if userID == 123456789 {
    ...
}

Gunakan:

permission.Check(
    ctx,
    actor,
    permission.Owner,
)

---

19. Plugin Context

Plugin tidak menerima seluruh Runtime.

Plugin menerima scoped context.

Contoh API:

type Context interface {
    Context() context.Context

    Manifest() Manifest
    Logger() Logger
    Config() Config

    Events() EventBus
    Storage() Storage
    Scheduler() Scheduler
    Workers() WorkerManager
    Tasks() TaskManager
    Telegram() Telegram
    Services() ServiceRegistry

    HTTP() HTTPClient
    Files() Filesystem
    Process() ProcessManager
    Secrets() SecretReader

    Go(func(context.Context))
    Defer(func())
}

Dengan demikian:

Plugin
   ↓
Plugin Context
   ↓
Scoped Capabilities

bukan:

Plugin
   ↓
Global Runtime

---

20. Plugin Scope

Plugin Scope adalah boundary resource plugin.

Scope menyimpan:

Context
Cancellation
Event subscriptions
Scheduler jobs
Worker tasks
Managed goroutines
Cleanup callbacks
Temporary files
HTTP sessions
WebSockets
External processes
Locks
Metrics metadata

Ketika plugin dihentikan:

scope.Cancel()
     ↓
cancel tasks
     ↓
cancel jobs
     ↓
unsubscribe events
     ↓
close HTTP/WebSocket resources
     ↓
stop processes
     ↓
stop goroutines
     ↓
cleanup files
     ↓
release locks
     ↓
wait
     ↓
verify

---

21. Structured Concurrency

Semua background work harus memiliki parent.

Contoh:

ctx.Go(func(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        default:
        }
    }
})

Konsep:

Runtime Context
    │
    ├── Plugin Context
    │      │
    │      ├── Event Handler
    │      ├── Scheduler Task
    │      ├── Worker Task
    │      ├── HTTP Connection
    │      ├── Child Process
    │      └── Background Goroutine
    │
    └── Infrastructure

Jika plugin berhenti:

Plugin Context canceled
        ↓
semua child work berhenti

---

22. Cleanup Registry

Plugin dapat mendaftarkan cleanup:

ctx.Defer(func() {
    // cleanup
})

Cleanup harus:

- idempotent
- bounded
- cancellation-aware
- tidak bergantung pada resource yang sudah ditutup
- tercatat di Resource Manager

Cleanup harus memiliki timeout agar plugin tidak dapat menggantung shutdown selamanya.

---

23. EventBus

EventBus menjadi communication layer.

Flow:

Telegram
   ↓
Update Receiver
   ↓
Normalizer
   ↓
Internal Event
   ↓
EventBus
   ↓
Subscribers

EventBus bukan database.

EventBus juga bukan universal queue.

EventBus bertanggung jawab untuk:

publish
subscribe
unsubscribe
dispatch
ordering
priority
concurrency
error handling
panic recovery
backpressure policy
middleware
propagation

---

24. Managed Event Subscription

Subscription harus dimiliki plugin.

Contoh:

sub := ctx.Events().Subscribe(
    MessageReceived{},
    handler,
)

ctx.Defer(sub.Close)

Lebih baik:

ctx.Events().On(
    MessageReceived{},
    handler,
)

dan Plugin Scope otomatis memiliki subscription tersebut.

Ketika plugin stop:

Plugin Scope
    ↓
unsubscribe all

Tidak boleh ada subscription orphan.

---

25. Event Semantics

EventBus harus mendefinisikan:

Synchronous / Asynchronous
Ordering
Priority
Concurrency
Backpressure
Retry
Panic Recovery
Error Handling
Cancellation
Shutdown behavior
Propagation

Event handler tidak boleh dieksekusi sambil memegang internal lock EventBus.

---

25.1 Event Ordering

Event dapat memiliki ordering guarantee.

Contoh:

MessageReceived
MessageEdited
MessageDeleted

Untuk event dari source yang sama, framework dapat mempertahankan:

source_id + sequence

agar event tidak diproses secara acak.

Tidak semua event harus global-order.

Global ordering akan menjadi bottleneck.

Gunakan ordering berdasarkan scope yang diperlukan:

per chat
per message
per user
per plugin
global

---

25.2 Event Priority

Event dapat memiliki priority:

Critical
High
Normal
Low

Contoh:

RuntimeFailure      → Critical
TelegramUpdate      → High
MessageReceived     → Normal
PeriodicMetrics     → Low

Priority tidak boleh menjadi alasan untuk bypass concurrency dan backpressure policy.

---

25.3 Event Middleware

Event dapat melewati middleware:

Event
 ↓
Recovery
 ↓
Tracing
 ↓
Logging
 ↓
Deduplication
 ↓
Rate Limit
 ↓
Handler

Middleware tidak boleh mengubah ownership resource secara diam-diam.

---

26. Event Types

Contoh event:

MessageReceived
MessageEdited
MessageDeleted

ChatJoined
ChatLeft

UserUpdated
ChatUpdated

CallbackQueryReceived
InlineQueryReceived

MediaReceived

CommandReceived

PluginStarted
PluginStopped

SchedulerJobTriggered
TaskStarted
TaskCompleted
TaskFailed

Pisahkan:

External Event
Internal Event
Domain Event
Lifecycle Event

---

27. Telegram Update Normalization

Telegram update tidak boleh langsung diberikan ke feature.

Flow:

MTProto Update
      ↓
Update Receiver
      ↓
Update Normalizer
      ↓
Internal Event
      ↓
EventBus

Normalizer bertugas menghasilkan model internal yang konsisten.

Contoh:

Raw Telegram Update
        ↓
MessageReceived
        ↓
Message
        ├── ID
        ├── Chat
        ├── Sender
        ├── Text
        ├── Entities
        ├── Reply
        ├── Media
        └── Timestamp

---

28. Queue

Queue menjadi buffer antara producer dan consumer.

Producer
   ↓
Queue
   ↓
Worker

Queue harus bounded.

Contoh:

capacity = 1000

Jangan default:

unlimited queue

karena Telegram event burst dapat menyebabkan memory growth.

Queue harus memiliki policy ketika penuh:

Block
Reject
Drop
Coalesce
Priority

Policy harus eksplisit berdasarkan workload.

---

29. Worker

Worker bertanggung jawab atas concurrency.

Model:

Queue
  ↓
Worker 1
Worker 2
Worker 3
Worker 4

Worker menentukan:

«berapa task boleh berjalan bersamaan»

Task menentukan:

«apa yang dilakukan»

Prinsip:

Scheduler = WHEN
Queue     = PRESSURE / BUFFER
Worker    = HOW MANY
Task      = WHAT

---

29.1 Task Model

Task adalah unit pekerjaan runtime.

Task minimal memiliki:

type Task struct {
    ID             string
    Owner          string
    Name           string
    Priority       int

    Timeout        time.Duration
    Retry          RetryPolicy

    IdempotencyKey string

    Run            func(context.Context) error
}

Task lifecycle:

Created
   ↓
Queued
   ↓
Running
   ↓
Completed

Failure:

Running
   ├── Failed
   ├── Cancelled
   └── TimedOut

Task harus dapat diobservasi.

Contoh:

task_id
owner
created_at
started_at
completed_at
state
retry_count
duration
error

---

29.2 Task Ownership

Setiap task memiliki owner.

Contoh:

task:abc
owner: plugin:downloader

Plugin disable:

plugin:downloader
        ↓
Task Manager
        ↓
cancel owned tasks

Tidak boleh ada task plugin yang tetap berjalan tanpa owner.

---

29.3 Task Timeout

Task harus dapat memiliki timeout:

Task
  ↓
Context.WithTimeout
  ↓
Run

Timeout berbeda dari retry.

Timeout = berapa lama satu execution boleh berjalan

Retry = berapa kali execution boleh dicoba ulang

---

30. Jangan Satu Global Worker Pool

Tidak semua workload sama.

Jangan:

ALL TASKS
    ↓
Global Worker Pool

Lebih baik:

Event Workers
Job Workers
Download Workers
Media Workers
Task Workers

Contoh:

Download:
    concurrency = 3

Media:
    concurrency = 2

General Tasks:
    concurrency = 8

Hal ini mencegah workload berat menghabiskan seluruh concurrency runtime.

---

31. Plugin Worker Pool

Plugin mendapatkan managed worker capability.

Contoh:

ctx.Workers().Submit(ctx, Task{
    Name: "download",
    Run: func(ctx context.Context) error {
        return downloader.Download(ctx, url)
    },
})

Plugin tidak perlu membuat:

sync.WaitGroup
chan Task
go worker()

sendiri untuk workload standar.

Runtime yang mengontrol:

Concurrency
Queue
Timeout
Retry
Cancellation
Shutdown
Ownership
Metrics

---

32. Plugin Worker Quota

Plugin dapat memiliki quota.

Contoh:

max workers
max queued tasks
max running tasks
max background goroutines
max subscriptions
max scheduler jobs
max processes
max network requests

Contoh:

plugin:downloader
    workers: 3
    queue: 50

plugin:reminder
    workers: 2
    queue: 100

Quota mencegah satu plugin menghabiskan resource runtime.

Quota dapat diberlakukan bertahap.

Versi awal cukup:

max concurrent tasks
max queued tasks

---

33. Scheduler

Scheduler menentukan kapan pekerjaan dijalankan.

Scheduler bukan worker.

Scheduler
    ↓
Trigger
    ↓
Job
    ↓
Queue
    ↓
Worker
    ↓
Task

Scheduler cocok untuk:

Reminder
AFK timeout
Permit expiration
Scheduled message
Cleanup
Periodic synchronization
Retry

Heavy work sebaiknya tidak dieksekusi langsung oleh scheduler.

---

33.1 Job Model

Job berbeda dari Task.

Job = pekerjaan yang memiliki scheduling semantics

Task = satu execution dari pekerjaan

Contoh:

Job:
    reminder:123
    every 1 hour

Execution:
    task:abc
    run #1

Job minimal memiliki:

type Job struct {
    ID             string
    Owner          string
    Type           string
    Schedule       Schedule
    Payload        []byte

    Timeout        time.Duration
    Retry          RetryPolicy

    IdempotencyKey string
}

Job lifecycle:

Registered
   ↓
Scheduled
   ↓
Triggered
   ↓
Executing
   ↓
Completed

Failure:

Executing
   ├── Failed
   ├── Retrying
   ├── Cancelled
   └── TimedOut

---

33.2 Job Manager

Scheduler menentukan:

WHEN

Job Manager menentukan:

WHAT JOB

Task Manager menentukan:

WHAT EXECUTION

Worker menentukan:

HOW MANY

Flow lengkap:

Scheduler
    ↓
Job Trigger
    ↓
Job Manager
    ↓
Task Creation
    ↓
Queue
    ↓
Worker
    ↓
Execution

---

34. Scheduler API

Contoh:

job, err := ctx.Scheduler().Every(
    "5m",
    func(ctx context.Context) error {
        return task.Run(ctx)
    },
)

Lebih baik jika scheduler menerima metadata:

Job{
    ID:       "...",
    Owner:    "plugin:reminder",
    Schedule: "...",
    Timeout:  "...",
    Retry:    RetryPolicy{},
    Run:      ...,
}

Job harus memiliki identity.

---

35. Scheduler Job Ownership

Setiap job memiliki owner.

job:123
owner: plugin:reminder

Plugin tidak boleh membuat global job yang tidak tercatat.

Ketika plugin disable:

plugin:reminder
       ↓
Scheduler
       ↓
cancel all owned jobs

Resource Manager harus dapat menjawab:

Berapa job plugin ini?
Job apa saja?
Mana yang running?
Mana yang pending?

---

36. Persistent Scheduler

Scheduler dapat memiliki persistent jobs.

Contoh:

Reminder
Scheduled Message
Recurring Cleanup
Permit Expiration

Persistent job disimpan ke storage.

Runtime restart:

Database
   ↓
Load Jobs
   ↓
Validate
   ↓
Reconstruct Runtime Jobs

Jangan menyimpan function pointer.

Simpan declarative state:

job_id
plugin_id
type
schedule
payload
status
next_run
retry_policy
recovery_policy

---

37. Scheduler Recovery

Scheduler harus memiliki recovery policy.

Jika runtime mati ketika job seharusnya berjalan:

RUN_IMMEDIATELY
SKIP
RECALCULATE

Policy bergantung pada jenis job.

Contoh reminder:

RUN_IMMEDIATELY

Contoh periodic cleanup:

RECALCULATE

Scheduler harus menghindari duplicate execution.

Gunakan:

Job ID
Execution ID
Idempotency Key

---

38. Storage

Storage adalah infrastructure global.

Runtime memiliki:

DB connection
DB pool
transactions
migrations
health
backup/recovery integration

Plugin tidak membuka database sendiri.

Plugin menggunakan:

ctx.Storage()

Storage mendukung:

KV
Repository
Transaction
Migration
Namespace

---

38.1 Persistent State vs Runtime State

Tidak semua state harus disimpan di database.

Persistent State

State yang harus survive restart:

AFK configuration
Reminder
PM Permit configuration
Plugin settings
Scheduled jobs
User preferences

Runtime State

State yang hanya berlaku selama process hidup:

Plugin Running
Worker Busy
Connection Status
Current Queue Depth
Active Task

Ephemeral State

State sementara:

Temporary file
Download progress
Request cache
Transient event data

Rule:

Persistent State → Storage
Runtime State     → Runtime
Ephemeral State   → Scoped Resource

---

38.2 Transaction Boundary

Transaction harus ditentukan pada application/service boundary.

Contoh:

Application Use Case
      ↓
Service
      ↓
Repository
      ↓
Transaction

Jangan membuat transaction tersebar di plugin tanpa alasan.

Contoh:

CreateReminder
    ↓
BEGIN
    ↓
save reminder
save scheduler state
    ↓
COMMIT

Transaction digunakan ketika beberapa perubahan harus atomic.

---

39. Storage Namespace

Setiap plugin memiliki namespace.

Contoh:

plugins.afk
plugins.pmpermit
plugins.admin
plugins.notes
plugins.reminder

Plugin:

plugin:afk

tidak boleh membaca:

plugin:admin

secara default.

Namespace mencegah accidental coupling.

---

40. Repository Abstraction

Feature tidak bergantung langsung pada SQL driver.

Contoh:

type AFKRepository interface {
    Get(ctx context.Context, userID int64) (*AFKState, error)
    Set(ctx context.Context, state AFKState) error
    Delete(ctx context.Context, userID int64) error
}

Implementasi repository berada di infrastructure/storage layer.

Feature hanya mengetahui contract.

---

40.1 Migration

Migration adalah bagian dari lifecycle storage.

Migration harus:

versioned
ordered
repeatable
observable
transaction-aware

Contoh:

001_initial.sql
002_plugin_afk.sql
003_plugin_reminder.sql
004_add_job_execution.sql

Plugin yang memiliki persistent schema harus memiliki migration strategy yang jelas.

Migration tidak boleh dijalankan sembarangan ketika plugin menerima event.

---

41. Middleware

Middleware adalah cross-cutting execution pipeline.

Middleware tidak sama dengan EventBus, Worker, atau Service.

Middleware membungkus execution.

Contoh command:

Command
  ↓
Recovery
  ↓
Tracing
  ↓
Logging
  ↓
Authentication
  ↓
Authorization
  ↓
Rate Limit
  ↓
Deduplication
  ↓
Handler

Task:

Task
  ↓
Recovery
  ↓
Tracing
  ↓
Timeout
  ↓
Rate Limit
  ↓
Idempotency
  ↓
Execution

HTTP:

HTTP Request
  ↓
Timeout
  ↓
Tracing
  ↓
Rate Limit
  ↓
Retry
  ↓
HTTP Client

Middleware harus:

- composable
- ordered
- observable
- cancellation-aware
- tidak menyembunyikan error
- tidak mengubah ownership secara diam-diam

---

42. Idempotency & Deduplication

Telegram dan scheduler dapat menghasilkan kondisi duplicate execution.

Contoh:

same update
same message
same callback
same scheduled execution
same retry

Framework harus memiliki Idempotency Manager.

Konsep:

Input
  ↓
Idempotency Key
  ↓
Already processed?
  ├── YES → return previous result / skip
  └── NO  → execute

Idempotency key dapat berasal dari:

Telegram Update ID
Message ID
Callback ID
Task ID
Job ID + Execution ID
Application Request ID
Explicit Idempotency Key

Deduplication berbeda dengan idempotency.

Deduplication
    = jangan proses event yang sama dua kali

Idempotency
    = processing dua kali tidak menghasilkan efek berbeda

Idempotency sangat penting untuk:

Ban
Kick
Delete Message
Send Scheduled Message
Database Mutation
Payment-like external operation
Webhook

---

43. Rate Limiter

Rate Limiter adalah runtime capability untuk mengontrol throughput.

Rate limiting dapat berlaku pada:

Telegram
HTTP
Command
Plugin
User
Chat
API Endpoint
Task Type

Model:

Request
   ↓
Rate Limiter
   ↓
Allowed?
   ├── YES → Execute
   └── NO  → Reject / Delay / Queue

Rate limiter harus mendukung:

Global Limit
Plugin Limit
User Limit
Chat Limit
Endpoint Limit
Telegram Method Limit

Contoh:

plugin:downloader
    max requests = 10/sec

plugin:api
    max requests = 5/sec

Rate limiter berbeda dari worker concurrency.

Worker Limit
    = berapa execution aktif

Rate Limit
    = berapa request boleh terjadi dalam periode tertentu

---

43.1 Telegram Rate Limiting

Telegram memiliki batasan eksternal.

Framework harus memiliki:

Telegram Rate Limiter
Flood-Wait Manager
Request Classification
Retry Policy

Flow:

Plugin
   ↓
Telegram Service
   ↓
Rate Limiter
   ↓
MTProto RPC
   ↓
Telegram

Jika Telegram memberikan flood wait:

FloodWait
    ↓
Flood-Wait Manager
    ↓
Delay
    ↓
Retry

Plugin tidak boleh mengimplementasikan flood-wait handling sendiri secara umum.

---

44. Telegram Boundary

Telegram Boundary mengisolasi semua detail Telegram dari feature.

Komponen:

MTProto Client
Session
Authentication
Connection
Reconnect
Update Receiver
Update Normalizer
RPC
Rate Limiter
Flood-Wait Manager
Entity Resolver
Entity Cache
File Transfer
Media

Plugin menggunakan abstraction:

ctx.Telegram()

bukan raw MTProto.

---

45. Entity / Identity Model

Telegram memiliki banyak bentuk identity:

User
Chat
Channel
Group
Bot
Message
Peer
InputPeer

Feature tidak boleh menangani raw MTProto entity di setiap tempat.

Framework menyediakan internal model:

Actor
User
Chat
Message
Peer
Entity

Contoh:

type User struct {
    ID        int64
    Username  string
    FirstName string
    LastName  string
}

type Chat struct {
    ID       int64
    Type     ChatType
    Title    string
    Username string
}

type Message struct {
    ID        int64
    Chat      ChatRef
    Sender    ActorRef
    Text      string
    Timestamp time.Time
}

Tujuannya:

MTProto Entity
      ↓
Entity Resolver
      ↓
Internal Entity Model
      ↓
Feature

---

45.1 Entity Resolver

Entity Resolver bertugas mengubah:

username
user ID
chat ID
peer
message sender
reply sender

menjadi canonical internal identity.

Resolver juga menangani:

cache
missing entity
stale entity
access failure
peer resolution

---

45.2 Identity

Identity harus memiliki canonical representation.

Contoh:

User ID
Chat ID
Message ID

Jangan menjadikan username sebagai identity utama karena username dapat berubah.

Gunakan stable ID sebagai canonical identity.

---

46. MTProto

MTProto adalah protocol boundary.

Framework tidak boleh mencampurkan raw MTProto type ke seluruh application.

Boundary:

MTProto
   ↓
Telegram Adapter
   ↓
Telegram Abstraction
   ↓
Services
   ↓
Application
   ↓
Plugin

Raw MTProto access:

ctx.Telegram().Raw()

hanya jika plugin memiliki capability:

telegram.raw

---

47. Flood Wait

Flood wait harus ditangani centralized.

Model:

Telegram Request
      ↓
MTProto
      ↓
Flood Wait
      ↓
Flood-Wait Manager
      ↓
Retry / Delay

Flood wait harus observable:

method
duration
plugin
request
timestamp

Plugin tidak boleh membuat mekanisme flood-wait sendiri untuk request standar.

---

48. Media and File Transfer

Media adalah heavy workload.

Harus memiliki:

separate queue
concurrency limit
timeout
retry
progress
temporary storage
cleanup
cancellation

Flow:

Plugin
   ↓
MediaService
   ↓
Download Queue
   ↓
Download Worker
   ↓
Telegram / HTTP
   ↓
Temporary Storage
   ↓
Media Processing
   ↓
Output
   ↓
Cleanup

Media processing tidak boleh menghabiskan worker umum.

---

49. Network / HTTP Service

Network access harus menjadi capability yang dikontrol framework.

Jangan:

client := &http.Client{}
client.Get(...)

langsung dari plugin.

Gunakan:

Plugin
   ↓
Plugin Context
   ↓
HTTP / Network Service
   ↓
Internet

Network Service bertanggung jawab atas:

HTTP Client
Connection Pool
Timeout
Cancellation
Proxy
Rate Limit
Retry
Backoff
Metrics
Logging
Tracing
Resource Ownership

Plugin hanya meminta:

ctx.HTTP().Do(ctx, req)

---

49.1 Network Capability

Capability dapat dibuat granular:

network.http
network.websocket
network.tcp
network.udp

Default plugin sebaiknya hanya mendapat capability yang dibutuhkan.

Contoh:

Downloader:
    network.http

Realtime Monitor:
    network.websocket

Raw TCP/UDP harus dianggap privileged.

---

49.2 Per-Plugin Network Quota

Plugin dapat memiliki:

requests/sec
concurrent requests
connection limit
download bandwidth
timeout

Contoh:

plugin:api
    requests/sec = 5
    concurrent = 2
    timeout = 15s

Tujuannya mencegah plugin menghabiskan network resource.

---

49.3 WebSocket Ownership

WebSocket adalah long-lived resource.

Setiap WebSocket harus:

owned
cancelable
observable
closed on plugin stop

Contoh:

plugin:market
    websocket:ws-123

Plugin stop:

Plugin Scope
    ↓
cancel context
    ↓
close websocket
    ↓
wait

---

50. Filesystem Manager

Filesystem juga harus menjadi capability.

Plugin tidak boleh bebas menggunakan seluruh filesystem host.

Jangan:

os.WriteFile("/etc/whatever", ...)

Gunakan:

ctx.Files().Data(...)
ctx.Files().Cache(...)
ctx.Files().Temp(...)

Framework menyediakan scoped directory:

data/
    plugins/
        afk/
        downloader/
        notes/

cache/
    plugins/
        downloader/

tmp/
    plugins/
        downloader/

---

50.1 Filesystem Capability

Capability:

filesystem.data
filesystem.cache
filesystem.temp

Access harus dibatasi berdasarkan plugin.

Plugin:

plugin:afk

tidak boleh secara default menulis:

plugins:downloader

---

50.2 Temporary File Lifecycle

Temporary file harus memiliki owner.

temp-file-123
owner: plugin:downloader

Ketika task selesai:

Task Completed
    ↓
cleanup temp file

Ketika plugin berhenti:

Plugin Stop
    ↓
cleanup remaining temp files

Runtime juga dapat melakukan orphan cleanup ketika startup.

---

51. Process Manager

Beberapa feature membutuhkan external process:

ffmpeg
yt-dlp
ImageMagick
7z
custom helper

Plugin tidak boleh menjalankan process secara bebas.

Jangan:

exec.Command(...)

langsung dari plugin.

Gunakan:

ctx.Process()

Process Manager bertanggung jawab atas:

spawn
timeout
context cancellation
stdin/stdout/stderr
exit code
resource limits
process tracking
cleanup
logging
ownership

---

51.1 Process Ownership

Contoh:

process:ffmpeg-123
owner: plugin:media

Jika plugin dihentikan:

Plugin Scope
    ↓
Process Manager
    ↓
terminate process
    ↓
wait
    ↓
cleanup

Tidak boleh ada orphan child process.

---

51.2 Process Capability

Capability:

process.execute

sebaiknya privileged.

Lebih aman jika command yang boleh dieksekusi dapat di-whitelist.

Contoh:

plugin:media

allowed:
    ffmpeg
    ffprobe

bukan:

/usr/bin/*

---

52. Secret Manager

Secret tidak boleh dianggap sebagai configuration biasa.

Contoh secret:

API Key
Bot Token
Telegram Credentials
HTTP Authorization Token
Webhook Secret
Third-party API Secret

Plugin tidak boleh hard-code:

const APIKey = "secret..."

atau menyimpan secret di source code.

Gunakan:

ctx.Secrets()

Secret source dapat berasal dari:

Environment
Config Reference
Secret File
Encrypted Storage
External Secret Provider

---

52.1 Secret Capability

Plugin harus mendeklarasikan:

secret.read

Jika tidak membutuhkan secret:

no secret capability

Secret access harus observable tanpa mencatat nilai secret.

Contoh log:

plugin=downloader
secret=YOUTUBE_API_KEY
action=read

Jangan:

key=AIza....

---

52.2 Secret Rotation

Jika memungkinkan, Secret Manager harus mendukung reload/rotation.

Plugin sebaiknya tidak menyimpan secret secara permanen dalam global variable.

Lebih baik:

request
  ↓
Secret Manager
  ↓
current secret

atau menggunakan scoped credential provider.

---

53. Health

Health subsystem menjawab:

«Apakah runtime sehat?»

Health check harus mencakup:

Runtime
Storage
Telegram
EventBus
Queue
Workers
Scheduler
Network
Filesystem
Process Manager
Plugins

Contoh:

Runtime:
    healthy

Storage:
    healthy

Telegram:
    healthy

Workers:
    healthy

Scheduler:
    healthy

Queue:
    degraded

Plugin downloader:
    unhealthy

Health berbeda dengan log.

Log
    = apa yang terjadi

Health
    = kondisi sistem sekarang

---

53.1 Readiness

Readiness menjawab:

«Apakah sistem siap menerima workload?»

Contoh:

Database ready
Telegram ready
EventBus ready
Workers ready
Plugins initialized

Baru setelah semua critical dependency siap:

Runtime = Ready

---

53.2 Liveness

Liveness menjawab:

«Apakah process/runtime masih hidup?»

Contoh indikator:

runtime loop alive
worker manager responsive
scheduler responsive
event dispatcher responsive

Liveness tidak boleh menganggap semua dependency harus selalu healthy.

Contoh:

Telegram temporarily unavailable

tidak otomatis berarti:

process dead

---

54. Diagnostics

Diagnostics berbeda dari logging.

Diagnostics memberikan snapshot kondisi internal runtime.

Contoh:

Runtime
    state: running
    uptime: 4h32m

Plugins
    loaded: 12
    running: 11
    failed: 1

Queue
    depth: 120
    capacity: 1000

Workers
    busy: 7
    capacity: 12

Tasks
    running: 7
    queued: 120
    failed: 3

Scheduler
    jobs: 45
    running: 2

Telegram
    connected: true
    flood_wait: false

Network
    active_requests: 3

Processes
    running: 1

Resources
    subscriptions: 84
    goroutines: 43
    temp_files: 6

Diagnostics harus dapat digunakan untuk:

debugging
health
admin command
CLI
metrics
support
failure analysis

---

55. Resource Leak Detection

Diagnostics harus dapat mendeteksi resource yang seharusnya sudah hilang.

Contoh:

Plugin stopped

Expected:
    subscriptions = 0
    jobs = 0
    tasks = 0
    goroutines = 0
    processes = 0
    websocket = 0
    temp_files = 0

Jika masih ada:

PLUGIN RESOURCE LEAK

plugin: downloader

tasks: 1
temp_files: 2
processes: 1

Runtime dapat:

log warning
emit diagnostic event
mark plugin degraded
disable plugin
force cleanup

Policy dapat dikonfigurasi.

---

56. Lock & Concurrency Control

Worker concurrency tidak cukup untuk semua synchronization.

Framework dapat menyediakan:

Keyed Lock
Mutex
Semaphore
Lease

Contoh keyed lock:

lock:user:123
lock:chat:-100123
lock:job:reminder-123

Gunanya:

prevent duplicate mutation
serialize per-user operation
serialize per-chat operation
protect shared state

Lock harus memiliki:

owner
timeout
cancellation
diagnostics

Jangan membuat global lock besar untuk seluruh userbot jika hanya satu entity yang perlu diserialisasi.

---

57. Configuration Manager

Configuration harus centralized.

Tanggung jawab:

Load
Parse
Validate
Default
Override
Environment
Plugin Config
Secret Reference
Reload

Contoh:

runtime:
  shutdown_timeout: 30s

workers:
  general:
    concurrency: 8

network:
  timeout: 15s

plugins:
  downloader:
    enabled: true
    workers: 3

Plugin configuration berada di:

plugins.<plugin_id>

---

58. Cache

Cache adalah reusable capability.

Cache harus mendukung:

namespace
TTL
size limit
eviction
invalidation
metrics

Contoh:

plugin:admin
    entity cache

plugin:downloader
    metadata cache

Cache bukan persistent storage.

Storage
    = source of truth

Cache
    = optimization

Plugin disable tidak harus mempertahankan cache transient.

---

59. Error Model

Error harus dikategorikan.

Contoh:

ConfigurationError
InitializationError
ValidationError
PermissionError
TelegramError
FloodWaitError
StorageError
SchedulerError
TaskError
PluginError
NetworkError
ProcessError
TimeoutError
CancellationError
RateLimitError
IdempotencyError

Kategori error membantu menentukan:

retry
abort
log
notify
ignore
disable

---

60. Retry Policy

Retry harus centralized.

Contoh:

type RetryPolicy struct {
    MaxAttempts int
    Backoff     BackoffPolicy
    Jitter      bool
    Retryable   []ErrorType
}

Retry harus memperhatikan:

idempotency
timeout
rate limit
flood wait
cancellation
error category

Tidak semua error boleh di-retry.

Contoh:

PermissionError
    → no retry

InvalidInput
    → no retry

TemporaryNetworkError
    → retry

FloodWait
    → delay/retry

ContextCanceled
    → no retry

---

61. Command Runtime

Command adalah application entry point dari Telegram.

Flow:

MessageReceived
      ↓
Command Parser
      ↓
Command Registry
      ↓
Command Resolver
      ↓
Middleware
      ↓
Permission
      ↓
Cooldown / Rate Limit
      ↓
Idempotency
      ↓
Handler
      ↓
Application Use Case
      ↓
Service
      ↓
Telegram

---

61.1 Command Registry

Registry menyimpan:

Name
Aliases
Description
Usage
Owner
Priority
Permission
Cooldown
Rate Limit
Handler

Command harus dapat didaftarkan dan dihapus berdasarkan plugin owner.

Plugin disable:

plugin stop
    ↓
unregister commands

---

62. Permission & Authorization

Permission Service menjadi centralized authorization system.

Model:

Actor
  ↓
Authentication
  ↓
Permission
  ↓
Policy
  ↓
Decision

Permission tidak boleh tersebar sebagai:

if senderID == ownerID

di seluruh feature.

Policy dapat melibatkan:

User
Chat
Role
Command
Plugin
Action
Context

---

63. Application Layer

Application layer berisi use case.

Contoh:

CreateReminder
SetAFK
ApprovePM
DownloadMedia
BanUser
MuteUser
SaveNote
ApplyFilter

Application layer tidak tahu:

MTProto implementation
SQL driver
worker channel implementation
HTTP client implementation

Ia menggunakan service contracts.

---

64. Services

Services menyediakan reusable capability.

Contoh:

MessageService
PermissionService
ModerationService
EntityService
MediaService
DownloaderService
CacheService
NetworkService
NotificationService
TelegramService

Service tidak menjadi tempat feature-specific policy jika policy tersebut hanya milik plugin.

Contoh:

ModerationService
    = bagaimana melakukan ban/mute/kick

Admin Plugin
    = kapan dan siapa yang boleh melakukan ban/mute/kick

---

65. Feature vs Plugin

Feature adalah business concept.

Plugin adalah lifecycle/package boundary.

Contoh:

Feature:
    AFK

Plugin:
    plugins/afk

Feature:
    Admin moderation

Plugin:
    plugins/admin

Tidak perlu:

plugins/ban
plugins/kick
plugins/mute
plugins/unban

jika semua merupakan satu domain Admin.

---

66. Feature Contract

Feature harus memiliki contract yang jelas.

Contoh:

Inputs
Dependencies
Events
Commands
Persistent State
Runtime State
Outputs
Side Effects
Permissions
Capabilities

Contoh AFK:

Inputs:
    MessageReceived

State:
    afk status
    afk message
    timestamp

Dependencies:
    EventBus
    Storage
    Permission
    Telegram

Side effects:
    send reply
    update state

---

67. Feature State Model

Feature state dibagi:

Persistent
Runtime
Ephemeral

Contoh Downloader:

Persistent:
    download preferences

Runtime:
    active downloads

Ephemeral:
    temporary file
    current progress

Restart:

Persistent → restored

Runtime → reconstructed

Ephemeral → discarded

---

68. Feature Dependencies

Feature dapat bergantung pada service atau plugin lain.

Contoh:

Admin Plugin
    ↓
PermissionService
    ↓
EntityService
    ↓
ModerationService

Plugin dependency harus deklaratif jika benar-benar bergantung pada plugin lain.

Dependency resolver harus:

validate
topological sort
detect cycle
determine startup order
determine shutdown order

---

69. Feature Conflict

Feature dapat conflict.

Contoh:

AutoReply
    vs
AntiSpam

atau:

Two command plugins
    both register
    ".status"

Conflict harus ditangani oleh runtime/registry.

Jangan membiarkan:

last plugin wins

secara diam-diam.

---

70. Command Conflict Resolution

Jika dua plugin mendaftarkan:

.status

registry harus memiliki policy:

Reject
Priority
Namespace
Override

Default yang aman:

Reject duplicate command

Plugin harus menggunakan:

priority
namespace
alias

jika memang membutuhkan overlap.

---

71. Observability

Framework harus menyediakan:

Logs
Metrics
Health
Diagnostics
Tracing
Audit

Plugin metrics:

events_received
events_failed
commands_executed
commands_failed
tasks_submitted
tasks_running
tasks_failed
jobs_registered
jobs_running
queue_depth
worker_busy
errors
execution_duration
network_requests
processes_running
resource_leaks

---

72. Correlation ID

Request/event harus dapat ditelusuri.

Flow:

Telegram Update
      ↓
Correlation ID
      ↓
Event
      ↓
Command
      ↓
Application
      ↓
Task
      ↓
Service
      ↓
Telegram Response

Contoh:

request_id=abc123
plugin=admin
command=.ban
task=task-123
chat=-100123
user=123

Hal ini sangat membantu debugging.

---

73. Audit Log

Audit berbeda dengan debug log.

Audit digunakan untuk security-sensitive action:

Ban
Kick
Mute
Delete
Permission Change
Plugin Enable
Plugin Disable
Configuration Change
Secret Access

Audit record:

timestamp
actor
action
target
plugin
result
correlation_id

Secret value tidak boleh masuk audit.

---

74. Notification / Alert Service

Runtime dapat memberi notifikasi ketika terjadi kondisi penting.

Contoh:

Plugin crash
Database unavailable
Telegram disconnected
Flood wait
Queue overloaded
Storage low
Resource leak
Process crash
Health degraded

Notification Service dapat mengirim:

Telegram
CLI
Log
Webhook

tetap melalui capability dan policy.

---

75. Graceful Failure Isolation

Panic pada satu plugin tidak boleh menjatuhkan seluruh runtime.

Flow:

Plugin Handler
    ↓
Recovery Middleware
    ↓
panic
    ↓
capture stack
    ↓
log
    ↓
diagnostics
    ↓
metrics
    ↓
failure policy

Policy dapat berupa:

Ignore
Restart Handler
Disable Plugin
Restart Plugin
Escalate

Runtime core failure tetap dapat menyebabkan runtime masuk:

Failed

---

76. Plugin Diagnostics

Setiap plugin harus memiliki diagnostics:

State
Version
Dependencies
Capabilities
Subscriptions
Jobs
Tasks
Goroutines
Processes
HTTP Connections
Temp Files
Queue Depth
Errors
Last Failure
Uptime

Contoh:

plugin:downloader

state: running

tasks:
    running: 2
    queued: 7

network:
    requests: 2

process:
    ffmpeg: 1

temp:
    files: 3

errors:
    last: 2m ago

---

77. Runtime Invariants

Framework harus mempertahankan invariant berikut:

plugin disabled
    → no new plugin work

plugin stopped
    → no plugin-owned transient resource

queue
    → depth <= capacity

workers
    → running <= concurrency

scheduler
    → does not bypass worker policy

plugin
    → cannot use ungranted capability

task
    → has owner

job
    → has owner

resource
    → has owner

long-lived work
    → has cancellation path

temporary resource
    → has cleanup path

persistent state
    → does not depend on in-memory runtime object

secret
    → never appears in logs

external process
    → cannot survive plugin shutdown unintentionally

---

78. Anti-Patterns

Jangan:

clone Python architecture
feature-first development
unmanaged goroutines
plugin-owned scheduler
plugin-owned global worker pool
plugin-owned DB connection
raw MTProto by default
raw http.Client per plugin
raw os/exec per plugin
unrestricted filesystem access
hardcoded owner IDs
unlimited queue
unlimited concurrency
global singleton everywhere
UI mutating internals
plugin controlling global lifecycle
secret hard-coded in source
duplicate command silently overriding
retry without idempotency consideration
external process without cleanup
temporary file without owner
WebSocket without cancellation

---

79. Development Order

Development order tetap architecture-first:

01 Architecture Principles
02 Dependency Graph
03 Package Boundaries
04 Public Interfaces
05 Error Model
06 Context Model
07 Lifecycle State Machine
08 Runtime Skeleton
09 Resource Ownership Model
10 Resource Manager
11 Event Model
12 EventBus
13 Event Middleware
14 Queue
15 Backpressure
16 Worker Model
17 Task Model
18 Worker Manager
19 Scheduler
20 Job Model
21 Job Manager
22 Scheduler Persistence
23 Idempotency
24 Rate Limiter
25 Lock / Concurrency Control
26 Storage
27 Transaction Boundary
28 Migration
29 Filesystem Manager
30 Secret Manager
31 Process Manager
32 Network / HTTP Service
33 Telegram Abstraction
34 MTProto Adapter
35 Entity / Identity Model
36 Entity Resolver
37 Update Receiver
38 Update Normalizer
39 Telegram Events
40 Telegram Rate Limiter
41 Flood-Wait Manager
42 Service Registry
43 Core Services
44 Command Engine
45 Command Middleware
46 Permission System
47 Application Use Cases
48 Plugin Runtime
49 Plugin Manifest
50 Plugin Context
51 Plugin Scope
52 Capability System
53 Resource Manager Integration
54 Plugin Storage Namespace
55 Plugin Scheduler
56 Plugin Workers
57 Plugin Task Management
58 Plugin Network
59 Plugin Filesystem
60 Plugin Process Management
61 Plugin Dependency Resolver
62 Plugin Conflict Resolver
63 Plugin Configuration
64 Plugin Diagnostics
65 Plugin Lifecycle
66 Health / Readiness
67 Observability
68 Audit
69 Notification
70 CLI
71 Test Harness
72 First Plugin
73 More Feature Plugins

---

80. Core Definition of Done

Runtime dianggap memiliki foundation yang cukup ketika tersedia:

Config
Logger
Lifecycle
Context
Graceful Shutdown
Runtime State

Resource Manager

EventBus
Event Ordering
Event Middleware

Bounded Queue
Backpressure

Worker Manager
Task Manager

Scheduler
Job Manager
Retry
Timeout

Idempotency
Rate Limiter
Lock

Storage
Transaction
Migration

Filesystem Manager
Secret Manager
Process Manager
Network / HTTP Service

Telegram Connection
MTProto
Update Receiver
Update Normalizer
Entity Resolver
Flood-Wait Handling

Service Registry
Command Runtime
Permission System

Plugin Runtime
Plugin Manifest
Plugin Context
Plugin Scope
Capability Manager
Dependency Resolver
Conflict Resolver

Health
Diagnostics
Metrics
Logging
Failure Isolation
Leak Detection

---

81. Telegram Definition of Done

Telegram layer harus memiliki:

MTProto connection
Authentication
Session
Reconnect
Update receiving
Update normalization
Internal Telegram models
Entity resolution
Entity cache
RPC abstraction
Rate limiting
Flood-wait handling
Request cancellation
Request timeout
Request retry
Idempotency support
Media transfer
File transfer
Progress tracking
Cleanup

---

82. Plugin Runtime Definition of Done

Plugin Runtime harus memiliki:

Discovery
Manifest
Validation
Dependency resolution
Conflict resolution
Lifecycle
Context
Scope
Capability system
Resource Manager
Storage namespace
Scheduler ownership
Worker ownership
Task ownership
Network ownership
Filesystem ownership
Process ownership
Configuration
Diagnostics
Panic isolation
Failure policy
Leak detection
API version
Migration strategy

---

83. Recommended Runtime Mental Model

Framework dapat dipahami sebagai mini operating system untuk userbot.

Runtime
│
├── Process / Lifecycle
│
├── Resources
│
├── Event System
│
├── Queue
│
├── Workers
│
├── Tasks
│
├── Scheduler
│
├── Jobs
│
├── Storage
│
├── Network
│
├── Filesystem
│
├── Processes
│
├── Secrets
│
├── Rate Limiting
│
├── Idempotency
│
├── Middleware
│
├── Health
│
└── Diagnostics

Plugin berada di atasnya:

Plugin
│
├── Commands
├── Events
├── Jobs
├── Tasks
├── Services
├── State
└── Features

Plugin tidak mengontrol OS/runtime tersebut.

Plugin mengonsumsi capability yang diberikan runtime.

---

84. Final Architectural Principles

Prinsip utama framework:

1. Runtime owns lifecycle

Plugin tidak mengontrol lifecycle global.

2. Every long-lived resource has an owner

No anonymous resource.

3. Persistent state survives restart

Transient resource does not.

4. Scheduler decides WHEN

Scheduler = timing

5. Queue controls PRESSURE

Queue = buffering / backpressure

6. Worker controls HOW MANY

Worker = concurrency

7. Task defines WHAT EXECUTION

Task = execution unit

8. Job defines WHAT IS SCHEDULED

Job = persistent/scheduled work definition

9. Rate Limiter controls throughput

RateLimit != Worker concurrency

10. Idempotency controls duplicate effects

Dedup != Idempotency

11. Middleware controls cross-cutting execution

Recovery
Tracing
Authorization
RateLimit
Dedup
Timeout

12. Entity layer owns identity normalization

Raw Telegram entity
    ↓
Canonical identity

13. Network belongs to runtime

Plugin
    ↓
HTTP / Network Capability
    ↓
Internet

14. Filesystem belongs to runtime

Plugin
    ↓
Scoped Filesystem
    ↓
Host filesystem

15. External processes belong to runtime

Plugin
    ↓
Process Manager
    ↓
ffmpeg / yt-dlp / etc.

16. Secrets belong to Secret Manager

Plugin
    ↓
Secret Capability
    ↓
Secret Manager

17. Telegram details stay behind boundary

Plugin
    ↓
Telegram Service
    ↓
MTProto

18. Health tells current condition

Health = current system condition

19. Diagnostics explain current runtime state

Diagnostics = internal runtime snapshot

20. Plugins consume capabilities

Plugins consume capabilities;
they do not own the runtime.

---

85. Final Architecture

                         ┌─────────────────────┐
                         │       UI / CLI      │
                         └──────────┬──────────┘
                                    │
                         ┌──────────▼──────────┐
                         │    Application      │
                         │ Use Cases / Policy  │
                         └──────────┬──────────┘
                                    │
                         ┌──────────▼──────────┐
                         │   Plugin Runtime    │
                         │ Lifecycle / Scope   │
                         │ Capability / Owner  │
                         └──────────┬──────────┘
                                    │
                  ┌─────────────────▼─────────────────┐
                  │             Plugins               │
                  │ AFK / Admin / PMPermit / etc.    │
                  └─────────────────┬─────────────────┘
                                    │
                  ┌─────────────────▼─────────────────┐
                  │             Services              │
                  │ Message / Permission / Entity    │
                  │ Media / Network / Downloader     │
                  └─────────────────┬─────────────────┘
                                    │
       ┌────────────────────────────▼────────────────────────────┐
       │                  Runtime Infrastructure                  │
       │                                                          │
       │ EventBus ─ Queue ─ Worker ─ Task                         │
       │ Scheduler ─ Job Manager                                  │
       │ Storage ─ Transaction ─ Migration                        │
       │ Resource Manager ─ Lock                                  │
       │ Middleware ─ Rate Limiter ─ Idempotency                   │
       │ Filesystem ─ Process Manager ─ Secret Manager             │
       │ Health ─ Diagnostics ─ Metrics                            │
       └────────────────────────────┬────────────────────────────┘
                                    │
             ┌──────────────────────▼──────────────────────┐
             │              External Boundaries            │
             │                                             │
             │ Telegram / MTProto                          │
             │ Internet / HTTP                             │
             │ Host Filesystem                             │
             │ External Processes                          │
             └─────────────────────────────────────────────┘

Core flow:

Telegram Update
      ↓
MTProto
      ↓
Update Receiver
      ↓
Normalizer
      ↓
EventBus
      ↓
Middleware
      ↓
Command / Feature Handler
      ↓
Application Use Case
      ↓
Service
      ↓
Task
      ↓
Queue
      ↓
Worker
      ↓
External Boundary

Scheduled flow:

Persistent Job
      ↓
Scheduler
      ↓
Job Trigger
      ↓
Task
      ↓
Queue
      ↓
Worker
      ↓
Service
      ↓
External System

Network flow:

Plugin
      ↓
Capability Check
      ↓
HTTP / Network Service
      ↓
Rate Limiter
      ↓
Middleware
      ↓
HTTP Client
      ↓
Internet

Media flow:

Telegram / HTTP
      ↓
Downloader
      ↓
Download Queue
      ↓
Download Worker
      ↓
Temporary Files
      ↓
Process Manager
      ↓
ffmpeg / yt-dlp
      ↓
Output
      ↓
Cleanup

Plugin shutdown flow:

Disable Plugin
      ↓
Stop Intake
      ↓
Cancel Plugin Context
      ↓
Cancel Tasks
      ↓
Cancel Jobs
      ↓
Unsubscribe Events
      ↓
Close HTTP / WebSocket
      ↓
Stop Child Processes
      ↓
Stop Goroutines
      ↓
Cleanup Temporary Files
      ↓
Release Locks
      ↓
Resource Manager Verification
      ↓
Plugin Disabled

Target philosophy:

                         RUNTIME
                            │
       ┌────────────────────┼────────────────────┐
       │                    │                    │
   Scheduling            Execution           Resources
       │                    │                    │
     Job                Task/Worker          Ownership
       │                    │                    │
    WHEN                 HOW MANY             CLEANUP
       │                    │                    │
       └────────────────────┼────────────────────┘
                            │
                      CAPABILITIES
                            │
       ┌────────────┬───────┼────────┬────────────┐
       │            │       │        │            │
    Telegram     Storage  Network  Filesystem  Process
       │            │       │        │            │
       └────────────┴───────┼────────┴────────────┘
                            │
                         PLUGINS
                            │
                         FEATURES

Kesimpulan:

«Runtime mengontrol bagaimana sistem hidup.

Scheduler menentukan kapan pekerjaan terjadi.

Job mendefinisikan pekerjaan terjadwal.

Queue mengontrol pressure dan buffering.

Worker mengontrol concurrency.

Task mendefinisikan execution.

EventBus mendistribusikan event.

Middleware mengontrol cross-cutting execution.

Rate Limiter mengontrol throughput.

Idempotency mencegah duplicate side effects.

Entity/Identity menormalkan identitas Telegram.

Resource Manager memastikan setiap resource memiliki owner dan cleanup path.

Storage menyimpan persistent state.

Filesystem mengelola file secara scoped.

Process Manager mengontrol external process.

Network Service mengontrol akses Internet.

Secret Manager mengontrol credential.

Health dan Diagnostics menjelaskan kondisi runtime.

Plugin hanya mengonsumsi capability tersebut.

Feature menjadi business behavior di atas plugin.»

Dengan model ini, framework tidak lagi sekadar Telegram userbot dengan plugin, tetapi menjadi managed application runtime yang kebetulan menggunakan Telegram MTProto sebagai salah satu external boundary.