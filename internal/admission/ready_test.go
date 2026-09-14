package admission

import (
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

func testQuantum() map[tasks.PriorityClass]int {
	return map[tasks.PriorityClass]int{
		tasks.PriorityInteractive: 1,
		tasks.PriorityNormal:      1,
		tasks.PriorityBackground:  1,
		tasks.PriorityMaintenance: 1,
	}
}

func TestReady_PreservesFIFOWithinOwner(t *testing.T) {
	ready, err := NewReady(testQuantum(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []tasks.TaskID{"a1", "a2", "a3"} {
		if err := ready.Enqueue(Item{TaskID: id, Pool: "p", Class: tasks.PriorityNormal, Owner: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []tasks.TaskID{"a1", "a2", "a3"} {
		got, ok := ready.Next("p", nil)
		if !ok || got.TaskID != want {
			t.Fatalf("Next() = %q,%v want %q,true", got.TaskID, ok, want)
		}
	}
}

func TestReady_BlockedOwnerDoesNotBlockOtherOwner(t *testing.T) {
	ready, _ := NewReady(testQuantum(), 1)
	_ = ready.Enqueue(Item{TaskID: "blocked", Pool: "p", Class: tasks.PriorityNormal, Owner: "a"})
	_ = ready.Enqueue(Item{TaskID: "ready", Pool: "p", Class: tasks.PriorityNormal, Owner: "b"})

	got, ok := ready.Next("p", func(item Item) bool { return item.Owner != "a" })
	if !ok || got.TaskID != "ready" {
		t.Fatalf("Next() = %q,%v want ready,true", got.TaskID, ok)
	}
	if ready.Len() != 1 {
		t.Fatalf("Len() = %d want 1", ready.Len())
	}
	got, ok = ready.Next("p", nil)
	if !ok || got.TaskID != "blocked" {
		t.Fatalf("blocked head was lost: %q,%v", got.TaskID, ok)
	}
}

func TestReady_RemoveIsExactAndDoesNotLeaveGhostEntry(t *testing.T) {
	ready, _ := NewReady(testQuantum(), 1)
	_ = ready.Enqueue(Item{TaskID: "a1", Pool: "p", Class: tasks.PriorityNormal, Owner: "a"})
	_ = ready.Enqueue(Item{TaskID: "a2", Pool: "p", Class: tasks.PriorityNormal, Owner: "a"})
	if removed, ok := ready.Remove("a1"); !ok || removed.TaskID != "a1" {
		t.Fatalf("Remove() = %+v,%v", removed, ok)
	}
	if _, ok := ready.Remove("a1"); ok {
		t.Fatal("removed task remained indexed")
	}
	got, ok := ready.Next("p", nil)
	if !ok || got.TaskID != "a2" {
		t.Fatalf("Next() = %q,%v want a2,true", got.TaskID, ok)
	}
}

func TestReady_OwnerDRRProvidesWeightedStartOpportunities(t *testing.T) {
	ready, _ := NewReady(testQuantum(), 1)
	if err := ready.SetOwnerWeight("heavy", 3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		_ = ready.Enqueue(Item{TaskID: tasks.TaskID("h" + string(rune(0x100+i))), Pool: "p", Class: tasks.PriorityNormal, Owner: "heavy"})
		_ = ready.Enqueue(Item{TaskID: tasks.TaskID("l" + string(rune(0x200+i))), Pool: "p", Class: tasks.PriorityNormal, Owner: "light"})
	}
	counts := map[tasks.QuotaOwner]int{}
	for i := 0; i < 80; i++ {
		item, ok := ready.Next("p", nil)
		if !ok {
			t.Fatal("queue unexpectedly empty")
		}
		counts[item.Owner]++
	}
	if counts["heavy"] < 55 || counts["light"] < 15 {
		t.Fatalf("unexpected weighted DRR split: %+v", counts)
	}
}

func TestReady_ClassDRRDoesNotStarveMaintenance(t *testing.T) {
	quantum := testQuantum()
	quantum[tasks.PriorityInteractive] = 8
	ready, _ := NewReady(quantum, 1)
	for i := 0; i < 64; i++ {
		_ = ready.Enqueue(Item{TaskID: tasks.TaskID("i" + string(rune(0x300+i))), Pool: "p", Class: tasks.PriorityInteractive, Owner: "interactive"})
		_ = ready.Enqueue(Item{TaskID: tasks.TaskID("m" + string(rune(0x400+i))), Pool: "p", Class: tasks.PriorityMaintenance, Owner: "maintenance"})
	}
	maintenance := 0
	for i := 0; i < 32; i++ {
		item, ok := ready.Next("p", nil)
		if !ok {
			t.Fatal("queue unexpectedly empty")
		}
		if item.Class == tasks.PriorityMaintenance {
			maintenance++
		}
	}
	if maintenance == 0 {
		t.Fatal("maintenance class starved behind interactive work")
	}
}
