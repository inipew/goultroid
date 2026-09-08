Ya. Saya akan jadikan ini **blueprint arsitektur target**, bukan sekadar pemindahan folder. Prinsip utamanya: **tidak rewrite Goultroid**, tidak merusak lifecycle/plugin manager yang sudah ada, dan `clone` menjadi pilot untuk membuktikan pola baru sebelum fitur lain dipindahkan.

Riset Go sendiri mendukung package berdasarkan cohesion, menghindari package “grab bag”, serta menempatkan server implementation di `internal/`; untuk dependency graph yang besar, compile-time wiring juga lebih mudah dilacak daripada service locator/runtime magic. ([Go][1]) Fowler juga menekankan bahwa monolith tetap bisa sangat modular bila boundary antar-modul dijaga, termasuk boundary terhadap persistent data. ([martinfowler.com][2])

# 1. Architecture Blueprint

## Target utama

Goultroid sebaiknya berubah dari:

```text
                 ┌───────────────┐
                 │ app/catalog   │
                 └───────┬───────┘
                         │
          ┌──────────────┼──────────────┐
          ↓              ↓              ↓
       plugin         database       services
          ↓              ↓
      clone/...       clone.go
      ocr/...         afk.go
      notes/...       notes.go
```

menjadi:

```text
                         ┌─────────────────────┐
                         │    Composition      │
                         │       Root          │
                         │ app/runtime         │
                         └──────────┬──────────┘
                                    │
                    register / wire modules
                                    │
        ┌───────────────────────────┼──────────────────────────┐
        ↓                           ↓                          ↓
┌───────────────┐           ┌───────────────┐          ┌───────────────┐
│   clone       │           │     notes     │          │      ocr      │
│   feature     │           │   feature     │          │    feature    │
├───────────────┤           ├───────────────┤          ├───────────────┤
│ commands      │           │ commands      │          │ commands      │
│ callbacks     │           │ service       │          │ service       │
│ service       │           │ repository    │          │ provider      │
│ repository    │           │ sqlite        │          │               │
│ sqlite        │           │ migration     │          │ migration     │
│ migration     │           │ tests         │          │ tests         │
│ tests         │           │               │          │               │
└───────┬───────┘           └───────┬───────┘          └───────┬───────┘
        │                           │                           │
        └───────────────────────────┼───────────────────────────┘
                                    ↓
                           ┌─────────────────┐
                           │    Platform     │
                           ├─────────────────┤
                           │ database        │
                           │ telegram       │
                           │ storage        │
                           │ logging        │
                           │ lifecycle      │
                           └─────────────────┘
```

**Feature menjadi unit arsitektur utama.**

Bukan:

> plugin + database + migration + callback + service

yang tersebar.

Tetapi:

> **clone adalah satu feature yang kebetulan mempunyai plugin, persistence, migration, callback, dan service.**

Ini inti perubahan.

---

# 2. Empat Boundary Utama

Target architecture memiliki empat boundary.

### Boundary A — Feature

```text
features/clone
features/ocr
features/notes
features/afk
...
```

Feature memiliki ownership atas behavior-nya.

### Boundary B — Platform

```text
platform/database
platform/telegram
platform/storage
platform/logging
platform/lifecycle
```

Platform tidak boleh mengetahui feature.

### Boundary C — Core/Application

```text
core/
application/
```

Berisi abstraction yang memang cross-feature.

### Boundary D — Composition Root

```text
app/
```

Satu-satunya tempat yang boleh mengetahui **semua feature concrete implementation**.

---

# 3. Target Directory Tree

Saya tidak menyarankan langsung mengubah seluruh repository ke struktur ini.

Ini adalah **target state**:

