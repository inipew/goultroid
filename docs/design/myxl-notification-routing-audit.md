# Phase N1 — Architecture Audit & Planning: MyXL Notification Routing

**Repository:** `inipew/goultroid`  
**Branch:** `test-next`  
**Baseline M1–M8:** `d6f8bb31e17cc4a41069643b16717b3ab8e94fc7`  
**Verified HEAD:** `d6f8bb31e17cc4a41069643b16717b3ab8e94fc7`  
**Date:** 2026-11-25  
**Status:** Phase N1 Complete (No production code or migrations modified)

---

## 1. Remote HEAD & Baseline Audit

- `origin/test-next` HEAD: `d6f8bb31e17cc4a41069643b16717b3ab8e94fc7`
- `git pull --ff-only`: Already up to date.
- Local `HEAD`: `d6f8bb31e17cc4a41069643b16717b3ab8e94fc7`
- Working tree: Clean.

---

## 2. Existing Notification Flow (M6 Baseline)

Saat ini pada `plugins/myxl/`:
1. `Module.Register` (`plugins/myxl/module.go:44-45`) menyuntikkan `rt.OwnerID` dan `rt.MessageService` ke `Plugin.SetOwnerNotifier`.
2. `handleTokenMaintenanceJob` (`plugins/myxl/maintenance_scheduler.go:225-245`) menjalankan `runTokenMaintenanceCycle`, menyimpan `MaintenanceRunSummary` ke `myxl_token_maintenance_runs`, mencatat diagnostics & structured logs, lalu memanggil `p.maybeNotifyOwner(*cfg, summary, outcomes)`.
3. `maybeNotifyOwner` (`plugins/myxl/maintenance_observability.go:213-253`) mengevaluasi `shouldNotifyOwner`:
   - `RunStatusCompleted` → `cfg.NotifyOnCompletion && summary.RefreshedCount > 0`
   - `RunStatusPartialFailure` / `RunStatusFailed` → `cfg.NotifyOnFailure`
   - `RunStatusCancelled` / `no_op` → tidak mengirim notifikasi
4. Pengiriman menggunakan `msgSvc.SendMessage(notifyCtx, &tg.InputPeerUser{UserID: ownerID}, text)` dengan timeout sinkron `5s` (`maintenanceNotifyTimeout`), best-effort (kegagalan dicatat pada `NotificationsFailed` dan log warning tanpa mengubah status run).

---

## 3. Existing Telegram Delivery APIs

| API / Komponen | Lokasi | Kapabilitas Terverifikasi |
|---|---|---|
| `core.MessageServicer` | `internal/core/telegram_capabilities.go:12-25` | `SendMessage(ctx, tg.InputPeerClass, text) (*tg.Message, error)` |
| `telegram.Service.SendMessage` | `internal/telegram/service.go:619-664` | Menerima semua `tg.InputPeerClass`, mem-parsing HTML (`parseHTML`), dan mengeksekusi via `execNonIdempotentPeerVal` (`RPCExecutor` + rate limiter + `checkPeerError` + `recordBotSent`) |
| `core.PeerResolver` | `internal/core/resolver.go:25-29` | `Resolve(ctx, ref)`, `ResolveUser(ctx, ref)`, `ResolveChat(ctx, ref)` — tersedia di `rt.Resolver` (`internal/module/module.go:59`) |
| `telegram.Resolver` | `internal/telegram/resolver.go:128-345` | Mendukung `"self"`/`"me"`, `@username`, numeric ID (memerlukan `AccessHash` di `PeerCache`/`PeerStorage` untuk user & `-100...` channel/supergroup) |
| `userlog.Service` | `internal/services/userlog/service.go:36-54, 242-301` | Global log destination (`LogDestination{Type, ID, AccessHash, Title}` + `InputPeer()`), tersedia di `rt.UserlogService` (`internal/module/module.go:92`) |

**Kesimpulan Transport:** `rt.MessageService` sudah melewati `RPCExecutor` dan mendukung seluruh tipe peer Telegram tanpa perlu membuat delivery engine atau worker baru.

---

## 4. Destination Support Matrix

