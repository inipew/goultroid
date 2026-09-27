package app

import (
	"runtime"
	"testing"
)

func TestProcessMemoryDiagnostics(t *testing.T) {
	got := processMemoryDiagnostics()
	if got.NumGoroutine < 1 || got.Sys < got.HeapAlloc || got.HeapObjects == 0 {
		t.Fatalf("invalid process memory snapshot: %+v", got)
	}
	if runtime.GOOS == "linux" && !got.RSSAvailable {
		t.Fatal("Linux process RSS unavailable")
	}
}
