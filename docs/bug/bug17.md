# Bug 17 — Plugin Rework Architecture Audit: Gaps, Bugs, and Required Hardening

**Status:** OPEN — static audit against `main` commit `4a7c362c26645830d27d576094375341bf173544` (`rework plugin`)

**Scope:** seluruh arsitektur plugin/module, feature ownership, database/migration boundary, module discovery/generation, dependency enforcement, bootstrap/wiring, style/design/pattern, dan kesesuaian terhadap:

- `docs/plans/rework-plugin.md`
- `docs/plans/plugin-rework-architecture-design.md`
- `docs/plans/plugin-rework-tasklist.md`

**Audit mode:** source/static audit melalui repository GitHub. Tidak mengklaim `go test ./...`, race test, atau production runtime test berhasil karena workflow untuk commit audit belum memberikan workflow result yang dapat diverifikasi.

---

## 1. Executive Summary

Rework yang sekarang **sudah menyelesaikan sebagian besar problem shotgun-surgery pada registration dan lima persistent feature** (`clone`, `notes`, `afk`, `filters`, `blacklist`, `sudo`) dengan `module.go`, feature-owned repository/SQLite/migration, serta generated module registry.

Namun implementasi saat ini **belum memenuhi architecture target secara penuh** dan ada beberapa masalah yang harus dianggap blocking sebelum arsitektur dinyatakan production-grade.

Masalah terpenting:

1. **`internal/module.Runtime` sudah berubah menjadi service-locator/runtime-bag raksasa.** Ini memindahkan sebagian coupling dari `catalog.go` ke satu struct global. Penambahan capability baru tetap mengubah central runtime + app wiring.
2. **Legacy migration history tidak lagi dipertahankan secara immutable di source.** Current `internal/database/migrations.go` berhenti di version 14, sementara feature migration clone masih mengadopsi legacy 15/16. Installation yang pernah memiliki 15/16 sekarang tidak mempunyai definisi historical migration tersebut di source. Ini bertentangan dengan hard rule dokumen dan dapat menyembunyikan checksum/history inconsistency.
3. **Legacy migration checksum validation hanya efektif untuk migration yang masih ada di `migrations`.** Applied versions yang sudah tidak didefinisikan tidak divalidasi sebagai unknown/orphan versions.
4. **Feature migration adoption terlalu percaya pada integer legacy marker.** Jika `schema_migrations.version = 15` ada, `clone.001` langsung di-adopt tanpa memverifikasi checksum legacy atau schema yang sebenarnya sudah sesuai. Ini membuat migration safety lebih lemah daripada yang dijanjikan.
5. **Test migration belum benar-benar menguji jalur aplikasi Fresh Install → legacy migrations → feature migrations.** Test clone membuat `schema_migrations` dan marker 15/16 secara manual dengan checksum `legacy`, sehingga tidak membuktikan compatibility dengan historical migration implementation.
6. **Architecture test terlalu sempit.** Ia hanya memeriksa sebagian boundary (`database/core ↛ plugins`, `plugins ↛ app`, `plugins ↛ plugin lain`). Ia belum menegakkan seluruh dependency matrix dari desain.
7. **`module.Manifest.Dependencies` belum dipakai oleh bootstrap.** Tidak ada dependency graph resolution, cycle detection, missing dependency detection, atau deterministic topological registration. Field tersebut saat ini hanya metadata.
8. **Feature generator belum memvalidasi semantic contract.** Generator hanya mencari package-level variable bernama `Module`; AST tidak memastikan tipe tersebut benar-benar implement `module.Module` atau manifest valid. Compile mungkin menangkap sebagian error, tetapi generator sendiri tidak memberikan diagnostic arsitektural.
9. **CI tidak memverifikasi generated registry up-to-date.** Developer dapat menambah `module.go` tetapi lupa menjalankan `go generate`; `go test`/`go build` tidak otomatis memastikan generated file identik dengan source discovery.
10. **Final architecture masih mencampur feature-oriented plugin layer dengan legacy domain repositories di `internal/database`.** `settings`, `scheduler`, `peer`, `moderation`, `pmpermit`, `voice`, addon dan subsystem lain masih membuat `internal/database` bukan platform DB yang benar-benar generic.

