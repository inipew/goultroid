package scheduler

import (
	"testing"
	"time"
)

func TestIndexedHeap_OrderingAndTieBreaking(t *testing.T) {
	ih := NewIndexedHeap()
	now := time.Now()

	e1 := &TimerEntry{ID: "e1", Deadline: now.Add(10 * time.Second)}
	e2 := &TimerEntry{ID: "e2", Deadline: now.Add(5 * time.Second)}
	e3 := &TimerEntry{ID: "e3", Deadline: now.Add(5 * time.Second)} // same deadline as e2

	ih.Push(e1)
	ih.Push(e2)
	ih.Push(e3)

	if ih.Len() != 3 {
		t.Fatalf("expected len 3, got %d", ih.Len())
	}

	earliest, ok := ih.PeekEarliest()
	if !ok || earliest.ID != "e2" {
		t.Fatalf("expected e2 (earlier sequence), got %v", earliest)
	}

	due := ih.PopDue(now.Add(6*time.Second), 10)
	if len(due) != 2 {
		t.Fatalf("expected 2 due entries, got %d", len(due))
	}
	if due[0].ID != "e2" || due[1].ID != "e3" {
		t.Errorf("tie-breaking order mismatch: %s, %s", due[0].ID, due[1].ID)
	}
}

func TestIndexedHeap_Remove(t *testing.T) {
	ih := NewIndexedHeap()
	now := time.Now()

	e1 := &TimerEntry{ID: "e1", Deadline: now.Add(5 * time.Second)}
	e2 := &TimerEntry{ID: "e2", Deadline: now.Add(10 * time.Second)}
	ih.Push(e1)
	ih.Push(e2)

	ih.Remove(e1)
	if ih.Len() != 1 {
		t.Fatalf("expected len 1 after remove, got %d", ih.Len())
	}

	earliest, _ := ih.PeekEarliest()
	if earliest.ID != "e2" {
		t.Errorf("expected e2 as earliest, got %s", earliest.ID)
	}
}
