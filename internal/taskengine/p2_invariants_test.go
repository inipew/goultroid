package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*manualTimer]struct{}
}

type manualTimer struct {
	clock    *manualClock
	ch       chan time.Time
	deadline time.Time
	active   bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0).UTC(), timers: make(map[*manualTimer]struct{})}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(delay time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{clock: c, ch: make(chan time.Time, 1), deadline: c.now.Add(delay), active: true}
	c.timers[t] = struct{}{}
	return t
}

func (c *manualClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	for timer := range c.timers {
		if timer.active && !timer.deadline.After(now) {
			timer.active = false
			select {
			case timer.ch <- now:
			default:
			}
		}
	}
	c.mu.Unlock()
}

func (t *manualTimer) C() <-chan time.Time { return t.ch }
func (t *manualTimer) Reset(delay time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.deadline = t.clock.now.Add(delay)
	t.active = true
	select {
	case <-t.ch:
	default:
	}
	return wasActive
}
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func assertP2Conservation(t *testing.T, stats Stats) {
	t.Helper()
	if stats.ResultCreditsUsed < 0 || stats.ResultCreditsUsed > stats.ResultCapacity {
		t.Fatalf("result credit invariant violated: used=%d capacity=%d", stats.ResultCreditsUsed, stats.ResultCapacity)
	}
	for poolID, pool := range stats.Pools {
		if got := pool.Idle + pool.Reserved + pool.Assigned + pool.Running; got != pool.Workers {
			t.Fatalf("pool %s conservation=%d want %d: %+v", poolID, got, pool.Workers, pool)
		}
	}
	for ownerID, owner := range stats.Owners {
		if owner.Running > owner.Reserved || owner.Active != owner.Reserved {
			t.Fatalf("owner %s accounting invalid: %+v", ownerID, owner)
		}
	}
	for name, resource := range stats.Resources {
		if resource.Used > resource.Capacity {
			t.Fatalf("resource %s over capacity: %+v", name, resource)
		}
	}
}

func TestEngine_WorkerEventsAreLifecycleFenced(t *testing.T) {
	cfg := engineConfig()
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
	})

	permit, err := tasks.NewPhysicalPermit("general", "general-1", 1, "ghost", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Started(permit, time.Now().UTC()); !errors.Is(err, ErrWorkerEventInvalid) {
		t.Fatalf("Started(stale) error=%v want ErrWorkerEventInvalid", err)
	}
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: "ghost", Outcome: tasks.OutcomeSucceeded, Cause: tasks.ResultCauseNone, FinishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Completed(permit, result); err != nil {
		t.Fatalf("Completed enqueue error=%v", err)
	}
	if _, ok := h.engine.Snapshot("ghost"); ok {
		t.Fatal("stale completion created task state")
	}
	assertP2Conservation(t, h.engine.Stats())
}

func TestEngine_PreStartCancelIsExactlyOneTerminalResult(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20}
	started := make(chan struct{})
	release := make(chan struct{})
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			if string(payload.Data()) == "first" {
				close(started)
				<-release
			}
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "first", "owner-a", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "queued", "owner-b", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	if receipt, err := h.engine.Cancel("queued", tasks.CancelCaller); err != nil || !receipt.Requested {
		t.Fatalf("Cancel() receipt=%+v err=%v", receipt, err)
	}
	snapshot := waitState(t, h.engine, "queued", true)
	if snapshot.State() != tasks.LifecycleCancelled || !snapshot.StartedAt().IsZero() {
		t.Fatalf("queued cancel state=%v started=%v", snapshot.State(), snapshot.StartedAt())
	}
	first, ok, err := h.engine.ConsumeResult("queued")
	if err != nil || !ok || first.Outcome() != tasks.OutcomeCancelled {
		t.Fatalf("ConsumeResult() outcome=%v ok=%v err=%v", first.Outcome(), ok, err)
	}
	second, ok, err := h.engine.ConsumeResult("queued")
	if err != nil || !ok || second.Outcome() != tasks.OutcomeCancelled {
		t.Fatalf("second ConsumeResult() outcome=%v ok=%v err=%v", second.Outcome(), ok, err)
	}
	if h.engine.Stats().ResultCreditsUsed != 1 {
		t.Fatalf("result credits after repeated consume=%d want 1", h.engine.Stats().ResultCreditsUsed)
	}
	assertP2Conservation(t, h.engine.Stats())
	close(release)
}