Kesimpulan: **arah rework benar, tetapi DoD Phase 5 belum tercapai secara arsitektural.** Status yang tepat adalah `implemented / incomplete / hardening required`, bukan `fully completed`.

---

# 2. P0 — Migration History Integrity Regression

## 2.1 Problem

Target architecture secara eksplisit menyatakan migration legacy adalah immutable historical record. Namun source saat ini mendefinisikan legacy migrations hanya sampai version 14 di `internal/database/migrations.go`, sedangkan `plugins/clone/migration.go` mendeklarasikan:

```text
clone.001 -> LegacyVersions [15]
clone.002 -> LegacyVersions [16]
```

Artinya migration 15/16 tidak lagi merupakan bagian dari canonical legacy migration registry.

Ini sangat berbahaya karena migration version adalah historical contract. Menghapus definisinya bukan refactor biasa.

## 2.2 Dampak

Database yang pernah menjalankan migration 15/16 dapat memiliki rows:

```text
schema_migrations
15
16
```

tetapi binary baru tidak lagi mempunyai definition canonical untuk version tersebut.

Current migration runner memuat applied versions dari database, tetapi iterasi validasi checksum hanya berjalan terhadap migration yang ada di `migrations`. Dengan demikian version historical yang hilang dari source dapat menjadi **orphan applied migration** tanpa error eksplisit.

Lebih buruk, feature migration kemudian menggunakan keberadaan version 15/16 sebagai dasar adoption.

## 2.3 Kenapa ini berbeda dengan migration adoption

Migration adoption seharusnya berarti:

```text
legacy migration X
        │
        ├── canonical definition masih diketahui
        ├── checksum historical diketahui
        ├── database marker cocok
        └── schema equivalent terbukti
                ↓
        feature migration adopted
```

Current implementation secara efektif memungkinkan:

```text
version 15 exists
        ↓
anggap clone.001 sudah dilakukan
```

tanpa canonical definition version 15 di current source.

## 2.4 Fix wajib

Jangan menghapus historical migration 15/16.

Pertahankan migration definition immutable, misalnya:

```text
legacy 1..16
```

Kemudian tandai 15/16 sebagai historical/owned-by-clone jika perlu, tetapi tetap tersedia untuk checksum validation dan audit.

Setelah itu:

```text
legacy 15 -> clone.001
legacy 16 -> clone.002
```

harus menjadi explicit compatibility mapping.

**Jangan mengandalkan hilangnya migration definition sebagai cara menghilangkan checksum mismatch.** Itu hanya menghilangkan pemeriksaan.

---

# 3. P0 — Migration Adoption Trusts Marker Without Schema/Checksum Proof

`internal/database/feature_migrations.go` memakai:

```go
anyLegacyVersionApplied(ctx, db, migration.LegacyVersions())
```

Jika ditemukan version legacy, feature migration langsung dicatat sebagai adopted.

Tidak ada verifikasi:

- checksum legacy yang sesuai;
- migration description/identity;
- schema fingerprint;
- required table/column/index;
- compatibility state;
- apakah marker tersebut benar-benar berasal dari equivalent migration.

## Dampak

Database corruption atau manual/incomplete migration marker dapat menyebabkan:

```text
feature migration tidak dijalankan
```

meskipun schema belum benar.

Ini adalah false-positive migration success.

## Fix

Buat compatibility record eksplisit:

```go
type LegacyAdoption struct {
    LegacyVersion int
    LegacyChecksum string
    SchemaCheck func(context.Context, SQLExecutor) error
}
```

Adoption harus:

1. memastikan legacy version diketahui;
2. memvalidasi checksum historical;
3. menjalankan schema invariant check;
4. hanya kemudian membuat marker feature migration.

Untuk clone misalnya invariant minimal:

```text
clone_state exists
owner_id PK exists
original_first_name exists
original_last_name exists
original_bio exists
original_photo_path exists
active exists
updated_at exists
```

untuk `clone.002`:

```text
cloned_photo exists
```

---

