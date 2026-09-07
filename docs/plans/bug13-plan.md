# Bug13 - Architecture Over-Abstraction Remediation Plan

> Dokumen ini adalah perencanaan atomik, berurutan, dan verifikabel untuk menangani 43 poin audit `docs/bug/bug13.md`.
> **Prinsip:** Setiap langkah harus kecil, dapat direview, dan memiliki kriteria sukses yang terukur. Jangan refactor sekaligus.

---

## 0. Status Validasi Awal (2026-09-07)

Verifikasi langsung di kode `main`:

| Area | File | Status Riil |
|------|------|-------------|
| `App` God Object | `internal/app/app.go:23` 124 baris | Sudah dipecah ke `bootstrap.go:58` (291), `dependencies.go:28`, `lifecycle.go:12`, `shutdown.go` - sisa bottleneck di `buildCore` (9 subsystem) + `buildPlugins` (25 plugin hardcoded) |
| `Dispatcher` SetX | `internal/telegram/dispatcher.go:84` `DispatcherDeps` | Ada deps struct tapi delegasi ke 9 `SetX:150,264,310,317,335,366,384,397,410` - masih `nil`-able, 20+ defensive `if nil` |
| `Router.Dispatch` | `internal/services/callback/router.go:95` 281 baris | Sudah ada `CallbackFailure:58` + `reject():385` (duplikasi berkurang 40%) tapi belum middleware chain |
| `StateStore` lifecycle | `internal/services/callback/state.go:35` `cancel` | Sudah `cancel` model, `lifecycle.go:15` `Start(ctx)` root. Inkonsisten: `internal/services/inline/cache.go:20` masih `stopCh` |
| `Selected string` | `plugins/settings/settings.go:38,387,393,407,420` | Paling kritikal tersisa - `SettingTarget:27` + `ActionValue:42` sudah ada tapi dual-write `Selected = fmt.Sprintf("%s:%s:%s")` |
| `Import` atomic | `internal/settings/service.go:342` | Sudah 3 fase + `SetSettingsBatch:398` atomic. `Set():207` masih dual-write DB+EventBus |
| `Reset` actor | `plugins/settings/settings.go:485` | Sudah `Reset(..., ctx.UserID)` |
| `_ = Register` | `internal/app/bootstrap.go:176` | Sudah `if err != nil return`. Sisa `_ = AnswerCallbackQuery` di `router.go:110` |
| `noop` | `internal/services/callback/types.go:35` `ActionNoop` | Konstanta ada, 5 literal `== "noop"` tersisa di `router.go:108`, `settings.go:294,372` |

---

## 1. Aturan Main

1.  Setiap task di bawah **harus** memiliki test yang gagal sebelum fix dan pass sesudah fix (atau architecture test).
2.  Satu commit = satu task atomik. Tidak mencampur P0+P1.
3.  Setiap task yang menyentuh `StateStore`/`Dispatcher`/`Settings` wajib `go test -race`.
4.  `go vet ./...` dan `gofmt -l` harus hijau di setiap commit.
5.  Tidak menghapus `Selected` langsung - migrasi dual-read 1 rilis (TTL 15 menit `state.go:61`).

---

## 2. Fase 0: Safety Net (WAJIB SEBELUM UBAHAN APAPUN)

Tujuan: Menjamin regresi sekecil apapun terdeteksi.

### Task 0.1 - Architecture Isolation Tests (Sudah ada `internal/core/arch_test.go:15`, perluas)

| Field | Detail |
|-------|--------|
| File baru | `internal/core/arch_phase0_test.go` atau perluas `arch_test.go` |
| Isi | Tambah 4 guard baru: |
| | 1. `TestArchitecture_UILayerIsolation` - `internal/ui` tidak boleh import `database`, `plugins`, `scheduler`, `telegram` (`bug13 #40`) |
| | 2. `TestArchitecture_DomainNoTGImport` - `internal/settings` + `internal/services/callback` + `internal/domain/*` tidak boleh import `github.com/gotd/td/tg` |
| | 3. `TestArchitecture_PluginNoDirectDB` - `plugins/*` tidak boleh `database/sql` atau `db.Exec` raw |
| | 4. `TestArchitecture_TelegramAdapterNoBusinessRule` - `internal/telegram` tidak boleh import `internal/settings` |
| Kriteria sukses | `go test ./internal/core -run TestArchitecture -v` 7 tests pass |