func TestEngine_GlobalQuotaUsesBlockedOwnerIndex(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools = map[tasks.PoolID]PoolLimits{
		"a": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
		"b": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
	}
	cfg.DefaultOwner.MaxActive = 1
	starts := make(chan string, 4)
	releases := make(chan struct{}, 4)
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			starts <- string(payload.Data())
			<-releases
			return tasks.ResultRef{}, nil
		},
	})
	for i, pool := range []tasks.PoolID{"a", "b"} {
		id := tasks.TaskID(fmt.Sprintf("same-%d", i))
		if _, err := h.engine.Submit(context.Background(), h.spec(t, id, "shared", pool, time.Time{}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	<-starts
	select {
	case second := <-starts:
		t.Fatalf("quota-blocked owner started second task: %s", second)
	case <-time.After(25 * time.Millisecond):
	}
	assertP2Conservation(t, h.engine.Stats())
	releases <- struct{}{}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("blocked owner did not reactivate after reservation release")
	}
	releases <- struct{}{}
}

func TestEngine_RepeatedRetentionPurgeReturnsStateToBaseline(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 16, MaxWaitingBytes: 1 << 20}
	cfg.ResultCredits = 16
	cfg.ResultRetention = 50 * time.Millisecond
	clock := newManualClock()

	resolver := &engineResolver{handlers: map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
	}}
	executor, err := workers.NewPhysicalExecutor(workerCounts(cfg), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer executor.Stop(context.Background())
	catalog, handler := testCatalog(t, cfg)
	engine, err := newWithClock(cfg, catalog, executor, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(context.Background())
	scope, _ := tasks.NewScopeIdentity("scope:retention", 1)

	for burst := 0; burst < 3; burst++ {
		for i := 0; i < 6; i++ {
			id := tasks.TaskID(fmt.Sprintf("burst-%d-%d", burst, i))
			payload, _ := tasks.NewPayloadRef("test", 1, []byte(id))
			spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{ID: id, Scope: scope, QuotaOwner: tasks.QuotaOwner(fmt.Sprintf("owner-%d", i)), Pool: "general", Class: tasks.PriorityNormal, Cause: tasks.CauseManual, Handler: handler, Input: payload})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Submit(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			waitState(t, engine, id, true)
		}
		clock.Advance(cfg.ResultRetention + time.Millisecond)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			stats := engine.Stats()
			if stats.Tasks == 0 && stats.ResultCreditsUsed == 0 && stats.RetentionEntries == 0 && len(stats.Owners) == 0 {
				assertP2Conservation(t, stats)
				break
			}
			time.Sleep(time.Millisecond)
		}
		stats := engine.Stats()
		if stats.Tasks != 0 || stats.ResultCreditsUsed != 0 || stats.RetentionEntries != 0 || len(stats.Owners) != 0 {
			t.Fatalf("burst %d retained state: tasks=%d credits=%d retention=%d owners=%d", burst, stats.Tasks, stats.ResultCreditsUsed, stats.RetentionEntries, len(stats.Owners))
		}
	}
}

func testCatalog(t *testing.T, cfg Config) (*Catalog, tasks.HandlerRef) {
	t.Helper()
	catalog, err := NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := tasks.NewHandlerRef("test.run", 1)
	if err != nil {
		t.Fatal(err)
	}
	pools := make([]tasks.PoolID, 0, len(cfg.Pools))
	for pool := range cfg.Pools {
		pools = append(pools, pool)
	}
	if err := catalog.RegisterHandler(HandlerDescriptor{Ref: handler, PayloadKind: "test", PayloadVersions: []uint16{1}, MaxPayloadBytes: 4096, AllowedPools: pools, AllowedClasses: []tasks.PriorityClass{tasks.PriorityInteractive, tasks.PriorityNormal, tasks.PriorityBackground, tasks.PriorityMaintenance}}); err != nil {
		t.Fatal(err)
	}
	return catalog, handler
}
