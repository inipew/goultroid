package admission

import (
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestReady_BlockedOwnerLeavesActiveRingAndPreservesFIFO(t *testing.T) {
	ready, err := NewReady(testQuantum(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Item{
		{TaskID: "a1", Pool: "p", Class: tasks.PriorityNormal, Owner: "a"},
		{TaskID: "b1", Pool: "p", Class: tasks.PriorityNormal, Owner: "b"},
	} {
		if err := ready.Enqueue(item); err != nil {
			t.Fatal(err)
		}
	}

	ready.BlockOwner("a")
	if !ready.OwnerBlocked("a") {
		t.Fatal("owner a was not marked blocked")
	}
	if err := ready.Enqueue(Item{TaskID: "a2", Pool: "p", Class: tasks.PriorityNormal, Owner: "a"}); err != nil {
		t.Fatal(err)
	}

	got, ok := ready.Next("p", nil)
	if !ok || got.TaskID != "b1" {
		t.Fatalf("Next() while a blocked = %q,%v want b1,true", got.TaskID, ok)
	}
	if _, ok := ready.Next("p", nil); ok {
		t.Fatal("blocked owner remained dispatchable after other active owner drained")
	}
	if ready.LenPool("p") != 2 {
		t.Fatalf("LenPool() = %d want 2 blocked items", ready.LenPool("p"))
	}

	ready.UnblockOwner("a")
	if ready.OwnerBlocked("a") {
		t.Fatal("owner a remained blocked after UnblockOwner")
	}
	for _, want := range []tasks.TaskID{"a1", "a2"} {
		got, ok := ready.Next("p", nil)
		if !ok || got.TaskID != want {
			t.Fatalf("Next() after unblock = %q,%v want %q,true", got.TaskID, ok, want)
		}
	}
}

func TestReady_BlockOwnerAppliesAcrossPoolsAndClasses(t *testing.T) {
	ready, err := NewReady(testQuantum(), 1)
	if err != nil {
		t.Fatal(err)
	}
	items := []Item{
		{TaskID: "a-p1", Pool: "p1", Class: tasks.PriorityInteractive, Owner: "a"},
		{TaskID: "a-p2", Pool: "p2", Class: tasks.PriorityBackground, Owner: "a"},
		{TaskID: "b-p1", Pool: "p1", Class: tasks.PriorityNormal, Owner: "b"},
	}
	for _, item := range items {
		if err := ready.Enqueue(item); err != nil {
			t.Fatal(err)
		}
	}
	ready.BlockOwner("a")

	got, ok := ready.Next("p1", nil)
	if !ok || got.TaskID != "b-p1" {
		t.Fatalf("p1 Next() = %q,%v want b-p1,true", got.TaskID, ok)
	}
	if _, ok := ready.Next("p2", nil); ok {
		t.Fatal("owner a stayed active in second pool")
	}

	ready.UnblockOwner("a")
	if got, ok := ready.Next("p1", nil); !ok || got.TaskID != "a-p1" {
		t.Fatalf("p1 owner a did not reactivate: %q,%v", got.TaskID, ok)
	}
	if got, ok := ready.Next("p2", nil); !ok || got.TaskID != "a-p2" {
		t.Fatalf("p2 owner a did not reactivate: %q,%v", got.TaskID, ok)
	}
}