# 4. P0 — Migration Tests Tidak Merepresentasikan Real Upgrade Path

`plugins/clone/migration_test.go` menguji legacy adoption dengan membuat sendiri:

```sql
CREATE TABLE clone_state (...);
INSERT INTO schema_migrations(version, ...) VALUES (15, ...), (16, ...)
```

Checksum yang dimasukkan adalah string `legacy`, bukan checksum canonical migration 15/16.

Test tetap sukses karena current runner hanya mengecek keberadaan version saat adoption.

Ini justru membuktikan bug P0: **test akan tetap hijau walaupun historical checksum salah.**

## Fix

Minimal test matrix:

### A. Fresh installation

```text
empty DB
→ legacy migrations 1..16
→ feature migrations
→ final schema assertion
```

### B. Existing legacy installation

```text
DB migrated through legacy 1..16
→ current binary
→ feature adoption
→ final schema assertion
```

### C. Tampered legacy checksum

```text
legacy version 15
wrong checksum
→ startup MUST FAIL
```

### D. Missing required schema

```text
legacy version 15
clone_state incomplete
→ adoption MUST FAIL
```

### E. Already adopted

```text
clone.001/002 already present
→ rerun
→ no duplicate execution
```

### F. Feature checksum tampering

```text
feature_schema_migrations clone.001 checksum != binary
→ startup MUST FAIL
```

---

# 5. P1 — `module.Runtime` Is a Service Locator in Disguise

Current `internal/module/module.go` memiliki struct dengan banyak dependency:

```text
DB
OwnerID
Permissions
Plugins
Router
EventBus
Metrics
Logger
StartTime
TelegramService
Resolver
Callbacks
CallbackStore
Storage
DownloadRegistry
MediaService
ModService
PMPermitService
BroadcastService
UserlogService
AddonManager
SettingsService
SchedEngine
```

Ini memang explicit field access, tetapi secara design sudah menjadi **runtime dependency bag**.

Masalahnya sama secara struktural dengan service locator:

```text
module → Runtime → semua service
```

bukan:

```text
module → dependency yang benar-benar dibutuhkan
```

## Dampak

Penambahan subsystem baru kembali membutuhkan perubahan global:

```text
internal/module/module.go
internal/app/app.go
internal/app/wiring_*.go
```

Ini bertentangan dengan tujuan utama rework: perubahan feature seharusnya local.

## Fix

Pertahankan `Runtime` hanya untuk dependency yang benar-benar universal, misalnya:

```text
Context
Config
DB/SQL capability
Plugin registration
Logger
Core Telegram capability
```

Untuk feature yang memerlukan subsystem khusus, gunakan constructor/provider yang narrow.

Contoh:

```go
func NewModule(deps Dependencies) ModuleType

type Dependencies struct {
    Telegram core.TelegramServicer
    Storage storage.Storage
}
```

Lebih baik lagi, gunakan provider function pada composition root sehingga dependency graph tetap statically visible.

Go sendiri merekomendasikan explicit dependency construction melalui constructor parameters; compile-time generation dapat membantu pada graph besar tanpa runtime service locator. citeturn0search0turn0search9

---

# 6. P1 — Manifest Dependencies Belum Functional

`module.Manifest` memiliki:

```go
Dependencies []string
```

tetapi `registerBuiltinModules()` hanya:

```text
validate
→ Register
→ next
```

Tidak ada:

- duplicate module ID detection;
- missing dependency detection;
- dependency ordering;
- cycle detection;
- optional dependency semantics;
- deterministic topological sort.

## Dampak

Field `Dependencies` memberikan kesan ada dependency graph, padahal runtime belum menggunakannya.

## Fix

Sebelum registration:

```text
collect manifests
→ validate IDs
→ reject duplicates
→ validate dependency existence
→ topological sort
→ detect cycle
→ register in deterministic order
```

Jika dependency system belum dibutuhkan, hapus field tersebut sementara daripada memiliki contract palsu.

---

# 7. P1 — Architecture Test Belum Menegakkan Architecture Design

`internal/architecture/imports_test.go` saat ini hanya memeriksa beberapa aturan utama.

