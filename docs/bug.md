Saya sudah membaca ulang state **`main` terbaru pada commit `b4cf03674f9ed052292683f26c8371db963611b6`**. Fokus audit kali ini murni pada koordinasi antar-komponen/service: lifecycle ownership, state transition, admission/drain, transactional boundary, cancellation, rollback, dan failure isolation.

Secara dasar arsitekturnya sudah menuju arah yang benar: ada `Runtime`, dependency DAG, component lifecycle, worker admission, plugin scope, event bus, transactional outbox, dan explicit shutdown. Tetapi saat ini sistem masih berada di fase **“beberapa mekanisme robust berjalan berdampingan”**, belum menjadi **satu orchestration model yang konsisten**. Itu sumber utama friction dan edge-case.

## Temuan paling penting

| Severity | Area                | Masalah                                                                                               |
| -------- | ------------------- | ----------------------------------------------------------------------------------------------------- |
| 🔴 P1    | Lifecycle ownership | `Runtime` diklaim sole lifecycle owner, tetapi `App.Shutdown()` masih menutup banyak resource sendiri |
| 🔴 P1    | Settings/EventBus   | Transactional outbox + direct `Publish()` menyebabkan event ganda                                     |
| 🔴 P1    | EventBus            | `PublishDurable()` menahan `RLock` ketika menjalankan arbitrary subscriber                            |
| 🔴 P1    | Workers             | `Health()` mengambil `RLock` lalu memanggil `AllStats()` yang mengambil `RLock` lagi                  |
| 🔴 P1    | Runtime             | `Start()`/`Stop()` masih menjalankan component code sambil memegang coordinator mutex                 |
| 🔴 P1    | Runtime             | startup cancellation tidak benar-benar membatasi `Component.Start()`                                  |
| 🔴 P1    | Plugin              | registration manifest/gate belum benar-benar transactional                                            |
| 🟠 P1/P2 | Plugin              | concurrent `Enable()` dapat menjalankan initialization plugin yang sama dua kali                      |
| 🟠 P1/P2 | EventBus            | shutdown context tidak benar-benar membatasi `Close()`                                                |
| 🟠 P1/P2 | Worker              | panic dalam task dapat menjatuhkan seluruh process                                                    |
| 🟠 P2    | Settings outbox     | sukses publish tetapi gagal mark processed akan redeliver; consumer harus idempotent                  |
| 🟠 P2    | Addon               | lifetime process masih terikat ke context yang diberikan ke `Start()`                                 |

Yang paling besar sebenarnya bukan satu bug, melainkan **ownership model**.

---

# 1. `Runtime` belum benar-benar menjadi sole lifecycle owner

ADR menyatakan:

> `internal/runtime.Runtime` adalah sole lifecycle owner.

Dan shutdown seharusnya berasal dari component dependencies, bukan daftar manual.

Tetapi `App.Shutdown()` masih melakukan:

```go
addonMgr.ShutdownRuntimes()
limiter.Close()
interLimiter.Close()
idemp.Close()
runtime.Stop(ctx)
db.Close()
logger.Sync()
```

Ini menghasilkan **dua orchestration domain**:

```text
App
 ├─ addon
 ├─ limiter
 ├─ idempotency
 ├─ Runtime
 │   ├─ eventbus
 │   ├─ workers
 │   ├─ scheduler
 │   ├─ settings
 │   ├─ dispatcher
 │   └─ plugins
 └─ database
```

Masalahnya dependency sebenarnya silang.

Contohnya plugin berhenti melalui `Runtime.Stop()`, tetapi addon runtime sudah dimatikan **sebelum Runtime mulai shutdown**.

Secara konseptual:

```text
App.Shutdown
   ↓
addon STOPPED
   ↓
rate limiter STOPPED
   ↓
idempotency STOPPED
   ↓
Runtime.Stop
   ↓
plugins.Stop
```

Kalau `plugin.Shutdown()` masih memerlukan addon/process/idempotency/service tertentu, teardown terjadi dalam urutan yang salah.

Ini bertentangan dengan dependency ownership yang sebenarnya.

### Target yang lebih benar

Semua long-lived thing seharusnya menjadi `runtime.Component`.

