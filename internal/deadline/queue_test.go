package deadline

import (
	"testing"
	"time"
)

func TestQueueStableOrderingAndReschedule(t *testing.T) {
	q := New()
	base := time.Unix(100, 0).UTC()
	first, err := q.Upsert(Entry{Key: "first", Deadline: base})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Upsert(Entry{Key: "second", Deadline: base})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence >= second.Sequence {
		t.Fatalf("sequence did not advance: first=%d second=%d", first.Sequence, second.Sequence)
	}

	// Moving first away and back must retain its tie-breaking sequence rather
	// than accumulating stale nodes or jumping behind newer entries.
	if _, err := q.Upsert(Entry{Key: "first", Deadline: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 2 {
		t.Fatalf("len after reschedule=%d want 2", q.Len())
	}
	if _, err := q.Upsert(Entry{Key: "first", Deadline: base}); err != nil {
		t.Fatal(err)
	}
	entry, ok := q.PopDue(base)
	if !ok || entry.Key != "first" {
		t.Fatalf("first due=%+v ok=%v", entry, ok)
	}
	entry, ok = q.PopDue(base)
	if !ok || entry.Key != "second" {
		t.Fatalf("second due=%+v ok=%v", entry, ok)
	}
}

func TestQueueRestoreSequenceAndRemove(t *testing.T) {
	q := New()
	at := time.Unix(200, 0).UTC()
	if _, err := q.Upsert(Entry{Key: "restored", Deadline: at, Sequence: 99}); err != nil {
		t.Fatal(err)
	}
	next, err := q.Upsert(Entry{Key: "next", Deadline: at})
	if err != nil {
		t.Fatal(err)
	}
	if next.Sequence != 100 {
		t.Fatalf("next sequence=%d want 100", next.Sequence)
	}
	removed, ok := q.Remove("restored")
	if !ok || removed.Sequence != 99 {
		t.Fatalf("removed=%+v ok=%v", removed, ok)
	}
	if q.Len() != 1 {
		t.Fatalf("len=%d want 1", q.Len())
	}
}
