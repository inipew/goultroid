package taskengine

import "testing"

func TestSetDurabilityConcurrencyBeforeStart(t *testing.T) {
	engine := NewEngine(DefaultConfig)
	if err := engine.SetDurabilityConcurrency(7); err != nil {
		t.Fatal(err)
	}
	if engine.durability == nil || engine.durability.workers != 7 {
		t.Fatalf("durability workers = %#v, want 7", engine.durability)
	}
	engine.runStarted = true
	if err := engine.SetDurabilityConcurrency(3); err == nil {
		t.Fatal("expected live durability resize to be rejected")
	}
}
