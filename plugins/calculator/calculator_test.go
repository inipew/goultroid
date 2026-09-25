package calculator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

type fakeRenderer struct {
	calls   int
	request selfinline.Request
	err     error
}

func (f *fakeRenderer) Render(_ context.Context, request selfinline.Request) (selfinline.Result, error) {
	f.calls++
	f.request = request
	return selfinline.Result{QueryID: 1, ResultID: "calculator", RandomID: 2}, f.err
}

type calculatorCommandService struct {
	core.MockTelegramServicer
	edited      string
	sent        string
	deleteCalls int
}

func (s *calculatorCommandService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	s.edited = text
	return nil
}

func (s *calculatorCommandService) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.sent = text
	return &tg.Message{ID: 100, Message: text}, nil
}

func (s *calculatorCommandService) DeleteMessage(_ context.Context, _ tg.InputPeerClass, _ []int) error {
	s.deleteCalls++
	return nil
}

func TestCalculatorFeatureDeclaresTypedInlineSurface(t *testing.T) {
	p := New()
	spec := p.FeatureSpec()
	if spec.ID != "calculator" {
		t.Fatalf("feature ID = %q", spec.ID)
	}
	seenInline := false
	actions := 0
	for _, interaction := range spec.Interactions {
		switch interaction.Kind {
		case feature.InteractionInline:
			seenInline = interaction.ID == interactionInlineID
		case feature.InteractionAction:
			actions++
		}
	}
	if !seenInline || actions != len(calculatorActions) {
		t.Fatalf("inline/actions = %v/%d, want true/%d", seenInline, actions, len(calculatorActions))
	}
	bindings := p.InlineBindings()
	if len(bindings) != 1 || bindings[0].InteractionID != interactionInlineID {
		t.Fatalf("inline bindings = %+v", bindings)
	}
}

