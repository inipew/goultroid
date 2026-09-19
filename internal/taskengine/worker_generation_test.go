package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestRetiredWorkerSlotGetsFreshMailboxGeneration(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"zero": {
				Concurrency: 1, ZeroIdle: true,
				IdleTimeout: 10 * time.Millisecond, BacklogLimit: 4, PayloadBudget: 1 << 20,
			},
		},
		ResultCapacity: 4,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	run := func(id tasks.TaskID) {
		ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID: id, Pool: "zero", QuotaOwner: "owner",
			Handler: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ticket.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	run("generation-1")
	first := e.workerMailboxes["zero"][0]
	if first == nil {
		t.Fatal("first worker mailbox was not created")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats, err := e.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stats.Pools["zero"].Workers == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	run("generation-2")
	second := e.workerMailboxes["zero"][0]
	if second == nil {
		t.Fatal("second worker mailbox was not created")
	}
	if first == second {
		t.Fatal("retired physical worker mailbox was reused across generations")
	}
}
