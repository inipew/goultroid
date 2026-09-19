package telegram

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type behaviorTaskClient struct {
	inner tasks.Client

	mu        sync.Mutex
	submits   map[string]int
	resources map[string][]tasks.ResourceRequirement
}

func newBehaviorTaskClient(inner tasks.Client) *behaviorTaskClient {
	return &behaviorTaskClient{
		inner:     inner,
		submits:   make(map[string]int),
		resources: make(map[string][]tasks.ResourceRequirement),
	}
}

func behaviorTaskKind(id tasks.TaskID) string {
	s := string(id)
	switch {
	case strings.HasPrefix(s, "decision:"):
		return "decision"
	case strings.HasPrefix(s, "hook:"):
		return "hook"
	case strings.HasPrefix(s, "cmd:"):
		return "cmd"
	case strings.HasPrefix(s, "event:"):
		return "event"
	default:
		return "other"
	}
}

func (c *behaviorTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	kind := behaviorTaskKind(spec.ID)
	c.mu.Lock()
	c.submits[kind]++
	c.resources[kind] = append([]tasks.ResourceRequirement(nil), spec.Resources...)
	c.mu.Unlock()
	return c.inner.Submit(ctx, spec)
}

func (c *behaviorTaskClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return c.inner.Cancel(id, reason)
}

func (c *behaviorTaskClient) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	return c.inner.CancelScope(scope, reason)
}

func (c *behaviorTaskClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return c.inner.Snapshot(id)
}

func (c *behaviorTaskClient) count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submits[kind]
}

func (c *behaviorTaskClient) resourcesFor(kind string) []tasks.ResourceRequirement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tasks.ResourceRequirement(nil), c.resources[kind]...)
}

type dispatcherBehaviorExpectation struct {
	decisionTasks int
	hookTasks     int
	commandTasks  int
	eventTasks    int

	decisionCalls int32
	hookCalls     int32
	commandCalls  int32
	eventCalls    int32
	durableClaims int32
}

type dispatcherBehaviorCase struct {
	name string

	message       *tg.Message
	dispatchTwice bool
	preQuiesce    bool

	hasCommand        bool
	commandName       string
	commandPermission core.Permission
	commandInvocation core.InvocationPolicy
	commandResources  []tasks.ResourceRequirement

	decisionActive   bool
	decisionSuppress bool
	eventActive      bool
	subscribeEvent   bool

	want dispatcherBehaviorExpectation
}

func cloneBehaviorMessage(msg *tg.Message) *tg.Message {
	if msg == nil {
		return nil
	}
	cp := *msg
	return &cp
}

