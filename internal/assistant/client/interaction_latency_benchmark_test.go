package client

import (
	"context"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

const t10AssistantLatencySamples = 8192

type t10AssistantBenchmarkPort struct{}

func (*t10AssistantBenchmarkPort) Send(_ context.Context, target presentation.Target, _ presentation.CompiledView) (presentation.Target, error) {
	return target, nil
}

func (*t10AssistantBenchmarkPort) Edit(context.Context, presentation.Target, presentation.CompiledView) error {
	return nil
}

func (*t10AssistantBenchmarkPort) Answer(context.Context, presentation.Answer) error { return nil }

type t10AssistantBenchmarkAck struct {
	immediate atomic.Int64
	final     atomic.Int64
}

func (a *t10AssistantBenchmarkAck) acknowledge(context.Context, int64) {
	a.immediate.Add(1)
}

func (a *t10AssistantBenchmarkAck) ensureAnswered(context.Context, int64, error) {
	a.final.Add(1)
}

type t10AssistantBenchmarkTasks struct {
	submits atomic.Int64
}

func (c *t10AssistantBenchmarkTasks) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits.Add(1)
	if spec.OnComplete != nil {
		spec.OnComplete(tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted})
	}
	return nil, nil
}

func (*t10AssistantBenchmarkTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (*t10AssistantBenchmarkTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (*t10AssistantBenchmarkTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func t10ReportAssistantLatency(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	quantile := func(p float64) time.Duration {
		return samples[int(float64(len(samples)-1)*p)]
	}
	b.ReportMetric(float64(quantile(0.50).Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(quantile(0.95).Nanoseconds()), "p95-ns")
	b.ReportMetric(float64(quantile(0.99).Nanoseconds()), "p99-ns")
}

func BenchmarkT10AssistantCallbackIngressAdmission(b *testing.B) {
	catalog := feature.NewRegistry()
	surface := execution.SurfaceAssistant
	spec, err := feature.BindCanonicalCommands(feature.Spec{
		ID:   "t10_callback_benchmark",
		Name: "T10 callback benchmark",
		Interactions: []feature.Interaction{{
			ID:       "next",
			Kind:     feature.InteractionAction,
			Surfaces: surface,
			Policy:   feature.PublicPolicy(surface),
		}},
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	scope := tasks.ScopeIdentity{Owner: "plugin:t10_callback_benchmark", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: "t10_callback_benchmark", Scope: scope}, spec)
	if err != nil {
		b.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		b.Fatal(err)
	}
	defer sessions.Close()
	actions := rootinteraction.NewDispatcher(sessions)
	engine, err := orchestration.New(sessions, actions, &t10AssistantBenchmarkPort{})
	if err != nil {
		b.Fatal(err)
	}
	actionRegistration, err := engine.RegisterPreparedAction(
		scope,
		"t10_callback_benchmark",
		"next",
		func(context.Context, rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
			return rootinteraction.ActionAdmission{
				Scope: scope,
				Profile: tasks.ExecutionProfile{
					Pool:             tasks.PoolID("interactive"),
					Class:            tasks.PriorityInteractive,
					ExecutionTimeout: 15 * time.Second,
				},
				AckPolicy: rootinteraction.AckImmediate,
			}, nil
		},
		func(*orchestration.Context) error { return nil },
	)
	if err != nil {
		b.Fatal(err)
	}
	defer actionRegistration.Close()

	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: "t10_callback_benchmark",
		Binding: rootinteraction.Binding{
			ActorID:   7,
			ChatID:    42,
			MessageID: 77,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		b.Fatal(err)
	}

	ack := &t10AssistantBenchmarkAck{}
	taskClient := &t10AssistantBenchmarkTasks{}
	ingress := &interactionIngress{engine: engine, ack: ack, tasks: taskClient}
	target := presentationtelegram.MessageTarget{
		Peer:      &tg.InputPeerUser{UserID: 7},
		ChatID:    42,
		MessageID: 77,
	}
	ctx := context.Background()

	sampleCap := b.N
	if sampleCap > t10AssistantLatencySamples {
		sampleCap = t10AssistantLatencySamples
	}
	samples := make([]time.Duration, 0, sampleCap)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		err := ingress.dispatchCallback(
			ctx,
			orchestration.CallbackRequest{
				Data:    data,
				ActorID: 7,
				QueryID: int64(i + 1),
				Target:  target,
			},
			tasks.TaskID("t10:callback:"+strconv.Itoa(i)),
			"callback:msg:42:77",
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(samples) < sampleCap {
			samples = append(samples, time.Since(start))
		}
	}
	b.StopTimer()

	if got := taskClient.submits.Load(); got != int64(b.N) {
		b.Fatalf("TaskEngine admissions=%d, want %d", got, b.N)
	}
	if got := ack.immediate.Load(); got != int64(b.N) {
		b.Fatalf("immediate acknowledgements=%d, want %d", got, b.N)
	}
	if got := ack.final.Load(); got != int64(b.N) {
		b.Fatalf("completion acknowledgements=%d, want %d", got, b.N)
	}
	t10ReportAssistantLatency(b, samples)
	b.ReportMetric(1, "task-admissions/op")
}