```text
Runtime
 ├── database
 ├── eventbus
 ├── idempotency
 ├── rate-limiters
 ├── workers
 ├── process-manager
 ├── addon-manager
 ├── settings
 ├── scheduler
 ├── dispatcher
 ├── assistant
 ├── plugins
 └── telegram-ingress
```

Lalu:

```go
func (a *App) Shutdown(ctx context.Context) error {
    return a.runtime.Stop(ctx)
}
```

Logger pun idealnya menjadi diagnostics component atau satu-satunya object yang ditutup setelah Runtime.

**Ini perubahan yang paling saya rekomendasikan.**

---

# 2. Lifecycle sebaiknya bukan hanya Start/Stop

Saat ini component contract:

```go
Start(context.Context) error
Stop(context.Context) error
```

Untuk service cukup kompleks seperti ini, `Stop()` membawa terlalu banyak makna.

Anda sebenarnya mempunyai minimal empat fase berbeda:

```text
Running
   ↓
Quiescing
   ↓
Draining
   ↓
Stopping
   ↓
Stopped
```

`Quiesce` berarti:

> jangan menerima pekerjaan baru.

`Drain` berarti:

> selesaikan pekerjaan yang sudah diterima.

`Stop` berarti:

> hentikan goroutine/background loop.

`Close` berarti:

> lepaskan DB/socket/file/resource final.

Ini sangat berguna untuk dependency graph.

Contohnya:

```text
Telegram ingress
      ↓ Quiesce

Scheduler triggers
      ↓ Quiesce

Plugin/router admission
      ↓ Quiesce

Workers
      ↓ Drain

Plugins
      ↓ Stop

EventBus
      ↓ Drain

Addon/process/network
      ↓ Stop

Database
      ↓ Close
```

Saat ini beberapa subsystem sudah melakukan konsep tersebut secara lokal, terutama worker manager, tetapi Runtime belum mengetahui adanya fase-fase itu.

Saya akan membuat optional interface:

```go
type Quiescer interface {
    Quiesce(context.Context) error
}

type Drainer interface {
    Drain(context.Context) error
}
```

dan Runtime melakukan shutdown phased:

```text
phase 1: Quiesce seluruh ingress
phase 2: Drain seluruh accepted work
phase 3: Stop reverse DAG
phase 4: Close infrastructure
```

Ini akan membuat shutdown jauh lebih deterministik.

---

# 3. Runtime masih memanggil arbitrary component code di bawah mutex

`Runtime.Start()` mengambil:

```go
r.mu.Lock()
defer r.mu.Unlock()
```

kemudian tetap memanggil:

```go
comp.Start(...)
```

Demikian juga `performStop()` memegang `r.mu` saat:

```go
comp.Stop(ctx)
```

Ini pattern yang sebaiknya dihindari pada orchestrator.

Karena `Component.Start/Stop()` adalah **foreign code dari sudut pandang Runtime**.

Jika suatu component secara langsung atau tidak langsung memanggil:

```go
runtime.Component(...)
runtime.Uptime()
```

ia membutuhkan `r.mu` lagi.

Potensi:

```text
Runtime.mu
   │
   └── component.Stop()
           │
           └── Runtime.Component()
                    │
                    └── Runtime.mu
                         DEADLOCK
```

`Health()` sudah diperbaiki dengan benar: snapshot components di bawah lock lalu lock dilepas sebelum `comp.Health()`.

Gunakan pattern **yang sama untuk Start dan Stop**.

---

# 4. Runtime membutuhkan operation serialization, bukan lock sepanjang operation

Solusinya bukan sekadar melepas `r.mu`.

Gunakan state sebagai reservation.

Konsep:

```go
r.mu.Lock()

if state != Created {
    ...
}

state = Starting
components := snapshot(...)

r.mu.Unlock()

// external operations
for ... {
    comp.Start(...)
}

r.mu.Lock()
state = Running
r.mu.Unlock()
```

Jadi mutex hanya melindungi:

```text
state
component registry
ownership metadata
operation generation
```

bukan durasi kerja component.

Ini jauh lebih aman.

---

# 5. Startup cancellation saat ini masih kurang kuat

`Runtime.Start(ctx)` mengecek:

```go
select {
case <-ctx.Done():
    ...
default:
}
```