**Implementasi referensi (mirip `arch_test.go:18`):**
```go
pkgDir := "../ui"
disallowed := []string{"github.com/inipew/goultroid/internal/database", "github.com/inipew/goultroid/plugins", ...}
```

### Task 0.2 - Callback Contract Tests (5 skenario `bug13 #42`)

| Field | Detail |
|-------|--------|
| File | `internal/services/callback/contract_test.go` (baru) atau perluas `router_test.go:184` |
| Skenario | 1. `wrong user` -> `ErrUnauthorized` + `IsAlert=true` 2. `wrong chat` -> `ErrUnauthorized` 3. `expired` -> `ErrStateExpired` 4. `consumed` (single-use replay) -> `ErrStateNotFound` 5. `valid` -> `handled=true` |
| Catatan | Test `router_test.go:184,244,264,289,324` sudah cover 5 ini - Task 0.2 hanya menambahkan assertion `MetricTag` dan `UserAlert` text untuk guard regressions |
| Kriteria | `go test ./internal/services/callback -run TestRouter -v` 9 pass |

### Task 0.3 - Settings Hierarchical Tests

| Field | Detail |
|-------|--------|
| File | `internal/settings/contract_test.go` (baru) |
| Skenario | `Resolve` chat>user>global>default; `Set` + `bus` invalidation; `ScopeRef.Validate` untuk `global/chat/user` |
| Guard | `internal/core/arch_test.go:112` sudah ada `ScopeInvariants` - tambahkan `TestArchitecture_SettingsCacheInvalidation` |
| Kriteria | `go test ./internal/settings -run TestArchitecture -v` pass |

### Task 0.4 - Linter Config

| Field | Detail |
|-------|--------|
| File | `.golangci.yml` (baru, root) |
| Isi | `linters: enable: [errcheck, staticcheck, unused, ineffassign, gocritic, revive, misspell, depguard]` + `depguard: rules: ui: deny: [github.com/inipew/goultroid/internal/database]` |
| CI | `.github/workflows/ci.yml` sudah ada `go vet` + `go test -race` - tambahkan step `golangci-lint run` (allow failure dulu di Fase 0, enforced di Fase 3) |
| Kriteria | `golangci-lint run ./...` (jika terinstall) atau `go vet ./...` tetap pass |

### Task 0.5 - Baseline Metrics

| Field | Detail |
|-------|--------|
| File | `docs/plans/bug13-baseline.md` (opsional, catat) |
| Isi | `go test -race -count=1 ./... 2>&1 | tail -20` + `go vet ./...` output + `wc -l internal/app/bootstrap.go internal/telegram/dispatcher.go internal/services/callback/router.go` |
| Kriteria | Tercatat sebelum ubahan - jadi referensi regresi |

**Verifikasi Fase 0:**
```bash
go vet ./...
go test ./internal/core -run TestArchitecture -v
go test ./internal/services/callback -run TestRouter -v -race
go test ./internal/settings -run Test -v -race
go test -race ./... # full suite
```

---

## 3. Fase 1: P0 - Wajib (Urutan Dependency-Aware)

### Task 1.1 - Pecah `bootstrap.go` Wiring (Issue #1)

| Field | Detail |
|-------|--------|
| Masalah | `internal/app/bootstrap.go:58` `buildCore` 9 subsystem, `bootstrap.go:232` 25 plugin hardcoded - setiap fitur baru sentuh file sentral |
| File ubah | `internal/app/bootstrap.go` (pecah), `internal/app/dependencies.go:28` (tambah `Dependencies` struct), baru `internal/app/wiring/core.go`, `wiring/telegram.go`, `wiring/services.go`, `wiring/plugins/catalog.go` |
| Langkah atomik | 1. Buat `internal/app/wiring/catalog.go` pindahkan `buildPlugins` return list tanpa `Register` loop. 2. `app.go:69` `pluginManager.Register` loop tetap di `app.go:79`. 3. `dependencies.go:28` definisikan `type Dependencies struct { DB, EventBus, Permissions, Settings, Dispatcher, Callbacks, Inline }` dan pakai sebagai return `buildCore`. 4. Tidak ubah behavior - hanya pindah baris. |
| Pitfall | `_ = eventBus.Close()` cleanup di `bootstrap.go:98` jangan hilang - buat `cleanupCore(*coreDependencies)` helper |
| Test | `go test ./internal/app -v` existing `app_test.go` harus pass. `go vet ./...` pass. |
| Kriteria | `wc -l internal/app/bootstrap.go` <150, `internal/app/app.go:40` `New()` hanya 4 panggilan `build*` |

