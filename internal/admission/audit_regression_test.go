package admission

import (
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestControllerSetOwnerLimitsUpdatesActiveQuantum(t *testing.T) {
	c := NewController(map[tasks.PoolID]PoolConfig{"p": {BacklogLimit: 100}})
	for _, owner := range []tasks.OwnerID{"a", "b"} {
		for i := 0; i < 20; i++ {
			c.Enqueue(&QueueEntry{Spec: tasks.WorkSpec{
				ID: tasks.TaskID(fmt.Sprintf("%s-%d", owner, i)), Pool: "p",
				Class: tasks.PriorityNormal, QuotaOwner: owner,
			}})
		}
	}

	// Both owners are already resident in the DRR rings. Updating b after
	// enqueue must take effect immediately rather than only after ring rebuild.
	c.SetOwnerLimits("b", OwnerLimits{Weight: 3, MaxWaiting: 100, MaxActive: 100})

	counts := map[tasks.OwnerID]int{}
	for i := 0; i < 16; i++ {
		entry, err := c.SelectCandidate("p")
		if err != nil {
			t.Fatal(err)
		}
		counts[entry.Spec.QuotaOwner]++
		c.OnTaskTerminal(entry.Spec)
	}
	if counts["b"] != 12 || counts["a"] != 4 {
		t.Fatalf("dynamic weight did not apply to active ring: %+v", counts)
	}
}

func TestControllerCancellationAndExpiryReclaimOwnerState(t *testing.T) {
	now := time.Date(2026, 9, 15, 7, 0, 0, 0, time.UTC)
	c := NewControllerWithClock(map[tasks.PoolID]PoolConfig{"p": {BacklogLimit: 100}}, func() time.Time { return now })

	cancelled := tasks.WorkSpec{ID: "cancelled", Pool: "p", Class: tasks.PriorityNormal, QuotaOwner: "cancel-owner"}
	c.Enqueue(&QueueEntry{Spec: cancelled, PayloadSize: 7})
	if _, ok := c.RemoveTask(cancelled.ID); !ok {
		t.Fatal("failed to remove queued task")
	}
	if _, ok := c.ownerWaitingCount["cancel-owner"]; ok {
		t.Fatal("cancelled owner waiting count retained at zero")
	}
	if _, ok := c.ownerWaitingBytes["cancel-owner"]; ok {
		t.Fatal("cancelled owner byte accounting retained at zero")
	}
	if _, ok := c.pools["p"].queues[tasks.PriorityNormal]["cancel-owner"]; ok {
		t.Fatal("cancelled owner ready queue retained after becoming empty")
	}

	expired := tasks.WorkSpec{
		ID: "expired", Pool: "p", Class: tasks.PriorityNormal, QuotaOwner: "expired-owner",
		QueueDeadline: now.Add(-time.Second),
	}
	c.Enqueue(&QueueEntry{Spec: expired, PayloadSize: 9})
	if got := c.PopExpiredN("p", now, 1); len(got) != 1 {
		t.Fatalf("expected one expired task, got %d", len(got))
	}
	if _, ok := c.ownerWaitingCount["expired-owner"]; ok {
		t.Fatal("expired owner waiting count retained at zero")
	}
	if _, ok := c.pools["p"].queues[tasks.PriorityNormal]["expired-owner"]; ok {
		t.Fatal("expired owner ready queue retained after becoming empty")
	}
}

func TestControllerPopExpiredNHonorsTurnBudget(t *testing.T) {
	now := time.Date(2026, 9, 15, 7, 0, 0, 0, time.UTC)
	c := NewControllerWithClock(map[tasks.PoolID]PoolConfig{"p": {BacklogLimit: 100}}, func() time.Time { return now })
	for i := 0; i < 10; i++ {
		c.Enqueue(&QueueEntry{Spec: tasks.WorkSpec{
			ID: tasks.TaskID(fmt.Sprintf("expired-%d", i)), Pool: "p",
			Class: tasks.PriorityNormal, QuotaOwner: tasks.OwnerID(fmt.Sprintf("owner-%d", i)),
			QueueDeadline: now.Add(-time.Second),
		}})
	}

	if got := c.PopExpiredN("p", now, 3); len(got) != 3 {
		t.Fatalf("first bounded sweep=%d want=3", len(got))
	}
	if got := c.WaitingCount("p"); got != 7 {
		t.Fatalf("waiting=%d want=7 after bounded sweep", got)
	}
	if got := c.PopExpiredN("p", now, 3); len(got) != 3 {
		t.Fatalf("second bounded sweep=%d want=3", len(got))
	}
	if got := c.PopExpiredN("p", now, 100); len(got) != 4 {
		t.Fatalf("final bounded sweep=%d want=4", len(got))
	}
}