**sebelum** memanggil component.

Tetapi component dipanggil menggunakan:

```go
comp.Start(r.rootCtx)
```

bukan `ctx`.

Jadi:

```text
ctx deadline 5s

component.Start(rootCtx)
      ↓
component hang 2 menit

ctx expired
      ↓
Runtime tidak tahu
```

Pemeriksaan `ctx.Done()` berikutnya tidak pernah dicapai.

Tetapi langsung mengganti menjadi:

```go
comp.Start(ctx)
```

juga bukan solusi yang bagus, karena startup context dan **lifetime context** berbeda semantik.

Saya lebih suka explicit:

```go
type StartContext struct {
    Lifetime context.Context
    Operation context.Context
}
```

atau Runtime dibuat dengan lifecycle parent:

```go
runtime.New(appCtx)
```

dan setiap Start mempunyai operation deadline terpisah.

Konsep:

```text
lifetimeCtx
   └── hidup selama Runtime

startCtx
   └── hanya untuk readiness/startup deadline
```

Komponen background hidup berdasarkan `lifetimeCtx`.

Prosedur startup dibatasi oleh `startCtx`.

---

# 6. Startup rollback cancellation path juga belum konsisten

Pada critical component failure, rollback memakai timeout 10 detik.

Tetapi ketika caller `ctx` cancelled:

```go
_ = r.graph.Rollback(context.Background(), started)
```

Itu berarti rollback bisa **tidak terbatas**.

Dan error-nya dibuang.

Padahal path cancellation biasanya justru salah satu kondisi sistem paling stressful.

Gunakan satu policy:

```go
rollbackCtx, cancel :=
    context.WithTimeout(context.Background(), rollbackTimeout)

rollbackErrs := rollback(...)
```

kemudian `errors.Join(startErr, rollbackErrs...)`.

---

# 7. Worker Manager punya potensi `RWMutex` self-deadlock

Ini cukup penting.

`Health()`:

```go
m.mu.RLock()
defer m.mu.RUnlock()

stats := m.AllStats()
```

Sedangkan `AllStats()`:

```go
m.mu.RLock()
defer m.mu.RUnlock()
```

Kelihatannya harmless karena sama-sama read lock.

Tetapi recursive `RWMutex.RLock()` bukan pattern aman apabila ada writer yang sudah waiting.

Skenario:

```text
G1: Health()
    RLock acquired

G2: AddPool()
    Lock waiting

G1: AllStats()
    RLock again
    ↓
    blocked behind pending writer

G2:
    menunggu first RLock dilepas

DEADLOCK
```

Fix sangat sederhana:

```go
func (m *Manager) Health(ctx context.Context) runtime.ComponentHealth {
    stats := m.AllStats()
    ...
}
```

Tidak perlu outer lock sama sekali.

Atau:

```go
allStatsLocked()
```

tetapi jangan recursive RLock.

---

# 8. Worker admission design sekarang sudah lebih baik, tetapi lifecycle-nya perlu diformalisasi

Current code sudah mempunyai:

```go
accepting
acceptingEnd
admissionCtx
admissionWG
```

dan reservation sebelum logical task masuk queue.

Ini desain yang bagus.

Flow sekarang kira-kira:

```text
Submit
  ↓
reserve physical admission
  ↓
register task
  ↓
WaitStart(owner quota)
  ↓
enqueue accepted
  ↓
worker execution
```

Ini jauh lebih baik dibanding worker thread menunggu quota.

Tetapi konsep `accepting` sebenarnya adalah **Quiesce state**.

Jangan biarkan hanya workers yang punya konsep tersebut.

Naikkan ke runtime lifecycle.

---

# 9. Task execution harus mempunyai panic boundary

Worker:

```go
taskErr := task.Execute(p.ctx)
```

tanpa `recover`.

Dalam Go, panic tak tertangani pada goroutine bukan sekadar membunuh worker; ia dapat menjatuhkan seluruh process.

Untuk sebuah extensible/plugin-heavy application, ini terlalu fragile.

Minimal:

```go
func executeSafely(...) (err error) {
    defer func() {
        if v := recover(); v != nil {
            err = fmt.Errorf("task panic: %v", v)
        }
    }()

    return task.Execute(ctx)
}
```

