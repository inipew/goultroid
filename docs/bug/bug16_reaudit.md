# Re-Audit Bug16 & Bug16_1 — Assistant v2.5 Functional Parity

**Tanggal:** 2026-09-07  
**Scope:** `docs/bug/bug16.md` (23 §) + `docs/bug/bug16_1.md` (35 §) vs repo `HEAD`  
**Metode:** `Read` penuh `internal/{plugin,core,execution,application/*,assistant/*,app/*,settings,addon}` + `plugins/*` + `wiring_*` (read-only, plan mode)

---

## 1. Ringkasan Eksekutif

| Area | Nilai | Keterangan |
|---|---|---|
| **Invariant §0 `One impl, multiple surfaces`** | **PARTIAL** | Infra `execution.Source/SurfaceMask` + `UnifiedRegistry` + `CapabilityRegistry` ada & di-wire `app.go:76`, tapi 5 command `assistant/command/{ping,alive,status,help,start}.go` masih `r.Register` lokal & take precedence atas `adapter.go:31` |
| **Transport boundary (§6, §32)** | **🟢** | `assistant/client,interaction,peer,callback,menu,presentation` benar sebagai adapter |
| **Functional parity (§16-18, §34)** | **🔴/🟠** | `FormatResult`/`CollectSnapshot` shared, execution duplikat; `settings` unified, `help` partial, `status` FAIL, `addon` FAIL |
| **Lifecycle (§20)** | **🟡** | `Manager` transactional + reverse shutdown OK, `assistant.Stop` tidak di `shutdown.go:11` |
| **Registry (§22 Phase A-C)** | **🟢 scaffold / 🟡 wiring** | Files DONE, dual-write `core.Router+UnifiedRegistry` `manager.go:169` belum single source |

**Kunci:** scaffold v2.5 70% landing, unifikasi business logic ~40% — sisa blocker adalah `local handlers shadowing`, `dua Source enum`, `capability enforcement absen`.

---

## 2. Bug16.md Sandingan (23 §)

| § | Title | Status | Bukti `file:line` |
|---|---|---|---|
| 1 Satu fungsional | Userbot+Assistant→same plugins | **PARTIAL** | `execution/source.go:6` + `application/command/registry.go:12` + `app.go:82` infra ada, `assistant/command/ping.go:18` vs `plugins/ping/ping.go:58` dual impl |
| 2 Bukti plugin | `Plugin{Name,Commands,Init}` Manager `RegisterBatch` | **PARTIAL** | `plugin/plugin.go:11` base, `manager.go:21` `router *core.Router`, fix via optional `execution.CapabilityProvider` `capabilities.go:12` di `manager.go:196` |
| 3 Duplicate `/ping /alive` | Dua impl | **PARTIAL** | `plugins/alive/alive.go:50` vs `assistant/command/alive.go:15` duplicate `ReadMemStats`; `plugins/ping/ping.go:58` vs `ping.go:18` duplicate `time.Now→FormatResult` |
| 4 `/start /help` bukan plugin | Dua command system | **PARTIAL** | `assistant/command/start.go:15` static `BuildStartScreen`, `help.go:20` sudah `CommandsForSurface(Assistant)` OK |
| 5 Wiring special handler | Dedicated AssistantClient | **PARTIAL** | `wiring_telegram.go:19` `callbackRouter+inlineEngine` + `app.go:39` `assistant.NewApp`; mitigasi `app.go:82` `SetUnifiedRegistry` + `adapter.go:31` bridge |
| 6 Hybrid | `BotClient{v2Router}+legacy` | **PARTIAL** | `assistant/client/client.go:38` bersih, `telegram/dispatcher.go:31` masih dual `callbackRouter+inlineEngine` |
| 7 Salah desain surface | Assistant = surface | **PARTIAL** | `assistant/*` scope benar (§32), `plugins/*` belum source truth (8/24 declare `Capabilities()`) |
| 8 Analogi `PingUseCase` | Satu usecase dua adapter | **PARTIAL** | `application/ping/usecase.go:8` hanya `FormatResult`, belum `Execute`; `adapter.go:14` central |
| 9 Arsitektur final | `GoUltroid→Plugin→Execution→Adapters` | **PARTIAL** | layout `application/capability`, `execution`, `assistant` sesuai §2, wiring belum |
| 10 Source of truth | Plugin ≠ `assistant/ping` | **PARTIAL** | `plugins/ping/ping.go:30` `Surfaces: Userbot\|Assistant` declares, `assistant/command/ping.go:18` masih terpisah |
| 11 Tidak semua plugin Assistant | `ban Surfaces=Userbot\|Assistant` | **IMPLEMENTED** infra / **PARTIAL** enforcement | `core/command.go:44` `Surfaces` + `IsAvailableOn:53`, `registry.go:80` `FindForSurface`, `router.go:127` filter |
| 12 3 layer | Domain+Userbot+Assistant | **PARTIAL** | Shared domain `application/status/service.go:23`, `settings/service.go:19` ada; per-plugin `assistant.go` tidak |
| 13 Unified registry | `CommandRegistry` unified | **PARTIAL** | `application/command/registry.go:12` + `manager.go:69` dual-write `core.Router+UnifiedRegistry` |
| 14 ExecutionContext | `Source,Actor,Chat,Peer,Permissions,Message,Capabilities` | **PARTIAL** | `execution/context.go:9` partial, `core/context.go:196` canonical, `adapter.go:47` `core.Context{Ctx,Args,PeerID}` tanpa Source/Actor/Perms |
| 15 Servicer narrow | `Assistant narrow` ≠ isolated | **PARTIAL** | `assistant` narrow `interaction.MessageInteraction` OK, business `ping/alive` duplikat |
| 16 `/alive` | `RuntimeStatusService→AliveUseCase` | **PARTIAL** | `application/status/service.go:23` shared, `ownerID=0` vs `Perms.OwnerID`, `uptime` source beda |
| 17 `/ping` probe | `PingResult{Latency}` | **PARTIAL** | `FormatResult` unified, probe `time.Now→Since` duplikat |
| 18 Settings | `SettingsService` single | **PARTIAL** | `settings/service.go:238` + `usecase/set.go` shared `plugins/settings:468`, menu `controller.go:80` stub `v1:settings:nav:noop` |
| 19 Addon | `internal/addon` bridge | **MISSING** | `addon/types.go:8` `Capability string` ≠ `execution.Capability`; Assistant tidak auto-discover |
| 20-22 Target & gap `🔴` | Transparent | **PARTIAL→🟡** | Transport 🟢, parity 🔴→🟡 |

