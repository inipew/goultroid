package taskengine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type benchAckCommitPump struct{}

func (benchAckCommitPump) Enqueue(ctx context.Context, op func(context.Context) error) (<-chan error, error) {
	ch := make(chan error, 1)
	ch <- op(ctx)
	return ch, nil
}

func (benchAckCommitPump) EnqueueSizedAck(ctx context.Context, _ int64, op func(context.Context) error, ack func(error)) error {
	ack(op(ctx))
	return nil
}

// BenchmarkP2_ConcurrentSubmitBoundedInbox measures concurrent submitters
// contending on a bounded coordinator inbox.
func BenchmarkP2_ConcurrentSubmitBoundedInbox(b *testing.B) {
	cfg := Config{
		InboxCapacity: 2048,
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 8, BacklogLimit: 8192},
		},
		ResultCapacity: 100000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	var seq atomic.Uint64
	var rejected atomic.Uint64

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := tasks.TaskID(fmt.Sprintf("p2-conc-%d", seq.Add(1)))
			ticket, err := engine.Submit(ctx, tasks.WorkSpec{
				ID:         id,
				QuotaOwner: "user:bench",
				Pool:       "general",
				Class:      tasks.PriorityNormal,
				Handler:    func(context.Context) error { return nil },
			})
			if err != nil {
				rejected.Add(1)
				continue
			}
			_, _ = ticket.Wait(ctx)
		}
	})
	b.ReportMetric(float64(rejected.Load())/float64(max(b.N, 1)), "reject_ratio/op")
}

// BenchmarkP2_MixedSubmitAndStatsControl measures submit+completion throughput
// while concurrent Stats() and Snapshot() control calls contend on the single
// coordinator loop (exposing O(registry) Stats() scan cost).
func BenchmarkP2_MixedSubmitAndStatsControl(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 8, BacklogLimit: 8192},
		},
		ResultCapacity: 4096,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	// Pre-populate retained terminal records to reflect steady-state registry size.
	for i := 0; i < 2000; i++ {
		tkt, err := engine.Submit(ctx, tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("p2-seed-%d", i)),
			QuotaOwner: "user:seed",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Handler:    func(context.Context) error { return nil },
		})
		if err != nil {
			b.Fatal(err)
		}
		_, _ = tkt.Wait(ctx)
	}

	stopPoller := make(chan struct{})
	var pollerWG sync.WaitGroup
	pollerWG.Add(1)
	go func() {
		defer pollerWG.Done()
		for {
			select {
			case <-stopPoller:
				return
			default:
				_, _ = engine.Stats(ctx)
				_, _ = engine.Snapshot("p2-seed-0")
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tkt, err := engine.Submit(ctx, tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("p2-mixed-%d", i)),
			QuotaOwner: "user:bench",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Handler:    func(context.Context) error { return nil },
		})
		if err != nil {
			b.Fatal(err)
		}
		_, _ = tkt.Wait(ctx)
	}
	b.StopTimer()
	close(stopPoller)
	pollerWG.Wait()
}

// BenchmarkP2_AdaptiveWorkerChurn measures ZeroIdle scale-up and retirement
// churn across repeated small bursts.
func BenchmarkP2_AdaptiveWorkerChurn(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"churn": {
				Concurrency:  4,
				BacklogLimit: 1024,
				ZeroIdle:     true,
				IdleTimeout:  2 * time.Millisecond,
			},
		},
		ResultCapacity: 4096,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		const burst = 4
		var wg sync.WaitGroup
		wg.Add(burst)
		for j := 0; j < burst; j++ {
			id := tasks.TaskID(fmt.Sprintf("p2-churn-%d-%d", i, j))
			tkt, err := engine.Submit(ctx, tasks.WorkSpec{
				ID:         id,
				QuotaOwner: "user:churn",
				Pool:       "churn",
				Class:      tasks.PriorityNormal,
				Handler:    func(context.Context) error { return nil },
			})
			if err != nil {
				b.Fatal(err)
			}
			go func() {
				defer wg.Done()
				_, _ = tkt.Wait(ctx)
			}()
		}
		wg.Wait()
		time.Sleep(3 * time.Millisecond)
	}
}

