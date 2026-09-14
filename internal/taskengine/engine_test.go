package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

type engineResolver struct {
	mu       sync.RWMutex
	handlers map[string]tasks.HandlerFunc
}

func (r *engineResolver) ResolveHandler(ref tasks.HandlerRef) (tasks.HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[ref.Name()]
	return handler, ok
}

type engineHarness struct {
	engine   *Engine
	executor *workers.PhysicalExecutor
	handler  tasks.HandlerRef
	scope    tasks.ScopeIdentity
}

func newEngineHarness(t *testing.T, cfg Config, handlers map[string]tasks.HandlerFunc) *engineHarness {
	t.Helper()
	resolver := &engineResolver{handlers: handlers}
	executor, err := workers.NewPhysicalExecutor(workerCounts(cfg), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

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
	if err := catalog.RegisterHandler(HandlerDescriptor{
		Ref: handler, PayloadKind: "test", PayloadVersions: []uint16{1}, MaxPayloadBytes: 4096,
		AllowedPools: pools,
		AllowedClasses: []tasks.PriorityClass{
			tasks.PriorityInteractive, tasks.PriorityNormal, tasks.PriorityBackground, tasks.PriorityMaintenance,
		},
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(cfg, catalog, executor)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope, err := tasks.NewScopeIdentity("scope:test", 1)
	if err != nil {
		t.Fatal(err)
	}
	h := &engineHarness{engine: engine, executor: executor, handler: handler, scope: scope}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
		_ = executor.Stop(ctx)
	})
	return h
}

func workerCounts(cfg Config) map[tasks.PoolID]int {
	counts := make(map[tasks.PoolID]int, len(cfg.Pools))
	for pool, limits := range cfg.Pools {
		counts[pool] = limits.Workers
	}
	return counts
}

func engineConfig() Config {
	cfg := validConfig()
	cfg.Pools = map[tasks.PoolID]PoolLimits{
		"general": {Workers: 2, MaxWaiting: 64, MaxWaitingBytes: 1 << 20},
	}
	cfg.DefaultOwner = OwnerLimits{MaxWaiting: 32, MaxActive: 2, MaxWaitingBytes: 1 << 20, Weight: 1}
	cfg.ResultCredits = 64
	cfg.QueueTimeout = time.Second
	cfg.ExecutionTimeout = time.Second
	cfg.ResultRetention = time.Second
	return cfg
}

func (h *engineHarness) spec(t *testing.T, id tasks.TaskID, owner tasks.QuotaOwner, pool tasks.PoolID, deadline time.Time, resources []tasks.ResourceRequest) tasks.WorkSpec {
	t.Helper()
	payload, err := tasks.NewPayloadRef("test", 1, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: id, Scope: h.scope, QuotaOwner: owner, Pool: pool,
		Class: tasks.PriorityNormal, Cause: tasks.CauseManual, QueueDeadline: deadline,
		Handler: h.handler, Input: payload, Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func waitState(t *testing.T, engine *Engine, id tasks.TaskID, terminal bool) tasks.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := engine.Snapshot(id)
		if ok && snapshot.State().Terminal() == terminal {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not reach terminal=%v", id, terminal)
	return tasks.TaskSnapshot{}
}

func TestEngine_SubmitCancellationLinearizesWithoutHiddenExecution(t *testing.T) {
	var executions atomic.Int32
	h := newEngineHarness(t, engineConfig(), map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) {
			executions.Add(1)
			return tasks.ResultRef{}, nil
		},
	})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.engine.Submit(cancelledCtx, h.spec(t, "pre-cancel", "owner", "general", time.Time{}, nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v want context canceled", err)
	}
	if _, ok := h.engine.Snapshot("pre-cancel"); ok {
		t.Fatal("pre-linearization cancellation created an active task")
	}

	for i := 0; i < 50; i++ {
		id := tasks.TaskID(fmt.Sprintf("race-%d", i))
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			ticket, err := h.engine.Submit(ctx, h.spec(t, id, "owner", "general", time.Time{}, nil))
			if err == nil && ticket.TaskID() != id {
				result <- fmt.Errorf("ticket task ID = %s want %s", ticket.TaskID(), id)
				return
			}
			result <- err
		}()
		cancel()
		err := <-result
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("race Submit() error = %v", err)
			}
			if _, ok := h.engine.Snapshot(id); ok {
				t.Fatalf("task %s was hidden behind cancellation", id)
			}
			continue
		}
		waitState(t, h.engine, id, true)
		if _, ok, err := h.engine.ConsumeResult(id); err != nil || !ok {
			t.Fatalf("ConsumeResult(%s) = ok=%v err=%v", id, ok, err)
		}
	}
	if got := executions.Load(); got < 0 || got > 50 {
		t.Fatalf("impossible execution count %d", got)
	}
}