---

## 3. Bug16_1.md Sandingan (35 § — tanpa terlewat)

| § | Persyaratan | Status | Bukti |
|---|---|---|---|
| **0 Tujuan** invariant `One impl` | Data-driven | **PARTIAL** | `app.go:76` infra ada, `adapter.go:31` fallback bukan primary |
| **1 P0 Duplicate** | Single UseCase | **PARTIAL** | `ping.go:18` vs `plugins/ping:58` |
| **1 P0 Manager userbot-only** | Capability registry | **PARTIAL** | `manager.go:25` hold 2 registries, 18 plugin belum |
| **1 P0 Dua registry** | Unified | **PARTIAL** | `UnifiedRegistry` `registry.go:12` exists, dual-write `manager.go:169` |
| **1 P1 Hybrid** | V2 only | **PARTIAL** | Assistant clean, `services/callback` legacy masih untuk plugin |
| **1 P1 Menu = domain** | Presentation only | **PARTIAL** | `menu/controller.go:79` placeholder |
| **1 P1 Context** | `ExecutionContext` | **PARTIAL** | `execution/context.go:9` partial |
| **2 Final layout** `application/capability, execution` | Structure | **DONE** | `application/capability/*` `execution/*` `assistant/*` sesuai §2 |
| **3 Capability** `ID,Description,Userbot/Assistant/Inline` | `Capability+SurfaceMask` | **DONE** | `capabilities.go:4` `Surfaces SurfaceMask` + `source.go:30` |
| **4 Plugin provider** `Capabilities()` optional | Compat | **DONE** | `capabilities.go:12` + `manager.go:196` check |
| **5 Command surface-aware** `Surfaces,Permission` | Field | **DONE** | `command.go:44` `Surfaces` + `IsAvailableOn` |
| **6 Unified Registry** dispatcher split | Unified | **PARTIAL** | `registry.go:12` + `adapter.go:31` fallback |
| **7 Handler ≠ transport** `PingUseCase` | Adapter | **PARTIAL** | `adapter.go:14` central, probe duplikat |
| **8 ExecutionContext** `Source+Actor+Chat+Permissions` | Context | **PARTIAL** | `source.go:6` 3 enum, `context.go:9` missing Permissions/Peer |
| **9 Capability vs Permission** | 2 gate | **DONE** | `surface.go:6` vs `middleware.go:158` |
| **10 Unified flow** `Capability→Permission→Context` | Pipeline | **PARTIAL** | `executor.go:121` no capability, `adapter.go:47` no middleware |
| **11 Callback → UseCase** `toggle:foo→UseCase.Toggle` | Delegation | **PARTIAL** | Settings `settings.go:468` PASS, Help FAIL |
| **12 Menu Controller** `Button→Capability→UseCase` | Wiring | **PARTIAL** | `menu/screen.go:15` + `renderer.go:11` OK, `controller.go:222` direct render |
| **13 `/start`** capability-driven menu | Dynamic | **MISSING** | `start.go:15` static `BuildStartScreen` |
| **14 `/help`** registry-driven | Filter | **DONE** assistant / **PARTIAL** plugin | `help.go:20` `CommandsForSurface(Assistant)` |
| **15 `/status`** `RuntimeStatusService` | Shared service | **PARTIAL** | `StatusScreen` static `status.go:19` |
| **16 `/alive`** `RuntimeStatusService→AliveUseCase` | Shared | **PARTIAL** | `alive.go:15` duplicate, `service.go:23` shared |
| **17 `/ping`** `PingUseCase{Result}` | Probe | **PARTIAL** | `FormatResult` shared |
| **18 Settings** `SettingsService` single DB | Unified | **PARTIAL** | `service.go:19` unified, menu stub |
| **19 Addon** `Addon→Capability→Surface` | Declarative | **MISSING** | `addon/types.go:8` isolated |
| **20 Lifecycle** `transactional+reverse+drain` | Order | **PARTIAL** | `manager.go:82` OK, `shutdown.go:11` missing `assistant.Stop` |
| **21 No isolated registry** `Manager→Registries→surfaces` | Invariant | **DONE** wiring / **PARTIAL** usage | `app.go:82` `SetUnifiedRegistry` OK, local `AttachDefaultCommands` violate |
| **22 Phase A** `execution/` | Scaffold | **DONE** | `execution/*.go` 4 files |
| **22 Phase B** `capability/` | Registry | **DONE** files / **PARTIAL** coverage (8/24 plugins) |
| **22 Phase C** Unified registry | Dual dispatcher | **PARTIAL** | `UnifiedRegistry` exists, dual-write |
| **23 Phase D Ping** | Reference | **PARTIAL** | `FormatResult` OK |
| **24 Phase E Alive** | Parity field | **PARTIAL** | `owner` mismatch |
| **25 Phase F Help** | Dynamic | **PARTIAL** | Assistant dynamic, plugin legacy router |
| **26 Phase G Settings** | Domain split | **PARTIAL** | Domain OK, menu not delegating |
| **27 Phase H Callback** | `v2→UseCase` | **PARTIAL** | Settings PASS, generic FAIL |
| **28 Phase I Plugins** table 24 plugins | Explicit ✓/— | **PARTIAL** | 8 ✓, 16 default `SurfaceUserbot` |
| **29 Phase J Delete duplicate** `commands.go` | `adapter.go` only | **NOT STARTED** | `assistant/command/*` 7 files remain |
| **30 Phase K Legacy** `services/callback` | Generic infra | **PARTIAL** | Dual router intentional |
| **31 Graph** `Plugins→Registries→Context→Adapters` | Enforcement | **PARTIAL** | `ExecutionContext` not in `core.CommandHandler` |
| **32 Unique Assistant** `client/interaction/peer/callback/menu/presentation` | Allow | **DONE** | 42 files structure sesuai §32 |
| **33 Code review rule** `new functionality vs adapter?` | Guideline | **PARTIAL** | Duplicates fail rule |
| **34 DoD** 8+7+parity | Checklist | **5/8 functional FAIL, parity PARTIAL** |
| **35 Order** `A→B→C→D→E→F→G→H→I→J→K` | Safety | **A,B DONE, C-K PARTIAL** |