// BenchmarkP2_CallbackAndDurabilitySaturation measures completion callback
// delivery and durability acknowledgement lane activation/drain.
func BenchmarkP2_CallbackAndDurabilitySaturation(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 8, BacklogLimit: 8192},
		},
		ResultCapacity: 100000,
	}
	engine := NewEngine(cfg)
	engine.SetCommitPump(benchAckCommitPump{})
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		done := make(chan struct{})
		tkt, err := engine.Submit(ctx, tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("p2-cb-%d", i)),
			QuotaOwner: "user:dur",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Job:        &tasks.OccurrenceRef{AttemptID: "att", LeaseEpoch: 1},
			Commit:     func(context.Context, tasks.TaskResult) error { return nil },
			Handler:    func(context.Context) error { return nil },
			OnComplete: func(tasks.TaskResult) { close(done) },
		})
		if err != nil {
			b.Fatal(err)
		}
		_, _ = tkt.Wait(ctx)
		<-done
	}
}

// BenchmarkC1_StatsPollingOverhead isolates the coordinator overhead of
// fixed-ratio Stats()/Snapshot() calls (1 diagnostic pair per 64 submits) across
// identical submit+wait workloads at retained registry sizes of 0, 2000, and 10000.
func BenchmarkC1_StatsPollingOverhead(b *testing.B) {
	for _, retained := range []int{0, 2000, 10000} {
		for _, withPoller := range []bool{false, true} {
			name := fmt.Sprintf("retained=%d/poller=%t", retained, withPoller)
			b.Run(name, func(b *testing.B) {
				cfg := Config{
					Pools: map[tasks.PoolID]PoolEngineConfig{
						"general": {Concurrency: 8, BacklogLimit: 8192},
					},
					ResultCapacity: max(retained+4096, 4096),
				}
				engine := NewEngine(cfg)
				if err := engine.Start(context.Background()); err != nil {
					b.Fatal(err)
				}
				defer engine.Stop(context.Background())

				ctx := context.Background()
				for i := 0; i < retained; i++ {
					tkt, err := engine.Submit(ctx, tasks.WorkSpec{
						ID:         tasks.TaskID(fmt.Sprintf("c1-seed-%d", i)),
						QuotaOwner: "user:seed",
						Pool:       "general",
						Class:      tasks.PriorityNormal,
						Handler:    func(context.Context) error { return nil },
					})
					if err != nil {
						b.Fatal(err)
					}
					_, _ = tkt.Wait(ctx)
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if withPoller && i%64 == 0 {
						_, _ = engine.Stats(ctx)
						_, _ = engine.Snapshot("c1-seed-0")
					}
					tkt, err := engine.Submit(ctx, tasks.WorkSpec{
						ID:         tasks.TaskID(fmt.Sprintf("c1-op-%d", i)),
						QuotaOwner: "user:bench",
						Pool:       "general",
						Class:      tasks.PriorityNormal,
						Handler:    func(context.Context) error { return nil },
					})
					if err != nil {
						b.Fatal(err)
					}
					_, _ = tkt.Wait(ctx)
				}
			})
		}
	}
}

// BenchmarkC1_DirectStatsLatency measures direct Engine.Stats(ctx) latency
// across retained terminal registry sizes of 0, 2000, and 10000.
func BenchmarkC1_DirectStatsLatency(b *testing.B) {
	for _, retained := range []int{0, 2000, 10000} {
		b.Run(fmt.Sprintf("retained=%d", retained), func(b *testing.B) {
			cfg := Config{
				Pools: map[tasks.PoolID]PoolEngineConfig{
					"general": {Concurrency: 8, BacklogLimit: 8192},
				},
				ResultCapacity: max(retained+1024, 4096),
			}
			engine := NewEngine(cfg)
			if err := engine.Start(context.Background()); err != nil {
				b.Fatal(err)
			}
			defer engine.Stop(context.Background())

			ctx := context.Background()
			for i := 0; i < retained; i++ {
				tkt, err := engine.Submit(ctx, tasks.WorkSpec{
					ID:         tasks.TaskID(fmt.Sprintf("c1-stat-%d", i)),
					QuotaOwner: "user:seed",
					Pool:       "general",
					Class:      tasks.PriorityNormal,
					Handler:    func(context.Context) error { return nil },
				})
				if err != nil {
					b.Fatal(err)
				}
				_, _ = tkt.Wait(ctx)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := engine.Stats(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
