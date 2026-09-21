package client

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type syntheticPort struct {
	sent presentation.CompiledView
}

func (p *syntheticPort) Send(_ context.Context, _ presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	p.sent = view
	return presentationtelegram.MessageTarget{ChatID: 42, MessageID: 77}, nil
}
func (*syntheticPort) Edit(context.Context, presentation.Target, presentation.CompiledView) error {
	return nil
}
func (*syntheticPort) Answer(context.Context, presentation.Answer) error { return nil }

type syntheticAck struct {
	calls int
	err   error
}

func (a *syntheticAck) ensureAnswered(_ context.Context, _ int64, err error) {
	a.calls++
	a.err = err
}

func TestA2IngressSyntheticSurfaceEndToEnd(t *testing.T) {
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:synthetic", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: "synthetic", Scope: scope}, feature.Spec{
		ID:   "synthetic",
		Name: "Synthetic",
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
	called := false
	handlerRegistration, err := engine.RegisterAction(scope, "synthetic", "next", func(*orchestration.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterAction() error = %v", err)
	}
	defer handlerRegistration.Close()

	_, err = engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: "synthetic",
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
	if !isV2Callback(data) {
		t.Fatalf("compiled data %q is not a2", data)
	}

	ack := &syntheticAck{}
	ingress := &v2Ingress{engine: engine, ack: ack}
	handled, err := ingress.tryMessage(context.Background(), data, 7, 99, nil, 42, 77)
	if err != nil {
		t.Fatalf("tryMessage() error = %v", err)
	}
	if !handled || !called {
		t.Fatalf("handled=%v called=%v, want both true", handled, called)
	}
	if ack.calls != 1 || ack.err != nil {
		t.Fatalf("ack = calls:%d err:%v", ack.calls, ack.err)
	}

	handled, err = ingress.tryMessage(context.Background(), []byte("v1:menu:home:noop"), 7, 100, nil, 42, 77)
	if err != nil {
		t.Fatalf("legacy tryMessage() error = %v", err)
	}
	if handled {
		t.Fatal("legacy payload was claimed by a2 ingress")
	}
}
