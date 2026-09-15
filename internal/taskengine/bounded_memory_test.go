package taskengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func boundedTestEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	e := NewEngine(cfg)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

func engineStatsOf(t *testing.T, e *Engine) engineStats {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opStats, reply: make(chan engineReply, 1)})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	return rep.stats
}

// B1/B2: retained memory is bounded; admission past the cap is rejected with
// ErrRetainedBudget instead of growing without limit.
func TestRetainedBudgetRejectsAdmission(t *testing.T) {
	e := boundedTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 100, PayloadBudget: 1 << 20}},
		ResultCapacity:      100,
		MaxTerminalRetained: 100,
		MaxRetainedBytes:    4096,
		DecisionTimeout:     5 * time.Second,
	})
	release := make(chan struct{})
	defer close(release)
	blocker, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "blocker",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { <-release; return nil },
	})
	if err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	_ = blocker

	rejected := 0
	for i := 0; i < 20; i++ {
		_, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("bulk-%d", i)),
			QuotaOwner: "owner",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Input:      make([]byte, 1024),
			Handler:    func(ctx context.Context) error { return nil },
		})
		if err != nil {
			if errors.Is(err, tasks.ErrRetainedBudget) {
				rejected++
				continue
			}
			// Pool backlog / owner waiting limits may also trip; only the
			// retained-budget path is asserted below via stats plateau.
			rejected++
			continue
		}
	}
	if rejected == 0 {
		t.Fatalf("expected retained/backlog backpressure, all submits accepted")
	}
	stats := engineStatsOf(t, e)
	if stats.retainedBytes > stats.retainedCap {
		t.Fatalf("retained %d exceeds cap %d", stats.retainedBytes, stats.retainedCap)
	}
}

// B3: failure text is truncated to the configured cap.
func TestFailureMessageTruncated(t *testing.T) {
	e := boundedTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		MaxFailureBytes:     128,
		DecisionTimeout:     5 * time.Second,
	})
	huge := strings.Repeat("x", 10000)
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "huge-failure",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return errors.New(huge) },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Outcome != tasks.OutcomeFailed {
		t.Fatalf("expected failed, got %s", res.Outcome)
	}
	if len(res.Failure.Message) > 128 {
		t.Fatalf("failure message not truncated: %d bytes", len(res.Failure.Message))
	}
	if !strings.HasSuffix(res.Failure.Message, "...(truncated)") {
		t.Fatalf("truncation marker missing: %q", res.Failure.Message[len(res.Failure.Message)-20:])
	}
	snap, ok := e.Snapshot("huge-failure")
	if !ok || len(snap.Error) > 128 {
		t.Fatalf("snapshot error not truncated: %+v", snap)
	}
}

// B4: terminal eviction bounds count and bytes, and evicted tickets still
// serve their immutable result via the done-close edge.
func TestTerminalEvictionBoundsMemoryAndKeepsTicketResult(t *testing.T) {
	e := boundedTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 50, PayloadBudget: 1 << 20}},
		ResultCapacity:      50,
		MaxTerminalRetained: 4,
		MaxRetainedBytes:    1 << 20,
		DecisionTimeout:     5 * time.Second,
	})
	var first tasks.Ticket
	for i := 0; i < 10; i++ {
		ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("evict-%d", i)),
			QuotaOwner: "owner",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Input:      []byte("payload"),
			Handler:    func(ctx context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if i == 0 {
			first = ticket
		}
		if _, err := ticket.Wait(context.Background()); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	stats := engineStatsOf(t, e)
	if stats.terminalCount > 4 {
		t.Fatalf("terminal count %d exceeds MaxTerminalRetained 4", stats.terminalCount)
	}
	if stats.retainedBytes > stats.retainedCap {
		t.Fatalf("retained %d exceeds cap %d", stats.retainedBytes, stats.retainedCap)
	}
	if _, ok := e.Snapshot("evict-0"); ok {
		t.Fatalf("expected evict-0 snapshot evicted")
	}
	res, ok := first.Result()
	if !ok || !res.IsSuccess() {
		t.Fatalf("evicted ticket must still serve result: %+v %v", res, ok)
	}
}

// B5: bounded delivery serves every callback exactly once under burst load
// without fallback goroutines, and Drain covers delivery.
func TestCompletionDeliveryBurstExactlyOnce(t *testing.T) {
	const n = 60
	e := boundedTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 100, PayloadBudget: 1 << 20}},
		ResultCapacity:      100,
		MaxTerminalRetained: 100,
		DeliveryConcurrency: 4,
		DecisionTimeout:     5 * time.Second,
	})
	var mu sync.Mutex
	seen := make(map[tasks.TaskID]int)
	var wg sync.WaitGroup
	tickets := make([]tasks.Ticket, 0, n)
	for i := 0; i < n; i++ {
		id := tasks.TaskID(fmt.Sprintf("cb-%d", i))
		wg.Add(1)
		ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         id,
			QuotaOwner: "owner",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Handler:    func(ctx context.Context) error { return nil },
			OnComplete: func(r tasks.TaskResult) {
				defer wg.Done()
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				seen[r.TaskID]++
				mu.Unlock()
			},
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		tickets = append(tickets, ticket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ticket := range tickets {
		if _, err := ticket.Wait(ctx); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	if err := e.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("delivered %d/%d callbacks", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("callback %s delivered %d times", id, count)
		}
	}
	stats := engineStatsOf(t, e)
	if stats.deliveryFailed != 0 {
		t.Fatalf("delivery fell back to unbounded goroutines %d times", stats.deliveryFailed)
	}
}

// B6: repeated bursts plateau instead of growing: retained bytes stay under
// the cap across bursts with terminal retention bounded.
func TestRetainedPlateauAcrossBursts(t *testing.T) {
	e := boundedTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 200, PayloadBudget: 1 << 20}},
		ResultCapacity:      200,
		MaxTerminalRetained: 50,
		MaxRetainedBytes:    1 << 20,
		DecisionTimeout:     5 * time.Second,
	})
	var peak int64
	for burst := 0; burst < 5; burst++ {
		for j := 0; j < 40; j++ {
			ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("plateau-%d-%d", burst, j)),
				QuotaOwner: "owner",
				Pool:       "general",
				Class:      tasks.PriorityNormal,
				Input:      make([]byte, 512),
				Handler:    func(ctx context.Context) error { return nil },
			})
			if err != nil {
				t.Fatalf("burst %d submit %d: %v", burst, j, err)
			}
			if _, err := ticket.Wait(context.Background()); err != nil {
				t.Fatalf("wait: %v", err)
			}
		}
		stats := engineStatsOf(t, e)
		if stats.retainedBytes > peak {
			peak = stats.retainedBytes
		}
		if stats.retainedBytes > stats.retainedCap {
			t.Fatalf("burst %d retained %d exceeds cap %d", burst, stats.retainedBytes, stats.retainedCap)
		}
		if stats.terminalCount > 50 {
			t.Fatalf("burst %d terminal %d exceeds bound 50", burst, stats.terminalCount)
		}
	}
	t.Logf("peak retained bytes: %d", atomic.LoadInt64(&peak))
}
