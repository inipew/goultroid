package admission

import (
	"testing"
	"time"
)

func TestDeadlines_UpsertReplacesNodeWithoutGrowingHeap(t *testing.T) {
	d := NewDeadlines()
	now := time.Now().UTC()
	if err := d.Upsert("task", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.Upsert("task", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if d.Len() != 1 {
		t.Fatalf("Len() = %d want 1", d.Len())
	}
	id, deadline, ok := d.Peek()
	if !ok || id != "task" || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("Peek() = %q,%v,%v", id, deadline, ok)
	}
}

func TestDeadlines_RemoveDoesNotLeaveStaleNode(t *testing.T) {
	d := NewDeadlines()
	now := time.Now().UTC()
	_ = d.Upsert("first", now)
	_ = d.Upsert("second", now.Add(time.Minute))
	if _, ok := d.Remove("first"); !ok {
		t.Fatal("expected first deadline to be removed")
	}
	if d.Len() != 1 {
		t.Fatalf("Len() = %d want 1", d.Len())
	}
	if id, _, ok := d.PopDue(now.Add(time.Second)); ok || id != "" {
		t.Fatalf("removed deadline resurfaced: %q,%v", id, ok)
	}
}

func TestDeadlines_PopDueOrdersByDeadline(t *testing.T) {
	d := NewDeadlines()
	now := time.Now().UTC()
	_ = d.Upsert("later", now.Add(time.Minute))
	_ = d.Upsert("now", now)
	id, _, ok := d.PopDue(now)
	if !ok || id != "now" {
		t.Fatalf("PopDue() = %q,%v want now,true", id, ok)
	}
	if _, _, ok := d.PopDue(now); ok {
		t.Fatal("future deadline popped early")
	}
}
