package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type p6gRejectingTaskClient struct {
	err       error
	submitted int
	cancelled int
}

func (c *p6gRejectingTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	c.submitted++
	return nil, c.err
}

func (c *p6gRejectingTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	c.cancelled++
	return tasks.CancelReceipt{}, nil
}

func (c *p6gRejectingTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (c *p6gRejectingTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

// TestP6G_A2AdmissionRejectionFailsClosed freezes the final cutover invariant:
// a valid a2 callback may be prepared before admission, but feature code must
// never execute when TaskEngine rejects the work. The callback must still reach
// the acknowledgement boundary so Telegram is not left with a spinner.
func TestP6G_A2AdmissionRejectionFailsClosed(t *testing.T) {
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:p6g", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: "p6g", Scope: scope}, feature.Spec{
		ID:   "p6g",
		Name: "P6-G acceptance",
		Interactions: []feature.Interaction{{
			ID:       "next",
			Kind:     feature.InteractionAction,
			Surfaces: execution.SurfaceAssistant,
			Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
		}},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer sessions.Close()

	actions := rootinteraction.NewDispatcher(sessions)
	port := &syntheticPort{}
	engine, err := orchestration.New(sessions, actions, port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}

	called := 0
	handlerRegistration, err := engine.RegisterAction(scope, "p6g", "next", func(*orchestration.Context) error {
		called++
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterAction() error = %v", err)
	}
	defer handlerRegistration.Close()

	_, err = engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: "p6g",
		ActorID:   7,
		Target:    presentationtelegram.MessageTarget{ChatID: 42},
		View: presentation.View{
			Text: "probe",
			Rows: []presentation.Row{{{Text: "Next", ActionID: "next"}}},
		},
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	data := port.sent.Rows[0][0].Data
	if !isInteractionCallback(data) {
		t.Fatalf("compiled data %q is not a2", data)
	}

	admissionErr := errors.New("interactive admission saturated")
	taskClient := &p6gRejectingTaskClient{err: admissionErr}
	ack := &syntheticAck{}
	ingress := &interactionIngress{engine: engine, ack: ack, tasks: taskClient}

	handled, err := ingress.tryMessage(context.Background(), data, 7, 991, nil, 42, 77)
	if !handled {
		t.Fatal("valid a2 callback was not claimed")
	}
	if err == nil || !strings.Contains(err.Error(), admissionErr.Error()) {
		t.Fatalf("tryMessage() error = %v, want admission failure", err)
	}
	if taskClient.submitted != 1 {
		t.Fatalf("TaskEngine submissions = %d, want 1", taskClient.submitted)
	}
	if called != 0 {
		t.Fatalf("feature handler executed %d times after admission rejection, want 0", called)
	}
	if ack.calls != 1 || ack.err == nil || !strings.Contains(ack.err.Error(), admissionErr.Error()) {
		t.Fatalf("ack = calls:%d err:%v, want one terminal admission failure", ack.calls, ack.err)
	}
}