Lalu:

```text
panic
 ↓
task = Failed
 ↓
metrics increment
 ↓
worker tetap hidup
 ↓
process tetap hidup
```

Panic isolation sebaiknya ada di semua extension boundary:

```text
plugin callback
event handler
task
scheduler callback
addon callback
middleware
```

EventBus sudah memiliki sebagian proteksi ini.

---

# 10. Settings transactional outbox sekarang menghasilkan dua event

Ini adalah bug sinkronisasi/domain paling jelas yang saya temukan.

Repository `SetSetting()` melakukan transaction:

```text
UPDATE settings
INSERT setting_changes
INSERT setting_outbox
COMMIT
```

Semua atomic dalam satu transaction. Ini bagus.

Tetapi setelah repository sukses, `Service.Set()` melakukan:

```go
s.invalidate(...)
s.bus.Publish(&SettingChangedEvent{...})
```

Kemudian outbox worker mengambil row yang sama dan melakukan:

```go
s.bus.PublishDurable(ctx, evt)
```

Jadi flow aktual:

```text
DB transaction
 ├─ setting update
 └─ outbox insert
       ↓ commit

Service.Set
   ↓
Publish(event A)

500 ms kemudian

Outbox
   ↓
PublishDurable(event A lagi)
```

Untuk cache invalidation mungkin harmless.

Tetapi subscriber side-effect bisa:

```text
send notification ×2
schedule task ×2
write audit ×2
trigger addon ×2
```

`Reset()` juga mempunyai pola serupa, sementara repository deletion sendiri memasukkan outbox row.

### Pilihan yang saya rekomendasikan

Untuk SQLite:

**outbox adalah satu-satunya domain-event source.**

Setelah commit:

```go
s.invalidate(...)
return nil
```

Tidak ada direct bus publish.

Outbox kemudian bertanggung jawab untuk external event.

Jika membutuhkan latency hampir nol, jangan pakai ticker 500 ms; wake worker menggunakan channel notification:

```text
transaction commit
      ↓
nonblocking wake signal
      ↓
outbox worker langsung drain
```

Tetap durable, tetapi hampir realtime.

---

# 11. Outbox sebaiknya event-driven, bukan polling 500 ms

Sekarang:

```go
ticker := time.NewTicker(500 * time.Millisecond)
```

Padahal database writer berada di process yang sama.

Buat:

```go
outboxWake chan struct{}
```

setelah commit:

```go
select {
case outboxWake <- struct{}{}:
default:
}
```

Worker:

```text
select
 ├── ctx.Done
 ├── outboxWake
 └── fallback ticker 5–30s
```

Fallback ticker hanya untuk recovery missed wake/crash scenario.

Hasilnya:

```text
durable
atomic
low latency
tidak polling agresif
```

---

# 12. Outbox harus didefinisikan sebagai at-least-once

Current worker:

```go
PublishDurable(...)
MarkOutboxProcessed(...)
```

Jika:

```text
PublishDurable SUCCESS
       ↓
database temporarily fails
       ↓
MarkOutboxProcessed FAILED
       ↓
restart/tick
       ↓
publish lagi
```

duplicate memang tidak bisa dihindari tanpa distributed transaction.

Itu normal.

Jadi contract seharusnya eksplisit:

> Setting outbox = at-least-once delivery.

Untung outbox event sudah mempunyai deterministic ID:

```go
ID: fmt.Sprintf("outbox:setting:%d", e.ID)
```

Bagus.

Langkah berikutnya adalah subscriber yang melakukan external side effect harus punya **deduplication by event ID**.

---

# 13. `PublishDurable()` menjalankan subscriber sambil memegang EventBus `RLock`

Ini perlu diperbaiki.

Current:

```go
b.mu.RLock()
defer b.mu.RUnlock()

...

handler(ctx, event)
```

Artinya arbitrary subscriber code berjalan sambil EventBus read-lock masih aktif.

Misalnya handler mencoba:

```go
sub.Close()
```

`Subscription.Close()` membutuhkan:

```go
bus.mu.Lock()
```

Maka:

```text
PublishDurable
    RLock
       ↓
subscriber handler
       ↓
subscription.Close()
       ↓
Lock
       ↓
DEADLOCK
```