func TestEngine_ResultCreditBackpressuresUntilConsumed(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20}
	cfg.ResultCredits = 1
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
	})

	if _, err := h.engine.Submit(context.Background(), h.spec(t, "first", "owner", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.engine, "first", true)

	_, err := h.engine.Submit(context.Background(), h.spec(t, "second", "owner", "general", time.Time{}, nil))
	var admissionErr *tasks.AdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Reason != tasks.RejectResultBackpress {
		t.Fatalf("second Submit() error = %v want result backpressure", err)
	}
	if _, ok := h.engine.Snapshot("second"); ok {
		t.Fatal("rejected task entered the registry")
	}
	if _, ok, err := h.engine.ConsumeResult("first"); err != nil || !ok {
		t.Fatalf("ConsumeResult(first) = ok=%v err=%v", ok, err)
	}
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "second", "owner", "general", time.Time{}, nil)); err != nil {
		t.Fatalf("result credit was not released: %v", err)
	}
}

func TestEngine_PhysicalPermitConservation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newEngineHarness(t, engineConfig(), map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) {
			started <- struct{}{}
			<-release
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "running", "owner", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	<-started
	stats := h.engine.Stats()
	pool := stats.Pools["general"]
	if got := pool.Idle + pool.Reserved + pool.Assigned + pool.Running; got != pool.Workers {
		t.Fatalf("physical conservation = %d want %d: %+v", got, pool.Workers, pool)
	}
	if stats.Owners["owner"].Active != 1 {
		t.Fatalf("owner active = %d want 1", stats.Owners["owner"].Active)
	}
	close(release)
	waitState(t, h.engine, "running", true)
	stats = h.engine.Stats()
	pool = stats.Pools["general"]
	if got := pool.Idle + pool.Reserved + pool.Assigned + pool.Running; got != pool.Workers || pool.Idle != pool.Workers {
		t.Fatalf("physical conservation after result failed: %+v", pool)
	}
}

func TestEngine_QueueDeadlineExpiresWithoutExecution(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20}
	startedFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	var secondRan atomic.Bool
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(ctx context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			if string(payload.Data()) == "first" {
				close(startedFirst)
				<-releaseFirst
				return tasks.ResultRef{}, nil
			}
			secondRan.Store(true)
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "first", "owner-a", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	<-startedFirst
	deadline := time.Now().Add(25 * time.Millisecond)
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "second", "owner-b", "general", deadline, nil)); err != nil {
		t.Fatal(err)
	}
	snapshot := waitState(t, h.engine, "second", true)
	if snapshot.State() != tasks.LifecycleExpired {
		t.Fatalf("second state = %v want expired", snapshot.State())
	}
	if secondRan.Load() {
		t.Fatal("expired queued task executed")
	}
	close(releaseFirst)
}

func TestEngine_GlobalOwnerActiveLimitAppliesAcrossPools(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools = map[tasks.PoolID]PoolLimits{
		"a": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
		"b": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
	}
	cfg.DefaultOwner.MaxActive = 1
	starts := make(chan string, 2)
	release := make(chan struct{}, 2)
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			starts <- string(payload.Data())
			<-release
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "task-a", "shared", "a", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "task-b", "shared", "b", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case second := <-starts:
		t.Fatalf("owner started a second cross-pool task while MaxActive=1: %s", second)
	case <-time.After(25 * time.Millisecond):
	}
	stats := h.engine.Stats()
	if stats.Owners["shared"].Active != 1 {
		t.Fatalf("shared owner active = %d want 1", stats.Owners["shared"].Active)
	}
	release <- struct{}{}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("second task did not start after owner capacity returned")
	}
	release <- struct{}{}
}