func TestCalculatorInlineProducesBoundedPrivateTypedState(t *testing.T) {
	h := &inlineHandler{}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{Args: []string{"1", "+", "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Cache != inlineservice.CacheNone || !response.Private || len(response.Results) != 1 {
		t.Fatalf("response policy = cache:%v private:%v results:%d", response.Cache, response.Private, len(response.Results))
	}
	result := response.Results[0]
	if result.ID != "calculator" || string(result.InteractionState) != "1+2" {
		t.Fatalf("result ID/state = %q/%q", result.ID, result.InteractionState)
	}
	if len(result.InteractionState) > maxExpressionBytes || len(result.ActionRows) == 0 {
		t.Fatalf("state bytes/rows = %d/%d", len(result.InteractionState), len(result.ActionRows))
	}
	for _, row := range result.ActionRows {
		for _, button := range row {
			if _, ok := actionToken(button.ActionID); !ok && button.ActionID != "clear" && button.ActionID != "back" && button.ActionID != "equals" {
				t.Fatalf("unexpected raw/untyped action %q", button.ActionID)
			}
		}
	}
}

func TestCalculatorCommandUsesSelfInlineRenderer(t *testing.T) {
	renderer := &fakeRenderer{}
	p := New()
	p.SetSelfInlineRenderer(renderer)
	svc := &calculatorCommandService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		RawArgs: " (1 + 2) * 3 ",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{
			ID:        9,
			ReplyToID: 5,
			TopicID:   4,
		},
		Svc: svc,
	}
	if err := p.handleCommand(ctx); err != nil {
		t.Fatal(err)
	}
	if renderer.calls != 1 || renderer.request.Query != "calc (1+2)*3" || renderer.request.ResultID != "calculator" {
		t.Fatalf("renderer calls/request = %d/%+v", renderer.calls, renderer.request)
	}
	if renderer.request.ReplyToID != 5 || renderer.request.TopicID != 4 {
		t.Fatalf("reply/topic = %d/%d", renderer.request.ReplyToID, renderer.request.TopicID)
	}
	if svc.edited != "" || svc.sent != "" {
		t.Fatalf("healthy self-inline path emitted native output: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if svc.deleteCalls != 1 {
		t.Fatalf("successful self-inline command delete calls=%d, want 1", svc.deleteCalls)
	}
}

func TestCalculatorCommandWithoutExpressionPrefersSelfInlineKeypad(t *testing.T) {
	renderer := &fakeRenderer{}
	p := New()
	p.SetSelfInlineRenderer(renderer)
	svc := &calculatorCommandService{}
	ctx := calculatorCommandContext(svc, "")

	if err := p.handleCommand(ctx); err != nil {
		t.Fatalf("handleCommand() error=%v", err)
	}
	if renderer.calls != 1 || renderer.request.Query != "calc" || renderer.request.ResultID != "calculator" {
		t.Fatalf("renderer calls/request=%d/%+v", renderer.calls, renderer.request)
	}
	if svc.edited != "" || svc.sent != "" {
		t.Fatalf("healthy keypad path emitted native usage: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if svc.deleteCalls != 1 {
		t.Fatalf("successful keypad command delete calls=%d, want 1", svc.deleteCalls)
	}
}

func TestCalculatorActionMutationIsBoundedAndSafe(t *testing.T) {
	expression := ""
	for _, action := range []string{"key_1", "op_add", "key_2", "op_mul", "key_3"} {
		var err error
		expression, _, err = applyAction(expression, action)
		if err != nil {
			t.Fatal(err)
		}
	}
	if expression != "1+2*3" {
		t.Fatalf("expression = %q", expression)
	}
	result, _, err := applyAction(expression, "equals")
	if err != nil || result != "7" {
		t.Fatalf("equals = %q, %v", result, err)
	}
	if _, _, err := applyAction(strings.Repeat("1", maxExpressionBytes), "key_1"); err == nil {
		t.Fatal("expression growth beyond bound succeeded")
	}
	if _, _, err := applyAction("1", "raw_callback"); err == nil {
		t.Fatal("unknown action unexpectedly succeeded")
	}
}

func TestCalculatorCommandFallsBackToNativeWithoutRenderer(t *testing.T) {
	tests := []struct {
		name    string
		rawArgs string
		want    []string
	}{
		{
			name:    "expression",
			rawArgs: " (1 + 2) * 3 ",
			want:    []string{"Calculator", "<code>(1+2)*3</code>", "<b>9</b>"},
		},
		{
			name: "usage",
			want: []string{"Usage:", ".calc &lt;expression&gt;"},
		},
		{
			name:    "invalid",
			rawArgs: "1+",
			want:    []string{"Error:", "Invalid expression:"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			svc := &calculatorCommandService{}
			ctx := calculatorCommandContext(svc, tc.rawArgs)
			if err := p.handleCommand(ctx); err != nil {
				t.Fatalf("handleCommand() error=%v", err)
			}
			if svc.sent != "" {
				t.Fatalf("native userbot fallback unexpectedly sent a second message: %q", svc.sent)
			}
			for _, want := range tc.want {
				if !strings.Contains(svc.edited, want) {
					t.Fatalf("native output=%q, want %q", svc.edited, want)
				}
			}
			if svc.deleteCalls != 0 {
				t.Fatalf("native fallback deleted command, calls=%d", svc.deleteCalls)
			}
		})
	}
}

func TestCalculatorCommandFallsBackAfterSafeSelfInlineFailure(t *testing.T) {
	tests := []struct {
		name  string
		stage selfinline.RenderStage
		err   error
	}{
		{name: "preflight", stage: selfinline.RenderStagePreflight, err: selfinline.ErrInlineDisabled},
		{name: "query", stage: selfinline.RenderStageQuery, err: selfinline.ErrQueryFailed},
		{name: "select", stage: selfinline.RenderStageSelect, err: selfinline.ErrNoResults},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			renderer := &fakeRenderer{err: &selfinline.RenderFailure{Stage: tc.stage, Err: tc.err}}
			p := New()
			p.SetSelfInlineRenderer(renderer)
			svc := &calculatorCommandService{}
			ctx := calculatorCommandContext(svc, "1 + 2 * 3")

			if err := p.handleCommand(ctx); err != nil {
				t.Fatalf("handleCommand() error=%v", err)
			}
			if renderer.calls != 1 {
				t.Fatalf("renderer calls=%d, want 1", renderer.calls)
			}
			if !strings.Contains(svc.edited, "<code>1+2*3</code>") || !strings.Contains(svc.edited, "<b>7</b>") {
				t.Fatalf("safe self-inline failure did not produce native result: %q", svc.edited)
			}
			if svc.deleteCalls != 0 {
				t.Fatalf("safe fallback deleted command, calls=%d", svc.deleteCalls)
			}
		})
	}
}