```text
goultroid/
│
├── cmd/
│   └── goultroid/
│       └── main.go
│
├── internal/
│
│   ├── app/
│   │   ├── app.go
│   │   ├── runtime.go
│   │   ├── bootstrap.go
│   │   └── generated_modules.go
│   │
│   ├── core/
│   │   ├── command/
│   │   ├── event/
│   │   ├── execution/
│   │   ├── peer/
│   │   └── router/
│   │
│   ├── application/
│   │   ├── capability/
│   │   ├── events/
│   │   └── contracts/
│   │
│   ├── platform/
│   │   ├── database/
│   │   │   ├── db.go
│   │   │   ├── tx.go
│   │   │   ├── migration.go
│   │   │   └── runner.go
│   │   │
│   │   ├── telegram/
│   │   ├── storage/
│   │   ├── logging/
│   │   └── lifecycle/
│   │
│   └── features/
│       │
│       ├── clone/
│       │   ├── module.go
│       │   ├── plugin.go
│       │   ├── service.go
│       │   ├── repository.go
│       │   ├── sqlite.go
│       │   ├── migration.go
│       │   ├── callbacks.go
│       │   └── clone_test.go
│       │
│       ├── ocr/
│       │   ├── module.go
│       │   ├── plugin.go
│       │   ├── service.go
│       │   └── ocr_test.go
│       │
│       ├── wikipedia/
│       ├── quote/
│       ├── afk/
│       ├── notes/
│       ├── filters/
│       ├── blacklist/
│       ├── pmpermit/
│       ├── userlog/
│       ├── broadcast/
│       ├── addon/
│       ├── settings/
│       └── scheduler/
│
├── migrations/
│   └── legacy/
│
├── scripts/
│
├── docs/
│
├── go.mod
└── go.sum
```

Namun ada satu aturan penting:

**jangan membuat semua feature menjadi 7–10 package kecil.**

Go sendiri memperingatkan dua ekstrem: package terlalu besar/grab-bag dan package terlalu banyak/split berlebihan. ([Go][1])

Jadi:

```text
clone/
    service.go
    repository.go
    sqlite.go
```

masih satu package:

```go
package clone
```

bukan:

```text
clone/service/
clone/repository/
clone/storage/
clone/plugin/
...
```

---

# 4. Dependency Rules

Ini bagian paling penting.

Folder saja **tidak cukup**.

Kita harus punya aturan dependency yang dapat diuji.

## Rule 1 — Feature boleh import platform

```text
features/clone
       │
       ├──→ core
       ├──→ application
       └──→ platform
```

Valid.

---

## Rule 2 — Platform TIDAK BOLEH import feature

Tidak boleh:

```text
platform/database
       ↓
features/clone
```

atau:

```go
package database

import "github.com/inipew/goultroid/internal/features/clone"
```

**Hard violation.**

Database harus tidak tahu bahwa `clone` bahkan ada.

---

# 5. Rule 3 — Feature tidak boleh import feature lain

Default:

```text
clone ─X→ notes
clone ─X→ afk
notes ─X→ clone
```

Kenapa?

Karena kalau:

```go
clone → notes → afk → settings → clone
```

akhirnya kita kembali ke distributed architecture.

Kalau clone membutuhkan capability dari feature lain:

```text
clone
  │
  ↓
application/capability
  │
  ↓
runtime/provider
```

atau event:

```text
clone
  │
  ↓
application/event
  │
  ↓
subscriber
```

---

# 6. Rule 4 — `app` boleh mengetahui semua feature

Ini memang pengecualian.

```text
app
 ├── clone
 ├── notes
 ├── ocr
 ├── afk
 └── ...
```

Karena `app` adalah **composition root**.

Ia memang bertugas menyatukan dependency graph.

---

# 7. Rule 5 — Feature tidak boleh mengakses DB secara langsung

Tidak:

```go
db.Query(...)
db.Exec(...)
```

dari command handler.

Yang benar:

```text
Plugin
  ↓
Service
  ↓
Repository interface
  ↓
SQLite implementation
  ↓
platform/database
```

Contoh:

```go
type CloneRepository interface {
    Get(ctx context.Context, ownerID int64) (*CloneState, error)
    Save(ctx context.Context, state CloneState) error
    Clear(ctx context.Context, ownerID int64) error
}
```

Interface ini **milik clone**.

Bukan:

```text
database.CloneRepository
```

---

# 8. Rule 6 — Repository implementation juga milik feature

```text
features/clone/repository.go
features/clone/sqlite.go
```

bukan:

```text
database/clone.go
```

Dengan begitu seluruh konsep clone berada dalam satu ownership boundary.

---

# 9. Rule 7 — Migration milik feature

Saat clone berubah schema:

```text
features/clone/migration.go
```