func TestEngine_ResourceReservationSerializesAcrossWorkers(t *testing.T) {
	cfg := engineConfig()
	cfg.DefaultOwner.MaxActive = 2
	cfg.ResourceCapacity = map[string]uint32{"media": 1}
	resource, err := tasks.NewResourceRequest("media", 1)
	if err != nil {
		t.Fatal(err)
	}
	starts := make(chan string, 2)
	release := make(chan struct{}, 2)
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			starts <- string(payload.Data())
			<-release
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "first", "owner-a", "general", time.Time{}, []tasks.ResourceRequest{resource})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "second", "owner-b", "general", time.Time{}, []tasks.ResourceRequest{resource})); err != nil {
		t.Fatal(err)
	}
	<-starts
	select {
	case second := <-starts:
		t.Fatalf("resource capacity was oversubscribed by %s", second)
	case <-time.After(25 * time.Millisecond):
	}
	if used := h.engine.Stats().Resources["media"].Used; used != 1 {
		t.Fatalf("media resource used = %d want 1", used)
	}
	release <- struct{}{}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("resource-blocked task did not start after release")
	}
	release <- struct{}{}
}

func TestEngine_ResultRetentionReclaimsCreditAndRecord(t *testing.T) {
	cfg := engineConfig()
	cfg.ResultCredits = 1
	cfg.ResultRetention = 25 * time.Millisecond
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(context.Context, tasks.PayloadRef) (tasks.ResultRef, error) { return tasks.ResultRef{}, nil },
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "retained", "owner", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.engine, "retained", true)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.engine.Snapshot("retained"); !ok {
			if h.engine.Stats().ResultCreditsUsed != 0 {
				t.Fatal("retention deleted record without releasing result credit")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("retention did not reclaim terminal task")
}

func TestEngine_CloseScopeCancelsGenerationAndRejectsLateWork(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20}
	started := make(chan struct{})
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(ctx context.Context, _ tasks.PayloadRef) (tasks.ResultRef, error) {
			close(started)
			<-ctx.Done()
			return tasks.ResultRef{}, ctx.Err()
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "active", "owner", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	<-started
	if count, err := h.engine.CloseScope(h.scope); err != nil || count != 1 {
		t.Fatalf("CloseScope() = %d,%v want 1,nil", count, err)
	}
	snapshot := waitState(t, h.engine, "active", true)
	if !snapshot.CancelRequested() || snapshot.State() != tasks.LifecycleCancelled {
		t.Fatalf("scope close snapshot = state=%v cancel=%v", snapshot.State(), snapshot.CancelRequested())
	}
	_, err := h.engine.Submit(context.Background(), h.spec(t, "late", "owner", "general", time.Time{}, nil))
	var admissionErr *tasks.AdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Reason != tasks.RejectScopeClosed {
		t.Fatalf("late Submit() error = %v want scope closed", err)
	}
}

func TestEngine_CancelRunningDoesNotReleaseCapacityUntilHandlerReturns(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20}
	cfg.DefaultOwner.MaxActive = 1
	starts := make(chan string, 2)
	releaseFirst := make(chan struct{})
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			id := string(payload.Data())
			starts <- id
			if id == "first" {
				<-releaseFirst // deliberately ignore cancellation
			}
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "first", "shared", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	if got := <-starts; got != "first" {
		t.Fatalf("first start = %q", got)
	}
	if _, err := h.engine.Cancel("first", tasks.CancelCaller); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "second", "shared", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-starts:
		t.Fatalf("capacity released before cancelled handler returned; started %q", got)
	case <-time.After(25 * time.Millisecond):
	}
	stats := h.engine.Stats()
	if stats.Owners["shared"].Active != 1 || stats.Pools["general"].Idle != 0 {
		t.Fatalf("cancel request released physical/logical capacity early: owner=%+v pool=%+v", stats.Owners["shared"], stats.Pools["general"])
	}
	close(releaseFirst)
	first := waitState(t, h.engine, "first", true)
	if first.State() != tasks.LifecycleSucceeded || !first.CancelRequested() {
		t.Fatalf("late cancellation outcome = state=%v requested=%v", first.State(), first.CancelRequested())
	}
	select {
	case got := <-starts:
		if got != "second" {
			t.Fatalf("second start = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second task did not start after first actually returned")
	}
}