---

## 4. Detail Temuan per Area

### 4.1 Plugin Interface & Manager
- `plugin/plugin.go:11` masih `Name+Commands+Init`; optional `execution.CapabilityProvider` `capabilities.go:12` di `manager.go:196` (`if cp,ok:=p.(CapabilityProvider)`). **PARTIAL**: 8 plugin (`ping,alive,help,settings,admin,media,broadcast,scheduler`) declare `Capabilities() SurfaceUserbot|Assistant`, 16 plugin default `SurfaceUserbot` (`command.go:54-60` fallback).
- `manager.go:82-207` transactional (validate outside lock, `Init` outside lock, compensating `Shutdown`, atomic commit `RegisterBatch` + hooks `174`), `manager.go:223-249` reverse shutdown + hook detach before plugin → **DONE** §20.

### 4.2 Command & Execution
- `core/command.go:44` `Surfaces execution.SurfaceMask` + `IsAvailableOn(Source)` + `execution/source.go:30` bitmask `SurfaceUserbot=1<<0...` → **DONE** §5.
- `application/command/registry.go:12` `UnifiedRegistry` transactional `RegisterBatch` + `FindForSurface(Source)` + `CommandsForSurface` → **DONE** §6, tapi `core.Router` + `assistant.Router` remain dual, `assistant/command/router.go:118-133` local-first bukan sole → **PARTIAL**.
- `execution/source.go:6` 3 enum vs `core/execution.go:14` 6 enum (`Interactive,Scheduled,Assistant,System,Addon,Automation`) — dualitas §8 → **PARTIAL**. `execution/context.go:9` `ExecutionContext{Source,Actor,ChatID,MessageID,Capabilities}` tanpa `Peer/Message/Permissions/Transport` → **PARTIAL**.

