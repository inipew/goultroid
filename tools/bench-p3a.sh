#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

head_sha="$(git rev-parse HEAD)"
short_sha="${head_sha:0:12}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
out_dir="${P3A_OUTPUT_DIR:-${TMPDIR:-/tmp}/goultroid-p3a-${short_sha}-${stamp}}"
mkdir -p "$out_dir"

status="$(git status --porcelain)"
if [[ -n "$status" && "${P3A_ALLOW_DIRTY:-0}" != "1" ]]; then
  echo "P3-A benchmark requires a clean checkout." >&2
  echo "Set P3A_ALLOW_DIRTY=1 only for non-acceptance diagnostics." >&2
  printf '%s\n' "$status" >&2
  exit 2
fi

manifest="$out_dir/manifest.txt"
{
  echo "Goultroid P3-A benchmark evidence"
  echo "timestamp_utc=$stamp"
  echo "head=$head_sha"
  echo "branch=$(git branch --show-current || true)"
  echo "commit=$(git log -1 --pretty=%s)"
  echo "dirty=$([[ -n "$status" ]] && echo yes || echo no)"
  echo "go_version=$(go version)"
  echo "goos=$(go env GOOS)"
  echo "goarch=$(go env GOARCH)"
  echo "cgo_enabled=$(go env CGO_ENABLED)"
  echo "gomaxprocs_env=${GOMAXPROCS:-runtime-default}"
  echo "uname=$(uname -a 2>/dev/null || true)"
  if command -v nproc >/dev/null 2>&1; then
    echo "logical_cpus=$(nproc)"
  fi
  if command -v lscpu >/dev/null 2>&1; then
    cpu_model="$(lscpu | awk -F: '/Model name/ {sub(/^[ \t]+/, "", $2); print $2; exit}')"
    [[ -n "$cpu_model" ]] && echo "cpu_model=$cpu_model"
  elif [[ -r /proc/cpuinfo ]]; then
    cpu_model="$(awk -F: '/model name/ {sub(/^[ \t]+/, "", $2); print $2; exit}' /proc/cpuinfo)"
    [[ -n "$cpu_model" ]] && echo "cpu_model=$cpu_model"
  fi
} | tee "$manifest"

echo
echo "[1/3] Inline registry + cache"
go test -run='^$' \
  -bench='BenchmarkRegistryResolveOwnedExplicitP0D|BenchmarkCache.*P3A' \
  -benchmem -benchtime=1s -count=5 \
  ./internal/services/inline | tee "$out_dir/inline.txt"

echo
echo "[2/3] Generic rate limiter"
go test -run='^$' \
  -bench='BenchmarkLimiter.*P3A' \
  -benchmem -benchtime=1s -count=5 \
  ./internal/services/ratelimit | tee "$out_dir/ratelimit.txt"

echo
echo "[3/3] Production hierarchical RPC limiter"
go test -run='^
P3-A evidence bundle

HEAD: $head_sha
Generated: $stamp

Files:
- manifest.txt
- inline.txt
- ratelimit.txt
- telegram-rpc-limiter.txt
- summary.md

Formal P3-A review should use a clean checkout. If P3A_ALLOW_DIRTY=1 was used,
treat the result as diagnostic only.
EOF2

echo
echo "P3-A benchmark evidence captured in: $out_dir"
echo "Share summary.md plus the raw bundle files for closure review."
 \
  -bench='BenchmarkHierarchicalRPCLimiter' \
  -benchmem -benchtime=1s -count=5 \
  ./internal/telegram | tee "$out_dir/telegram-rpc-limiter.txt"

echo
echo "[review] Validate bundle + aggregate medians"
go run ./tools/p3areview -bundle "$out_dir" -expect-head "$head_sha"

cat > "$out_dir/README.txt" <<EOF2
P3-A evidence bundle

HEAD: $head_sha
Generated: $stamp

Files:
- manifest.txt
- inline.txt
- ratelimit.txt
- telegram-rpc-limiter.txt

Formal P3-A review should use a clean checkout. If P3A_ALLOW_DIRTY=1 was used,
treat the result as diagnostic only.
EOF2

echo
echo "P3-A benchmark evidence captured in: $out_dir"
echo "Share manifest.txt plus the three benchmark output files for closure review."
