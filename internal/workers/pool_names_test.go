package workers

import "testing"

func TestManager_InteractivePoolIsProvisioned(t *testing.T) {
	manager := NewManager()
	if PoolInteractive != "interactive" {
		t.Fatalf("PoolInteractive = %q, want interactive", PoolInteractive)
	}
	if _, ok := manager.Get(PoolInteractive); !ok {
		t.Fatal("interactive workload pool is not provisioned")
	}
}