yang berubah adalah clone.

Tidak lagi:

```text
database/clone.go
database/clone_migration.go
database/migrations.go
app/catalog.go
```

untuk satu feature.

Itulah salah satu masalah utama yang sedang kita selesaikan.

---

# 10. Rule 8 — `platform/database` hanya Generic

Target:

```go
package database

type DB struct {
    sql *sql.DB
}

type Tx interface {
    ExecContext(...)
    QueryContext(...)
}

type Migration interface {
    ID() string
    Up(context.Context, *DB) error
}
```

Tidak boleh ada:

```go
type CloneState struct {...}
type AFKState struct {...}
type Note struct {...}
type Filter struct {...}
```

di sini.

Database infrastructure tidak boleh berubah ketika feature berubah.

---

# 11. Module Contract

Sekarang bagian inti.

Saya menyarankan **Module bukan sekadar Plugin**.

Plugin adalah salah satu bagian module.

```go
type Module interface {
    Manifest() Manifest
    Register(*Runtime) error
}
```

Contoh:

```go
type Manifest struct {
    ID          string
    Version     string
    Description string

    Dependencies []string
}
```

Kemudian:

```go
type Runtime struct {
    Config      *Config
    DB          *database.DB
    Telegram    Telegram
    Dispatcher  *Dispatcher
    EventBus    *EventBus
    Scheduler   *Scheduler
    Storage     *Storage
}
```

Namun jangan menjadikan `Runtime` sebagai:

```go
map[string]any
```

atau service locator.

Dependency harus tetap explicit.

Go's compile-time DI model memang menekankan dependency melalui function parameters/type graph dan menghindari runtime service locator. ([Go][3])

---

# 12. Bentuk Module Clone

Target konseptual:

```go
var Module = module.Define(
    module.Manifest{
        ID:          "clone",
        Version:     "1",
        Description: "Clone another Telegram user's profile",
    },
    Register,
)
```

Kemudian:

```go
func Register(rt *Runtime) error {
    repo := NewSQLiteRepository(rt.DB)

    service := NewService(
        repo,
        rt.Telegram,
        rt.Storage,
    )

    plugin := NewPlugin(service)

    return rt.Plugins.Register(plugin)
}
```

Perhatikan:

**semua wiring clone ada di clone.**

Bukan:

```text
app/catalog.go
   ↓
clone.New()
   ↓
database.CloneRepository
   ↓
...
```

---

# 13. Module Lifecycle

Saya ingin mempertahankan lifecycle Plugin Manager yang sekarang karena sebelumnya sudah cukup baik.

Module jangan menggantinya.

Layer-nya:

```text
Module
  ↓
Plugin Manager
  ↓
Plugin
  ↓
Command / Hook / Callback
```

Jadi:

```text
Module
```

adalah **feature composition boundary**.

Sedangkan:

```text
Plugin
```

adalah **Telegram runtime behavior**.

Ini penting supaya kita tidak menghancurkan abstraction yang sudah ada.

---

# 14. Migration Contract

Saya tidak menyarankan langsung membuang migration system existing.

Sebaliknya buat contract baru:

```go
type Migration interface {
    ID() string
    Description() string

    Up(context.Context, *DB) error
}
```

Jika membutuhkan rollback:

```go
type ReversibleMigration interface {
    Migration

    Down(context.Context, *DB) error
}
```

Tetapi **Down tidak wajib**.

Untuk production database, rollback SQL otomatis sering tidak aman.

---

# 15. Migration ID

Ini perubahan penting.

Sekarang:

```text
15
16
17
18
```

Target:

```text
clone.001
clone.002

ocr.001

notes.001
notes.002

afk.001
```

Kenapa?

Karena ownership menjadi jelas.

Misalnya clone menambah field:

```text
clone.001
clone.002
clone.003
```

Tidak perlu:

```text
database/migrations.go

version 31
version 32
version 33
```

yang menjadi global bottleneck.

---

# 16. Tetapi Existing Migration Jangan Diubah

Ini **hard rule**.

Migration:

```text
1
2
3
...
16
```

sudah pernah berjalan pada installation user.

Jangan rewrite.

Jangan renumber.

Jangan memindahkan history seolah-olah belum pernah dijalankan.