| Tipe Tujuan | `tg.InputPeerClass` | Dukungan `MessageService` | Kebutuhan `AccessHash` / Validasi |
|---|---|---|---|
| **Owner PM** | `&tg.InputPeerUser{UserID: ownerID}` (atau `&tg.InputPeerSelf{}`) | Penuh | Tersedia dari `rt.OwnerID` (default destination) |
| **Basic Group** | `&tg.InputPeerChat{ChatID: id}` | Penuh | Tidak butuh `AccessHash` |
| **Supergroup / Log Group** | `&tg.InputPeerChannel{ChannelID: id, AccessHash: hash}` | Penuh | **Wajib** `AccessHash` valid (diperoleh dari `ctx.PeerID`, `Resolver`, atau `UserlogService`) |
| **Bot PM** | `&tg.InputPeerUser{UserID: botID, AccessHash: hash}` | Penuh | **Wajib** `AccessHash` valid + bot harus bisa dikirimi pesan oleh akun pengirim |
| **Channel** | `&tg.InputPeerChannel{ChannelID: id, AccessHash: hash}` | Penuh | **Wajib** `AccessHash` + akun pengirim harus memiliki hak tulis/post di channel |
| **Global Log Group** | `userlogDest.InputPeer()` | Penuh | Mengacu langsung ke `rt.UserlogService.GetDestination(ctx)` |

---

## 5. Existing Log Group Integration Findings

- Goultroid sudah memiliki `internal/services/userlog/service.go` (`rt.UserlogService` pada `module.Runtime`).
- `GetDestination(ctx)` mengembalikan `*userlog.LogDestination` dengan `Type` (`"chat"` | `"channel"`), `ID`, `AccessHash`, dan `Title`, serta `InputPeer()` yang siap dikirim ke `MessageServicer.SendMessage`.
- **Rekomendasi:** MyXL mendukung dua cara untuk log group tanpa menduplikasi tipe delivery:
  1. **Referensi Global Log (`type: "userlog"`):** merujuk dinamis ke `rt.UserlogService.GetDestination(ctx)` tanpa menyalin `peer_id`/`access_hash` ke DB MyXL.
  2. **Group/Supergroup dengan Label (`type: "group"`, `label: "log"`):** memakai jalur delivery grup yang sama jika pengguna ingin grup log khusus MyXL.

---

## 6. Peer Resolution & Permission Findings

1. **Mengapa `AccessHash` harus disimpan atau di-resolve:** `telegram.Resolver.ResolveChat` (`internal/telegram/resolver.go:307-332`) mengembalikan `core.ErrAccessHashMissing` untuk channel/supergroup `-100...` jika belum ada di `PeerCache`/`PeerStorage`. Hal yang sama berlaku untuk `ResolveUser` (`resolver.go:151-175`).
2. **Cara Pengambilan Peer yang Aman ("Pilih Chat Saat Ini" & `@username`):**
   - Menjalankan perintah registrasi di dalam chat tujuan (pola `.setlog` di `plugins/userlog/userlog.go:535-595`) memberikan `ctx.PeerID` bertipe `*tg.InputPeerChannel` / `*tg.InputPeerChat` / `*tg.InputPeerUser` lengkap dengan `AccessHash`.
   - Untuk Bot PM atau `@username` channel/group dari private chat, `rt.Resolver.Resolve(ctx, "@username")` dapat me-resolve `InputPeerClass` beserta `AccessHash` dari Telegram API.
3. **Validasi Izin Posting & Akses:**
   - Cara paling deterministik dan tanpa asumsi API admin tambahan adalah melakukan **test delivery** (atau pre-check `SendMessage` test) saat konfirmasi destination: jika akun tidak memiliki hak posting di channel (`CHAT_WRITE_FORBIDDEN`) atau diblokir bot (`USER_IS_BLOCKED`), Telegram RPC langsung mengembalikan error sehingga destination ditolak sebelum disimpan.
