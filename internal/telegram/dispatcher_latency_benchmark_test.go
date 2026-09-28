package telegram

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const maxLatencySamples = 8192

func reportLatencyQuantiles(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	quantile := func(p float64) time.Duration {
		idx := int(float64(len(samples)-1) * p)
		return samples[idx]
	}
	b.ReportMetric(float64(quantile(0.50).Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(quantile(0.95).Nanoseconds()), "p95-ns")
	b.ReportMetric(float64(quantile(0.99).Nanoseconds()), "p99-ns")
}

func BenchmarkDispatcherCallbackIngressObservation(b *testing.B) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	svc := newCallbackRecordingService()
	d.SetService(svc)
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer bus.Close()
	d.SetEventBus(bus)
	sub := bus.Subscribe(core.EventTypeCallbackQuery, func(core.Event) {})
	defer sub()

	ctx := context.Background()
	for i := 0; i < 128; i++ {
		_ = d.OnBotCallbackQuery(ctx, tg.Entities{}, &tg.UpdateBotCallbackQuery{
			QueryID: int64(i + 1), UserID: 42, Peer: &tg.PeerChat{ChatID: 10}, MsgID: 20, Data: []byte("noop"),
		})
	}

	sampleCap := b.N
	if sampleCap > maxLatencySamples {
		sampleCap = maxLatencySamples
	}
	samples := make([]time.Duration, 0, sampleCap)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, &tg.UpdateBotCallbackQuery{
			QueryID: int64(1000 + i), UserID: 42, Peer: &tg.PeerChat{ChatID: 10}, MsgID: 20, Data: []byte("noop"),
		}); err != nil {
			b.Fatal(err)
		}
		if len(samples) < sampleCap {
			samples = append(samples, time.Since(start))
		}
	}
	b.StopTimer()
	reportLatencyQuantiles(b, samples)
}

func BenchmarkDispatcherDecisionIngressNoop(b *testing.B) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.AddPrioritizedMessageHandler(PrioritySecurity, func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		return nil
	})
	ctx := context.Background()

	for i := 0; i < 128; i++ {
		_ = d.dispatch(ctx, tg.Entities{}, &tg.Message{ID: i + 1, PeerID: &tg.PeerChat{ChatID: 10}, Message: "plain"})
	}

	sampleCap := b.N
	if sampleCap > maxLatencySamples {
		sampleCap = maxLatencySamples
	}
	samples := make([]time.Duration, 0, sampleCap)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := d.dispatch(ctx, tg.Entities{}, &tg.Message{ID: 1000 + i, PeerID: &tg.PeerChat{ChatID: 10}, Message: "plain"}); err != nil {
			b.Fatal(err)
		}
		if len(samples) < sampleCap {
			samples = append(samples, time.Since(start))
		}
	}
	b.StopTimer()
	reportLatencyQuantiles(b, samples)
}