type resultFirstWorkers struct {
	slot tasks.WorkerSlot
}

func (w *resultFirstWorkers) Slots() []tasks.WorkerSlot { return []tasks.WorkerSlot{w.slot} }

func (w *resultFirstWorkers) Assign(_ context.Context, assignment tasks.WorkerAssignment, events tasks.WorkerEvents) error {
	started := time.Now().UTC()
	finished := started.Add(time.Millisecond)
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: assignment.Spec().ID(), Outcome: tasks.OutcomeSucceeded, Cause: tasks.ResultCauseNone,
		StartedAt: started, FinishedAt: finished,
	})
	if err != nil {
		return err
	}
	if err := events.Completed(assignment.Permit(), result); err != nil {
		return err
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = events.Started(assignment.Permit(), started)
	}()
	return nil
}

func TestEngine_ResultBeforeStartEventDoesNotDoubleAccount(t *testing.T) {
	cfg := engineConfig()
	cfg.Pools = map[tasks.PoolID]PoolLimits{
		"general": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
	}
	catalog, err := NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := tasks.NewHandlerRef("test.run", 1)
	if err := catalog.RegisterHandler(HandlerDescriptor{
		Ref: handler, PayloadKind: "test", PayloadVersions: []uint16{1}, MaxPayloadBytes: 128,
		AllowedPools: []tasks.PoolID{"general"}, AllowedClasses: []tasks.PriorityClass{tasks.PriorityNormal},
	}); err != nil {
		t.Fatal(err)
	}
	fake := &resultFirstWorkers{slot: tasks.WorkerSlot{Pool: "general", WorkerID: "general-1", Generation: 1}}
	engine, err := New(cfg, catalog, fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop(context.Background())
	scope, _ := tasks.NewScopeIdentity("scope:test", 1)
	payload, _ := tasks.NewPayloadRef("test", 1, []byte("result-first"))
	spec, _ := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: "result-first", Scope: scope, QuotaOwner: "owner", Pool: "general",
		Class: tasks.PriorityNormal, Cause: tasks.CauseManual, Handler: handler, Input: payload,
	})
	if _, err := engine.Submit(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	snapshot := waitState(t, engine, "result-first", true)
	if snapshot.State() != tasks.LifecycleSucceeded || snapshot.StartedAt().IsZero() {
		t.Fatalf("result-first snapshot = state=%v started=%v", snapshot.State(), snapshot.StartedAt())
	}
	time.Sleep(30 * time.Millisecond) // let stale Started event arrive after terminal result
	stats := engine.Stats()
	pool := stats.Pools["general"]
	if pool.Idle != 1 || pool.Running != 0 || pool.Assigned != 0 || stats.Owners["owner"].Active != 0 {
		t.Fatalf("late Started event double-accounted capacity: pool=%+v owner=%+v", pool, stats.Owners["owner"])
	}
}

func TestEngine_OwnerOverrideControlsGlobalActiveQuota(t *testing.T) {
	cfg := engineConfig()
	cfg.DefaultOwner.MaxActive = 2
	cfg.Owners = map[tasks.QuotaOwner]OwnerLimits{
		"limited": {MaxWaiting: 8, MaxActive: 1, MaxWaitingBytes: 1 << 20, Weight: 3},
	}
	starts := make(chan string, 2)
	release := make(chan struct{}, 2)
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(_ context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			starts <- string(payload.Data())
			<-release
			return tasks.ResultRef{}, nil
		},
	})
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "one", "limited", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Submit(context.Background(), h.spec(t, "two", "limited", "general", time.Time{}, nil)); err != nil {
		t.Fatal(err)
	}
	<-starts
	select {
	case got := <-starts:
		t.Fatalf("owner override MaxActive=1 allowed %q to start", got)
	case <-time.After(25 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("second limited-owner task did not start")
	}
	release <- struct{}{}
}