### Task 1.2 - Typed Scope & Callback Actions (Issue #13, #21, #28)

| Field | Detail |
|-------|--------|
| Masalah | `plugins/settings/settings.go:33` `Scope Scope + ScopeID int64` terpisah - bisa `Scope=chat, ScopeID=0`; `router.go:108` literal `"noop"`, `settings.go:294` literal `"settings","noop"` |
| File ubah | `plugins/settings/settings.go:33` `MenuState`, `internal/services/callback/types.go:35` (sudah ada konstanta), `internal/settings/types.go:170` `ScopeRef` |
| Langkah atomik | 1. Tambah `internal/services/callback/action.go` atau perluas `types.go:35` dengan `const ActionStep="step", ActionDur="dur"` (cek: `types.go` baru punya `ActionNoop, Nav, Toggle, Set, Reset, Back, Close, Select` - tambahkan `Step, Dur`). 2. Ganti literal di `router.go:108` `== "noop"` -> `== ActionNoop`, `settings.go:294,372` `EncodeCallbackData("settings","noop")` -> `ActionNoop`. 3. `MenuState` tambah method `ScopeRef() ScopeRef { return ScopeRef{Type: s.Scope, ID: s.ScopeID} }` + `Validate()` wrapper. 4. Di `settings.go:454` `applySettingAction` awal `if err := state.ScopeRef().Validate(); err != nil { return err }`. 5. `internal/database` mapper: pastikan `string(scope)` hanya di satu tempat `repository.go:1095`. |
| Test | `internal/core/arch_test.go:112` `ScopeInvariants` sudah pass - tambah `TestArchitecture_MenuStateScopeRefValid` |
| Kriteria | `grep -r '"noop"' --include="*.go" plugins/settings internal/services/callback` == 0 literal (hanya konstanta). `grep -r 'Scope.*ScopeID' plugins/settings` terdokumentasi via `ScopeRef()` |

### Task 1.3 - Hilangkan `Selected string` Protocol (Issue #12)

| Field | Detail |
|-------|--------|
| Masalah | `plugins/settings/settings.go:38` `Selected string` `ns:key` + `ns:key:value` via `fmt.Sprintf` - fragil untuk value `foo:bar`, `settings.go:473` `Split(":")` |
| File ubah | `plugins/settings/settings.go:27,38,46,60,387,393,407,420,473` |
| Langkah atomik | **Rilis N (dual-write read-both):** 1. `storeState()` tetap tulis `Target` + `ActionValue`, `Selected` dikosongkan `""` untuk entry baru. 2. `GetTarget()` sudah fallback `Split(Selected)` - pertahankan. 3. `renderSettingDetailScreen:387,393` ganti `decState.Selected = fmt.Sprintf(...)` -> `decState.ActionValue = fmt.Sprintf("%d", ...)` + `decState.Target = &SettingTarget{...}` (hapus `Selected` assignment). 4. `applySettingAction:473` sudah prioritas `ActionValue` -> fallback `Split(Selected)` - pertahankan. **Rilis N+1 (hapus):** Hapus field `Selected` dari struct, hapus `strings.Split` fallback. |
| Pitfall | State TTL 15 menit `state.go:61` - user yang sedang di dashboard saat deploy akan punya state lama `Selected` - fallback wajib 1 versi. Jangan hapus langsung. |
| Test | Baru `plugins/settings/state_migration_test.go`: marshal old `{"sel":"ns:key:val"}` -> `GetTarget()` + `ActionValue` priority. Test value `foo:bar:baz` tidak ter-split. |
| Kriteria | `grep -n "Selected" plugins/settings/settings.go` hanya di `GetTarget` fallback + struct tag `json:"sel,omitempty"` untuk kompat. `renderSettingDetailScreen` tidak ada `Selected =` |

### Task 1.4 - Fail-Fast Bootstrap & Transactional Import Guard (Issue #24, #26)

