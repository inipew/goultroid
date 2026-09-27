package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBenchmarkLine(t *testing.T) {
	line := "BenchmarkRegistryResolveOwnedExplicitP0D/custom/2-5  8123456  169.2 ns/op  64 B/op  3 allocs/op  2 handlers"
	s, ok := parseBenchmarkLine(line)
	if !ok {
		t.Fatal("benchmark line not parsed")
	}
	if s.name != "BenchmarkRegistryResolveOwnedExplicitP0D/custom/2" {
		t.Fatalf("name=%q", s.name)
	}
	if s.nsOp == nil || *s.nsOp != 169.2 {
		t.Fatalf("ns/op=%v", s.nsOp)
	}
	if s.bytesOp == nil || *s.bytesOp != 64 {
		t.Fatalf("B/op=%v", s.bytesOp)
	}
	if s.allocsOp == nil || *s.allocsOp != 3 {
		t.Fatalf("allocs/op=%v", s.allocsOp)
	}
}

func TestMedian(t *testing.T) {
	if got := median([]float64{5, 1, 3, 2, 4}); got != 3 {
		t.Fatalf("median odd=%v", got)
	}
	if got := median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Fatalf("median even=%v", got)
	}
}

func TestReviewBundleComplete(t *testing.T) {
	dir := t.TempDir()
	manifest := strings.Join([]string{
		"timestamp_utc=20260927T010000Z",
		"head=0123456789abcdef",
		"branch=test-next",
		"dirty=no",
		"go_version=go version go1.27.0 linux/amd64",
	}, "\n") + "\n"
	writeTestFile(t, filepath.Join(dir, "manifest.txt"), manifest)

	writeBenchmarkRuns(t, filepath.Join(dir, "inline.txt"), []string{
		"BenchmarkRegistryResolveOwnedExplicitP0D/custom/2-5",
		"BenchmarkCacheStartedSaturatedChurnP3A/working-set/501-5",
	})
	writeBenchmarkRuns(t, filepath.Join(dir, "ratelimit.txt"), []string{
		"BenchmarkLimiterSaturatedCapacityP3A-5",
	})
	writeBenchmarkRuns(t, filepath.Join(dir, "telegram-rpc-limiter.txt"), []string{
		"BenchmarkHierarchicalRPCLimiterCardinality/4096-5",
	})

	summary, err := reviewBundle(dir, "0123456789abcdef")
	if err != nil {
		t.Fatalf("reviewBundle: %v", err)
	}
	if !strings.Contains(summary, "Evidence shape: **COMPLETE**") {
		t.Fatalf("summary missing complete marker:\n%s", summary)
	}
	if !strings.Contains(summary, "Median ns/op") {
		t.Fatal("summary missing benchmark table")
	}
}

func TestReviewBundleRejectsDirtyOrWrongRunCount(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "manifest.txt"), "head=abc\ndirty=yes\n")
	writeTestFile(t, filepath.Join(dir, "inline.txt"), "BenchmarkRegistryResolveOwnedExplicitP0D/custom/2-5 1 170 ns/op 64 B/op 3 allocs/op\nBenchmarkCacheHighCardinalityP3A/hit/500-5 1 210 ns/op 320 B/op 1 allocs/op\n")
	writeTestFile(t, filepath.Join(dir, "ratelimit.txt"), "BenchmarkLimiterSaturatedCapacityP3A-5 1 250 ns/op 48 B/op 3 allocs/op\n")
	writeTestFile(t, filepath.Join(dir, "telegram-rpc-limiter.txt"), "BenchmarkHierarchicalRPCLimiterCardinality/4096-5 1 1000 ns/op 0 B/op 0 allocs/op\n")

	_, err := reviewBundle(dir, "")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "dirty") || !strings.Contains(err.Error(), "runs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeBenchmarkRuns(t *testing.T, path string, names []string) {
	t.Helper()
	var b strings.Builder
	for _, name := range names {
		for i := 0; i < expectedRuns; i++ {
			b.WriteString(name)
			b.WriteString(" 1000000 200 ns/op 64 B/op 2 allocs/op\n")
		}
	}
	writeTestFile(t, path, b.String())
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
