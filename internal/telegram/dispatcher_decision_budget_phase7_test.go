package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// phase7DecisionClient models accepted work without starting a second engine.
// It exposes the exact WorkSpec and terminal result seen by the dispatcher.
type phase7DecisionClient struct {
	specs       []tasks.WorkSpec
	outcome     tasks.Outcome
	waitErr     error
	run         bool
	cancelled   tasks.TaskID
	cancelCause tasks.Cause
}

func (c *phase7DecisionClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if c.run && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			result.Outcome = tasks.OutcomeFailed
		}
	}
	if c.outcome != "" {
		result.Outcome = c.outcome
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	done := make(chan struct{})
	close(done)
	return &decisionPolicyTicket{id: spec.ID, result: result, waitErr: c.waitErr, done: done}, nil
}

func (c *phase7DecisionClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	c.cancelled = id
	c.cancelCause = reason
	return tasks.CancelReceipt{TaskID: id, Accepted: true}, nil
}

func (*phase7DecisionClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*phase7DecisionClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestPhase7ExpiredDecisionBudgetHonorsSecurityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		priority HandlerPriority
		scoped   bool
		want     bool
	}{
		{name: "scoped security", priority: PrioritySecurity, scoped: true, want: true},
		{name: "scoped feature", priority: PriorityFeature, scoped: true},
		{name: "local security", priority: PrioritySecurity, want: true},
		{name: "local feature", priority: PriorityFeature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			client := &phase7DecisionClient{run: true}
			d.SetTasks(client)
			h, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
				t.Fatal("expired decision handler must never execute")
				return nil
			})
			if !tc.scoped {
				h.scope = tasks.ScopeIdentity{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got := d.executeDecisionHandlersEnvelope(ctx, []prioritizedHandler{h}, envelope, tg.Entities{}, msg)
			if got != tc.want {
				t.Fatalf("handled=%v want=%v", got, tc.want)
			}
			if len(client.specs) != 0 {
				t.Fatalf("expired decision submitted %d tasks", len(client.specs))
			}
		})
	}
}

func TestPhase7BudgetExpirationBetweenHandlersPreservesRemainingSecurity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		secondPriority HandlerPriority
		disableSecond  bool
		want           bool
	}{
		{name: "security next fails closed", secondPriority: PrioritySecurity, want: true},
		{name: "feature next fails open", secondPriority: PriorityFeature},
		{name: "disabled security next is skipped", secondPriority: PrioritySecurity, disableSecond: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			client := &phase7DecisionClient{run: true}
			d.SetTasks(client)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first, envelope, msg := decisionPolicyFixture(PriorityFeature, func(context.Context, *core.MessageEnvelope) error {
				cancel()
				return nil
			})
			second, _, _ := decisionPolicyFixture(tc.secondPriority, func(context.Context, *core.MessageEnvelope) error {
				t.Fatal("handler submitted after budget expired")
				return nil
			})
			second.id = 2
			if tc.disableSecond {
				second.stateGate = func(int64) bool { return false }
			}
			got := d.executeDecisionHandlersEnvelope(ctx, []prioritizedHandler{first, second}, envelope, tg.Entities{}, msg)
			if got != tc.want {
				t.Fatalf("handled=%v want=%v", got, tc.want)
			}
			if len(client.specs) != 1 {
				t.Fatalf("submissions=%d want=1", len(client.specs))
			}
		})
	}
}

func TestPhase7UnscopedHandlerCannotBypassSecurityAfterDeadline(t *testing.T) {
	for _, tc := range []struct {
		priority HandlerPriority
		want     bool
	}{
		{priority: PrioritySecurity, want: true},
		{priority: PriorityFeature},
	} {
		d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
		ctx, cancel := context.WithCancel(context.Background())
		h, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
			cancel()
			return nil
		})
		h.scope = tasks.ScopeIdentity{}
		got := d.executeDecisionHandlersEnvelope(ctx, []prioritizedHandler{h}, envelope, tg.Entities{}, msg)
		cancel()
		if got != tc.want {
			t.Fatalf("priority=%d handled=%v want=%v", tc.priority, got, tc.want)
		}
	}
}

func TestPhase7DecisionTaskDeadlineTracksSharedIngressBudget(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	client := &phase7DecisionClient{run: true}
	d.SetTasks(client)
	h, envelope, msg := decisionPolicyFixture(PrioritySecurity, func(ctx context.Context, _ *core.MessageEnvelope) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("worker hook missing shared deadline")
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	if got := d.executeDecisionHandlersEnvelope(ctx, []prioritizedHandler{h}, envelope, tg.Entities{}, msg); got {
		t.Fatal("successful security decision intercepted")
	}
	if len(client.specs) != 1 {
		t.Fatalf("submissions=%d want=1", len(client.specs))
	}
	spec := client.specs[0]
	if !spec.QueueDeadline.Equal(parentDeadline) {
		t.Fatalf("queue deadline=%v want parent deadline=%v", spec.QueueDeadline, parentDeadline)
	}
	if spec.ExecutionTimeout <= 0 || spec.ExecutionTimeout > 3*time.Second {
		t.Fatalf("execution timeout=%s not bounded by parent", spec.ExecutionTimeout)
	}
}

func TestPhase7TaskTerminalOutcomeRespectsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		priority HandlerPriority
		outcome  tasks.Outcome
		want     bool
	}{
		{name: "security timeout", priority: PrioritySecurity, outcome: tasks.OutcomeTimedOut, want: true},
		{name: "feature timeout", priority: PriorityFeature, outcome: tasks.OutcomeTimedOut},
		{name: "security failure", priority: PrioritySecurity, outcome: tasks.OutcomeFailed, want: true},
		{name: "feature failure", priority: PriorityFeature, outcome: tasks.OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			d.SetTasks(&phase7DecisionClient{outcome: tc.outcome})
			h, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
				t.Fatal("terminal result fixture must not invoke handler")
				return nil
			})
			got := d.executeDecisionHandlersEnvelope(context.Background(), []prioritizedHandler{h}, envelope, tg.Entities{}, msg)
			if got != tc.want {
				t.Fatalf("handled=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPhase7DecisionWaitFailureAvoidsBlockingControlPlaneCancellation(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	client := &phase7DecisionClient{waitErr: context.DeadlineExceeded}
	d.SetTasks(client)
	h, envelope, msg := decisionPolicyFixture(PrioritySecurity, func(context.Context, *core.MessageEnvelope) error {
		t.Fatal("wait failure fixture must not invoke handler")
		return nil
	})
	if got := d.executeDecisionHandlersEnvelope(context.Background(), []prioritizedHandler{h}, envelope, tg.Entities{}, msg); !got {
		t.Fatal("security task wait failure must fail closed")
	}
	if len(client.specs) != 1 {
		t.Fatalf("submissions=%d want=1", len(client.specs))
	}
	if client.cancelled != "" {
		t.Fatalf("decision ingress synchronously called control-plane Cancel on %s", client.cancelled)
	}
}