Yang sudah dicek:

```text
internal/database → plugins     forbidden
internal/core     → plugins     forbidden
plugins           → app         forbidden
plugins           → plugins     forbidden
```

Tetapi desain mendefinisikan boundary lebih luas.

Belum ada enforcement penuh terhadap:

```text
platform → feature
core → services
core → feature
application → feature
feature → cmd
feature → app
platform → app
platform → unrelated feature/domain package
```

Juga belum ada test yang memastikan `internal/database` benar-benar generic.

## Fix

Buat rule matrix terpusat dan test setiap edge.

Lebih baik daripada hard-code kondisi ad-hoc:

```go
rules := []Rule{
    Allow("internal/app", "plugins/*"),
    Allow("plugins/*", "internal/core"),
    Allow("plugins/*", "internal/database"),
    Deny("plugins/*", "plugins/*"),
    Deny("internal/database", "plugins/*"),
    ...
}
```

Tambahkan test bahwa domain symbols tidak muncul di platform database.

---

# 8. P1 — `internal/database` Belum Menjadi Generic Platform

Walaupun beberapa repository sudah dipindahkan, `internal/database` masih berisi domain-specific implementation untuk:

```text
addon
moderation
peers
pmpermit
scheduler
settings
voice
```

Ini berarti target:

```text
internal/database = generic DB mechanics only
```

belum tercapai.

Ini bukan bug migration untuk fitur yang sudah dirework; ini **architecture completion gap**.

## Fix strategy

Gunakan strangler migration:

```text
settings
scheduler
peer
moderation
pmpermit
voice
addon
```

satu per satu menjadi feature-owned persistence atau subsystem-owned repository.

`internal/database` akhirnya hanya memiliki:

```text
DB
transaction
SQLExecutor
migration runner
migration metadata
generic DB helpers
```

---

# 9. P1 — Feature Ownership Masih Bernama `plugins/`, Bukan Boundary yang Jelas

Dokumen blueprint awal mengarah ke:

```text
internal/features/<feature>
```

sementara implementation memilih:

```text
plugins/<feature>
```

Ini sendiri **tidak salah**, karena Go tidak membutuhkan folder `features` tambahan. Bahkan server-side Go code memang lazim ditempatkan di `internal`. citeturn0search2

Namun naming harus diputuskan secara final.

Saat ini `plugins` memiliki dua makna:

1. Telegram presentation/plugin runtime;
2. feature ownership boundary + persistence + migration.

Jika `plugins` memang sengaja menjadi feature package, dokumentasi harus menegaskan itu. Jika tidak, rename/reshape harus dilakukan secara bertahap.

Jangan membuat `internal/features/<name>/plugins/...` hanya demi struktur folder; itu akan memperbanyak package tanpa menambah boundary.

Go sendiri menyarankan menghindari package splitting yang tidak perlu dan memusatkan package berdasarkan cohesion. citeturn0search1

---

# 10. P1 — Feature Generator Belum Semantic-Safe

`tools/featuregen/main.go` mencari:

```text
var Module ...
```

dengan AST.

Ia tidak memvalidasi:

```text
ModuleType implements module.Module
Manifest().ID valid
Manifest().Version valid
```

dan tidak memeriksa bahwa variable tersebut benar-benar value yang intended untuk registration.

## Dampak

Generator dapat menghasilkan registry dari deklarasi yang kebetulan bernama `Module`, kemudian compilation gagal atau diagnostics menjadi terlambat.

## Fix

Generator harus minimal memvalidasi:

```text
package/module.go exists
exact var Module exists
Module type implements module.Module
Manifest ID non-empty
```

Idealnya gunakan `go/packages` + `go/types` daripada AST syntax-only untuk semantic discovery.

---

# 11. P1 — Generated Registry Tidak Diverifikasi Freshness-nya di CI

`internal/app/modules.go` memiliki:

```go
//go:generate go run ../../tools/featuregen
```

Tetapi `.github/workflows/ci.yml` hanya menjalankan formatting, vet/lint, tests, dan build. Tidak ada langkah:

