package addon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type addonInlineTicket struct {
	id   tasks.TaskID
	res  tasks.TaskResult
	done chan struct{}
}

func (t *addonInlineTicket) TaskID() tasks.TaskID                           { return t.id }
func (t *addonInlineTicket) State() tasks.TaskState                         { return tasks.StateCompleted }
func (t *addonInlineTicket) Done() <-chan struct{}                          { return t.done }
func (t *addonInlineTicket) Result() (tasks.TaskResult, bool)               { return t.res, true }
func (t *addonInlineTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type addonBoundaryTaskClient struct {
	mu        sync.Mutex
	lastScope tasks.ScopeIdentity
	cancelled tasks.ScopeIdentity
	submits   int
}

func (c *addonBoundaryTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.lastScope = spec.Scope
	c.submits++
	c.mu.Unlock()

	res := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err := spec.Handler(ctx); err != nil {
		res.Outcome = tasks.OutcomeFailed
		res.Failure.Message = err.Error()
	}
	if spec.OnComplete != nil {
		spec.OnComplete(res)
	}
	done := make(chan struct{})
	close(done)
	return &addonInlineTicket{id: spec.ID, res: res, done: done}, nil
}

func (*addonBoundaryTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *addonBoundaryTaskClient) CancelScope(scope tasks.ScopeIdentity, _ tasks.Cause) int {
	c.mu.Lock()
	c.cancelled = scope
	c.mu.Unlock()
	return 1
}
func (*addonBoundaryTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}
func (c *addonBoundaryTaskClient) snapshot() (tasks.ScopeIdentity, tasks.ScopeIdentity, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastScope, c.cancelled, c.submits
}

type addonRuntimeCall struct {
	method string
	event  CanonicalEventEnvelope
}

type addonFakeInvoker struct {
	calls chan addonRuntimeCall
}

func (f *addonFakeInvoker) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	envelope, _ := params.(CanonicalEventEnvelope)
	f.calls <- addonRuntimeCall{method: method, event: envelope}
	return json.RawMessage(`{}`), nil
}

func TestCanonicalizeEventDoesNotExposeRawTelegramTypes(t *testing.T) {
	event := &core.MessageCreatedEvent{
		At:     time.Unix(100, 0),
		ChatID: 77,
		PeerID: &tg.PeerChannel{ChannelID: 77},
		Message: &core.Message{
			ID:        9,
			SenderID:  42,
			Text:      "hello @owner",
			MediaType: "photo",
			Entities: []tg.MessageEntityClass{
				&tg.MessageEntityMention{Offset: 6, Length: 6},
			},
		},
	}
	envelope, err := CanonicalizeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"PeerChannel", "entities", "peer_id", "access_hash", "InputPeer"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("canonical addon event leaked raw Telegram field/type %q: %s", forbidden, text)
		}
	}
	if envelope.Type != EventMessageCreated {
		t.Fatalf("event type=%q", envelope.Type)
	}
	var payload MessageCreatedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != 9 || payload.ChatID != 77 || payload.SenderID != 42 || payload.MediaType != "photo" {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestCanonicalizeCallbackDropsRawTarget(t *testing.T) {
	event := &core.CallbackQueryEvent{
		At:      time.Unix(101, 0),
		QueryID: 1,
		UserID:  2,
		ChatID:  3,
		MsgID:   4,
		Data:    []byte("action"),
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      &tg.InputPeerChannel{ChannelID: 3, AccessHash: 999},
			MessageID: 4,
		},
	}
	envelope, err := CanonicalizeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "999") || strings.Contains(string(data), "InputPeer") || strings.Contains(string(data), "target") {
		t.Fatalf("callback DTO leaked raw target: %s", data)
	}
}