Fix:

```go
b.mu.RLock()
subscribers := snapshotSubscribers(...)
middlewares := snapshot(...)
b.mu.RUnlock()

for _, subscriber := range subscribers {
    execute(...)
}
```

Ini sangat penting untuk event-driven system.

**Jangan pernah menjalankan extension/user callback di bawah shared infrastructure lock.**

Prinsip ini juga berlaku untuk Runtime dan PluginManager.

---

# 14. EventBus shutdown tidak benar-benar menghormati shutdown context

`EventBus.Stop(ctx)` hanya:

```go
return b.Close()
```

sedangkan `Close()`:

```go
b.workers.Wait()
```

tanpa melihat `ctx`.

Jadi Runtime bisa memberikan deadline 30 detik, tetapi EventBus sendiri bisa tetap menunggu.

Memang handler mendapatkan timeout context default 5 detik.

Tetapi Go context bersifat cooperative.

Jika handler:

```go
func(ctx context.Context) {
    for {
        // ignores ctx
    }
}
```

timeout tidak bisa memaksanya berhenti.

Karena itu EventBus perlu:

```go
CloseContext(ctx)
```

dengan semantics jelas:

```text
stop admission
 ↓
drain until ctx deadline
 ↓
return timeout
```

Worker/goroutine yang bandel mungkin tetap leak sampai process termination, tetapi **global shutdown tidak boleh disandera**.

---

# 15. Plugin registration belum sepenuhnya transactional

Komentarnya mengatakan:

> no partial registration is visible on failure

Tetapi manifest dipasang lebih awal:

```go
m.manifests[name] = manifest
```

lalu:

```go
gate.RegisterManifest(manifest)
```

baru setelah itu plugin initialization/command registration dilakukan.

Jika kemudian:

```text
plugin.Init FAILED
```

manifest bisa sudah visible.

Capability gate juga bisa sudah memiliki manifest.

Jadi transaction boundary sebenarnya:

```text
manifest commit
gate commit
       ↓
plugin init
       ↓ may fail
router commit
manager commit
```

Ini bukan atomic.

Lebih baik gunakan staging:

```text
validate manifest
validate commands
create scope
initialize plugin
prepare capabilities
prepare hooks
       ↓
COMMIT
 ├─ gate
 ├─ router
 ├─ plugin map
 ├─ scope
 └─ metadata
```

Jika salah satu commit step bisa gagal, ada compensation reverse order.

---

# 16. Plugin `Enable()` membutuhkan per-plugin transition reservation

Sekarang `Enable()`:

```text
lock
check disabled
unlock

create scope
plugin.Init...

router.RegisterBatch

lock
delete disabled
store scope
unlock
```

Dua goroutine bisa:

```text
G1 Enable("x") ─┐
                ├─ keduanya melihat disabled=true
G2 Enable("x") ─┘

G1 plugin.Init()
G2 plugin.Init()
```

Plugin instance yang sama dapat di-initialize dua kali secara concurrent.

Salah satu akhirnya mungkin kalah pada router registration, tetapi damage dari double Init sudah terjadi.

Buat lifecycle state per plugin:

```text
Disabled
   ↓ CAS
Enabling
   ↓
Enabled

atau

Enabling
   ↓ error
Disabled/Failed
```

Jadi transition-nya mempunyai **reservation state**.

Hal yang sama lebih scalable untuk:

```text
Registering
Enabling
Enabled
Disabling
Disabled
Failed
```

---

# 17. Plugin disable sebaiknya membedakan logical admission dan physical shutdown

Current `Disable()` melakukan hal yang relatif benar:

```text
mark disabled
unregister commands
cancel jobs
cancel scheduler tasks
shutdown plugin
close scope
```

Ini bagus sebagai **fail-closed semantics**.

Tetapi state `disabled=true` dipasang sebelum shutdown benar-benar berhasil.

Jika shutdown gagal:

```text
manager says Disabled
resources mungkin masih hidup
```

Jadi model observability perlu:

```text
Enabled
 ↓
Disabling
 ↓ success
Disabled

Disabling
 ↓ teardown error
Degraded / FailedDisable
```

Bukan kembali Enabled; itu tidak aman.

