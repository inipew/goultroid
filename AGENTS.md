# Repository Guidelines

## Project Structure & Module Organization

`cmd/goultroid` contains the application entry point. Core runtime, Telegram integration, configuration, database access, and shared services live under `internal/`; retain its package-boundary rules, which are checked by `internal/architecture` tests. Userbot commands are organized as independent modules in `plugins/<feature>`. Generated module registration is maintained in `internal/app/generated_modules.go` by `tools/featuregen`. Keep architecture decisions in `docs/adr`, and treat `data/` as local runtime state rather than source.

## Build, Test, and Development Commands

- `cp .env.example .env` configures a local instance; never commit the resulting file.
- `go run ./cmd/goultroid` runs the bot locally.
- `go build -v ./cmd/goultroid` verifies that the production binary builds.
- `go test -v -race ./...` runs the complete unit suite with race detection, matching CI.
- `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out` reports coverage.
- `go vet ./...` and `golangci-lint run --timeout=3m` run the repository checks.
- `go run ./tools/featuregen` refreshes module registration after plugin changes; confirm `internal/app/generated_modules.go` is current.

## Coding Style & Naming Conventions

Target the Go version declared in `go.mod`. Format every changed Go file with `gofmt` (and `goimports` when imports change); CI rejects unformatted code. Follow idiomatic Go: tabs for indentation, exported identifiers in `PascalCase`, unexported identifiers in `camelCase`, concise package names, and lower-case error messages without punctuation. Keep plugin-facing behavior inside its feature package and use the shared abstractions rather than raw Telegram clients where an existing boundary provides one.

## Testing Guidelines

Place tests next to their implementation as `*_test.go`; name tests `TestBehavior` or `TestBehavior_Condition`. Add focused coverage for changed behavior, including error and permission paths when relevant. Run the full race suite before opening a pull request, especially when changing scheduling, dispatch, storage, or plugin registration.

## Commit & Pull Request Guidelines

Use short, imperative commit subjects. Existing history favors Conventional Commit-style scopes, for example `fix(core): harden peer resolution`, `feat(quote): add media previews`, and `test(ocr): cover retry behavior`. Pull requests should explain the behavior change, identify configuration or migration effects, link relevant issues, and list verification commands. Include screenshots only when rendered user-facing output changes.

## Security & Configuration

Never commit `.env`, Telegram API credentials, session files, or contents of `data/`. Start from `.env.example`, and keep local sessions under the configured data path.
