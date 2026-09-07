# Bug13 Baseline - 2026-09-07 (Pre-Refactor)

Catatan metrik sebelum Fase 1 dimulai. Jadi referensi regresi.

## File Sizes

```
internal/app/app.go: 124 lines
internal/app/bootstrap.go: 291 lines
internal/app/dependencies.go: 64 lines
internal/app/lifecycle.go: 82 lines
internal/telegram/dispatcher.go: 1055 lines
internal/services/callback/router.go: 398 lines (Dispatch 281)
internal/services/callback/state.go: 226 lines
internal/services/callback/types.go: 438 lines
internal/settings/service.go: 427 lines
internal/settings/types.go: 269 lines
plugins/settings/settings.go: 651 lines
internal/ui: 12 files, 1479 lines total
```

## Tool Status

- `go vet ./...`: PASS (0 errors)
- `go test ./internal/core -run TestArchitecture -v`: 8 tests PASS (after Fase 0.1)
- `go test ./internal/services/callback -run TestContract -v`: 2 tests PASS
- `go test ./internal/settings -run TestContract -v`: 4 tests PASS
- `go test -race ./internal/core ./internal/settings ./internal/services/callback`: PASS
- `gofmt -l .`: many files unformatted (pre-existing, not Fase 0 regression) - Fase 0 file `arch_test.go`, `contract_test.go` formatted
- `.golangci.yml`: created, not yet enforced in CI (warn mode)

## Architecture Tests Existing

- `internal/core/arch_test.go:15` CoreLayerIsolation
- `internal/core/arch_test.go:50` SettingsLayerIsolation
- `internal/core/arch_test.go:81` CallbackLayerIsolation
- `internal/core/arch_test.go:112` ScopeInvariants
- New: `TestArchitecture_UILayerIsolation`, `DomainNoTGImport`, `PluginNoDirectDB`, `TelegramAdapterNoBusinessRule`

## Known Remaining Debt (untuk Fase 1)

- `Selected string` masih di `plugins/settings/settings.go:38`
- 9 SetX masih public di dispatcher
- `inline/cache.go` masih `stopCh`
- 45 `context.Background()` fallback tersebar
- `router.go:108` literal `"noop"` dan `settings.go:294` literal

## Full Race Suite (sample)

```
go test -race ./internal/core ./internal/settings ./internal/services/callback 1.5-1.7s each PASS
```

Full `go test -race ./...` belum dijalankan di baseline ini karena lama - akan dijalankan per Fase 1 task.
