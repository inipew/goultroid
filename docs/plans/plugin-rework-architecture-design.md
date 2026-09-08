# Goultroid Plugin Rework — Architectural Design & Specification

## 1. Executive Summary & Context

Dokumen ini merupakan spesifikasi teknis dan desain arsitektur resmi untuk merealisasikan blueprint [rework-plugin.md](file:///home/dhimas/any/random/ultroid-go/docs/plans/rework-plugin.md). Desain ini mengadopsi pola **Feature-Oriented Modular Monolith (Vertical Slice Architecture)** tanpa mengubah Goultroid menjadi microservices, tanpa Go dynamic plugins (`plugin.Open`), dan tanpa over-abstraction (Clean Architecture yang memecah tiap modul menjadi 8 package terpisah).

### Permasalahan Arsitektur Eksisting (Layered Architecture Blast Radius)
Sebelum rework, penambahan atau perubahan sebuah fitur (seperti `clone`, `afk`, atau `notes`) menimbulkan efek *shotgun surgery*:
1. Perubahan logika command di `plugins/<name>/`.
2. Model data dan method query SQL di `internal/database/<name>.go`.
3. Migrasi skema di-append via `init()` di `internal/database/<name>_migration.go`.
4. Versi integer global di-manage di `internal/database/migrations.go`.
5. Wiring manual di `internal/app/catalog.go`.
6. Callback registration di `internal/app/catalog.go` atau `internal/services/callback/`.
7. `internal/database` menjadi *God Package* yang memuat domain seluruh fitur.

### Prinsip Utama Target Arsitektur
1. **Feature Ownership Boundary**: Sebuah feature (misal `clone`, `notes`, `afk`) adalah unit kepemilikan utama (behavior, persistensi, query SQL, migrasi, dan testing dimiliki oleh package fitur tersebut).
2. **Generic Platform**: `internal/database` hanya mengelola koneksi DB, transaksi, dan runner migrasi generic. Tidak boleh ada tipe domain (`CloneState`, `Note`, `AFKState`) di dalam paket platform.
3. **Compile-time Static Discovery**: Registrasi modul dibangkitkan pada tahap compile-time menggunakan AST generator (`tools/featuregen`), menghasilkan `internal/app/generated_modules.go`. Menghindari `init()` side-effects dan runtime reflection.
4. **Two-Tier Migration Compatibility**: Migrasi integer lama (1..16) adalah immutable history. Migrasi baru menggunakan namespaced IDs (`clone.001`, `notes.001`) yang dapat mengadopsi versi legacy tanpa duplikasi eksekusi SQL.
5. **Right-Sized Feature Slices**: Fitur stateless (misal `ping`, `quote`) tidak dipaksa memiliki repository/migration kosong. Setiap fitur hanya mengimplementasikan layer yang benar-benar dibutuhkan.

---

## 2. 4-Tier Boundary & Dependency Invariants

Arsitektur target Goultroid dibagi menjadi 4 boundary tegas:

```
┌────────────────────────────────────────────────────────────────────────┐
│                      Tier 1: COMPOSITION ROOT                          │
│                     cmd/ & internal/app/                               │
│  - Merakit konfigurasi, logger, database connection, telegram client   │
│  - Mengimpor seluruh fitur concrete melalui generated_modules.go       │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │ (wires runtime & registers)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                      Tier 2: FEATURE MODULES                           │
│                     plugins/<feature>/                                 │
│  - Kepemilikan domain: Manifest, Commands, Services, Persistence, Test │
│  - Tidak boleh saling mengimpor antar fitur secara langsung            │
└───────────────────┬────────────────────────────────┬───────────────────┘
                    │                                │
                    ▼                                ▼
┌───────────────────────────────────────┐  ┌─────────────────────────────┐
│    Tier 3: APPLICATION & DOMAIN       │  │ Tier 4: PLATFORM & CORE     │
│  internal/core/, internal/application │  │  internal/database/ (pure)  │
│  internal/domain/, internal/services/ │  │  internal/telegram/ (client)│
│  - EventBus, Router, Permissions      │  │  internal/settings/ (engine)│
│  - Domain contracts, Shared Events    │  │  internal/scheduler/ (engine│
└───────────────────────────────────────┘  └─────────────────────────────┘
```

### Dependency Rules Matrix

| Source \ Target | `internal/app` | `plugins/<foo>` | `plugins/<bar>` | `internal/core` | `internal/database` | `internal/services` |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| **`internal/app`** | N/A | ALLOWED | ALLOWED | ALLOWED | ALLOWED | ALLOWED |
| **`plugins/<foo>`** | **FORBIDDEN** | N/A | **FORBIDDEN** | ALLOWED | ALLOWED (via SQLExecutor) | ALLOWED (read-only) |
| **`internal/core`** | **FORBIDDEN** | **FORBIDDEN** | **FORBIDDEN** | N/A | **FORBIDDEN** | **FORBIDDEN** |
| **`internal/database`**| **FORBIDDEN** | **FORBIDDEN** | **FORBIDDEN** | ALLOWED | N/A | **FORBIDDEN** |
| **`internal/services`**| **FORBIDDEN** | **FORBIDDEN** | **FORBIDDEN** | ALLOWED | ALLOWED | N/A |

Aturan ini diverifikasi secara otomatis dalam CI menggunakan AST parser di `internal/architecture/imports_test.go`.

---

## 3. Kontrak & Tipe Data

### 3.1. Module Manifest & Interface (`internal/module/module.go`)

```go
package module

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
)

type Manifest struct {
	ID           string   // Identifier unik, misal "clone", "afk", "ping"
	Version      string   // Semantic versioning, misal "1.0.0"
	Description  string   // Deskripsi singkat fungsionalitas
	Dependencies []string // Opsional: dependensi ID modul lain
}

// Runtime menyediakan dependensi eksplisit bagi modul saat bootstrap.
type Runtime struct {
	DB              *database.DB
	OwnerID         int64
	Permissions     *core.Permissions
	Plugins         *plugin.Manager
	Router          *core.Router
	EventBus        *core.EventBus
	Metrics         core.MetricsCollector
	Logger          *zap.Logger
	StartTime       time.Time
	TelegramService func() core.TelegramServicer
	Resolver        core.PeerResolver
	Callbacks       *callback.Router
	CallbackStore   *callback.StateStore
	Storage         storage.Storage
	SettingsService *settings.Service
}

type Module interface {
	Manifest() Manifest
	Register(context.Context, *Runtime) error
}
```

### 3.2. Database & Feature Migration Contract (`internal/database/`)

```go
package database

import "context"

type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type FeatureMigration interface {
	ID() string               // Format: "<feature>.<sequence_3_digits>", misal "notes.001"
	Description() string      // Human-readable summary
	Checksum() string         // SHA-256 hash representasi SQL atau definisi migrasi
	LegacyVersions() []int    // Versi migrasi integer legacy yang ekuivalen (jika ada)
	Up(context.Context, SQLExecutor) error
}

type MigrationProvider interface {
	Migrations() []Migration
}
```

---

## 4. Tipe-Tipe Fitur & Pola Implementasi (Taxonomy of Modules)

Modul diklasifikasikan ke dalam 3 pola implementasi:

### Pola A: Pure Stateless Feature (Contoh: `ping`, `quote`, `wikipedia`, `pin`, `alive`)
Hanya memerlukan 1-2 file:
- `module.go`: Mendefinisikan `ModuleType`, `Manifest()`, dan `Register()`.
- `<feature>.go`: Mengimplementasikan `plugin.Plugin` dan handler Telegram.

### Pola B: Self-Contained Persistent Feature (Contoh: `clone`, `notes`, `afk`, `filters`, `blacklist`, `sudo`)
Memiliki persistensi mandiri:
- `module.go`: Implementasi `module.Module` + `database.MigrationProvider`.
- `<feature>.go`: Plugin, command registration, Telegram request parsing.
- `repository.go`: Definisi interface repository domain dan struct model data lokal.
- `sqlite.go`: Implementasi query SQL dengan `*database.DB` atau `SQLExecutor`.
- `migration.go`: Slice `FeatureMigration` bertipe `<feature>.001`.
- `<feature>_test.go`: Repository tests + migration adoption tests.

### Pola C: Domain Subsystem Presentation (Contoh: `pmpermit`, `media`, `broadcast`, `userlog`, `admin`)
- Inti logika bisnis sudah berada di `internal/services/<name>`.
- `plugins/<name>` bertindak sebagai command & callback presentation layer.
- `module.go` menghubungkan service yang ada di `Runtime` ke instance plugin dan mendaftarkannya ke `plugin.Manager`.

---

## 5. Mekanisme Kompilasi & Discovery (`tools/featuregen`)

1. Setiap fitur yang siap dimigrasi mengekspor package-level variable:
   ```go
   var Module ModuleType
   ```
2. Tool `tools/featuregen/main.go` memindai seluruh direktori `plugins/*/module.go` secara statis tanpa me-load binary (menggunakan `go/parser` dan AST).
3. Tool men-generate `internal/app/generated_modules.go`:
   ```go
   // Code generated by featuregen. DO NOT EDIT.
   package app
   ...
   var builtinModules = []module.Module{
       afk.Module,
       clone.Module,
       notes.Module,
       ping.Module,
       ...
   }
   ```
4. Menggunakan directive `//go:generate go run ../../tools/featuregen` di `internal/app/modules.go`.
5. Transisi menerapkan **Strangler Fig Pattern**: modul yang belum memiliki `module.go` tetap berjalan via legacy catalog di `internal/app/catalog.go`.