```text
go generate ./internal/app

git diff --exit-code internal/app/generated_modules.go
```

## Dampak

Developer dapat menambah module tetapi lupa regenerate registry.

Code tetap compile dan test bisa tetap hijau karena feature baru tidak pernah terdaftar.

Ini bug yang sangat relevan dengan tujuan utama rework.

## Fix

CI harus memiliki gate:

```bash
go generate ./internal/app

git diff --exit-code -- internal/app/generated_modules.go
```

atau generator dibuat deterministic dan dijalankan sebagai pre-test step.

---

# 12. P1 — Registration Error Context Belum Memisahkan Validation dan Dependency Failure

`registerBuiltinModules()` membungkus error secara umum:

```text
failed to register feature module
```

Namun tidak menyertakan manifest dependency resolution karena memang belum ada.

Setelah dependency graph ditambahkan, error sebaiknya eksplisit:

```text
module clone dependency "storage" unavailable
module notes depends on unknown module "foo"
module a -> b -> a dependency cycle
```

Hal ini penting karena startup error adalah primary diagnostic surface untuk modular bootstrap.

---

# 13. P1 — Migration Registry Tidak Memiliki Transactional Global Bootstrap Semantics

Feature migration individual dibungkus transaction saat SQL migration dijalankan, tetapi adoption marker menggunakan direct `db.ExecContext` tanpa transaction bersama schema verification.

Untuk adoption yang lebih kuat:

```text
BEGIN
  validate legacy marker
  validate schema invariant
  insert feature adoption marker
COMMIT
```

Jika validasi gagal, tidak boleh ada marker parsial.

---

# 14. P1 — Feature Migration Metadata Kurang Lengkap dari Design

Design menyebut metadata:

```text
id
description
checksum
applied_at
duration
```

Current table hanya memiliki:

```text
id
description
checksum
applied_at
```

`duration` tidak wajib untuk correctness, tetapi merupakan gap terhadap design dan berguna untuk production diagnostics.

Lebih penting lagi, metadata adoption reason saat ini hanya dimasukkan ke description string:

```text
Description + " (legacy adoption)"
```

Lebih robust jika ada field:

```text
execution_kind = applied | adopted
source_legacy_version
```

---

# 15. P2 — Module Validation Terlalu Lemah

`module.Validate()` hanya memeriksa:

```text
module != nil
ID != ""
Version != ""
```

Belum memvalidasi:

- format module ID;
- uniqueness;
- semver format;
- description policy;
- dependency IDs;
- self-dependency;
- duplicate dependency.

Minimal ID harus memiliki grammar stabil, misalnya:

```text
^[a-z0-9][a-z0-9_-]*$
```

Dan duplicate IDs harus ditolak sebelum registration.

---

# 16. P2 — Generator Uses Package Directory Name as Import Alias

Generator memakai:

```text
filepath.Base(dir)
```

sebagai alias import.

Ini bekerja untuk current plugin names, tetapi bukan design yang paling robust bila nested feature paths atau package names berbeda dari directory names muncul di masa depan.

Semantic package loading akan lebih aman karena package name dapat diperoleh dari Go package metadata.

---

# 17. P2 — `Runtime` Contains Feature-Specific `OwnerID`

`OwnerID` adalah dependency clone/admin-like tertentu tetapi ditempatkan pada global Runtime.

Ini memperkuat indikasi bahwa Runtime berkembang berdasarkan kebutuhan feature satu per satu.

`OwnerID` seharusnya berada pada capability/service yang memang membutuhkannya atau configuration/security context, bukan global module runtime.

---

# 18. P2 — Module Registration and Plugin Registration Are Tightly Coupled

Current contract:

```text
Module.Register()
    ↓
rt.Plugins.Register(...)
```

Artinya setiap module harus tahu `plugin.Manager` dan registration semantics.

Ini acceptable untuk transisi, tetapi target yang lebih bersih adalah module composition menghasilkan registrations:

```text
Module
  ↓
ModuleContribution
  ├── Plugins
  ├── Migrations
  ├── Callbacks
  └── Lifecycle hooks
```

Kemudian composition root menyerahkan contributions ke runtime.

