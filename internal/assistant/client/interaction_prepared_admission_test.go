package client

import (
	"context"
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

type preparedCallbackPort struct{}

func (*preparedCallbackPort) Send(context.Context, presentation.Target, presentation.CompiledView) (presentation.Target, error) {
	return nil, nil
}
func (*preparedCallbackPort) Edit(context.Context, presentation.Target, presentation.CompiledView) error {
	return nil
}
func (*preparedCallbackPort) Answer(context.Context, presentation.Answer) error { return nil }

type preparedCallbackAck struct {
	calls int
	err   error
}

func (a *preparedCallbackAck) ensureAnswered(_ context.Context, _ int64, err error) {
	a.calls++
	a.err = err
}

type preparedCallbackTicket struct {
	id     tasks.TaskID
	result tasks.TaskResult
	done   chan struct{}
}

func (t *preparedCallbackTicket) TaskID() tasks.TaskID { return t.id }
func (t *preparedCallbackTicket) State() tasks.TaskState {
	if t.result.IsSuccess() {
		return tasks.StateCompleted
	}
	return tasks.StateFailed
}
func (t *preparedCallbackTicket) Done() <-chan struct{} { return t.done }
func (t *preparedCallbackTicket) Result() (tasks.TaskResult, bool) {
	return t.result, true
}
func (t *preparedCallbackTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return t.result, nil
}

type preparedCallbackTasks struct {
	spec  tasks.WorkSpec
	calls int
}

func (c *preparedCallbackTasks) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.spec = spec
	c.calls++
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err := spec.Handler(ctx); err != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure.Message = err.Error()
	}
	done := make(chan struct{})
	close(done)
	return &preparedCallbackTicket{id: spec.ID, result: result, done: done}, nil
}
func (*preparedCallbackTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*preparedCallbackTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*preparedCallbackTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestInteractionIngressCarriesPreparedActionAdmissionToTaskEngine(t *testing.T) {
	catalog := feature.NewRegistry()
	surface := execution.SurfaceAssistant
	spec, err := feature.BindCanonicalCommands(feature.Spec{
		ID:   "prepared_callback",
		Name: "Prepared callback",
		Interactions: []feature.Interaction{{
			ID:       "deliver",
			Kind:     feature.InteractionAction,
			Surfaces: surface,
			Policy:   feature.PublicPolicy(surface),
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	featureScope := tasks.ScopeIdentity{Owner: "plugin:prepared_callback", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: "prepared_callback", Scope: featureScope}, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	actions := rootinteraction.NewDispatcher(sessions)
	engine, err := orchestration.New(sessions, actions, &preparedCallbackPort{})
	if err != nil {
		t.Fatal(err)
	}

	providerScope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 12}
	handlerCalls := 0
	actionRegistration, err := engine.RegisterPreparedAction(
		featureScope,
		"prepared_callback",
		"deliver",
		func(context.Context, rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
			return rootinteraction.ActionAdmission{
				Scope:     providerScope,
				Resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
			}, nil
		},
		func(*orchestration.Context) error {
			handlerCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer actionRegistration.Close()

	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: "prepared_callback",
		Binding: rootinteraction.Binding{
			ActorID:   7,
			ChatID:    42,
			MessageID: 77,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, "deliver")
	if err != nil {
		t.Fatal(err)
	}

	taskClient := &preparedCallbackTasks{}
	ack := &preparedCallbackAck{}
	ingress := &interactionIngress{engine: engine, ack: ack, tasks: taskClient}
	err = ingress.dispatchCallback(
		context.Background(),
		orchestration.CallbackRequest{
			Data:    data,
			ActorID: 7,
			QueryID: 9001,
			Target: presentationtelegram.MessageTarget{
				Peer:      &tg.InputPeerUser{UserID: 7},
				ChatID:    42,
				MessageID: 77,
			},
		},
		tasks.TaskID("test:prepared-callback"),
		"callback:test",
	)
	if err != nil {
		t.Fatalf("dispatchCallback() error=%v", err)
	}
	if taskClient.calls != 1 || handlerCalls != 1 {
		t.Fatalf("submits/handler=%d/%d, want 1/1", taskClient.calls, handlerCalls)
	}
	if taskClient.spec.Scope != providerScope {
		t.Fatalf("TaskEngine scope=%+v, want %+v", taskClient.spec.Scope, providerScope)
	}
	if taskClient.spec.Pool != tasks.PoolID("general") ||
		taskClient.spec.Class != tasks.PriorityInteractive ||
		taskClient.spec.ExecutionTimeout != 2*time.Minute {
		t.Fatalf(
			"TaskEngine admission pool=%q class=%q timeout=%s",
			taskClient.spec.Pool,
			taskClient.spec.Class,
			taskClient.spec.ExecutionTimeout,
		)
	}
	if len(taskClient.spec.Resources) != 1 ||
		taskClient.spec.Resources[0].Name != "media" ||
		taskClient.spec.Resources[0].Amount != 1 {
		t.Fatalf("TaskEngine resources=%+v, want media:1", taskClient.spec.Resources)
	}
	if ack.calls != 1 || ack.err != nil {
		t.Fatalf("callback ack calls=%d err=%v", ack.calls, ack.err)
	}
}
