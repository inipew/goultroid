package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

type deleteTestPort struct {
	*testPort
	deleted bool
	err     error
}

func (p *deleteTestPort) Delete(context.Context, presentation.Target) error {
	if p.err != nil {
		return p.err
	}
	p.deleted = true
	return nil
}

func TestContextDeleteUsesOptionalPresentationCapability(t *testing.T) {
	scope := tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}
	sessions, err := interaction.NewRuntime(testCatalog{scope: scope}, interaction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer sessions.Close()
	port := &deleteTestPort{testPort: &testPort{messageID: 77}}
	engine, err := New(sessions, interaction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := ctx.Delete(); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !port.deleted {
		t.Fatal("Delete() did not reach presentation capability")
	}
}

func TestContextDeleteFailsClosedWhenPortDoesNotSupportIt(t *testing.T) {
	engine, _, _, _ := testEngine(t)
	ctx, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := ctx.Delete(); !errors.Is(err, ErrDeleteUnsupported) {
		t.Fatalf("Delete() error = %v, want %v", err, ErrDeleteUnsupported)
	}
}
