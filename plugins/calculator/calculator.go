package calculator

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

const (
	interactionInlineID = "calculator"
	interactionTTL      = 10 * time.Minute
)

var calculatorActions = []string{
	"key_0", "key_00", "key_1", "key_2", "key_3", "key_4", "key_5", "key_6", "key_7", "key_8", "key_9",
	"dot", "op_add", "op_sub", "op_mul", "op_div", "op_mod", "op_pow", "paren_l", "paren_r",
	"clear", "back", "equals",
}

// Plugin is a callback-heavy calculator canary. All mutable calculator state is
// held in the shared bounded interaction session runtime, never in the plugin.
type Plugin struct {
	renderer selfinline.Renderer
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "calculator" }

func (p *Plugin) Description() string {
	return "Bounded interactive calculator using Inline vNext and typed a2 actions"
}

func (p *Plugin) Init() error { return nil }

// SetSelfInlineRenderer is application composition, not feature-owned transport
// construction. The renderer remains capability-gated by the App wrapper.
func (p *Plugin) SetSelfInlineRenderer(renderer selfinline.Renderer) {
	if p != nil {
		p.renderer = renderer
	}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{{
		Name:        "calc",
		Aliases:     []string{"calculator"},
		Description: "Open the interactive calculator",
		Usage:       ".calc [expression]",
		Category:    "Utility",
		Permission:  core.PermissionOwner,
		Surfaces:    execution.SurfaceUserbot,
		Handler:     p.handleCommand,
	}}
}

func (p *Plugin) FeatureSpec() feature.Spec {
	inlinePolicy := feature.OwnerPolicy(execution.SurfaceInline)
	interactions := []feature.Interaction{{
		ID:          interactionInlineID,
		Kind:        feature.InteractionInline,
		Description: "Interactive calculator inline surface",
		Surfaces:    execution.SurfaceInline,
		Policy:      inlinePolicy,
	}}
	for _, actionID := range calculatorActions {
		interactions = append(interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Calculator typed action",
			Surfaces:    execution.SurfaceInline,
			Policy:      inlinePolicy,
		})
	}
	return feature.Spec{
		ID:           p.Name(),
		Name:         "Calculator",
		Description:  p.Description(),
		Category:     "Utility",
		Interactions: interactions,
	}
}

func (p *Plugin) InlineBindings() []inlineservice.Binding {
	return []inlineservice.Binding{{
		InteractionID: interactionInlineID,
		Handler:       &inlineHandler{},
		Priority:      20,
	}}
}

func (p *Plugin) AssistantFeatureID() string { return p.Name() }

// BindAssistant attaches only typed action handlers. Initial state/session
// creation remains owned by Inline vNext when it compiles ActionRows.
func (p *Plugin) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, orchestration.ErrInvalidEngine
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope.IsZero() {
		return nil, fmt.Errorf("calculator: feature scope unavailable")
	}
	registrations := make([]interface{ Close() }, 0, len(calculatorActions))
	for _, id := range calculatorActions {
		actionID := id
		registration, err := rt.Engine.RegisterAction(scope, p.Name(), actionID, func(ctx *orchestration.Context) error {
			if ctx == nil {
				return orchestration.ErrInvalidEngine
			}
			session := ctx.Session()
			if err := rt.Admit(p.Name(), feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return p.handleAction(ctx, actionID)
		})
		if err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("calculator: register action %s: %w", actionID, err)
		}
		registrations = append(registrations, registration)
	}
	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
	}, nil
}

func (*Plugin) HandleAssistantInput(*orchestration.Context, string) error { return nil }

func (p *Plugin) handleCommand(ctx *core.Context) error {
	if ctx == nil || ctx.PeerID == nil {
		return core.ErrInvalidArgs
	}
	if p == nil || p.renderer == nil {
		return ctx.EditOrReply("⚠️ Interactive calculator is unavailable because the Assistant inline renderer is not running.")
	}
	expression := compactExpression(ctx.RawArgs)
	if len(expression) > maxExpressionBytes {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Expression is limited to %d bytes.", maxExpressionBytes))
	}
	query := "calc"
	if expression != "" {
		query += " " + expression
	}
	request := selfinline.Request{Peer: ctx.PeerID, Query: query, ResultID: "calculator"}
	if ctx.Message != nil {
		request.ReplyToID = ctx.Message.ReplyToID
		request.TopicID = ctx.Message.TopicID
	}
	if _, err := p.renderer.Render(ctx.Ctx, request); err != nil {
		return ctx.EditOrReply("⚠️ Unable to open the interactive calculator: " + core.EscapeHTML(err.Error()))
	}
	if ctx.Message != nil && ctx.Message.ID > 0 && ctx.Svc != nil {
		_ = ctx.Svc.DeleteMessage(ctx.Ctx, ctx.PeerID, []int{ctx.Message.ID})
	}
	return nil
}

func (p *Plugin) handleAction(ctx *orchestration.Context, actionID string) error {
	expression := string(ctx.State())
	if len(expression) > maxExpressionBytes || !utf8.ValidString(expression) {
		ctx.Cancel()
		return ctx.Answer("Calculator state is invalid. Reopen it.", true)
	}
	next, notice, err := applyAction(expression, actionID)
	if err != nil {
		return ctx.Answer(err.Error(), true)
	}
	if notice != "" {
		if err := ctx.Answer(notice, false); err != nil {
			return err
		}
	}
	return ctx.Transition([]byte(next), interactionTTL, calculatorView(next))
}