func runDispatcherBehaviorCase(t *testing.T, tc dispatcherBehaviorCase) {
	t.Helper()

	engineConfig := taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {Concurrency: 2, MinConcurrency: 1, BacklogLimit: 16},
			"general":     {Concurrency: 2, MinConcurrency: 1, BacklogLimit: 16},
		},
		ResultCapacity: 64,
	}
	if len(tc.commandResources) > 0 {
		engineConfig.ResourceCapacities = map[string]int64{"process": 1}
	}
	engine := taskengine.NewEngine(engineConfig)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start task engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	client := newBehaviorTaskClient(engine)

	bus := core.NewEventBus()
	bus.SetTasks(client)
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	router := core.NewRouter(".")
	var commandCalls atomic.Int32
	if tc.hasCommand {
		name := tc.commandName
		if name == "" {
			name = "run"
		}
		if err := router.Register(core.Command{
			Name:       name,
			Permission: tc.commandPermission,
			Invocation: tc.commandInvocation,
			Resources:  append([]tasks.ResourceRequirement(nil), tc.commandResources...),
			Handler: func(*core.Context) error {
				commandCalls.Add(1)
				return nil
			},
		}); err != nil {
			t.Fatalf("register command: %v", err)
		}
	}

	repo := &countingIdempotencyRepository{}
	idemp := idempotency.NewManager(time.Minute, repo)
	defer idemp.Close()

	d := NewDispatcher(router, core.NewPermissions(100, []int64{200}), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetTasks(client)
	d.SetEventBus(bus)
	d.SetIdempotency(idemp)

	var decisionCalls atomic.Int32
	var hookCalls atomic.Int32
	var eventCalls atomic.Int32

	d.AddScopedCanonicalMessageHandlerWithRoutingAndState(
		PrioritySecurity,
		tasks.ScopeIdentity{Owner: "plugin:behavior-decision", Generation: 1},
		core.MessageHookRouting{Lane: core.MessageHookDecision},
		func(int64) bool { return tc.decisionActive },
		func(context.Context, *core.MessageEnvelope) error {
			decisionCalls.Add(1)
			if tc.decisionSuppress {
				return core.ErrInterceptHandled
			}
			return nil
		},
	)
	d.AddScopedCanonicalMessageHandlerWithRoutingAndState(
		PriorityFeature,
		tasks.ScopeIdentity{Owner: "plugin:behavior-event", Generation: 1},
		core.MessageHookRouting{Lane: core.MessageHookEvent},
		func(int64) bool { return tc.eventActive },
		func(context.Context, *core.MessageEnvelope) error {
			hookCalls.Add(1)
			return nil
		},
	)

	var unsubscribe func()
	if tc.subscribeEvent {
		unsubscribe = bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) {
			eventCalls.Add(1)
		})
	}
	if unsubscribe != nil {
		defer unsubscribe()
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if tc.preQuiesce {
		if err := d.Drain(drainCtx); err != nil {
			t.Fatalf("pre-quiesce drain: %v", err)
		}
	}

	msg := cloneBehaviorMessage(tc.message)
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if tc.dispatchTwice {
		if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
			t.Fatalf("duplicate dispatch: %v", err)
		}
	}

	if err := d.Drain(drainCtx); err != nil {
		t.Fatalf("dispatcher drain: %v", err)
	}
	if err := bus.CloseContext(drainCtx); err != nil {
		t.Fatalf("event bus close: %v", err)
	}
	if err := engine.Stop(drainCtx); err != nil {
		t.Fatalf("task engine stop: %v", err)
	}

	got := dispatcherBehaviorExpectation{
		decisionTasks: client.count("decision"),
		hookTasks:     client.count("hook"),
		commandTasks:  client.count("cmd"),
		eventTasks:    client.count("event"),
		decisionCalls: decisionCalls.Load(),
		hookCalls:     hookCalls.Load(),
		commandCalls:  commandCalls.Load(),
		eventCalls:    eventCalls.Load(),
		durableClaims: repo.claims.Load(),
	}
	if got != tc.want {
		t.Fatalf("behavior mismatch\n got: %+v\nwant: %+v", got, tc.want)
	}
	if other := client.count("other"); other != 0 {
		t.Fatalf("unexpected unclassified task submissions=%d", other)
	}

	if tc.want.commandTasks == 1 && len(tc.commandResources) > 0 {
		gotResources := client.resourcesFor("cmd")
		if len(gotResources) != len(tc.commandResources) {
			t.Fatalf("command resource count=%d, want %d: got=%+v want=%+v",
				len(gotResources), len(tc.commandResources), gotResources, tc.commandResources)
		}
		for i := range tc.commandResources {
			if gotResources[i] != tc.commandResources[i] {
				t.Fatalf("command resource[%d]=%+v, want %+v", i, gotResources[i], tc.commandResources[i])
			}
		}
	}
}