func TestCalculatorCommandSendStageFailureDoesNotEmitNativeDuplicate(t *testing.T) {
	renderer := &fakeRenderer{err: &selfinline.RenderFailure{
		Stage:            selfinline.RenderStageSend,
		MayHaveCommitted: true,
		Err:              selfinline.ErrSendFailed,
	}}
	p := New()
	p.SetSelfInlineRenderer(renderer)
	svc := &calculatorCommandService{}
	ctx := calculatorCommandContext(svc, "1 + 2 * 3")

	if err := p.handleCommand(ctx); err != nil {
		t.Fatalf("handleCommand() error=%v", err)
	}
	if strings.Contains(svc.edited, "<b>7</b>") || strings.Contains(svc.sent, "<b>7</b>") {
		t.Fatalf("send-stage failure emitted native duplicate: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if !strings.Contains(svc.edited, "could not be confirmed") && !strings.Contains(svc.sent, "could not be confirmed") {
		t.Fatalf("send-stage failure missing delivery diagnostic: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("ambiguous send deleted command, calls=%d", svc.deleteCalls)
	}
}

func TestCalculatorCommandOversizeRemainsBoundedBeforeRenderer(t *testing.T) {
	renderer := &fakeRenderer{}
	p := New()
	p.SetSelfInlineRenderer(renderer)
	svc := &calculatorCommandService{}
	ctx := calculatorCommandContext(svc, strings.Repeat("1", maxExpressionBytes+1))

	if err := p.handleCommand(ctx); err != nil {
		t.Fatalf("handleCommand() error=%v", err)
	}
	if renderer.calls != 0 {
		t.Fatalf("oversized expression reached self-inline renderer, calls=%d", renderer.calls)
	}
	if !strings.Contains(svc.edited, "limited to 128 bytes") {
		t.Fatalf("oversize diagnostic=%q", svc.edited)
	}
}

func calculatorCommandContext(svc *calculatorCommandService, rawArgs string) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		RawArgs: rawArgs,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 77, IsOutgoing: true},
		Svc:     svc,
	}
}

type calculatorPort struct {
	edits   int
	last    presentation.CompiledView
	answers []presentation.Answer
}

func (p *calculatorPort) Send(_ context.Context, target presentation.Target, _ presentation.CompiledView) (presentation.Target, error) {
	return target, nil
}

func (p *calculatorPort) Edit(_ context.Context, _ presentation.Target, view presentation.CompiledView) error {
	p.edits++
	p.last = view
	return nil
}

func (p *calculatorPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.answers = append(p.answers, answer)
	return nil
}

func TestCalculatorTypedCallbackUsesSharedSessionRevisionAndActorBinding(t *testing.T) {
	p := New()
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:calculator", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
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
	port := &calculatorPort{}
	engine, err := orchestration.New(sessions, actions, port)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindAssistant(assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: catalog,
		Admit: func(_ string, _ feature.InteractionKind, _ string, actorID int64, target presentation.Target) error {
			if actorID != 42 || target.PresentationTargetKind() != "inline" {
				return core.ErrPermissionDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: p.Name(),
		Binding:   rootinteraction.Binding{ActorID: 42},
		State:     []byte("1+2"),
		TTL:       interactionTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, "equals")
	if err != nil {
		t.Fatal(err)
	}
	target := presentationtelegram.InlineTarget{BindingID: "inline:calculator:test"}
	request := orchestration.CallbackRequest{Data: data, ActorID: 42, QueryID: 99, Target: target}
	if err := engine.Dispatch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if port.edits != 1 || !strings.Contains(port.last.Text, "<code>3</code>") {
		t.Fatalf("edit count/view = %d/%q", port.edits, port.last.Text)
	}
	if len(port.answers) != 1 || port.answers[0].QueryID != 99 {
		t.Fatalf("callback answers = %+v", port.answers)
	}
	if _, err := sessions.ResolveCallback(context.Background(), data, rootinteraction.Binding{ActorID: 42, InlineMessageID: "inline:calculator:test"}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old callback error = %v, want stale token", err)
	}

	other, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: p.Name(), Binding: rootinteraction.Binding{ActorID: 42}, TTL: interactionTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherData, err := sessions.CallbackData(context.Background(), other.Session.ID, "key_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: otherData, ActorID: 99, QueryID: 100, Target: presentationtelegram.InlineTarget{BindingID: "inline:wrong-actor"},
	}); !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("wrong actor callback error = %v, want binding mismatch", err)
	}
}