Namun ini **bukan alasan untuk menambah abstraction sekarang**. Lakukan hanya jika plugin/callback lifecycle mulai membutuhkan independent ownership/testing.

---

# 19. P2 — Callback Ownership Belum Uniform

Blueprint menginginkan callback feature-owned. Current architecture masih memiliki generic callback infrastructure di:

```text
internal/services/callback
```

dan sebagian feature modules langsung menerima:

```text
Callbacks
CallbackStore
```

Ini belum otomatis salah: router/store memang platform/application infrastructure. Yang harus diuji adalah callback **handler/state ownership**.

Rule final:

```text
platform callback router → generic only
feature callback state/handler → feature-owned
```

Jangan menaruh feature-specific callback semantics kembali ke `internal/services/callback`.

---

# 20. Style / Design Findings

## 20.1 Yang sudah baik

- Feature package tetap satu package meskipun terdiri dari beberapa file.
- Constructor/repository ownership mulai jelas.
- Compile-time generated registry lebih predictable daripada global `init()` registration.
- SQL implementation persistent feature sudah dekat dengan domain owner.
- Migration IDs namespaced mengurangi global integer bottleneck.
- Architecture tests sudah menjadi executable documentation.

Go sendiri mendukung kedua ekstrem package size, tetapi menekankan cohesion dan memperingatkan split berlebihan. Pendekatan feature package saat ini lebih sehat daripada membuat `feature/service`, `feature/repository`, `feature/storage`, dan seterusnya menjadi package terpisah. citeturn0search1

## 20.2 Yang perlu diperbaiki

### Hindari "god runtime"

Jangan mengganti `catalog.go` god wiring menjadi `Runtime` god object.

### Hindari interface premature

Interface harus berada di sisi consumer/domain yang membutuhkan abstraction. Jangan membuat interface hanya karena semua komponen harus punya interface.

### Jangan membuat generic `utils`, `common`, `helpers`

Feature-specific helper tetap berada di feature.

### Jangan memindahkan semua code ke `internal/features`

Folder bukan boundary. Import graph adalah boundary.

### Jangan menggunakan dynamic plugin loading

Static compile-time modules cocok untuk Goultroid. Go compile-time dependency graph lebih mudah dipahami/debug dibanding runtime reflection/service locator. citeturn0search0

---

# 21. Recommended Target Architecture After Hardening

```text
cmd/goultroid
      │
      ▼
internal/app                 composition root
      │
      ├───────────────┐
      ▼               ▼
 module registry    platform/core
      │               │
      ▼               │
 plugins/<feature> ───┘
      │
      ├── behavior
      ├── service
      ├── repository
      ├── sqlite
      ├── migration
      ├── callback handlers
      └── tests
```

`plugins/` boleh dipertahankan sebagai feature boundary jika convention didokumentasikan dan enforced.

Platform database:

```text
internal/database
├── db.go
├── tx helpers
├── SQLExecutor
├── legacy migration history
├── feature migration runner
└── migration metadata
```

Tidak boleh lagi:

```text
internal/database/notes.go
internal/database/afk.go
internal/database/settings.go
internal/database/voice.go
...
```

setelah semua migration selesai.

---

# 22. Required Execution Order

## Gate A — Migration safety

1. Restore/preserve immutable legacy migration definitions.
2. Implement legacy checksum verification.
3. Implement schema invariant verification for adoption.
4. Add tampered legacy tests.
5. Add true fresh + upgrade integration tests.

**Tidak boleh lanjut sebelum Gate A hijau.**

## Gate B — Module graph

1. Validate unique IDs.
2. Implement dependency graph.
3. Topological sort.
4. Cycle detection.
5. Dependency diagnostics.

## Gate C — Generator safety

1. Semantic discovery.
2. Deterministic output.
3. CI freshness check.
4. Generator unit tests.

## Gate D — Architecture enforcement

1. Expand import matrix.
2. Enforce database generic boundary.
3. Detect cross-feature imports.
4. Detect forbidden app/platform dependencies.

## Gate E — Runtime decoupling

