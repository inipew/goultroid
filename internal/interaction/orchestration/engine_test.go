package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

type testCatalog struct{ scope tasks.ScopeIdentity }

func (c testCatalog) FeatureScope(featureID string) (tasks.ScopeIdentity, bool) {
	return c.scope, featureID == "demo"
}
func (testCatalog) HasAction(featureID, actionID string) bool {
	return featureID == "demo" && actionID == "next"
}

type testTarget struct {
	chatID    int64
	messageID int
}

func (testTarget) PresentationTargetKind() string { return "test" }
func (t testTarget) SessionBinding(actorID int64) interaction.Binding {
	return interaction.Binding{ActorID: actorID, ChatID: t.chatID, MessageID: t.messageID}
}
func (t testTarget) TargetBinding() (interaction.TargetBinding, bool) {
	if t.chatID == 0 || t.messageID <= 0 {
		return interaction.TargetBinding{}, false
	}
	return interaction.TargetBinding{ChatID: t.chatID, MessageID: t.messageID}, true
}

type testPort struct {
	messageID int
	sent      presentation.CompiledView
	edited    presentation.CompiledView
	answered  presentation.Answer
	sendErr   error
	editErr   error
}

func (p *testPort) Send(_ context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	if p.sendErr != nil {
		return nil, p.sendErr
	}
	p.sent = view
	t := target.(testTarget)
	t.messageID = p.messageID
	return t, nil
}
func (p *testPort) Edit(_ context.Context, _ presentation.Target, view presentation.CompiledView) error {
	if p.editErr != nil {
		return p.editErr
	}
	p.edited = view
	return nil
}
func (p *testPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.answered = answer
	return nil
}

