package addon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type addonInlineTicket struct {
	id   tasks.TaskID
	res  tasks.TaskResult
	done chan struct{}
}

func (t *addonInlineTicket) TaskID() tasks.TaskID { return t.id }
func (t *addonInlineTicket) State() tasks.TaskState { return tasks.StateCompleted }
func (t *addonInlineTicket) Done() <-chan struct{} { return t.done }
func (t *addonInlineTicket) Result() (tasks.TaskResult, bool) { return t.res, true }
func (t *addonInlineTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type addonBoundaryTaskClient struct {
	mu sync.Mutex
	lastScope tasks.ScopeIdentity
	cancelled tasks.ScopeIdentity
	submits int
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
		At: time.Unix(100, 0),
		ChatID: 77,
		PeerID: &tg.PeerChannel{ChannelID: 77},
		Message: &core.Message{
			ID: 9,
			SenderID: 42,
			Text: "hello @owner",
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
		At: time.Unix(101, 0),
		QueryID: 1,
		UserID: 2,
		ChatID: 3,
		MsgID: 4,
		Data: []byte("action"),
		Target: core.CallbackTarget{
			Origin: core.CallbackOriginMessage,
			Peer: &tg.InputPeerChannel{ChannelID: 3, AccessHash: 999},
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
	binding, err := manager.bindRuntimeEvents("sample", Manifest{
		Name: "sample", Version: "1.0.0",
		Events: []EventType{EventMessageCreated},
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
		At: time.Now(),
		ChatID: 77,
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
	_, err := manager.bindRuntimeEvents("sample", Manifest{
		Name: "sample", Version: "1.0.0",
		Events: []EventType{EventMessageCreated},
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
