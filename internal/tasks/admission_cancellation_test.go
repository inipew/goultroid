package tasks

import (
	"context"
	"testing"
	"time"
)

func TestParentCancellationWakesAdmissionControllers(t *testing.T) {
	m := NewManager()
	parent, cancelParent := context.WithCancel(context.Background())
	_, cancelTask, err := m.Register(parent, Task{
		ID: "cancel-wake", Owner: "owner",
		Run: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelTask()

	changed := m.SlotChanges()
	cancelParent()

	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not wake admission controllers")
	}

	m.Finish("cancel-wake", StateCancelled, context.Canceled)
	stats := m.Stats().Owners["owner"]
	if stats.Admitted != 0 || stats.Cancelled != 1 {
		t.Fatalf("cancelled admitted task was not released: %+v", stats)
	}
}