func testEngine(t *testing.T) (*Engine, *interaction.Runtime, *testPort, tasks.ScopeIdentity) {
	t.Helper()
	scope := tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}
	sessions, err := interaction.NewRuntime(testCatalog{scope: scope}, interaction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	port := &testPort{messageID: 77}
	engine, err := New(sessions, interaction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return engine, sessions, port, scope
}

func testView(text string) presentation.View {
	return presentation.View{Text: text, Rows: []presentation.Row{{{Text: "Next", ActionID: "next"}}}}
}

func TestBeginOwnsCreateCompileSendAndBind(t *testing.T) {
	engine, sessions, port, _ := testEngine(t)
	ctx, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, State: []byte("one"),
		Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if got := ctx.Session().Binding.MessageID; got != 77 {
		t.Fatalf("bound message id = %d, want 77", got)
	}
	if token, err := interaction.ParseCallbackToken(port.sent.Rows[0][0].Data); err != nil || token.Revision != 1 {
		t.Fatalf("initial token = %+v, err = %v", token, err)
	}
	if got := sessions.Stats().Sessions; got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestBeginCancelsSessionWhenSendFails(t *testing.T) {
	engine, sessions, port, _ := testEngine(t)
	port.sendErr = errors.New("send failed")
	_, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err == nil {
		t.Fatal("Begin() succeeded")
	}
	if got := sessions.Stats().Sessions; got != 0 {
		t.Fatalf("sessions = %d, want 0", got)
	}
}

func TestDispatchBuildsContextAndTransitionStalesOldButton(t *testing.T) {
	engine, _, port, scope := testEngine(t)
	registration, err := engine.RegisterAction(scope, "demo", "next", func(ctx *Context) error {
		if string(ctx.State()) != "one" {
			t.Fatalf("state = %q", ctx.State())
		}
		if err := ctx.Answer("ok", false); err != nil {
			return err
		}
		return ctx.Transition([]byte("two"), 0, testView("second"))
	})
	if err != nil {
		t.Fatalf("RegisterAction() error = %v", err)
	}
	defer registration.Close()
	initial, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, State: []byte("one"),
		Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	oldData := append([]byte(nil), port.sent.Rows[0][0].Data...)
	if err := engine.Dispatch(context.Background(), CallbackRequest{
		Data: oldData, ActorID: 7, QueryID: 99, Target: initial.Target(),
	}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if port.answered.QueryID != 99 || port.answered.Text != "ok" {
		t.Fatalf("answer = %+v", port.answered)
	}
	if token, err := interaction.ParseCallbackToken(port.edited.Rows[0][0].Data); err != nil || token.Revision != 2 {
		t.Fatalf("transition token = %+v, err = %v", token, err)
	}
	if err := engine.Dispatch(context.Background(), CallbackRequest{
		Data: oldData, ActorID: 7, QueryID: 100, Target: initial.Target(),
	}); !errors.Is(err, interaction.ErrStaleToken) {
		t.Fatalf("old token error = %v, want %v", err, interaction.ErrStaleToken)
	}
}

func TestDispatchRejectsDifferentTargetBeforeHandler(t *testing.T) {
	engine, _, port, scope := testEngine(t)
	called := false
	registration, err := engine.RegisterAction(scope, "demo", "next", func(*Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterAction() error = %v", err)
	}
	defer registration.Close()
	_, err = engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, Target: testTarget{chatID: 42}, View: testView("first"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := engine.Dispatch(context.Background(), CallbackRequest{
		Data: port.sent.Rows[0][0].Data, ActorID: 7, QueryID: 99,
		Target: testTarget{chatID: 42, messageID: 88},
	}); !errors.Is(err, interaction.ErrBindingMismatch) {
		t.Fatalf("Dispatch() error = %v, want %v", err, interaction.ErrBindingMismatch)
	}
	if called {
		t.Fatal("handler called for mismatched target")
	}
}

func TestAwaitAndTakeInputOwnRevisionAndTargetLifecycle(t *testing.T) {
	engine, sessions, port, _ := testEngine(t)
	ctx, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, State: []byte("detail"),
		Target: testTarget{chatID: 42}, View: testView("detail"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := ctx.AwaitInput([]byte("input"), time.Minute, testView("input")); err != nil {
		t.Fatalf("AwaitInput() error = %v", err)
	}
	if stats := sessions.Stats(); stats.Inputs != 1 {
		t.Fatalf("pending inputs = %d, want 1", stats.Inputs)
	}
	token, err := interaction.ParseCallbackToken(port.edited.Rows[0][0].Data)
	if err != nil || token.Revision != 2 {
		t.Fatalf("input token = %+v err=%v", token, err)
	}

	inputCtx, handled, err := engine.TakeInput(context.Background(), 7, 42)
	if err != nil {
		t.Fatalf("TakeInput() error = %v", err)
	}
	if !handled || string(inputCtx.State()) != "input" || inputCtx.Session().Revision != 3 {
		t.Fatalf("input context handled=%v session=%+v", handled, inputCtx.Session())
	}
	if err := inputCtx.SetTarget(testTarget{chatID: 42, messageID: 77}); err != nil {
		t.Fatalf("SetTarget() error = %v", err)
	}
	if inputCtx.Target() == nil {
		t.Fatal("input context target not attached")
	}
	if _, handled, err := engine.TakeInput(context.Background(), 7, 42); err != nil || handled {
		t.Fatalf("second TakeInput() handled=%v err=%v", handled, err)
	}
}

func TestAwaitInputReleasesClaimWhenPromptEditFails(t *testing.T) {
	engine, sessions, port, _ := testEngine(t)
	ctx, err := engine.Begin(context.Background(), BeginRequest{
		FeatureID: "demo", ActorID: 7, State: []byte("detail"),
		Target: testTarget{chatID: 42}, View: testView("detail"),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	port.editErr = errors.New("edit failed")
	if err := ctx.AwaitInput([]byte("input"), time.Minute, testView("input")); err == nil {
		t.Fatal("AwaitInput() succeeded")
	}
	if stats := sessions.Stats(); stats.Inputs != 0 {
		t.Fatalf("unseen prompt retained input claim: %+v", stats)
	}
}