| Field | Detail |
|-------|--------|
| Masalah | `_ = AnswerCallbackQuery` silent di `router.go:110,151,328`; `Import` sudah atomic tapi belum return `ImportResult` |
| File ubah | `internal/services/callback/router.go:110,151,328`, `internal/app/bootstrap.go:98` cleanup, `internal/settings/service.go:342` `Import` |
| Langkah atomik | 1. `router.go:110` `_ = svc.AnswerCallbackQuery` -> `if err := svc.AnswerCallbackQuery(...); err != nil { r.logger.Debug("answer failed", zap.Error(err)) }`. 2. `bootstrap.go:98` `_ = eventBus.Close()` -> `if err := eventBus.Close(); err != nil { logger.Warn("cleanup failed", zap.Error(err)) }`. 3. `service.go:342` `Import` sudah atomic - tambah test `Import` dengan 100 setting, 1 invalid di tengah -> assert 0 applied + error. Tidak perlu ubah signature di rilis ini (P2 baru `ImportResult`). |
| Test | `internal/settings/settings_test.go` sudah ada `TestImport` - tambah case `invalid mid-batch` |
| Kriteria | `go vet` pass, `grep -n "_ = svc.Answer" internal/services/callback/router.go` == 0 |

### Task 1.5 - Lifecycle `context.Background()` Sisa (Issue #6, #7)

| Field | Detail |
|-------|--------|
| Masalah | `bootstrap.go:69` `GetSudoUsers(Background())`, `bootstrap.go:211` `LoadInstalled(Background())`, `internal/services/inline/cache.go:20` `stopCh`, `internal/telegram/dispatcher.go:944` `Background()` fallback, `internal/scheduler/engine.go:121` 5x `Background()` |
| File ubah | `internal/app/bootstrap.go:69,211`, `internal/services/inline/cache.go:20,181`, `internal/telegram/dispatcher.go:790,944`, `internal/scheduler/engine.go:121` |
| Langkah atomik | 1. `bootstrap.go:69` `GetSudoUsers(context.Background())` -> `context.WithTimeout(context.Background(), 5*time.Second)` eksplisit + `defer cancel()` atau terima `ctx` dari `New()` (tambah param `ctx context.Context` ke `buildCore`). 2. `inline/cache.go:20` ganti `stopCh chan struct{}` -> `cancel context.CancelFunc` identik `state.go:35` pattern. 3. `dispatcher.go:944` `root = context.Background()` -> `if d.getRootContext()==nil { return error }` fail-fast. 4. `scheduler/engine.go:121` `Background()` -> pakai `e.ctx` yang di-set di `Start(ctx)`. |
| Test | `internal/services/callback/state_test.go:549` `TestStateStore_StartStopCancel` sudah ada - duplikat untuk `inline/cache`. `go test -race ./internal/services/inline -v` |
| Kriteria | `grep -rn "context.Background()" --include="*.go" internal/app internal/services/callback internal/services/inline internal/scheduler` == 0 (kecuali `cmd/goultroid/main.go:28` valid + `storage/fs.go` defensive) |

### Task 1.6 - Kunci `Dispatcher` Constructor (Issue #2)

| Field | Detail |
|-------|--------|
| Masalah | `dispatcher.go:84` `DispatcherDeps` ada tapi `NewDispatcherWithDeps:99` masih `if != nil Set` - objek bisa `nil` service/resolver |
| File ubah | `internal/telegram/dispatcher.go:84,97,118,150` |
| Langkah atomik | 1. `DispatcherDeps` jadikan required: `Router, Permissions, Logger, EventBus, Localizer, CallbackRouter, InlineEngine` - `NewDispatcherWithDeps` return `(*Dispatcher, error)` dan `if deps.Router==nil { return nil, error }` untuk tiap required. 2. Hapus `SetEventBus, SetLocalizer, SetCallbackRouter, SetInlineEngine, SetResolver` dari API publik - jadikan private `set*` hanya dipakai di constructor. Sisakan `SetSelfID, SetRootContext` yang memang runtime mutable. 3. Update `internal/app/bootstrap.go:135` `buildTelegramRuntime` untuk handle `error` return. |
| Pitfall | Circular `Service` (butuh `Dispatcher` untuk `Client`, `Client.Service` untuk `Dispatcher`) - `Service` tetap `nil` saat konstruksi, di-set via `dispatcher.setService(client.Service)` private sekali setelah `telegram.NewClient`. Dokumentasikan 2-phase. |
| Test | `internal/telegram/dispatcher_test.go` update `NewDispatcherWithDeps` error cases. `go test -race ./internal/telegram -v` |
| Kriteria | `grep -n "func.*SetEventBus\|SetLocalizer\|SetCallbackRouter\|SetInlineEngine" internal/telegram/dispatcher.go` == 0 public. `NewDispatcherWithDeps` return error jika required nil. |

