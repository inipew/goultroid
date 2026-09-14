package tasks

import (
	"context"
	"testing"
)

func TestTaskLifecycleSeparatesAdmissionQueueAndPhysicalRunning(t *testing.T) {
	m := NewManager()
	m.SetOwnerQuota("owner", Quota{MaxConcurrent: 1, MaxQueued: 4})

	ctx := context.Background()
	if _, _, err := m.Register(ctx, Task{
		ID: "lifecycle-1", Owner: "owner", Run: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	admitted, ok := m.GetTask("lifecycle-1")
	if !ok {
		t.Fatal("registered task missing")
	}
	if admitted.State != StateAdmitted || admitted.AdmittedAt.IsZero() || !admitted.StartedAt.IsZero() {
		t.Fatalf("unexpected admitted state: %+v", admitted)
	}

	if _, err := m.TryQueue("lifecycle-1"); err != nil {
		t.Fatal(err)
	}
	queued, _ := m.GetTask("lifecycle-1")
	if queued.State != StateQueued || queued.QueuedAt.IsZero() || !queued.StartedAt.IsZero() {
		t.Fatalf("unexpected queued state: %+v", queued)
	}
	stats := m.Stats().Owners["owner"]
	if stats.Admitted != 0 || stats.Queued != 1 || stats.Running != 0 {
		t.Fatalf("queued task counted as physical running: %+v", stats)
	}

	if _, err := m.MarkRunning("lifecycle-1"); err != nil {
		t.Fatal(err)
	}
	running, _ := m.GetTask("lifecycle-1")
	if running.State != StateRunning || running.StartedAt.IsZero() {
		t.Fatalf("unexpected running state: %+v", running)
	}
	stats = m.Stats().Owners["owner"]
	if stats.Admitted != 0 || stats.Queued != 0 || stats.Running != 1 {
		t.Fatalf("unexpected physical running counters: %+v", stats)
	}

	m.Finish("lifecycle-1", StateCompleted, nil)
	stats = m.Stats().Owners["owner"]
	if stats.Admitted != 0 || stats.Queued != 0 || stats.Running != 0 || stats.Completed != 1 {
		t.Fatalf("unexpected terminal counters: %+v", stats)
	}
}