func TestRuntimeEventBindingUsesScopedTaskAndClosesCleanly(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", []Capability{CapTelegramRead})
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())

	client := &addonBoundaryTaskClient{}
	bus := core.NewEventBus()
	bus.SetTasks(client)
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	manager.SetRuntimeBoundary(bus, client)

	invoker := &addonFakeInvoker{calls: make(chan addonRuntimeCall, 1)}
	binding, err := manager.bindRuntimeContract("sample", Manifest{
		Name: "sample", Version: "1.0.0",
		Events:       []EventType{EventMessageCreated},
		Capabilities: []Capability{CapTelegramRead},
	}, invoker)
	if err != nil {
		t.Fatal(err)
	}
	if binding.scope.Owner != "addon:sample" || binding.scope.Generation == 0 {
		t.Fatalf("scope=%+v", binding.scope)
	}
	if got := bus.SubscriptionCount("addon:sample"); got != 1 {
		t.Fatalf("subscriptions=%d, want 1", got)
	}

	bus.Publish(&core.MessageCreatedEvent{
		At:      time.Now(),
		ChatID:  77,
		Message: &core.Message{ID: 5, Text: "hello"},
	})
	select {
	case call := <-invoker.calls:
		if call.method != string(OperationEventHandle) || call.event.Type != EventMessageCreated {
			t.Fatalf("runtime call=%+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("addon runtime did not receive canonical event")
	}

	lastScope, _, submits := client.snapshot()
	if submits != 1 || lastScope != binding.scope {
		t.Fatalf("task scope=%+v submits=%d, want %+v/1", lastScope, submits, binding.scope)
	}

	binding.close(client)
	if got := bus.SubscriptionCount("addon:sample"); got != 0 {
		t.Fatalf("subscription remained after close: %d", got)
	}
	_, cancelled, _ := client.snapshot()
	if cancelled != binding.scope {
		t.Fatalf("cancelled scope=%+v, want %+v", cancelled, binding.scope)
	}
}

func TestRuntimeEventBindingFailsClosedWithoutBoundary(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", []Capability{CapTelegramRead})
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())
	_, err := manager.bindRuntimeContract("sample", Manifest{
		Name: "sample", Version: "1.0.0",
		Events:       []EventType{EventMessageCreated},
		Capabilities: []Capability{CapTelegramRead},
	}, &addonFakeInvoker{calls: make(chan addonRuntimeCall, 1)})
	if err != ErrRuntimeBoundaryUnavailable {
		t.Fatalf("error=%v, want ErrRuntimeBoundaryUnavailable", err)
	}
}

func TestRuntimeOperationCapabilityBinding(t *testing.T) {
	required, ok := RequiredCapability(OperationEventHandle)
	if !ok || required != CapTelegramRead {
		t.Fatalf("event operation capability=%q ok=%v", required, ok)
	}
	if _, ok := RequiredCapability(RuntimeOperation("raw.call")); ok {
		t.Fatal("arbitrary runtime operation was accepted")
	}
}

type addonCommandInvoker struct {
	response json.RawMessage
	calls    chan CommandInvocation
}

func (f *addonCommandInvoker) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != string(OperationCommandHandle) {
		return nil, errors.New("unexpected runtime operation: " + method)
	}
	invocation, ok := params.(CommandInvocation)
	if !ok {
		return nil, errors.New("unexpected command invocation payload")
	}
	f.calls <- invocation
	return append(json.RawMessage(nil), f.response...), nil
}