### Task 1.7 - Callback Pipeline Middleware Preparation (Issue #3, #4)

| Field | Detail |
|-------|--------|
| Masalah | `router.go:95` 281 baris single method - rate limit, state lookup, auth, scope, consume, lookup, timeout, recover semua inline |
| File ubah | `internal/services/callback/router.go:95,385`, baru `internal/services/callback/middleware.go`, `internal/services/callback/pipeline_test.go` |
| Langkah atomik | **Fase ini hanya struktur tanpa ubah behavior:** 1. Ekstrak `reject()` sudah ada `router.go:385` - tambah helper `func (r *Router) fail(...)` untuk 8 cabang. 2. Buat `middleware.go` dengan `type Middleware func(Handler) Handler` stub + `func RateLimitMiddleware`, `RecoverMiddleware` (belum dipakai `Dispatch`). 3. `Dispatch` pecah jadi private methods `parseAndValidate`, `checkRateLimit`, `resolveState`, `authorize`, `consumeIfNeeded` - masih dipanggil sequential di `Dispatch` (belum chain). Ini persiapan untuk chain penuh di Fase 2. |
| Test | `go test ./internal/services/callback -run TestRouter -v` tetap pass - refactor harus zero-behavior-change |
| Kriteria | `router.go:95` `Dispatch` <150 baris setelah ekstrak helper (ukur `wc -l`). `middleware.go` ada dengan 2 middleware stub. |

### Task 1.8 - Settings Mutation Path Unifikasi (Issue #11)

| Field | Detail |
|-------|--------|
| Masalah | `plugins/settings/settings.go:324` `renderSettingDetailScreen` switch `TypeBool/TypeInt/TypeDuration/TypeEnum` duplikat `Store`+`EncodeCallbackData`; `settings.go:214` scope switcher manual |
| File ubah | `plugins/settings/settings.go:324,174,244,454`, baru `internal/settings/viewmodel.go` (stub Fase1), `internal/ui/settings/widget.go` (stub) |
| Langkah atomik | 1. `settings.go:454` `applySettingAction` sudah unifikasi - perluas untuk handle `ActionValue` colon-safe (done di 1.3). 2. `renderSettingDetailScreen:374` ekstrak helper `func (p *Plugin) buildBoolRow(state, def) ButtonRow` untuk tiap type - kurangi duplikasi inline. 3. Fase1 belum buat `SettingWidgetFactory` penuh - cukup ekstrak 4 helper method agar `renderSettingDetailScreen` <80 baris. 4. `renderHomeScreen:214` scope switcher - tambah `func nextScope(current Scope) (Scope, ID)` helper. |
| Test | `plugins/settings/settings_test.go` + `internal/ui/interaction_test.go` snapshot tetap pass |
| Kriteria | `renderSettingDetailScreen` 122 baris -> <80 baris. Tidak ada `fmt.Sprintf("%s:%s:%s")` di file (sudah di 1.3). |

---

## 4. Fase 2: P1 - Sangat Disarankan (Setelah P0 Rilis)

### Task 2.1 - Typed `SettingValue` Ujung-ke-Ujung (Issue #8, #9)

| Field | Detail |
|-------|--------|
| File | `internal/settings/types.go:211` `SettingValue`, `internal/settings/service.go:146` `ResolveBool/Int/Duration`, `internal/settings/registry.go` |
| Langkah | `SettingValue` jadi `struct { raw string; def *SettingDefinition; err error }` dengan `Bool() (bool,error)` yang pakai `def.Canonicalize`. `Service.ResolveBool` jadi wrapper `return s.ResolveValue(...).Bool()`. Hapus duplikasi `strings.EqualFold` di `service.go:152`. Tambahkan `UIHint` ke `SettingDefinition:42` (Widget, Step, Presets) untuk auto-render. |
| Kriteria | `grep -n "strings.EqualFold.*true" internal/settings/service.go` == 0 (hanya di `types.go`) |

### Task 2.2 - Callback Pipeline Jadi Chain (Issue #4 lanjutan)

