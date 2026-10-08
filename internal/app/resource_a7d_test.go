package app

import (
	"fmt"
	"runtime"
	"testing"
)

// This repeated acceptance uses the canonical four-feature a2 runtime and
// real Plugin Manager restart proofs; it does not create its own session
// engine or bypass scoped lifecycle. Resource samples are diagnostic because
// a Go heap high-water mark is not a production RSS or an MTProto soak test.
func TestA7DRepeatedFourFeatureDurableRestartAndSettling(t *testing.T) {
	const rounds = 3
	var baselineMem, afterMem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&baselineMem)
	baselineG := runtime.NumGoroutine()
	var settledG [rounds]int
	var settledHeap [rounds]uint64

	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			t.Run("canonical_four_feature_store", TestA7C2FourFeatureDurableRestartAndScopedCallbackPressure)
			t.Run("managed_reload_sibling_isolation", TestA7C2ManagerReloadOneFeatureKeepsSiblingDurableCallbacks)
		})
		runtime.GC()
		runtime.ReadMemStats(&afterMem)
		settledG[round] = runtime.NumGoroutine()
		settledHeap[round] = afterMem.HeapAlloc
	}

	// Guard against *repeated* leaked workers while allowing the small
	// runtime/test goroutine variations seen across Go versions and hosts.
	// Individual round proofs enforce the deterministic session/state bounds.
	if settledG[rounds-1] > baselineG+8 && settledG[rounds-1] > settledG[0]+4 {
		t.Fatalf("goroutines accumulated over %d scope-restart rounds: baseline=%d settled=%v",
			rounds, baselineG, settledG)
	}
	t.Logf("A7-D host=%s/%s go=%s rounds=%d goroutines baseline=%d settled=%v heap baseline=%d settled=%v; heap/RSS diagnostic only",
		runtime.GOOS, runtime.GOARCH, runtime.Version(), rounds, baselineG,
		settledG, baselineMem.HeapAlloc, settledHeap)
}
