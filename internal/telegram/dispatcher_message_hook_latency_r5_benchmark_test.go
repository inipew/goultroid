package telegram

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/plugins/afk"
	"go.uber.org/zap"
)

type r5BenchmarkService struct {
	core.MockTelegramServicer
}

func (*r5BenchmarkService) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func (*r5BenchmarkService) IsBotSent(int) bool { return false }

func (*r5BenchmarkService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, nil
}

type r5BenchmarkTiming struct {
	handlerStart time.Time
	outputDone   time.Time
	err          error
}

func reportR5LatencyQuantiles(b *testing.B, prefix string, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	quantile := func(p float64) time.Duration {
		idx := int(float64(len(samples)-1) * p)
		return samples[idx]
	}
	b.ReportMetric(float64(quantile(0.50).Nanoseconds()), prefix+"-p50-ns")
	b.ReportMetric(float64(quantile(0.95).Nanoseconds()), prefix+"-p95-ns")
	b.ReportMetric(float64(quantile(0.99).Nanoseconds()), prefix+"-p99-ns")
}

func r5BenchmarkEngine(b *testing.B, dispatcher *Dispatcher) *taskengine.Engine {
	b.Helper()
	cfg := taskengine.NewDefaultConfig()
	cfg.ResultCapacity = 256
	cfg.MaxTerminalRetained = 32
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	dispatcher.SetTasks(engine)
	b.Cleanup(func() { _ = engine.Stop(context.Background()) })
	return engine
}

func BenchmarkR5InactiveAFKCommandLatencyStages(b *testing.B) {
	const ownerID int64 = 1001
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5BenchmarkEngine(b, dispatcher)
	dispatcher.SetSelfID(ownerID)

	svc := &r5BenchmarkService{}
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	p := afk.New(nil, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(p); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = mgr.Disable(context.Background(), "afk") })

	timings := make(chan r5BenchmarkTiming, 1)
	if err := router.Register(core.Command{
		Name:       "r5bench",
		Permission: core.PermissionOwner,
		Handler: func(ctx *core.Context) error {
			handlerStart := time.Now()
			err := ctx.Reply("r5-bench-output")
			timings <- r5BenchmarkTiming{
				handlerStart: handlerStart,
				outputDone:   time.Now(),
				err:          err,
			}
			return err
		},
	}); err != nil {
		b.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	sampleCap := b.N
	if sampleCap > maxLatencySamples {
		sampleCap = maxLatencySamples
	}
	l1Samples := make([]time.Duration, 0, sampleCap)
	l2Samples := make([]time.Duration, 0, sampleCap)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ingress := time.Now()
		if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      i + 1,
				Out:     true,
				Message: ".r5bench",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		}); err != nil {
			b.Fatal(err)
		}
		timing := <-timings
		if timing.err != nil {
			b.Fatal(timing.err)
		}
		if len(l1Samples) < sampleCap {
			l1Samples = append(l1Samples, timing.handlerStart.Sub(ingress))
			l2Samples = append(l2Samples, timing.outputDone.Sub(timing.handlerStart))
		}
	}
	b.StopTimer()

	reportR5LatencyQuantiles(b, "l1", l1Samples)
	reportR5LatencyQuantiles(b, "l2", l2Samples)
}

func BenchmarkR5ActiveToInactiveCommandStartL1(b *testing.B) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5BenchmarkEngine(b, dispatcher)
	dispatcher.SetSelfID(ownerID)

	svc := &r5BenchmarkService{}
	dispatcher.SetService(svc)

	repo := afk.NewSQLiteRepository(db)
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	p := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(p); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = mgr.Disable(context.Background(), "afk") })

	started := make(chan time.Time, 1)
	if err := router.Register(core.Command{
		Name:       "r5activebench",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			started <- time.Now()
			return nil
		},
	}); err != nil {
		b.Fatal(err)
	}

	afkCommand := p.Commands()[0]
	activate := func(id int) {
		ctx := &core.Context{
			Ctx:     context.Background(),
			Sender:  &core.User{ID: ownerID},
			Message: &core.Message{ID: id},
			Svc:     svc,
			PeerID:  &tg.InputPeerSelf{},
			Args:    []string{"on"},
			RawArgs: "on",
		}
		if err := afkCommand.Handler(ctx); err != nil {
			b.Fatal(err)
		}
		state, err := repo.GetAFK(context.Background(), ownerID)
		if err != nil || state == nil || !state.IsAFK {
			b.Fatalf("activation state=%+v err=%v", state, err)
		}
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	sampleCap := b.N
	if sampleCap > maxLatencySamples {
		sampleCap = maxLatencySamples
	}
	l1Samples := make([]time.Duration, 0, sampleCap)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		activate(1000000 + i)
		b.StartTimer()

		ingress := time.Now()
		if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      2000000 + i,
				Out:     true,
				Message: ".r5activebench",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		}); err != nil {
			b.Fatal(err)
		}
		handlerStart := <-started
		if len(l1Samples) < sampleCap {
			l1Samples = append(l1Samples, handlerStart.Sub(ingress))
		}

		b.StopTimer()
		state, err := repo.GetAFK(context.Background(), ownerID)
		if err != nil || state == nil || state.IsAFK {
			b.Fatalf("inactive state=%+v err=%v", state, err)
		}
		b.StartTimer()
	}
	b.StopTimer()

	reportR5LatencyQuantiles(b, "l1", l1Samples)
}
