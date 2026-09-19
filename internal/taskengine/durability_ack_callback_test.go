package taskengine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type callbackAckPump struct {
	legacyCalls atomic.Int32
	ackCalls    atomic.Int32
}

func (p *callbackAckPump) Enqueue(context.Context, func(context.Context) error) (<-chan error, error) {
	p.legacyCalls.Add(1)
	ch := make(chan error, 1)
	ch <- nil
	return ch, nil
}

func (p *callbackAckPump) EnqueueSizedAck(ctx context.Context, _ int64, op func(context.Context) error, ack func(error)) error {
	p.ackCalls.Add(1)
	ack(op(ctx))
	return nil
}

func TestDurableCommitCallbackPumpSkipsAckWaitLane(t *testing.T) {
	pump := &callbackAckPump{}
	e := durableTestEngine(t, Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, MinConcurrency: 1, BacklogLimit: 4, PayloadBudget: 1 << 20},
		},
		ResultCapacity:      4,
		MaxTerminalRetained: 4,
		DecisionTimeout:     time.Second,
	}, pump)

	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "callback-ack",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(context.Context) error { return nil },
		Commit:     func(context.Context, tasks.TaskResult) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsSuccess() {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := pump.ackCalls.Load(); got != 1 {
		t.Fatalf("callback ack calls=%d, want 1", got)
	}
	if got := pump.legacyCalls.Load(); got != 0 {
		t.Fatalf("legacy pump path used %d times", got)
	}
	if got := e.durability.remaining.Load(); got != 0 {
		t.Fatalf("durability ack lane spawned %d worker(s) for callback pump", got)
	}
}