| File | `internal/services/callback/middleware.go`, `router.go:95` |
| Langkah | `Router.Use(mw Middleware)` + `Dispatch` build chain `chain := r.buildChain(handler); return chain(ctx)`. Middleware `Decode, RateLimit, ResolveState, Authorize, ValidateScope, Consume, Timeout, Recover`. Order kritikal: `RateLimit` sebelum `State lookup`. |
| Kriteria | `router.go:95` `Dispatch` <50 baris (hanya `chain(ctx)`). Tiap middleware unit-test isolated. |

### Task 2.3 - StateStore Split (Issue #5)

| File | `internal/services/callback/state.go:35` (226 baris) |
| Langkah | Pecah `state.go` -> `store.go` (map+eviction), `scope.go` (authorization), `lifecycle/cleanup.go` (prune ticker). `StateScope` pindah ke `scope.go`. |
| Kriteria | `state.go` <100 baris, `store.go` + `scope.go` + `lifecycle.go` masing-masing <120 baris |

### Task 2.4 - Interaction Service & Wizard/Navigator (Issue #15, #16)

| File | `internal/ui/wizard.go:9`, `internal/ui/navigator.go:8` |
| Langkah | `wizard.go` rename -> `WizardRenderer`. Baru `internal/services/interaction/wizard.go` `InteractionWizard` (state, transition, validation, persistence via `StateStore`). `Navigator` pindah -> `internal/services/interaction/navigation/navigator.go`. `internal/ui` hanya `BackButton()`, `HomeButton()`. |
| Kriteria | `internal/ui` tidak import `internal/services/callback`. `go test ./internal/ui -run TestWizard -v` tetap pass dengan `WizardRenderer`. |

### Task 2.5 - Telegram Renderer Pisah (Issue #17, #18, #19)

| File | `internal/ui/screen.go:43`, `internal/ui/button.go:186` |
| Langkah | Baru `internal/ui/render/telegram.go` `TelegramRenderer` yang convert `RenderedScreen{Text, Markup}` -> `*tg.ReplyInlineMarkup`. `Screen.Render()` return `RenderedScreen` tanpa `tg.*`. `button.go` pecah `button.go` (model) + `pagination.go` + `navigation.go` + `render/telegram.go`. Hapus `NewPaginationMarkup` wrapper ganda - sederhanakan `ui.Pagination()`. |
| Kriteria | `grep -rn "tg\." --include="*.go" internal/ui | grep -v render` == 0. `internal/ui` zero `tg` import kecuali `render/`. |

### Task 2.6 - Use-Case Layer & Plugin Standarisasi (Issue #33, #34)

| File | `plugins/settings/settings.go:137`, `plugins/filters/*` |
| Langkah | Standarkan plugin: `plugin.go` (definition), `commands.go`, `callbacks.go`, `ui.go`, `usecase/*.go`. Command dan Button panggil `SetFilterUseCase.Execute()`. Mulai dari `settings` sebagai pilot: `plugins/settings/usecase/set.go` `SetSettingUseCase`. |
| Kriteria | `plugins/settings/commands.go` dan `callbacks.go` terpisah, tidak ada `service.Set` langsung di command handler - via usecase. |

### Task 2.7 - Error Mapping & OperationResult (Issue #36, #37)

| File | `internal/ui/feedback/errors.go` (baru), `plugins/settings/settings.go:468` |
| Langkah | `ErrorClassifier` map `ErrUnauthorized -> "⚠️ Not authorized"`, `sqlite constraint -> "❌ Invalid value"` (jangan `err.Error()` raw). `OperationResult[T]` untuk UI. Ganti `ui.AnswerErrorToast(ctx, "Failed: "+err.Error())` -> `ui.AnswerErrorToast(ctx, classifier.Message(err))`. |
| Kriteria | `grep -rn "Failed:.*err.Error()" --include="*.go" plugins` == 0 |

---

## 5. Fase 3: P2 - Quality / Maintainability