4. **Keamanan & Otorisasi:**
   - Seluruh mutasi destination dan perintah `.myxl schedule notify ...` tetap memakai `core.PermissionOwner`.
   - Menu a2 tetap memakai `feature.OwnerPolicy(execution.SurfaceAssistant)` (`PrivateOnly = true`) sehingga daftar peer ID tidak bocor ke grup publik.

---

## 7. Persistence Options

Saat ini `myxl_token_maintenance_config` (`myxl.008`) menyimpan singleton row `id = 1` dengan `notify_on_completion` (`0`) dan `notify_on_failure` (`1`).

| Opsi | Skema (`myxl.009`) | Kelebihan | Kekurangan |
|---|---|---|---|
| **A. Kolom di `myxl_token_maintenance_config`** | Tambah `notify_on_partial_failure INTEGER NOT NULL DEFAULT 1` + `destinations_json TEXT NOT NULL DEFAULT '[]'` | 1 transaksi atomik di `SaveTokenMaintenanceConfig` (pola sama dengan `selected_msisdns_json`), mudah dibatasi maks 5 item | Tidak terindeks per-destination (tidak perlu karena maks 5 item) |
| **B. Tabel `myxl_notification_destinations` terpisah** | Tabel relasional tersendiri + `notify_on_partial_failure` di config | Lebih terstruktur jika jumlah destination besar | Membutuhkan transaksi lintas 2 tabel saat mutasi config |

**Rekomendasi N1:** Gunakan **Opsi A** (`myxl.009` menambah `notify_on_partial_failure` dengan default `1` dan `destinations_json` bounded maksimal `5` destination), karena jumlah destination dibatasi kecil (`<= 5`), menjaga atomisitas `SaveTokenMaintenanceConfig`, dan kompatibel penuh dengan data M6 (`destinations_json = '[]'` otomatis fallback ke default Owner PM bila `NotifyOnCompletion` / `NotifyOnFailure` aktif, atau dimigrasi eksplisit ke entri `owner`).

---

## 8. a2 & Command Integration Findings

- **Single Authority:** Semua perubahan preferensi event (`completion`, `failure`, `partial_failure`) dan manajemen destination (`list`, `add`, `remove`, `enable`, `disable`, `test`) masuk melalui `MaintenanceService` (`plugins/myxl/maintenance_service.go`).
- **Command:** Memperluas `case "notify":` di `plugins/myxl/maintenance_telegram.go` tanpa parser baru.
- **Menu a2:** `assistantActionSlotCount = 32` di `plugins/myxl/assistant_interaction.go` masih memiliki kapasitas slot yang cukup untuk layar `BuildMaintenanceNotifyScreen` (toggle event, daftar destination aktif/nonaktif, tambah via `@username`/ID atau panduan "jalankan di chat tujuan", hapus, dan test destination) menggunakan `assistantScreenInput` wizard yang sudah ada.

---

## 9. Resource & Architecture Constraints

- Tetap **0** goroutine permanen, **0** ticker, **0** worker pool, **0** queue, **0** retry engine baru.
- Multi-destination delivery berjalan berurutan di akhir `handleTokenMaintenanceJob` setelah run summary tersimpan, dengan batas:
  - Maksimum `5` destination aktif (`MaxNotificationDestinations = 5`).
  - Timeout per pengiriman dibatasi (mis. `5s` per destination atau total budget `15s` untuk maks 5 destination, non-fatal).
  - Kegagalan satu destination tidak menghentikan pengiriman ke destination berikutnya dan tidak mengubah outcome maintenance.

---

## 10. Gap Analysis

1. `Module.Register` belum menyuntikkan `rt.Resolver` (`core.PeerResolver`) dan `rt.UserlogService` (`*userlog.Service`) ke `myxl.Plugin`.
2. `TokenMaintenanceConfig` belum membedakan `NotifyOnPartialFailure` dari `NotifyOnFailure`, dan belum memiliki daftar destination terstruktur.
3. Belum ada mekanisme resolusi/penyimpanan peer destination (`owner`, `group`, `userlog`, `bot_pm`, `channel`) beserta validasi test kirim.
4. `MaintenanceDiagnostics` belum mencatat pengiriman per-destination secara agregat (tetap tanpa label peer ID ber-cardinality tinggi).