func TestDispatcherBehaviorMatrix(t *testing.T) {
	incomingPlain := func(id int) *tg.Message {
		return &tg.Message{
			ID: id, PeerID: &tg.PeerChat{ChatID: 7},
			FromID: &tg.PeerUser{UserID: 300}, Message: "hello",
		}
	}
	incomingCommand := func(id int, name string) *tg.Message {
		return &tg.Message{
			ID: id, PeerID: &tg.PeerChat{ChatID: 7},
			FromID: &tg.PeerUser{UserID: 300}, Message: "." + name,
		}
	}
	outgoingCommand := func(id int, name string) *tg.Message {
		return &tg.Message{
			ID: id, Out: true, PeerID: &tg.PeerChat{ChatID: 7},
			Message: "." + name,
		}
	}

	cases := []dispatcherBehaviorCase{
		{
			name:           "plain inactive features have zero execution cost",
			message:        incomingPlain(1),
			decisionActive: false,
			eventActive:    false,
			want:           dispatcherBehaviorExpectation{},
		},
		{
			name:           "plain active features use decision and event lanes only",
			message:        incomingPlain(2),
			decisionActive: true,
			eventActive:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1, hookTasks: 1,
				decisionCalls: 1, hookCalls: 1,
			},
		},
		{
			name:           "unknown command remains observational but never durable",
			message:        incomingCommand(3, "missing"),
			decisionActive: true,
			eventActive:    true,
			subscribeEvent: true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1, hookTasks: 1, eventTasks: 1,
				decisionCalls: 1, hookCalls: 1, eventCalls: 1,
			},
		},
		{
			name:              "decision suppression stops before invocation durability and observers",
			message:           outgoingCommand(4, "run"),
			hasCommand:        true,
			commandName:       "run",
			commandPermission: core.PermissionEveryone,
			decisionActive:    true,
			decisionSuppress:  true,
			eventActive:       true,
			subscribeEvent:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1,
				decisionCalls: 1,
			},
		},
		{
			name:              "invocation denial stops before durability and task admission",
			message:           incomingCommand(5, "run"),
			hasCommand:        true,
			commandName:       "run",
			commandPermission: core.PermissionEveryone,
			decisionActive:    true,
			eventActive:       true,
			subscribeEvent:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1,
				decisionCalls: 1,
			},
		},
		{
			name:              "permission denial occurs inside admitted command task",
			message:           incomingCommand(6, "admin"),
			hasCommand:        true,
			commandName:       "admin",
			commandPermission: core.PermissionOwner,
			commandInvocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
			decisionActive:    true,
			eventActive:       true,
			subscribeEvent:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1, hookTasks: 1, commandTasks: 1, eventTasks: 1,
				decisionCalls: 1, hookCalls: 1, eventCalls: 1, durableClaims: 1,
			},
		},
		{
			name:              "valid owner command traverses full pipeline once",
			message:           outgoingCommand(7, "run"),
			hasCommand:        true,
			commandName:       "run",
			commandPermission: core.PermissionEveryone,
			commandResources:  []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			decisionActive:    true,
			eventActive:       true,
			subscribeEvent:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1, hookTasks: 1, commandTasks: 1, eventTasks: 1,
				decisionCalls: 1, hookCalls: 1, commandCalls: 1, eventCalls: 1, durableClaims: 1,
			},
		},
		{
			name:              "duplicate recognized command is stopped at ingress",
			message:           outgoingCommand(8, "run"),
			dispatchTwice:     true,
			hasCommand:        true,
			commandName:       "run",
			commandPermission: core.PermissionEveryone,
			decisionActive:    true,
			eventActive:       true,
			subscribeEvent:    true,
			want: dispatcherBehaviorExpectation{
				decisionTasks: 1, hookTasks: 1, commandTasks: 1, eventTasks: 1,
				decisionCalls: 1, hookCalls: 1, commandCalls: 1, eventCalls: 1, durableClaims: 1,
			},
		},
		{
			name:              "quiesced ingress cannot create any downstream work",
			message:           outgoingCommand(9, "run"),
			preQuiesce:        true,
			hasCommand:        true,
			commandName:       "run",
			commandPermission: core.PermissionEveryone,
			decisionActive:    true,
			eventActive:       true,
			subscribeEvent:    true,
			want:              dispatcherBehaviorExpectation{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDispatcherBehaviorCase(t, tc)
		})
	}
}

