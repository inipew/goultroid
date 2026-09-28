package client

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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
	calls     int
	immediate int
	err       error
}

func (a *preparedCallbackAck) acknowledge(context.Context, int64) {
	a.immediate++
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
	spec     tasks.WorkSpec
	calls    int
	onSubmit func()
}

func (c *preparedCallbackTasks) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.spec = spec
	c.calls++
	if c.onSubmit != nil {
		c.onSubmit()
	}
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err := spec.Handler(ctx); err != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure.Message = err.Error()
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
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
				Scope: providerScope,
				Profile: tasks.ExecutionProfile{
					Pool:             tasks.PoolID("general"),
					Class:            tasks.PriorityInteractive,
					ExecutionTimeout: 2 * time.Minute,
					Resources:        []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
				},
				AckPolicy: rootinteraction.AckImmediate,
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

	ack := &preparedCallbackAck{}
	taskClient := &preparedCallbackTasks{
		onSubmit: func() {
			if ack.immediate != 1 {
				t.Fatalf("TaskEngine Submit observed immediate acknowledgements=%d, want 1 before admission", ack.immediate)
			}
		},
	}
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
	if ack.immediate != 1 {
		t.Fatalf("immediate callback acknowledgements=%d, want 1", ack.immediate)
	}
	if ack.calls != 1 || ack.err != nil {
		t.Fatalf("callback completion ack calls=%d err=%v", ack.calls, ack.err)
	}
}

type blockingPreparedCallbackTasks struct {
	mu           sync.Mutex
	calls        int
	started      chan struct{}
	release      chan struct{}
	completed    chan struct{}
	startOnce    sync.Once
	completeOnce sync.Once
}

func (c *blockingPreparedCallbackTasks) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	go func() {
		c.startOnce.Do(func() { close(c.started) })
		<-c.release
		result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
		if err := spec.Handler(context.Background()); err != nil {
			result.Outcome = tasks.OutcomeFailed
			result.Failure.Message = err.Error()
		}
		if spec.OnComplete != nil {
			spec.OnComplete(result)
		}
		c.completeOnce.Do(func() { close(c.completed) })
	}()
	return nil, nil
}

func (*blockingPreparedCallbackTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*blockingPreparedCallbackTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*blockingPreparedCallbackTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func (c *blockingPreparedCallbackTasks) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type concurrentPreparedCallbackAck struct {
	mu               sync.Mutex
	calls            int
	busy             int
	cancelledContext int
}

func (a *concurrentPreparedCallbackAck) ensureAnswered(ctx context.Context, _ int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if ctx.Err() != nil {
		a.cancelledContext++
	}
	if message, ok := core.ExplicitUserMessage(err); ok && message == interactionBusyUserMessage {
		a.busy++
	}
}

func (a *concurrentPreparedCallbackAck) snapshot() (calls, busy, cancelledContext int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.busy, a.cancelledContext
}

func TestInteractionIngressSingleFlightCoalescesSameActorMessageBeforeTaskEngine(t *testing.T) {
	catalog := feature.NewRegistry()
	surface := execution.SurfaceAssistant
	spec, err := feature.BindCanonicalCommands(feature.Spec{
		ID:   "single_flight",
		Name: "Single flight",
		Interactions: []feature.Interaction{{
			ID:       "next",
			Kind:     feature.InteractionAction,
			Surfaces: surface,
			Policy:   feature.PublicPolicy(surface),
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.ScopeIdentity{Owner: "plugin:single_flight", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: "single_flight", Scope: scope}, spec)
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
	actionRegistration, err := engine.RegisterAction(scope, "single_flight", "next", func(*orchestration.Context) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer actionRegistration.Close()

	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: "single_flight",
		Binding: rootinteraction.Binding{
			ActorID:   7,
			ChatID:    42,
			MessageID: 77,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatal(err)
	}

	taskClient := &blockingPreparedCallbackTasks{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		completed: make(chan struct{}),
	}
	ack := &concurrentPreparedCallbackAck{}
	ingress := &interactionIngress{engine: engine, ack: ack, tasks: taskClient}
	request := func(queryID int64) orchestration.CallbackRequest {
		return orchestration.CallbackRequest{
			Data:    data,
			ActorID: 7,
			QueryID: queryID,
			Target: presentationtelegram.MessageTarget{
				Peer:      &tg.InputPeerUser{UserID: 7},
				ChatID:    42,
				MessageID: 77,
			},
		}
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- ingress.dispatchCallback(
			firstCtx,
			request(9101),
			tasks.TaskID("single-flight:first"),
			"callback:msg:42:77",
		)
	}()
	<-taskClient.started
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first callback admission error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first callback ingress remained blocked after TaskEngine admission")
	}
	cancelFirst()

	const duplicateBurst = 128
	var duplicateWG sync.WaitGroup
	duplicateWG.Add(duplicateBurst)
	duplicateErrs := make(chan error, duplicateBurst)
	for i := 0; i < duplicateBurst; i++ {
		i := i
		go func() {
			defer duplicateWG.Done()
			duplicateErrs <- ingress.dispatchCallback(
				context.Background(),
				request(int64(9102+i)),
				tasks.TaskID(fmt.Sprintf("single-flight:duplicate:%d", i)),
				"callback:msg:42:77",
			)
		}()
	}
	duplicateWG.Wait()
	close(duplicateErrs)
	for err := range duplicateErrs {
		if err != nil {
			t.Fatalf("coalesced callback error=%v", err)
		}
	}
	if got := taskClient.Calls(); got != 1 {
		t.Fatalf("coalesced callback submissions=%d, want 1", got)
	}
	calls, busy, cancelledContext := ack.snapshot()
	if calls != duplicateBurst || busy != duplicateBurst {
		t.Fatalf("coalesced callback acknowledgements calls/busy=%d/%d, want %d/%d", calls, busy, duplicateBurst, duplicateBurst)
	}
	if cancelledContext != 0 {
		t.Fatalf("coalesced callback acknowledgements used cancelled contexts=%d, want 0", cancelledContext)
	}

	close(taskClient.release)
	select {
	case <-taskClient.completed:
	case <-time.After(time.Second):
		t.Fatal("callback completion did not drain")
	}
	calls, busy, cancelledContext = ack.snapshot()
	if calls != duplicateBurst+1 {
		t.Fatalf("total callback acknowledgements=%d, want %d", calls, duplicateBurst+1)
	}
	if busy != duplicateBurst {
		t.Fatalf("busy callback acknowledgements=%d, want %d", busy, duplicateBurst)
	}
	if cancelledContext != 0 {
		t.Fatalf("completion acknowledgement reused cancelled update context")
	}
}
