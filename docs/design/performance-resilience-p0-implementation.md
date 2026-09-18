# Implementasi P0 — sentralisasi outbound Telegram

Tanggal: 18 September 2026

Audit asal: `performance-resilience-conformance-audit-2026-09-18.md`

## Status

P0 pada bagian “Correctness dan policy tunggal” telah diimplementasikan. Pekerjaan ini tidak menyatakan P1–P3 selesai; khususnya hierarchical limiter produksi dan stale-peer refresh terpusat tetap pekerjaan P1.

## Perubahan behavior

- `retryOnFloodWait` dihapus; tidak ada lagi executor sementara yang memberi label seluruh operasi sebagai read-only atau mengabaikan child context.
- Read RPC yang sebelumnya bypass (`GetMessage`, full user/chat, username resolve, profile photo lookup, safe purge, dan dialog warm-up) sekarang melalui `RPCExecutor`.
- Resolver selalu memiliki executor dan tidak lagi fallback ke `RetryRPC` untuk username network resolve.
- Assistant tidak lagi memberikan raw `tdClient.API()` ke interaction, peer fetcher, inline servicer, atau command menu. Seluruhnya menerima `managedAPI`.
- Port `internal/assistant/rpc.Executor` menjaga dependency satu arah. Adapter produksi di `internal/app/assistant_rpc.go` meneruskan operasi ke application-owned `telegram.RPCExecutor`.
- Assistant operation mempunyai kind eksplisit: read-only, idempotent mutation, atau non-idempotent mutation.
- Non-idempotent mutation dipaksa satu attempt oleh adapter produksi.
- Upload/download memakai parent context dan operation deadline 30 menit. Retry internal executor untuk satu streaming transfer dimatikan (`MaxAttempts: 1`).
- `photos.uploadProfilePhoto` diklasifikasikan non-idempotent untuk mencegah duplicate side effect saat outcome transport ambigu.
- Family RPC diturunkan dari namespace method (`messages`, `channels`, `users`, dan seterusnya) sehingga metadata limiter/metrics tidak kosong.

## Guardrail

`internal/architecture/telegram_rpc_test.go` mencegah regresi berikut:

- pengembalian `retryOnFloodWait`;
- fallback `RetryRPC` pada resolver;
- assistant interaction/fetcher/inline/menu kembali menerima raw `tdClient.API()`;
- hilangnya managed assistant API atau explicit operation kind.

Guard ini sengaja menjaga ingress/wiring. Low-level raw call tetap berada di adapter/service closure karena di sanalah request aktual harus dibuat setelah executor memberi izin.

## File utama

- `internal/telegram/service.go`
- `internal/telegram/purge_safe.go`
- `internal/telegram/profile_photo.go`
- `internal/telegram/resolver.go`
- `internal/telegram/client.go`
- `internal/assistant/rpc/executor.go`
- `internal/assistant/client/managed_api.go`
- `internal/assistant/client/client.go`
- `internal/assistant/interaction/message.go`
- `internal/assistant/menu/command_menu.go`
- `internal/assistant/app.go`
- `internal/app/assistant_rpc.go`
- `internal/app/wiring_telegram.go`
- `internal/architecture/telegram_rpc_test.go`

## Compatibility

- Public `core.TelegramServicer` tidak berubah.
- Constructor assistant tetap kompatibel. Standalone tests memakai `DirectExecutor`, sedangkan production app menggantinya dengan adapter shared executor sebelum `Start`.
- Interface command menu dilonggarkan dari concrete `*tg.Client` menjadi minimal method interface.
- Transport authentication, `Self`, dan update recovery loop tetap menjadi lifecycle transport operation; semuanya memakai lifecycle context dan bukan service RPC retry path.

## Verifikasi

| Perintah | Hasil |
|---|---|
| Focused unit tests Telegram, assistant, app, architecture | Lulus |
| Focused `-race` Telegram, assistant, app, architecture | Lulus |
| `go test -race ./...` | Lulus; dijalankan dengan izin localhost yang diperlukan dua package httptest |
| `go vet ./...` | Lulus |
| `go build -v ./cmd/goultroid` | Lulus |
| `git diff --check` | Lulus |

Tidak ada benchmark performance claim pada P0 ini; perubahan ditujukan pada correctness dan konsistensi policy.

## Risiko tersisa / P1

- `RPCExecutor` produksi masih memakai noop limiter sampai adapter atomic multi-dimension selesai.
- `RPCMeta.RefreshPeer` belum menjadi mekanisme stale recovery tunggal; assistant masih mempunyai bounded one-shot re-resolve di atas managed API.
- Peer key limiter belum ditetapkan karena desain bounded-cardinality/privacy harus diselesaikan bersama P1.
- Warm-up masih direct lifecycle goroutine; RPC-nya sudah terkelola, sedangkan ownership goroutine merupakan pekerjaan P2.
- Transport/auth/update calls tidak masuk service executor karena merupakan lifecycle milik gotd transport.

## Rollback

Rollback dilakukan per boundary: kembalikan wiring assistant ke raw API hanya bersamaan dengan penghapusan guard test dan adapter. Jangan menghidupkan kembali `retryOnFloodWait`; bila compatibility rollback diperlukan, gunakan shared executor dengan metadata eksplisit.
