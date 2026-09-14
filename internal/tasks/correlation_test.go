package tasks

import (
	"context"
	"testing"
)

func TestCancelByCorrelationID(t *testing.T) {
	m := NewManager()
	ctxA, _, err := m.Register(context.Background(), Task{ID: "a", Owner: "x", CorrelationID: "job:1", Run: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctxB, _, err := m.Register(context.Background(), Task{ID: "b", Owner: "x", CorrelationID: "job:2", Run: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.CancelByCorrelationID("job:1"); got != 1 {
		t.Fatalf("cancelled=%d want 1", got)
	}
	select {
	case <-ctxA.Done():
	default:
		t.Fatal("correlated task was not cancelled")
	}
	select {
	case <-ctxB.Done():
		t.Fatal("unrelated task was cancelled")
	default:
	}
}
