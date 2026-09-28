package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type decisionPolicyTicket struct {
	id      tasks.TaskID
	result  tasks.TaskResult
	waitErr error
	done    chan struct{}
}

func (t *decisionPolicyTicket) TaskID() tasks.TaskID { return t.id }
func (t *decisionPolicyTicket) State() tasks.TaskState {
	if t.result.IsSuccess() {
		return tasks.StateCompleted
	}
	return tasks.StateFailed
}
func (t *decisionPolicyTicket) Done() <-chan struct{} { return t.done }
func (t *decisionPolicyTicket) Result() (tasks.TaskResult, bool) {
	return t.result, true
}
func (t *decisionPolicyTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return t.result, t.waitErr
}

type decisionPolicyTaskClient struct {
	submitErr error
	waitErr   error
	run       bool
}

func (c *decisionPolicyTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if c.submitErr != nil {
		return nil, c.submitErr
	}
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if c.run && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			result.Outcome = tasks.OutcomeFailed
			result.Failure.Message = err.Error()
		}
	}
	if c.waitErr != nil {
		result.Outcome = tasks.OutcomeTimedOut
		result.Cause = tasks.CauseTimeout
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	done := make(chan struct{})
	close(done)
	return &decisionPolicyTicket{
		id:      spec.ID,
		result:  result,
		waitErr: c.waitErr,
		done:    done,
	}, nil
}

func (*decisionPolicyTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*decisionPolicyTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*decisionPolicyTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func decisionPolicyFixture(
	priority HandlerPriority,
	handler CanonicalMessageHandler,
) (prioritizedHandler, *core.MessageEnvelope, *tg.Message) {
	msg := &tg.Message{
		ID:      77,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 7},
		Message: "hello",
	}
	envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 1)
	return prioritizedHandler{
		id:               1,
		priority:         priority,
		failurePolicy:    failurePolicyForPriority(priority),
		routing:          core.MessageHookRouting{Lane: core.MessageHookDecision},
		canonicalHandler: handler,
		scope:            tasks.ScopeIdentity{Owner: "plugin:decision-policy", Generation: 1},
	}, envelope, msg
}

func TestDecisionHandlerInfrastructureFailureUsesRegisteredPolicy(t *testing.T) {
	infrastructureErr := errors.New("task infrastructure unavailable")
	cases := []struct {
		name     string
		priority HandlerPriority
		mode     string
		want     bool
	}{
		{name: "security missing task client fails closed", priority: PrioritySecurity, mode: "missing", want: true},
		{name: "feature missing task client fails open", priority: PriorityFeature, mode: "missing", want: false},
		{name: "security submit rejection fails closed", priority: PrioritySecurity, mode: "submit", want: true},
		{name: "feature submit rejection fails open", priority: PriorityFeature, mode: "submit", want: false},
		{name: "security wait failure fails closed", priority: PrioritySecurity, mode: "wait", want: true},
		{name: "feature wait failure fails open", priority: PriorityFeature, mode: "wait", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			registered, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
				t.Fatal("handler ran during infrastructure failure")
				return nil
			})
			switch tc.mode {
			case "submit":
				d.SetTasks(&decisionPolicyTaskClient{submitErr: infrastructureErr})
			case "wait":
				d.SetTasks(&decisionPolicyTaskClient{waitErr: infrastructureErr})
			case "missing":
			default:
				t.Fatalf("unknown mode %q", tc.mode)
			}

			got := d.executeDecisionHandlersEnvelope(
				context.Background(),
				[]prioritizedHandler{registered},
				envelope,
				tg.Entities{},
				msg,
			)
			if got != tc.want {
				t.Fatalf("handled=%v, want %v for policy=%v mode=%s", got, tc.want, registered.failurePolicy, tc.mode)
			}
		})
	}
}

func TestDecisionHandlerExecutionFailureUsesSameRegisteredPolicy(t *testing.T) {
	cases := []struct {
		name     string
		priority HandlerPriority
		panic    bool
		want     bool
	}{
		{name: "security handler error fails closed", priority: PrioritySecurity, want: true},
		{name: "feature handler error fails open", priority: PriorityFeature, want: false},
		{name: "security handler panic fails closed", priority: PrioritySecurity, panic: true, want: true},
		{name: "feature handler panic fails open", priority: PriorityFeature, panic: true, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			d.SetTasks(&decisionPolicyTaskClient{run: true})
			registered, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
				if tc.panic {
					panic("decision handler panic")
				}
				return errors.New("decision handler failure")
			})

			got := d.executeDecisionHandlersEnvelope(
				context.Background(),
				[]prioritizedHandler{registered},
				envelope,
				tg.Entities{},
				msg,
			)
			if got != tc.want {
				t.Fatalf("handled=%v, want %v for policy=%v panic=%v", got, tc.want, registered.failurePolicy, tc.panic)
			}
		})
	}
}