Kita perlakukan:

```text
legacy migration history
```

sebagai immutable historical record.

Target:

```text
legacy migrations
       │
       ↓
migration compatibility layer
       │
       ↓
feature migrations
```

---

# 17. Migration Registry

Module dapat expose:

```go
type MigrationProvider interface {
    Migrations() []Migration
}
```

Clone:

```go
func (Module) Migrations() []Migration {
    return []Migration{
        migration001{},
        migration002{},
    }
}
```

atau:

```go
func Migrations() []database.Migration {
    return []database.Migration{
        migration001{},
        migration002{},
    }
}
```

Kemudian application melakukan:

```text
discover modules
       ↓
collect migrations
       ↓
validate IDs
       ↓
sort deterministically
       ↓
run migration engine
```

---

# 18. Migration Ordering

ID harus deterministic.

Misalnya:

```text
afk.001
afk.002

clone.001
clone.002

notes.001
notes.002
```

Migration runner tidak boleh bergantung kepada:

* filesystem ordering
* Go map iteration
* `init()` order
* import order

Semua harus di-sort.

---

# 19. Migration Metadata

Migration table baru dapat menyimpan:

```text
id
description
checksum
applied_at
duration
```

Contoh:

```text
clone.001
clone.002
ocr.001
```

Checksum tetap penting.

Jika seseorang mengubah migration setelah production:

```text
clone.001
```

runner mendeteksi:

```text
checksum mismatch
```

dan **fail closed**.

Ini mempertahankan robustness migration engine existing.

---

# 20. Clone Pilot

Kenapa clone?

Karena clone sudah menjadi contoh sempurna dari masalah sekarang:

```text
plugins/clone
database/clone.go
database/clone_migration.go
database/migrations.go
app/catalog.go
```

Jadi kita bisa membuktikan apakah architecture baru benar-benar mengurangi blast radius.

Target:

```text
internal/features/clone/
```

menjadi:

```text
clone/
├── module.go
├── plugin.go
├── service.go
├── repository.go
├── sqlite.go
├── migration.go
├── callbacks.go
└── clone_test.go
```

---

# 21. Clone — `repository.go`

Domain ownership:

```go
type CloneState struct {
    OwnerID       int64
    OriginalFirst string
    OriginalLast  string
    OriginalBio   string
    OriginalPhoto string
    ClonedPhoto   bool
    Active        bool
    UpdatedAt     time.Time
}
```

dan:

```go
type Repository interface {
    Get(ctx context.Context, ownerID int64) (*CloneState, error)
    Save(ctx context.Context, state CloneState) error
    Clear(ctx context.Context, ownerID int64) error
}
```

Perhatikan perubahan:

```text
database.CloneState
```

menjadi:

```text
clone.CloneState
```

---

# 22. Clone — `sqlite.go`

Implementation:

```go
type SQLiteRepository struct {
    db *database.DB
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
    return &SQLiteRepository{db: db}
}
```

Semua SQL clone berada di sini.

Misalnya:

```go
func (r *SQLiteRepository) Get(...) ...
func (r *SQLiteRepository) Save(...) ...
func (r *SQLiteRepository) Clear(...) ...
```

Tidak ada SQL clone di:

```text
platform/database
```

---

# 23. Clone — `migration.go`

Migration existing:

```sql
CREATE TABLE IF NOT EXISTS clone_state (...)
```

dipindahkan ownership-nya menjadi:

```go
type migration001 struct{}

func (migration001) ID() string {
    return "clone.001"
}
```

dan migration berikutnya:

```go
type migration002 struct{}

func (migration002) ID() string {
    return "clone.002"
}
```

`clone.002` adalah equivalent dari existing:

```sql
ALTER TABLE clone_state
ADD COLUMN cloned_photo ...
```

**Bukan membuat table baru.**

---

# 24. Bagaimana Compatibility Clone Migration?

Ini bagian yang harus sangat hati-hati.

Jangan:

```text
old migration 15
↓
delete
↓
new clone.001
```

Karena installation existing sudah punya migration 15/16.

Kita butuh compatibility marker.

Contohnya:

```text
legacy 15 → clone.001 equivalent
legacy 16 → clone.002 equivalent
```