type inlineHandler struct{}

func (*inlineHandler) Pattern() string     { return "calc" }
func (*inlineHandler) Description() string { return "Interactive calculator" }
func (*inlineHandler) Matcher() inlineservice.InlineMatcher {
	return inlineservice.NewPrefixMatcher("calc")
}
func (*inlineHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{OwnerOnly: true}
}
func (*inlineHandler) CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheNone }

func (h *inlineHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (*inlineHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	if ctx == nil {
		return nil, core.ErrInvalidArgs
	}
	expression := compactExpression(strings.Join(ctx.Args, ""))
	if len(expression) > maxExpressionBytes {
		return nil, fmt.Errorf("%w: expression exceeds %d bytes", core.ErrInvalidArgs, maxExpressionBytes)
	}
	view := calculatorView(expression)
	return &inlineservice.InlineResponse{
		Results: []inlineservice.InlineResult{{
			ID:               "calculator",
			Type:             inlineservice.ResultArticle,
			Title:            "Calculator",
			Description:      calculatorDescription(expression),
			Text:             view.Text,
			ActionRows:       view.Rows,
			InteractionState: []byte(expression),
			InteractionTTL:   interactionTTL,
		}},
		Cache:     inlineservice.CacheNone,
		CacheTime: 0,
		Private:   true,
	}, nil
}

func compactExpression(input string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(input)), "")
}

func calculatorDescription(expression string) string {
	if expression == "" {
		return "Tap the buttons to start calculating"
	}
	value, err := evaluateExpression(expression)
	if err != nil {
		return "Editing: " + expression
	}
	return expression + " = " + formatResult(value)
}

func calculatorView(expression string) presentation.View {
	display := expression
	if display == "" {
		display = "0"
	}
	text := "• <b>GoUltroid Inline Calculator</b> •\n\n<code>" + core.EscapeHTML(display) + "</code>"
	if expression != "" {
		if value, err := evaluateExpression(expression); err == nil {
			text += "\n\n<b>Answer :</b> " + core.EscapeHTML(formatResult(value))
		}
	}
	return presentation.View{Text: text, Rows: calculatorRows()}
}

func calculatorRows() []presentation.Row {
	return []presentation.Row{
		{{Text: "AC", ActionID: "clear"}, {Text: "C", ActionID: "clear"}, {Text: "⌫", ActionID: "back"}, {Text: "%", ActionID: "op_mod"}},
		{{Text: "7", ActionID: "key_7"}, {Text: "8", ActionID: "key_8"}, {Text: "9", ActionID: "key_9"}, {Text: "+", ActionID: "op_add"}},
		{{Text: "4", ActionID: "key_4"}, {Text: "5", ActionID: "key_5"}, {Text: "6", ActionID: "key_6"}, {Text: "−", ActionID: "op_sub"}},
		{{Text: "1", ActionID: "key_1"}, {Text: "2", ActionID: "key_2"}, {Text: "3", ActionID: "key_3"}, {Text: "×", ActionID: "op_mul"}},
		{{Text: "00", ActionID: "key_00"}, {Text: "0", ActionID: "key_0"}, {Text: ".", ActionID: "dot"}, {Text: "÷", ActionID: "op_div"}},
		{{Text: "=", ActionID: "equals"}},
	}
}

func applyAction(expression, actionID string) (next string, notice string, err error) {
	if len(expression) > maxExpressionBytes || !utf8.ValidString(expression) {
		return expression, "", fmt.Errorf("calculator state is invalid")
	}
	switch actionID {
	case "clear":
		return "", "", nil
	case "back":
		if expression == "" {
			return "", "", nil
		}
		_, size := utf8.DecodeLastRuneInString(expression)
		return expression[:len(expression)-size], "", nil
	case "equals":
		if expression == "" {
			return "", "", nil
		}
		value, evalErr := evaluateExpression(expression)
		if evalErr != nil {
			return expression, "", fmt.Errorf("invalid expression: %w", evalErr)
		}
		return formatResult(value), "Result updated", nil
	}
	token, ok := actionToken(actionID)
	if !ok {
		return expression, "", fmt.Errorf("unknown calculator action")
	}
	if len(expression)+len(token) > maxExpressionBytes {
		return expression, "", fmt.Errorf("expression is limited to %d bytes", maxExpressionBytes)
	}
	return expression + token, "", nil
}

func actionToken(actionID string) (string, bool) {
	switch actionID {
	case "key_00":
		return "00", true
	case "key_0", "key_1", "key_2", "key_3", "key_4", "key_5", "key_6", "key_7", "key_8", "key_9":
		return strings.TrimPrefix(actionID, "key_"), true
	case "dot":
		return ".", true
	case "op_add":
		return "+", true
	case "op_sub":
		return "-", true
	case "op_mul":
		return "*", true
	case "op_div":
		return "/", true
	case "op_mod":
		return "%", true
	case "op_pow":
		return "^", true
	case "paren_l":
		return "(", true
	case "paren_r":
		return ")", true
	}
	return "", false
}

var _ assistantinteraction.FeatureDriver = (*Plugin)(nil)
var _ inlineservice.InlineHandlerV2 = (*inlineHandler)(nil)