func TestDispatcherBehaviorMissingTaskRuntimeFailsClosedBeforeDurability(t *testing.T) {
	router := core.NewRouter(".")
	var commandCalls atomic.Int32
	if err := router.Register(core.Command{
		Name:       "run",
		Permission: core.PermissionEveryone,
		Handler: func(*core.Context) error {
			commandCalls.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	repo := &countingIdempotencyRepository{}
	idemp := idempotency.NewManager(time.Minute, repo)
	defer idemp.Close()

	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idemp)

	var decisionCalls atomic.Int32
	d.AddScopedCanonicalMessageHandlerWithRoutingAndState(
		PrioritySecurity,
		tasks.ScopeIdentity{Owner: "plugin:security", Generation: 1},
		core.MessageHookRouting{Lane: core.MessageHookDecision},
		func(int64) bool { return true },
		func(context.Context, *core.MessageEnvelope) error {
			decisionCalls.Add(1)
			return nil
		},
	)

	msg := &tg.Message{
		ID: 100, Out: true, PeerID: &tg.PeerChat{ChatID: 7}, Message: ".run",
	}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := decisionCalls.Load(); got != 0 {
		t.Fatalf("security handler calls=%d, want 0 without TaskEngine", got)
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("durable claims=%d, want 0 when security execution authority is unavailable", got)
	}
	if got := commandCalls.Load(); got != 0 {
		t.Fatalf("command calls=%d, want 0", got)
	}
}

type behaviorSequenceRecorder struct {
	mu     sync.Mutex
	values []string
}

func (r *behaviorSequenceRecorder) add(value string) {
	r.mu.Lock()
	r.values = append(r.values, value)
	r.mu.Unlock()
}

func (r *behaviorSequenceRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.values...)
}

type orderedIdempotencyRepository struct {
	sequence *behaviorSequenceRecorder
}

func (r *orderedIdempotencyRepository) record(value string) {
	r.sequence.add(value)
}

func (*orderedIdempotencyRepository) InitSchema(context.Context) error { return nil }
func (r *orderedIdempotencyRepository) Claim(context.Context, string, time.Time, time.Time) (bool, error) {
	r.record("durable-claim")
	return true, nil
}
func (*orderedIdempotencyRepository) IsProcessed(context.Context, string, time.Time) (bool, error) {
	return false, nil
}
func (*orderedIdempotencyRepository) DeleteExpired(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (*orderedIdempotencyRepository) EarliestExpiry(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (*orderedIdempotencyRepository) Size(context.Context, time.Time) (int, error) {
	return 0, nil
}

type orderedTaskClient struct {
	inner    tasks.Client
	sequence *behaviorSequenceRecorder
}

func (c *orderedTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if behaviorTaskKind(spec.ID) == "cmd" {
		c.sequence.add("command-admission")
	}
	return c.inner.Submit(ctx, spec)
}
func (c *orderedTaskClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return c.inner.Cancel(id, reason)
}
func (c *orderedTaskClient) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	return c.inner.CancelScope(scope, reason)
}
func (c *orderedTaskClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return c.inner.Snapshot(id)
}

func TestDispatcherBehaviorDurableClaimPrecedesCommandAdmission(t *testing.T) {
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {Concurrency: 1, MinConcurrency: 1, BacklogLimit: 4},
		},
		ResultCapacity: 8,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	sequence := &behaviorSequenceRecorder{}
	repo := &orderedIdempotencyRepository{sequence: sequence}
	idemp := idempotency.NewManager(time.Minute, repo)
	defer idemp.Close()
	client := &orderedTaskClient{inner: engine, sequence: sequence}

	router := core.NewRouter(".")
	if err := router.Register(core.Command{
		Name:       "mutate",
		Permission: core.PermissionEveryone,
		Handler:    func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idemp)
	d.SetTasks(client)

	msg := &tg.Message{
		ID: 101, Out: true, PeerID: &tg.PeerChat{ChatID: 7}, Message: ".mutate",
	}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	gotSequence := sequence.snapshot()
	if len(gotSequence) != 2 {
		t.Fatalf("sequence=%v, want [durable-claim command-admission]", gotSequence)
	}
	if gotSequence[0] != "durable-claim" || gotSequence[1] != "command-admission" {
		t.Fatalf("pipeline ordering regressed: %v", gotSequence)
	}
}