Migration framework harus bisa melakukan:

```text
legacy migration detected
       ↓
mark feature migration as adopted
       ↓
do NOT execute SQL again
```

Contoh metadata:

```text
clone.001 | adopted-from | legacy:15
clone.002 | adopted-from | legacy:16
```

Jadi database existing:

```text
migration 15
migration 16
```

tidak disentuh.

Tetapi database baru:

```text
clone.001
clone.002
```

langsung dijalankan.

---

# 25. Ini Sangat Penting: Dua Installation Path

Setelah migration refactor, harus ada dua test scenario.

### Fresh install

```text
empty DB
   ↓
legacy migrations
   ↓
feature migrations
   ↓
clone.001
clone.002
```

### Existing install

```text
DB already has legacy 1..16
   ↓
application upgrade
   ↓
adopt clone.001
adopt clone.002
   ↓
continue
```

Keduanya harus menghasilkan schema yang sama.

---

# 26. Clone Plugin

Existing plugin behavior harus **tidak berubah**.

Refactor hanya ownership.

Target:

```go
type Plugin struct {
    service *Service
}
```

Command handler:

```go
func (p *Plugin) Commands() []core.Command {
    ...
}
```

Handler tidak boleh melakukan SQL.

```text
Telegram
 ↓
Plugin
 ↓
Service
 ↓
Repository
 ↓
SQLite
```

---

# 27. Clone Service

Service memiliki business logic:

```go
type Service struct {
    repo    Repository
    telegram Telegram
    storage Storage
}
```

Misalnya:

```go
func (s *Service) StartClone(...)
func (s *Service) StopClone(...)
func (s *Service) Restore(...)
```

Service tidak tahu:

```text
app/catalog.go
database/migrations.go
```

---

# 28. Callback Ownership

Callback clone juga harus masuk:

```text
features/clone/callbacks.go
```

bukan:

```text
core/callback/clone.go
```

Core hanya menangani generic callback routing.

Contoh:

```text
callback router
      ↓
"clone:restore"
      ↓
clone callback handler
      ↓
clone service
```

Jadi generic infrastructure tetap generic.

---

# 29. Target Change Flow

Setelah architecture selesai:

### Sekarang

Menambah feature:

```text
1. plugins/foo
2. database/foo.go
3. database/migration
4. database/migrations.go
5. app/catalog.go
6. mungkin services
7. mungkin callback registry
8. tests
```

### Target

```text
features/foo/
├── module.go
├── plugin.go
├── service.go
├── repository.go       # jika persistent
├── sqlite.go           # jika persistent
├── migration.go        # jika persistent
└── foo_test.go
```

dan:

```text
go generate
```

meng-update registry.

**Idealnya developer hanya membuat satu directory.**

---

# 30. Generated Module Registry

Saya lebih memilih generated registry daripada `init()`.

Misalnya:

```text
internal/app/generated_modules.go
```

```go
// Code generated by featuregen. DO NOT EDIT.

var builtinModules = []module.Module{
    afk.Module,
    addon.Module,
    clone.Module,
    filters.Module,
    notes.Module,
    ocr.Module,
    quote.Module,
    wikipedia.Module,
}
```

Generator membaca:

```text
internal/features/*/module.go
```

dan menghasilkan registry.

---

# 31. Kenapa Tidak `init()`?

Bisa saja:

```go
func init() {
    registry.Register(clone.Module)
}
```

tetapi saya **tidak merekomendasikannya** untuk core architecture.

Masalah:

```text
import side effect
        ↓
global mutable registry
        ↓
hidden registration
        ↓
hidden initialization order
```

Generated registry lebih eksplisit.

Go juga menekankan pentingnya package boundaries dan menghindari package/API yang tidak jelas ownership-nya. ([Go][1])

---

# 32. Dependency Graph Target

Secara formal:

```text
                       cmd
                        │
                        ▼
                       app
                        │
             ┌──────────┼───────────┐
             ▼          ▼           ▼
         features   application   core
             │          │           │
             └────┬─────┴─────┬─────┘
                  ▼           ▼
               platform    infrastructure
```

Dependency yang dilarang:

