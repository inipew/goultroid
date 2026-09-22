#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "== P7-L acceptance =="
echo "HEAD: $(git rev-parse HEAD 2>/dev/null || echo unknown)"
echo "Go:   $(go version)"

echo
echo "[1/6] go vet"
go vet ./...

echo
echo "[2/6] build"
tmp_bin="$(mktemp -u "${TMPDIR:-/tmp}/goultroid-p7l.XXXXXX")"
trap 'rm -f "$tmp_bin"' EXIT
go build -o "$tmp_bin" ./cmd/goultroid

echo
echo "[3/6] repository tests"
go test ./...

echo
echo "[4/6] group-plane race suite"
go test -race \
  ./internal/assistant/... \
  ./internal/core \
  ./internal/admission \
  ./internal/taskengine \
  ./internal/app \
  ./internal/services/groupstate \
  ./internal/telegram \
  ./plugins/admin \
  ./plugins/blacklist \
  ./plugins/filters

echo
echo "[5/6] P7-L focused acceptance"
go test -v \
  ./internal/assistant/command \
  ./internal/assistant/client \
  ./internal/assistant/groupauth \
  ./internal/assistant/groupevents \
  ./internal/assistant/grouprules \
  ./internal/core \
  ./internal/admission \
  ./internal/taskengine \
  ./internal/app \
  ./internal/services/groupstate \
  ./internal/telegram \
  ./internal/architecture \
  ./plugins/filters \
  -run 'P7L|P7C|P7G|P7H|P7I|P7J|P7K|TelegramRoleResolver|SQLiteStore|HierarchicalRPCLimiter|RPCExecutor'

echo
echo "[6/6] irrelevant-group hot-path benchmark"
go test ./internal/assistant/client \
  -run '^$' \
  -bench '^BenchmarkP7LIrrelevantGroupMessageHotPath$' \
  -benchmem \
  -count=5

echo
echo "P7-L acceptance gates completed successfully."