---

## 11. Recommended Architecture

1. Perluas `Module.Register` untuk menyuntikkan `rt.Resolver` dan `rt.UserlogService` (opsional/nil-safe) ke `myxl.Plugin`.
2. Model destination bounded (`<= 5`) menyimpan `{ID, Type, Label, PeerID, AccessHash, Enabled}` (dengan `type: "owner"` dan `type: "userlog"` yang tidak perlu menyimpan `AccessHash`).
3. Pisahkan evaluasi **Event Policy** (`completion`, `partial_failure`, `failure`, `cancelled`=off, `no_op`=off) dari **Destination Resolver** (`owner`, `userlog`, `group`/`channel`/`bot_pm`).
4. Pengiriman multi-destination dilakukan berurutan, bounded timeout, best-effort melalui `rt.MessageService`.

---

## 12. Risks & Unresolved Decisions (untuk Diputuskan di N2)

1. **Multi-destination:** Apakah satu event dikirim ke semua destination yang `Enabled == true` sekaligus? *(Rekomendasi: Ya, dibatasi maks 5.)*
2. **Global Log Group:** Apakah `type: "userlog"` langsung merujuk `rt.UserlogService` secara dinamis? *(Rekomendasi: Ya, tanpa duplikasi peer ID.)*
3. **Bot PM:** Apakah target Bot PM adalah bot eksternal (via `@username`/peer) dan bukan bot assistant internal? *(Perlu konfirmasi di N2; secara teknis `MessageService` mengirim sebagai akun userbot ke bot eksternal mana pun yang pernah berinteraksi/resolved.)*
4. **Default Owner PM:** Apakah saat upgrade dari M6 tanpa `destinations`, Owner PM tetap aktif secara default? *(Rekomendasi: Ya, agar kompatibel 100% dengan M6.)*
5. **Scope Manual Run:** Apakah `.myxl schedule run` memakai kebijakan notifikasi yang sama dengan scheduled run? *(Rekomendasi: Ya.)*

---

## 13. Proposed N2–N9 Roadmap

| Fase | Fokus |
|---|---|
| **N2** | Design & Contract (`docs/design/myxl-notification-routing-design.md`): model destination, event policy, migrasi `myxl.009`, kompatibilitas M6, kontrak command & a2 |
| **N3** | Persistence & Destination Core: migrasi `myxl.009`, validasi destination, batas maks 5, repository |
| **N4** | Telegram Delivery Integration: destination resolver (`owner`, `userlog`, `group`, `bot_pm`, `channel`), multi-destination best-effort delivery |
| **N5** | Command Management: perluasan `.myxl schedule notify ...` dan `MaintenanceService` |
| **N6** | a2 Menu Integration: `MenuManager` + `assistant_interaction.go` notification routing screens & test destination |
| **N7** | Diagnostics & Security: structured logging aman (tanpa bocor peer ID sensitif/token), diagnostics counters |
| **N8** | Hardening & Performance: test kegagalan parsial antar-destination, peer tidak valid/terhapus, SQLite contention, race suite |
| **N9** | Documentation Closure: finalisasi dokumentasi N1–N9 |

---

## 14. File & API yang Diperkirakan Berubah (Fase N3–N7)

- `plugins/myxl/module.go` (inject `rt.Resolver`, `rt.UserlogService`)
- `plugins/myxl/myxl.go` (field resolver & userlog di `Plugin`)
- `plugins/myxl/migration.go` (`myxl.009`)
- `plugins/myxl/maintenance_types.go` (`NotificationDestination`, `NotifyOnPartialFailure`)
- `plugins/myxl/maintenance_repo.go` (encode/decode `notify_on_partial_failure` & `destinations_json`)
- `plugins/myxl/maintenance_service.go` (mutasi event preferences & destination CRUD/test)
- `plugins/myxl/maintenance_observability.go` (destination resolver & multi-destination `maybeNotifyDestinations`)
- `plugins/myxl/maintenance_telegram.go`, `plugins/myxl/menu.go`, `plugins/myxl/assistant_interaction.go` (UX command & menu a2)