```text
platform → feature       ❌
feature → feature        ❌
core → feature           ❌
application → feature    ❌
feature → cmd            ❌
feature → app            ❌
```

Yang diperbolehkan:

```text
app → feature            ✅
app → platform           ✅
app → core               ✅

feature → core           ✅
feature → application    ✅
feature → platform       ✅

application → core       ✅
platform → core          ✅
```

Tetapi bahkan `feature → platform` sebaiknya dibatasi ke abstraction yang memang diperlukan.

---

# 33. Architecture Enforcement

Jangan hanya menulis aturan di dokumentasi.

Kita harus membuat **architecture test**.

Misalnya:

```text
internal/architecture/
    imports_test.go
    features_test.go
```

Test akan membaca Go package/import graph.

Contoh assertion:

```text
database must not import features/*
```

```text
features/foo must not import features/bar
```

```text
feature must not import app
```

```text
platform must not import feature
```

Kalau developer melanggar:

```text
go test ./...
```

langsung gagal.

Ini jauh lebih kuat daripada convention.

Fowler juga menyoroti bahwa boundary modular dalam monolith mudah ditembus bila tidak ada disiplin/enforcement. ([martinfowler.com][2])

---

# 34. Migration Plan Keseluruhan

Saya membaginya menjadi **8 phase**.

## Phase 0 — Freeze

Jangan pindahkan feature lain dulu.

Tetapkan:

```text
new features → feature-oriented architecture
existing features → legacy
```

Tidak ada perubahan behavior.

---

## Phase 1 — Introduce Contracts

Tambahkan:

```text
Module
Manifest
Runtime
Migration
MigrationRunner
```

tanpa memindahkan feature.

Goal:

```text
architecture infrastructure exists
```

---

## Phase 2 — Clone Pilot

Migrasikan:

```text
plugins/clone
database/clone.go
database/clone_migration.go
```

menjadi:

```text
features/clone
```

**Tanpa perubahan behavior.**

---

## Phase 3 — Migration Compatibility

Implement:

```text
legacy migration adoption
```

dan test:

```text
fresh DB
existing DB
upgrade DB
```

Ini gate penting.

---

## Phase 4 — Module Registration

Clone tidak lagi manual:

```text
app/catalog.go
```

tetapi:

```text
clone.Module
```

di generated registry.

---

## Phase 5 — Architecture Enforcement

Tambahkan:

```text
dependency tests
module validation
migration validation
```

Baru architecture mulai “terkunci”.

---

# 35. Phase 6 — Migrate Simple Features

Urutan yang saya sarankan:

```text
quote
wikipedia
ocr
ping/alive/simple utility
```

Karena risiko rendah.

Kemudian:

```text
addon
broadcast
```

---

# 36. Phase 7 — Persistent Features

Setelah pattern terbukti:

```text
notes
afk
filters
blacklist
sudo
userlog
pmpermit
settings
```

Baru migrasikan yang memiliki state kompleks.

---

# 37. Phase 8 — Scheduler / Complex Infrastructure

Terakhir:

```text
scheduler
voice
peer/entity persistence
```

Karena fitur-fitur ini kemungkinan mempunyai coupling lebih tinggi dan tidak cocok dijadikan pilot awal.

---

# 38. Definition of Done Clone

Clone **belum dianggap selesai** hanya karena folder sudah pindah.

Gate-nya:

### Architecture

* [ ] `clone` tidak lagi berada di `database`
* [ ] `database` tidak mengenal clone
* [ ] repository owned by clone
* [ ] SQL owned by clone
* [ ] migration owned by clone
* [ ] plugin owned by clone
* [ ] callback owned by clone
* [ ] service owned by clone

### Runtime

* [ ] command tetap bekerja
* [ ] callback tetap bekerja
* [ ] clone start tetap bekerja
* [ ] clone stop tetap bekerja
* [ ] restore tetap bekerja
* [ ] photo handling tetap bekerja
* [ ] restart tetap aman

### Database

* [ ] existing DB tetap kompatibel
* [ ] fresh DB berhasil
* [ ] upgrade DB berhasil
* [ ] checksum tetap divalidasi
* [ ] migration id deterministic
* [ ] migration tidak double-execute

### Dependency