1. Stop adding feature-specific fields to `module.Runtime`.
2. Extract narrow capabilities/providers.
3. Migrate subsystem-backed modules to explicit dependencies.

## Gate F — Remaining database domains

Migrate:

```text
settings
scheduler
peer
moderation
pmpermit
voice
addon
```

according to actual ownership/coupling, not mechanically.

---

# 23. Definition of Done Revised

Rework plugin baru boleh disebut selesai jika:

### Feature ownership

- [ ] feature behavior local
- [ ] feature repository local
- [ ] feature SQL local
- [ ] feature migration local
- [ ] feature callback handler local
- [ ] feature tests local

### Composition

- [ ] no manual catalog edit
- [ ] generated registry deterministic
- [ ] CI verifies generated registry freshness
- [ ] duplicate module IDs rejected
- [ ] dependency graph deterministic

### Database

- [ ] legacy migrations immutable
- [ ] all applied historical versions remain known
- [ ] historical checksum verified
- [ ] feature adoption verifies schema
- [ ] fresh install tested
- [ ] upgrade tested
- [ ] tamper tested
- [ ] rerun/idempotency tested

### Architecture

- [ ] no feature → feature imports
- [ ] no feature → app imports
- [ ] no platform → feature imports
- [ ] database contains no feature domain model after migration phase
- [ ] architecture rules executable in CI

### Runtime

- [ ] no growth of global Runtime for feature-specific dependencies
- [ ] lifecycle/shutdown remains deterministic
- [ ] callback ownership remains local
- [ ] plugin registration remains idempotent/safe

---

# 24. Research Basis

Go's official guidance recommends packages be organized around coherent functionality, warns against both grab-bag packages and excessive package splitting, and recommends `internal` for server implementation code. citeturn0search1turn0search2

Go's compile-time DI guidance also favors explicit constructor dependencies and points out that generated dependency wiring keeps the graph statically knowable and avoids runtime service-locator/reflection behavior. citeturn0search0turn0search9

Martin Fowler's modularity discussion emphasizes that module boundaries in a monolith are useful but can be bypassed unless boundaries are actively enforced; persistent data ownership is part of the coupling boundary. citeturn0search7turn0search8

Karena itu, solusi yang tepat untuk Goultroid bukan microservices dan bukan dynamic plugins. Solusi yang tepat adalah **modular monolith dengan feature ownership, explicit dependencies, static composition, durable migration history, dan executable architecture rules**.

---

# 25. Final Assessment

| Area | Status | Assessment |
|---|---|---|
| Feature-oriented ownership | 🟡 | Sudah mulai benar, tetapi masih ada legacy DB domains |
| Generated registration | 🟢 | Sudah berjalan dan deterministic secara dasar |
| Plugin catalog decoupling | 🟢 | `catalog.go` sudah kosong secara efektif |
| Migration namespacing | 🟢 | Contract dan runner sudah ada |
| Legacy migration compatibility | 🔴 | Historical 15/16 ownership/history tidak aman |
| Migration adoption safety | 🔴 | Marker dipercaya tanpa checksum/schema proof |
| Migration integration tests | 🔴 | Belum merepresentasikan full real upgrade path |
| Dependency graph | 🔴 | Manifest dependencies belum functional |
| Runtime dependency design | 🔴 | Runtime terlalu besar dan feature-accumulative |
| Architecture enforcement | 🟡 | Ada, tetapi belum sesuai seluruh matrix |
| Generator semantic validation | 🟡 | AST-only discovery terlalu lemah |
| CI generated-file enforcement | 🔴 | Belum ada freshness gate |
| Database decoupling | 🟡 | Lima feature sudah dipindahkan, subsystem lain masih legacy |
| Go package cohesion | 🟢 | Feature sebagai satu package adalah arah yang benar |

**Overall:** `~7/10 — good architectural direction, not yet production-grade.`

Prioritas absolut: **jangan menambah feature baru dengan memperluas `Runtime`, dan jangan menganggap migration history 15/16 selesai hanya karena definition-nya dihapus.** Perbaiki migration integrity terlebih dahulu, lalu kunci generated registry + dependency graph + architecture enforcement.