### 4.3 Duplicate `/ping /alive /status /help`
- Shared infra: `application/ping/usecase.go:14` `FormatResult`, `application/status/service.go:23` `CollectSnapshot` + `RenderAliveCard`. Kedua surface pakai formatter sama (`plugins/ping:66`, `assistant/ping:27`, `plugins/alive:69`, `assistant/alive:26`) — parity formatter OK.
- Execution duplikat: `plugins/ping:58` `EditOrReply→Since` vs `assistant/ping:18` `Reply→Since` vs seharusnya `PingUseCase.Execute` tunggal; `alive` `ownerID 0` vs `Perms.OwnerID` → **PARTIAL** §16-17.
- `/help` assistant `help.go:20` `CommandsForSurface(Assistant)` + `menu/controller.go:236` dynamic sort → **DONE** registry-driven (fallback `controller.go:120` hard-coded list masih). Plugin help `help.go:229` filter `IsAvailableOn(SourceUserbot)` — **PARTIAL**.

### 4.4 Wiring & Lifecycle
- `app.go:73-84` `capRegistry:=capability.NewRegistry(); unifiedCmdReg:=command.NewUnifiedRegistry(); pluginManager.Set*` + `tgRuntime.assistant.SetUnifiedRegistry(unifiedCmdReg)` → `client.go:222` `SetUnifiedRegistry` → `cmdRouter+menuCtrl` via `CommandSource` interface — no import cycle, inversion benar → **DONE** §21.
- `shutdown.go:11-90` missing `assistant.Stop` sebelum `dispatcher.Stop` — `client/lifecycle.go:8` `TryStart/TryStop` deterministic tapi drain `Assistant→dispatcher→plugin` §20 tidak orchestrated → **PARTIAL**.
- `services/callback/*` 9 files masih untuk plugin (`wiring_core.go:50`, `catalog.go:58`), `assistant/callback/router.go:56` v2 sole untuk assistant → split intentional §32 → **PARTIAL** (Phase K belum generic).

### 4.5 Settings & Addon
- `settings/service.go:19` single `Service{repo,reg,bus,cache}` + `usecase/set.go` shared `plugins/settings:468` (`applySettingAction` dipakai command+callback) → **DONE** unified state §18.
- Assistant menu `controller.go:79` placeholder `v1:settings:nav:noop` tidak delegasi `Service` → **PARTIAL**.
- `addon/types.go:8` `Capability string telegram.read` ≠ `execution.Capability`; `addon/manifest.go:42` tidak register `CapabilityRegistry` → **MISSING** §19.

### 4.6 Menu & Presentation
- `menu/screen.go:15` `Screen{ID,Title,Body,Rows}`, `presentation/renderer.go:11` `RenderScreen→ReplyInlineMarkup`, `screen.go:101` `BuildHelpScreenWithCommands` dynamic → **DONE** structure §12.
- Actions `controller.go:222` direct `Build*Screen→tx.Edit`, `ping` toast-only `268`, `settings` noop `80` — bukan `SettingsUseCase.Toggle` → **PARTIAL** §11-12.

---

## 5. Top Blocker DoD §34

1. **Local handlers shadowing unified registry** — `assistant/command/router.go:118` local-first → 5 command `ping/alive/status/help/start` harus jadi adapter-only (`adapter.go:31` `FindForSurface(SourceAssistant)` primary).
2. **Dua Source enum** — satukan `core.ExecutionSource` 6 vs `execution.Source` 3.
3. **Capability enforcement absen** — `core/executor.go:121` & `assistant/adapter.go:47` tidak `Surface` check sebelum `Permission`. Tambah `CapabilityMiddleware` / `Surface check` pipeline §10.
4. **`status` tanpa source of truth** — tidak ada `plugins/status`; buat `AliveUseCase/StatusUseCase` di `application/status`.
5. **Addon surface gap** — `addon.Manager` publish `execution.Capability{Surfaces}` ke `CapabilityRegistry`.

---

## 6. Rekomendasi Urutan Aman (§35)

`A execution Context → B CapabilityRegistry → C UnifiedCommands → D Ping reference (pola) → E Alive → F Help → G Settings → H Callback→UseCase → I Plugins 24 → J Hapus duplicate (adapter-only) → K Hapus legacy callback`.

Mulai `ping` sebagai reference karena 4 fitur mewakili 4 jenis: `ping` (shared command), `alive` (shared service), `help` (registry-driven), `settings` (shared state+callback).