| Task | File | Langkah |
|------|------|---------|
| 3.1 Import Restrictions Enforced | `.golangci.yml`, `internal/core/arch_phase0_test.go` | `depguard` deny rules jadi error (bukan warn). CI `golangci-lint run` required. |
| 3.2 Linter Strict | `.golangci.yml` | Enable `errcheck, staticcheck, unused, ineffassign, gocritic, revive, misspell` - fix semua `ineffassign` di `state.go:74` |
| 3.3 Naming Convention | `docs/conventions.md` | `Service/Engine/Manager/Registry/Router/Store` definitions (`bug13 #38, #39`) - `go vet` via `revive` config |
| 3.4 Dispatcher Split (Issue #30) | `internal/telegram/dispatcher.go:38` 1055 baris | Pecah `dispatcher/message.go`, `command/executor.go`, `callback/adapter.go`, `inline/adapter.go`, `peer/cache_worker.go`. Orchestrator `Dispatch()` <50 baris. |
| 3.5 Settings Resolver Batch & Cache | `internal/settings/service.go:86` | `GetEffectiveSettingsBatch()` + `Resolve` pakai single query `WHERE (scope_type,scope_id) IN (...)` + cache invalidation grouping. Benchmark `go test -bench=BenchmarkResolve`. |
| 3.6 Transactional Outbox (Issue #25) | `internal/settings/service.go:256` | `SettingChangedEvent` via outbox table `setting_outbox` + `BEGIN; UPSERT settings; INSERT outbox; COMMIT` + worker publish. Jika tidak, minimal `DB commit -> publish` dengan retry log. |
| 3.7 Integration & Race Tests | `go test -race ./...` | Tambah `TestIntegration_SettingFlow` + `TestIntegration_CallbackFullFlow` di `internal/app/integration_test.go` |

---

## 6. Verifikasi Akhir Setiap Fase

```bash
# Fase 0
go vet ./...
go test ./internal/core -run TestArchitecture -v
go test ./internal/services/callback -v -race
go test ./internal/settings -v -race
go test -race ./... 2>&1 | tail -30
gofmt -l .  # harus kosong

# Fase 1 tiap task
go test -race ./internal/app ./internal/services/callback ./plugins/settings -v
go vet ./...

# Fase 2/3
golangci-lint run ./...  # setelah Task 3.2
go test -race -bench=. ./internal/settings -run=^$  # benchmark resolver
```

---

## 7. Risiko & Mitigasi Lintas Fase

| Risiko | Mitigasi |
|--------|----------|
| `StateStore` TTL 15m state lama | Task 1.3 dual-read 1 rilis, fallback `Split` dipertahankan |
| Circular `Dispatcher <-> Service` | Task 1.6 private `setService` 2-phase terdokumentasi |
| `StateStore` eviction oldest salah | Task 1.5 + 2.3 clock mock test `maxStateStoreEntries=5000` |
| `Dispatcher` split lupa `inFlight.Wait` | `shutdown.go` drain test |
| `Resolve` cache race | `cacheMu` + `invalidate` dalam `Set` TX, `-race` |
| `_ = Answer` sembunyikan bug | Task 1.4 log debug |
| Plugin list lupa 1 | `catalog.go` test `len(DefaultPlugins())==25` |

---

## 8. Estimasi

| Fase | Estimasi | Ketergantungan |
|------|----------|---------------|
| Fase 0 | 1-2 hari | - |
| Fase 1 (8 tasks) | 5-7 hari | Fase 0 |
| Fase 2 (7 tasks) | 3-5 hari | Fase 1 rilis |
| Fase 3 (7 tasks) | 2-3 hari | Fase 2 |

**Jangan paralel semua P0 - urutan 1.1 -> 1.2/1.3 -> 1.4 -> 1.5 -> 1.6 -> 1.7/1.8 sesuai DAG di atas.**

---

## 9. Referensi File Kunci

- `internal/app/app.go:23,40,105` - composition root
- `internal/app/bootstrap.go:58,135,172,232` - wiring
- `internal/app/dependencies.go:28` - deps structs
- `internal/app/lifecycle.go:12,62` - lifecycle
- `internal/telegram/dispatcher.go:38,84,97,150,730,944` - dispatcher
- `internal/services/callback/router.go:15,95,385` - router
- `internal/services/callback/state.go:11,35,59,192` - state
- `internal/services/callback/types.go:28,35,46,316` - types
- `internal/settings/service.go:21,86,146,207,273,342` - settings service
- `internal/settings/types.go:11,22,42,170,211` - settings types
- `plugins/settings/settings.go:27,33,38,137,174,244,324,454,494` - settings plugin
- `internal/ui/button.go:73,75,186` - ui button
- `internal/ui/screen.go:43,66` - ui screen
- `internal/ui/wizard.go:9`, `internal/ui/navigator.go:8` - wizard/nav
- `internal/core/arch_test.go:15,50,81,112` - arch tests existing