* [ ] clone tidak import feature lain
* [ ] platform tidak import clone
* [ ] database tidak import clone
* [ ] clone tidak import app

### Tests

* [ ] repository tests
* [ ] service tests
* [ ] migration tests
* [ ] module registration tests
* [ ] plugin integration tests
* [ ] architecture dependency tests

---

# 39. KPI yang Kita Inginkan

Ini ukuran keberhasilan yang lebih bagus daripada sekadar “kode lebih rapi”.

### Sebelum

Satu feature:

```text
5–8 locations touched
```

### Target

```text
1 feature directory
+
0 manual global registry changes
```

Untuk feature tanpa persistence:

```text
features/foo/*
```

Untuk feature dengan persistence:

```text
features/foo/*
```

tetap sama.

Perbedaannya hanya isi directory.

---

# 40. Architecture Target Final

Dengan demikian, Goultroid menjadi:

```text
                    GOULTROID
                       │
              ┌────────┴────────┐
              │                 │
         Composition        Runtime
             Root               │
              │                 │
       ┌──────┴──────┐          │
       │             │          │
    Modules       Platform      │
       │             │          │
 ┌─────┼─────┐       │          │
 │     │     │       │          │
clone notes  ocr     DB       Telegram
 │     │     │        │          │
 └─────┴─────┴────────┴──────────┘
              │
          Application
              │
        Core Contracts
```

Dan ownership-nya:

```text
                 OWNERSHIP

clone ────────────────► clone behavior
clone ────────────────► clone state
clone ────────────────► clone SQL
clone ────────────────► clone migrations
clone ────────────────► clone callbacks
clone ────────────────► clone tests

platform/database ────► database mechanics
platform/telegram ────► Telegram mechanics
core ─────────────────► generic application mechanics
app ──────────────────► composition/wiring
```

**Tidak ada lagi `database` yang menjadi gudang semua feature.**

---

## Kesimpulan

Saya akan memilih arsitektur **Feature-Oriented Modular Monolith**, bukan microservices, bukan Go native dynamic plugins, dan bukan Clean Architecture yang memecah setiap feature menjadi banyak layer/package.

Alasannya sangat cocok dengan kondisi Goultroid saat ini:

1. **Core yang sudah bagus tetap dipertahankan.**
2. **Feature ownership dipindahkan ke satu boundary.**
3. **Persistence mengikuti feature.**
4. **Migration mengikuti feature.**
5. **Composition root tetap explicit.**
6. **Module registration dapat digenerate.**
7. **Dependency graph dapat diuji.**
8. **Migration lama tidak di-rewrite.**
9. **Clone menjadi pilot yang kecil tetapi mencakup seluruh problem.**
10. Setelah clone berhasil, pola tersebut menjadi template untuk seluruh feature.

Secara arsitektur, target akhirnya bukan sekadar:

> “menambahkan plugin lebih mudah.”

Tetapi:

> **blast radius perubahan sebuah feature menjadi lokal, dependency graph tetap statically understandable, persistence tidak bocor antar-domain, dan architecture rule dapat ditegakkan otomatis.**

Itu jauh lebih penting untuk Goultroid jangka panjang. Prinsip ini juga sejalan dengan pendekatan modular monolith: boundary yang baik membuat perubahan dapat terkonsentrasi pada domain yang relevan, sementara Go sendiri mendorong package cohesion dan explicit dependency construction. ([martinfowler.com][4])

**Urutan implementasi yang saya sarankan:** `Module contract → Migration contract/compatibility layer → Clone pilot → generated registry → architecture tests → baru migrasi feature lain.` Jangan mulai dengan memindahkan seluruh plugin sekaligus; itu justru menciptakan rework besar.

[1]: https://go.dev/blog/organizing-go-code?utm_source=chatgpt.com "Organizing Go code - The Go Programming Language"
[2]: https://martinfowler.com/articles/microservice-trade-offs.html?utm_source=chatgpt.com "Microservice Trade-Offs"
[3]: https://go.dev/blog/wire?utm_source=chatgpt.com "Compile-time Dependency Injection With Go Cloud's Wire - The Go Programming Language"
[4]: https://martinfowler.com/articles/linking-modular-arch.html?utm_source=chatgpt.com "Linking Modular Architecture to Development Teams"
