package assistant_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/resource"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/plugins/calculator"
	"github.com/inipew/goultroid/plugins/downloader"
	"github.com/inipew/goultroid/plugins/wikipedia"
	"go.uber.org/zap"
)

const (
	p8iInlineQueries   = 10000
	p8iCallbackBurst   = 512
	p8iHeapSettleSlack = 64 << 20
	p8iRSSSettleSlack  = 128 << 20
)

type p8iRoundTripper func(*http.Request) (*http.Response, error)

func (f p8iRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type p8iPort struct {
	edits   atomic.Int64
	answers atomic.Int64
}

func (*p8iPort) Send(_ context.Context, target presentation.Target, _ presentation.CompiledView) (presentation.Target, error) {
	return target, nil
}

func (p *p8iPort) Edit(_ context.Context, _ presentation.Target, _ presentation.CompiledView) error {
	p.edits.Add(1)
	return nil
}

func (p *p8iPort) Answer(_ context.Context, _ presentation.Answer) error {
	p.answers.Add(1)
	return nil
}

type p8iProcessSample struct {
	Goroutines int
	HeapAlloc  uint64
	RSSBytes   uint64
}

func p8iSampleProcess() p8iProcessSample {
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	return p8iProcessSample{
		Goroutines: goruntime.NumGoroutine(),
		HeapAlloc:  mem.HeapAlloc,
		RSSBytes:   p8iRSSBytes(),
	}
}

func p8iRSSBytes() uint64 {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func p8iRegisterFeature(t *testing.T, catalog *feature.Registry, id string, scope tasks.ScopeIdentity, spec feature.Spec) {
	t.Helper()
	registration, err := catalog.Register(feature.Owner{ID: id, Scope: scope}, spec)
	if err != nil {
		t.Fatalf("register feature %s: %v", id, err)
	}
	t.Cleanup(registration.Close)
}

func p8iRegisterInlineBindings(t *testing.T, registry *inlineservice.Registry, featureID string, scope tasks.ScopeIdentity, bindings []inlineservice.Binding) {
	t.Helper()
	for _, binding := range bindings {
		registration, err := registry.RegisterOwned(featureID, binding.InteractionID, scope, binding.Handler, binding.Priority)
		if err != nil {
			t.Fatalf("register inline %s:%s: %v", featureID, binding.InteractionID, err)
		}
		t.Cleanup(registration.Close)
	}
}

func TestP8ICombinedResourceIdleHighLoadAcceptance(t *testing.T) {
	ctx := context.Background()
	baseline := p8iSampleProcess()

	calc := calculator.New()
	wiki := wikipedia.New()
	dl := downloader.New()

	calcScope := tasks.ScopeIdentity{Owner: "plugin:calculator", Generation: 1}
	wikiScope := tasks.ScopeIdentity{Owner: "plugin:wikipedia", Generation: 1}
	downloaderScope := tasks.ScopeIdentity{Owner: "plugin:downloader", Generation: 1}

	catalog := feature.NewRegistry()
	p8iRegisterFeature(t, catalog, calc.Name(), calcScope, calc.FeatureSpec())
	p8iRegisterFeature(t, catalog, wiki.Name(), wikiScope, wiki.FeatureSpec())
	p8iRegisterFeature(t, catalog, dl.Name(), downloaderScope, dl.FeatureSpec())

	resourceManager := resource.NewManager()
	var wikiHTTPCalls atomic.Int64
	wikiHTTP := network.NewService(&http.Client{Transport: p8iRoundTripper(func(req *http.Request) (*http.Response, error) {
		wikiHTTPCalls.Add(1)
		body := `{"pages":[{"key":"Goultroid","title":"Goultroid","excerpt":"bounded result","description":"P8-I lookup","thumbnail":{"url":"https://example.invalid/thumb.png"}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}, resourceManager)
	wiki.SetHTTP(wikiHTTP)

	inlineRegistry := inlineservice.NewRegistry()
	p8iRegisterInlineBindings(t, inlineRegistry, calc.Name(), calcScope, calc.InlineBindings())
	p8iRegisterInlineBindings(t, inlineRegistry, wiki.Name(), wikiScope, wiki.InlineBindings())
	p8iRegisterInlineBindings(t, inlineRegistry, dl.Name(), downloaderScope, dl.InlineBindings())

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatalf("interaction runtime: %v", err)
	}
	actions := rootinteraction.NewDispatcher(sessions)
	port := &p8iPort{}
	interactionEngine, err := orchestration.New(sessions, actions, port)
	if err != nil {
		t.Fatalf("interaction engine: %v", err)
	}
	calcCleanup, err := calc.BindAssistant(assistantinteraction.DriverRuntime{
		Engine:  interactionEngine,
		Catalog: catalog,
		Admit: func(_ string, _ feature.InteractionKind, _ string, _ int64, _ presentation.Target) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("bind calculator assistant: %v", err)
	}

	inlineEngine := inlineservice.NewEngine(inlineRegistry, zap.NewNop())
	inlineEngine.SetFeatureCatalog(catalog)
	inlineEngine.SetInteractionRuntime(sessions)
	inlineEngine.SetPermissions(core.NewPermissions(1, []int64{2}))

	taskRuntime := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {
				Concurrency: 4, MinConcurrency: 0, ZeroIdle: true,
				IdleTimeout: 20 * time.Millisecond, BacklogLimit: 128, PayloadBudget: 8 << 20,
			},
			"download": {
				Concurrency: 2, MinConcurrency: 0, ZeroIdle: true,
				IdleTimeout: 20 * time.Millisecond, BacklogLimit: 32, PayloadBudget: 32 << 20,
			},
		},
		ResultCapacity:     512,
		ResourceCapacities: map[string]int64{"download": 1, "process": 1},
	})
	if err := taskRuntime.Start(ctx); err != nil {
		t.Fatalf("TaskEngine Start: %v", err)
	}
	idleStats, err := taskRuntime.Stats(ctx)
	if err != nil {
		t.Fatalf("TaskEngine Stats(idle): %v", err)
	}
	for pool, stats := range idleStats.Pools {
		if stats.Workers != 0 {
			t.Fatalf("P8-I idle pool %s workers=%d, want zero", pool, stats.Workers)
		}
	}
	if stats := sessions.Stats(); stats.Sessions != 0 || stats.Inputs != 0 || stats.StateBytes != 0 {
		t.Fatalf("P8-I interaction runtime is not idle: %+v", stats)
	}
	if stats := inlineEngine.RuntimeStats(); stats.CacheEntries != 0 || stats.CacheBytes != 0 {
		t.Fatalf("P8-I inline cache is not idle: %+v", stats)
	}
	if snapshot := resourceManager.AllSnapshots(); len(snapshot) != 0 {
		t.Fatalf("P8-I resource manager is not idle: %+v", snapshot)
	}
	taskStopped := false
	t.Cleanup(func() {
		calcCleanup()
		_ = sessions.Close()
		if !taskStopped {
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = taskRuntime.Stop(stopCtx)
		}
	})

	downloaderStarted := make(chan struct{})
	downloaderTicket, err := taskRuntime.Submit(ctx, tasks.WorkSpec{
		ID:         "p8i-downloader-active",
		Scope:      downloaderScope,
		QuotaOwner: "plugin:downloader",
		Pool:       "download",
		Class:      tasks.PriorityInteractive,
		Resources: []tasks.ResourceRequirement{
			{Name: "download", Amount: 1},
			{Name: "process", Amount: 1},
		},
		Handler: func(runCtx context.Context) error {
			close(downloaderStarted)
			<-runCtx.Done()
			return runCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("submit downloader workload: %v", err)
	}
	select {
	case <-downloaderStarted:
	case <-time.After(time.Second):
		t.Fatal("downloader workload did not start")
	}

	activeStats, err := taskRuntime.Stats(ctx)
	if err != nil {
		t.Fatalf("TaskEngine Stats(active): %v", err)
	}
	if activeStats.Resources["download"].Used != 1 || activeStats.Resources["process"].Used != 1 {
		t.Fatalf("active downloader resources=%+v", activeStats.Resources)
	}

	callbackSession, err := sessions.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: calc.Name(),
		Binding:   rootinteraction.Binding{ActorID: 1},
		State:     nil,
	})
	if err != nil {
		t.Fatalf("create calculator callback session: %v", err)
	}
	callbackTarget := presentationtelegram.InlineTarget{BindingID: "inline:p8i:calculator"}
	for i := 0; i < p8iCallbackBurst; i++ {
		actionID := "key_1"
		if i%2 == 1 {
			actionID = "back"
		}
		data, err := sessions.CallbackData(ctx, callbackSession.Session.ID, actionID)
		if err != nil {
			t.Fatalf("callback data at %d: %v", i, err)
		}
		if err := interactionEngine.Dispatch(ctx, orchestration.CallbackRequest{
			Data:    data,
			ActorID: 1,
			QueryID: int64(i + 1),
			Target:  callbackTarget,
		}); err != nil {
			t.Fatalf("calculator callback %d: %v", i, err)
		}
	}
	if got := port.edits.Load(); got != p8iCallbackBurst {
		t.Fatalf("calculator callback edits=%d want=%d", got, p8iCallbackBurst)
	}
	sessions.Cancel(callbackSession.Session.ID)

	for i := 0; i < rootinteraction.DefaultMaxSessionsPerActor; i++ {
		if _, err := sessions.Create(ctx, rootinteraction.CreateRequest{
			FeatureID: calc.Name(),
			Binding:   rootinteraction.Binding{ActorID: 1},
			State:     []byte{byte(i)},
		}); err != nil {
			t.Fatalf("calculator pressure session %d: %v", i, err)
		}
	}
	if _, err := sessions.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: calc.Name(),
		Binding:   rootinteraction.Binding{ActorID: 1},
	}); !errors.Is(err, rootinteraction.ErrCapacity) {
		t.Fatalf("calculator pressure overflow error=%v want=%v", err, rootinteraction.ErrCapacity)
	}
	interactionStats := sessions.Stats()
	if interactionStats.Sessions != rootinteraction.DefaultMaxSessionsPerActor || interactionStats.CapacityRejected == 0 {
		t.Fatalf("interaction pressure stats=%+v", interactionStats)
	}
	if removed := sessions.CancelScope(calcScope); removed != rootinteraction.DefaultMaxSessionsPerActor {
		t.Fatalf("calculator CancelScope removed=%d want=%d", removed, rootinteraction.DefaultMaxSessionsPerActor)
	}

	for i := 0; i < 520; i++ {
		if err := inlineEngine.Execute(ctx, nil, int64(3000+i), int64((i%32)+10), fmt.Sprintf("wiki load-%03d", i), ""); err != nil {
			t.Fatalf("rich lookup warm query %d: %v", i, err)
		}
	}
	for i := 520; i < p8iInlineQueries; i++ {
		if err := inlineEngine.Execute(ctx, nil, int64(3000+i), int64((i%128)+10), fmt.Sprintf("wiki load-%03d", i%32), ""); err != nil {
			t.Fatalf("rich lookup query %d: %v", i, err)
		}
	}
	inlineStats := inlineEngine.RuntimeStats()
	if inlineStats.CacheEntries <= 0 || inlineStats.CacheEntries > 500 {
		t.Fatalf("inline cache entries=%d, want 1..500", inlineStats.CacheEntries)
	}
	if inlineStats.CacheBytes <= 0 || inlineStats.CacheBytes > 8<<20 {
		t.Fatalf("inline cache bytes=%d, want 1..%d", inlineStats.CacheBytes, 8<<20)
	}
	if calls := wikiHTTPCalls.Load(); calls > 600 {
		t.Fatalf("managed Wikipedia HTTP calls=%d, cache failed to absorb repeated load", calls)
	}
	if snapshot := resourceManager.OwnerSnapshot("wikipedia"); snapshot.TotalActive != 0 || snapshot.Leaked != 0 {
		t.Fatalf("Wikipedia resource snapshot after load=%+v", snapshot)
	}

	rpcMetrics := telegram.NewInMemoryRPCMetrics()
	for i := 0; i < p8iInlineQueries; i++ {
		rpcMetrics.ObserveRequest(fmt.Sprintf("method-%03d", i%700), telegram.RPCSuccess, 1+(i%4), time.Duration(i%11)*time.Millisecond)
		rpcMetrics.ObserveWait(fmt.Sprintf("scope-%02d", i%64), time.Microsecond)
		if i%1000 == 0 {
			rpcMetrics.ObserveFloodWait("messages.getInlineBotResults", time.Second, true)
		}
	}
	rpcSnapshot := rpcMetrics.Snapshot()
	if rpcSnapshot.TotalRequests != p8iInlineQueries {
		t.Fatalf("RPC requests=%d want=%d", rpcSnapshot.TotalRequests, p8iInlineQueries)
	}
	if len(rpcSnapshot.RequestsByMethod) > 513 {
		t.Fatalf("RPC method metric cardinality=%d exceeds bounded labels+overflow", len(rpcSnapshot.RequestsByMethod))
	}
	if len(rpcSnapshot.WaitCountByScope) > 33 {
		t.Fatalf("RPC wait metric cardinality=%d exceeds bounded labels+overflow", len(rpcSnapshot.WaitCountByScope))
	}
	if rpcSnapshot.FloodWaitCount != p8iInlineQueries/1000 {
		t.Fatalf("RPC FloodWait count=%d", rpcSnapshot.FloodWaitCount)
	}

	if cancelled := taskRuntime.CancelScope(downloaderScope, tasks.CauseScopeClosed); cancelled != 1 {
		t.Fatalf("downloader CancelScope cancelled=%d want=1", cancelled)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
	downloaderResult, waitErr := downloaderTicket.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		t.Fatalf("wait downloader cancellation: %v", waitErr)
	}
	if downloaderResult.Outcome != tasks.OutcomeCancelled || downloaderResult.Cause != tasks.CauseScopeClosed {
		t.Fatalf("downloader cancellation result=%+v", downloaderResult)
	}
	settledTaskStats, err := taskRuntime.Stats(ctx)
	if err != nil {
		t.Fatalf("TaskEngine Stats(settled): %v", err)
	}
	if settledTaskStats.Resources["download"].Used != 0 || settledTaskStats.Resources["process"].Used != 0 {
		t.Fatalf("downloader resources retained after disable=%+v", settledTaskStats.Resources)
	}
	workerDeadline := time.Now().Add(time.Second)
	for time.Now().Before(workerDeadline) {
		settledTaskStats, err = taskRuntime.Stats(ctx)
		if err != nil {
			t.Fatalf("TaskEngine Stats(worker settle): %v", err)
		}
		allZero := true
		for _, pool := range settledTaskStats.Pools {
			if pool.Workers != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for pool, stats := range settledTaskStats.Pools {
		if stats.Workers != 0 {
			t.Fatalf("TaskEngine pool %s retained %d worker(s) after load", pool, stats.Workers)
		}
	}

	restartSession, err := sessions.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: calc.Name(),
		Binding:   rootinteraction.Binding{ActorID: 1},
	})
	if err != nil {
		t.Fatalf("create pre-restart session: %v", err)
	}
	oldCallback, err := sessions.CallbackData(ctx, restartSession.Session.ID, "key_1")
	if err != nil {
		t.Fatalf("pre-restart callback: %v", err)
	}
	calcCleanup()
	if err := sessions.Close(); err != nil {
		t.Fatalf("close Assistant interaction runtime: %v", err)
	}
	restartedSessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatalf("restart interaction runtime: %v", err)
	}
	if _, err := restartedSessions.ResolveCallback(ctx, oldCallback, rootinteraction.Binding{ActorID: 1}); !errors.Is(err, rootinteraction.ErrNotFound) {
		_ = restartedSessions.Close()
		t.Fatalf("old callback after Assistant restart error=%v want=%v", err, rootinteraction.ErrNotFound)
	}
	if _, err := restartedSessions.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: calc.Name(),
		Binding:   rootinteraction.Binding{ActorID: 1},
	}); err != nil {
		_ = restartedSessions.Close()
		t.Fatalf("new interaction after Assistant restart: %v", err)
	}
	if err := restartedSessions.Close(); err != nil {
		t.Fatalf("close restarted interaction runtime: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
	if err := taskRuntime.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("TaskEngine Stop: %v", err)
	}
	stopCancel()
	taskStopped = true

	goruntime.GC()
	time.Sleep(25 * time.Millisecond)
	settled := p8iSampleProcess()
	if settled.Goroutines > baseline.Goroutines+32 {
		t.Fatalf("goroutines did not settle: baseline=%d settled=%d", baseline.Goroutines, settled.Goroutines)
	}
	if settled.HeapAlloc > baseline.HeapAlloc+p8iHeapSettleSlack {
		t.Fatalf("heap did not settle: baseline=%d settled=%d", baseline.HeapAlloc, settled.HeapAlloc)
	}
	if baseline.RSSBytes != 0 && settled.RSSBytes != 0 && settled.RSSBytes > baseline.RSSBytes+p8iRSSSettleSlack {
		t.Fatalf("RSS did not settle: baseline=%d settled=%d", baseline.RSSBytes, settled.RSSBytes)
	}

	t.Logf("P8-I baseline=%+v settled=%+v inline=%+v interactions=%+v rpc_requests=%d wiki_http=%d task_resources=%+v",
		baseline, settled, inlineStats, interactionStats, rpcSnapshot.TotalRequests, wikiHTTPCalls.Load(), settledTaskStats.Resources)
}