func TestRuntimeCommandBindingRegistersScopedOwnerOnlyCommand(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", nil)
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())
	client := &addonBoundaryTaskClient{}
	router := core.NewRouter(".")
	manager.SetRuntimeBoundary(nil, client)
	manager.SetCommandRouter(router)

	invoker := &addonCommandInvoker{
		response: json.RawMessage(`{"disposition":"success"}`),
		calls:    make(chan CommandInvocation, 1),
	}
	binding, err := manager.bindRuntimeContract("sample", Manifest{
		Name: "sample", Version: "1.0.0", Commands: []string{"hello"},
	}, invoker)
	if err != nil {
		t.Fatal(err)
	}
	cmd, ok := router.Find("hello")
	if !ok {
		t.Fatal("addon command was not registered")
	}
	if cmd.Scope != binding.scope {
		t.Fatalf("command scope=%+v, want %+v", cmd.Scope, binding.scope)
	}
	if cmd.Permission != core.PermissionOwner ||
		cmd.Invocation.Userbot != core.InvocationSelfOnly ||
		cmd.Invocation.Assistant != core.InvocationSelfOnly {
		t.Fatalf("unsafe addon command policy: %+v", cmd)
	}
	if !cmd.IsAvailableOn(execution.SourceUserbot) || !cmd.IsAvailableOn(execution.SourceAssistant) {
		t.Fatalf("addon command surfaces=%v, want userbot+assistant", cmd.Surfaces)
	}

	ctx := &core.Context{
		Ctx:           context.Background(),
		Source:        core.ExecutionInteractive,
		Command:       "hello",
		Args:          []string{"one", "two"},
		RawArgs:       "one two",
		CorrelationID: "corr-1",
	}
	if err := cmd.Handler(ctx); err != nil {
		t.Fatalf("addon command handler: %v", err)
	}
	select {
	case invocation := <-invoker.calls:
		if invocation.Command != "hello" || invocation.RawArgs != "one two" ||
			invocation.Source != "interactive" || invocation.CorrelationID != "corr-1" {
			t.Fatalf("invocation=%+v", invocation)
		}
		data, err := json.Marshal(invocation)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"peer", "access_hash", "entities", "InputPeer", "PeerChannel"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("command invocation leaked Telegram internals %q: %s", forbidden, data)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not receive command invocation")
	}

	binding.close(client)
	if _, ok := router.Find("hello"); ok {
		t.Fatal("addon command remained registered after binding close")
	}
	_, cancelled, _ := client.snapshot()
	if cancelled != binding.scope {
		t.Fatalf("cancelled scope=%+v, want %+v", cancelled, binding.scope)
	}
}

func TestRuntimeCommandResultPreservesTypedSemantics(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", nil)
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())
	client := &addonBoundaryTaskClient{}
	router := core.NewRouter(".")
	manager.SetRuntimeBoundary(nil, client)
	manager.SetCommandRouter(router)

	invoker := &addonCommandInvoker{
		response: json.RawMessage(`{"disposition":"retryable","code":"upstream_busy","error":"try later","retry_after_ms":250}`),
		calls:    make(chan CommandInvocation, 1),
	}
	binding, err := manager.bindRuntimeContract("sample", Manifest{
		Name: "sample", Version: "1.0.0", Commands: []string{"retry"},
	}, invoker)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.close(client)

	cmd, ok := router.Find("retry")
	if !ok {
		t.Fatal("retry command missing")
	}
	err = cmd.Handler(&core.Context{Ctx: context.Background(), Source: core.ExecutionInteractive})
	if err == nil {
		t.Fatal("retryable addon result returned nil error")
	}
	semantics := execution.SemanticsOf(err)
	if semantics.Disposition != execution.DispositionRetryable ||
		semantics.Code != "upstream_busy" ||
		semantics.RetryAfter != 250*time.Millisecond {
		t.Fatalf("semantics=%+v", semantics)
	}
}

func TestRuntimeCommandReplyRequiresTelegramSend(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", nil)
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())
	client := &addonBoundaryTaskClient{}
	router := core.NewRouter(".")
	manager.SetRuntimeBoundary(nil, client)
	manager.SetCommandRouter(router)

	invoker := &addonCommandInvoker{
		response: json.RawMessage(`{"disposition":"success","reply":"hello"}`),
		calls:    make(chan CommandInvocation, 1),
	}
	binding, err := manager.bindRuntimeContract("sample", Manifest{
		Name: "sample", Version: "1.0.0", Commands: []string{"reply"},
	}, invoker)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.close(client)

	cmd, _ := router.Find("reply")
	err = cmd.Handler(&core.Context{Ctx: context.Background(), Source: core.ExecutionInteractive})
	if err == nil {
		t.Fatal("reply side effect succeeded without telegram.send")
	}
	semantics := execution.SemanticsOf(err)
	if semantics.Disposition != execution.DispositionRejected || semantics.Code != "addon_capability_denied" {
		t.Fatalf("reply denial semantics=%+v", semantics)
	}
}