Tetapi jangan menyebut state finalnya `Disabled` kalau teardown belum selesai.

---

# 18. Addon process lifetime masih tercampur dengan startup operation context

Addon menggunakan:

```go
cmd := exec.CommandContext(ctx, path)
```

Artinya process lifetime mengikuti context yang diberikan ke `ExternalRuntime.Start()`.

Padahal addon adalah long-lived runtime resource.

Ada dua context yang berbeda:

```text
start operation context
process lifetime context
```

Harus dipisah.

Ideal:

```go
func (r *ExternalRuntime) Start(
    startupCtx context.Context,
    lifetimeCtx context.Context,
) error
```

Process menggunakan:

```go
exec.CommandContext(lifetimeCtx, path)
```

handshake/readiness memakai:

```go
startupCtx
```

Sehingga timeout handshake tidak secara tidak sengaja mendefinisikan lifetime addon.

---

# 19. Process manager resource tracking sebaiknya transactional juga

`StartCmd()` register resource dahulu:

```go
resourceMgr.Register(...)
```

kemudian `cmd.Start()`.

Jika `Start()` gagal, registration direlease. Itu compensation yang benar.

Namun error dari:

```go
resourceMgr.Register(...)
```

dibuang.

Kalau ResourceManager adalah bagian dari correctness/ownership system, jangan best-effort.

Pilih salah satu contract:

```text
tracking mandatory
→ Register failure = process tidak boleh start
```

atau:

```text
tracking diagnostic only
→ explicit metric/log bahwa process untracked
```

Jangan ambiguous.

Untuk architecture yang ingin “anti error”, saya pilih **mandatory tracking untuk plugin/addon resources**.

---

# Target architecture yang saya sarankan

Bukan rewrite besar. Evolusi dari struktur sekarang:

```text
                     ┌─────────────────────────┐
                     │       App / CLI         │
                     │   composition only      │
                     └───────────┬─────────────┘
                                 │
                          Runtime.Orchestrator
                                 │
          ┌──────────────────────┼──────────────────────┐
          │                      │                      │
      Lifecycle DAG         State Registry         Error Ledger
          │
          │ Start forward
          │ Stop reverse
          │
          ├─ storage/database
          ├─ eventbus
          ├─ idempotency
          ├─ platform managers
          ├─ workers/tasks/jobs
          ├─ settings/outbox
          ├─ addons
          ├─ scheduler
          ├─ dispatcher
          ├─ plugins
          └─ telegram ingress

Shutdown:

Ingress
  ↓ QUIESCE

Scheduler / dispatcher / plugins
  ↓ QUIESCE

Workers / EventBus / outbox
  ↓ DRAIN

plugins / addons / services
  ↓ STOP

network / process / database
  ↓ CLOSE
```

Dan saya akan menerapkan prinsip berikut sebagai invariant sistem:

1. **Exactly one lifecycle owner.** `App` hanya construct + `Runtime.Start/Stop`.
2. **Never call foreign code while holding coordinator locks.**
3. **Every state transition has reservation state** seperti `Starting`, `Stopping`, `Enabling`, bukan check-unlock-do-lock.
4. **Durable state mutation + domain event memakai satu transactional outbox**, bukan outbox + direct publish.
5. **At-least-once delivery + idempotent consumer**, bukan berpura-pura exactly-once.
6. **Quiesce sebelum Drain, Drain sebelum Stop, Stop sebelum Close.**
7. **Startup operation context dipisah dari lifetime context.**
8. **Every extension boundary has panic recovery, timeout, ownership, dan error attribution.**
9. **Error tidak dibuang** pada migration, rollback, resource registration, outbox acknowledgment, dan cleanup penting.
10. **Health tidak boleh mengubah state dan tidak boleh mengambil recursive/coordinator locks.**

Kalau targetnya benar-benar **“sinkronisasi komponen/service mulus, atomic, predictable, dan tahan partial failure”**, prioritas terbesar saya bukan menambah mutex lagi. Justru **kurangi lock scope dan satukan ownership/lifecycle state machine**. Kode sekarang sudah memiliki hampir semua building block yang diperlukan; yang kurang adalah membuat semuanya tunduk pada **satu orchestration protocol yang sama**.