func TestCommandHandleCannotUseGenericRuntimeCall(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", []Capability{CapTelegramRead})
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())

	required, ok := RequiredCapability(OperationCommandHandle)
	if !ok || required != "" {
		t.Fatalf("command operation capability=%q ok=%v, want host-contract operation", required, ok)
	}
	if _, err := manager.CallRuntimeOperation(context.Background(), "sample", OperationCommandHandle, CommandInvocation{}); err == nil {
		t.Fatal("generic runtime API reached host-contract command operation")
	} else if semantics := execution.SemanticsOf(err); semantics.Disposition != execution.DispositionRejected {
		t.Fatalf("generic command denial semantics=%+v", semantics)
	}
	if _, err := manager.CallRuntimeWithCapability(context.Background(), "sample", CapTelegramRead, string(OperationCommandHandle), CommandInvocation{}); !errors.Is(err, ErrUnauthorizedCapability) {
		t.Fatalf("legacy generic call error=%v, want ErrUnauthorizedCapability", err)
	}
}

func TestStartRuntimeRejectsExistingOrConcurrentGeneration(t *testing.T) {
	manager := NewManager(nil, NewCapabilityGate(), "1.0.0", zap.NewNop())

	manager.runtimeMu.Lock()
	manager.runtimes["sample"] = &ExternalRuntime{}
	manager.runtimeMu.Unlock()
	if err := manager.StartRuntime(context.Background(), "sample", "", ""); !errors.Is(err, ErrAddonRuntimeRunning) {
		t.Fatalf("running guard error=%v, want ErrAddonRuntimeRunning", err)
	}

	manager.runtimeMu.Lock()
	delete(manager.runtimes, "sample")
	manager.runtimeStarting["sample"] = &runtimeStartup{cancel: func() {}, done: make(chan struct{})}
	manager.runtimeMu.Unlock()
	if err := manager.StartRuntime(context.Background(), "sample", "", ""); !errors.Is(err, ErrAddonRuntimeRunning) {
		t.Fatalf("starting guard error=%v, want ErrAddonRuntimeRunning", err)
	}
}

func TestStopRuntimeCancelsAndWaitsForStartup(t *testing.T) {
	manager := NewManager(nil, NewCapabilityGate(), "1.0.0", zap.NewNop())
	cancelled := make(chan struct{})
	startup := &runtimeStartup{
		cancel: func() { close(cancelled) },
		done:   make(chan struct{}),
	}
	manager.runtimeMu.Lock()
	manager.runtimeStarting["sample"] = startup
	manager.runtimeMu.Unlock()

	go func() {
		<-cancelled
		close(startup.done)
	}()

	if err := manager.StopRuntime("sample"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startup.done:
	default:
		t.Fatal("StopRuntime returned before in-progress startup finished")
	}
}

func TestRuntimeBindingRollsBackCommandsWhenEventBindingFails(t *testing.T) {
	gate := NewCapabilityGate()
	gate.Register("sample", nil)
	manager := NewManager(nil, gate, "1.0.0", zap.NewNop())
	client := &addonBoundaryTaskClient{}
	router := core.NewRouter(".")
	bus := core.NewEventBus()
	manager.SetRuntimeBoundary(bus, client)
	manager.SetCommandRouter(router)

	invoker := &addonCommandInvoker{
		response: json.RawMessage(`{}`),
		calls:    make(chan CommandInvocation, 1),
	}
	_, err := manager.bindRuntimeContract("sample", Manifest{
		Name:         "sample",
		Version:      "1.0.0",
		Commands:     []string{"hello"},
		Events:       []EventType{EventMessageCreated},
		Capabilities: nil,
	}, invoker)
	if err == nil {
		t.Fatal("binding succeeded without telegram.read")
	}
	if _, ok := router.Find("hello"); ok {
		t.Fatal("command registration was not rolled back")
	}
	if got := bus.SubscriptionCount("addon:sample"); got != 0 {
		t.Fatalf("subscriptions remained after rollback: %d", got)
	}
	_, cancelled, _ := client.snapshot()
	if cancelled.Owner != "addon:sample" || cancelled.Generation == 0 {
		t.Fatalf("scope cancellation missing after rollback: %+v", cancelled)
	}
}
