package native

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	defaultCallbackTimeout = 15 * time.Second
	callbackAnswerTimeout  = 5 * time.Second
	maxPendingAnswers      = 4096
)

var (
	ErrUnavailable       = errors.New("interaction/native: unavailable")
	ErrInvalidInvocation = errors.New("interaction/native: invalid invocation")
)

// ServiceProvider resolves the currently connected userbot Telegram service.
// The provider is intentionally late-bound because application composition is
// completed before the MTProto client establishes its live service.
type ServiceProvider func() core.TelegramServicer

// BeginRequest opens one declared native/userbot screen through the shared a2
// session runtime. ScreenID is metadata used for admission; callback ownership
// remains entirely in interaction.Runtime.
type BeginRequest struct {
	FeatureID string
	ScreenID  string
	State     []byte
	TTL       time.Duration
	View      presentation.View
}

// Adapter binds the canonical interaction runtime to the native userbot
// Telegram transport. It owns no session store, handler registry, worker, or
// ticker; all lifecycle state remains in the shared a2 runtime and TaskEngine.
type Adapter struct {
	catalog feature.Catalog
	engine  *orchestration.Engine
	perms   *core.Permissions
	tasks   tasks.Client
	port    *telegramPort
}

func New(
	catalog feature.Catalog,
	sessions *rootinteraction.Runtime,
	actions *rootinteraction.Dispatcher,
	taskClient tasks.Client,
	service ServiceProvider,
	perms *core.Permissions,
) (*Adapter, error) {
	if catalog == nil || sessions == nil || actions == nil || actions.Runtime() != sessions || taskClient == nil || service == nil {
		return nil, ErrUnavailable
	}
	port := newTelegramPort(service)
	engine, err := orchestration.New(sessions, actions, port)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		catalog: catalog,
		engine:  engine,
		perms:   perms,
		tasks:   taskClient,
		port:    port,
	}, nil
}

// Begin opens a native message interaction without requiring Assistant identity
// or transport state.
func (a *Adapter) Begin(cmd *core.Context, request BeginRequest) (*orchestration.Context, error) {
	if a == nil || a.engine == nil || cmd == nil || cmd.PeerID == nil || cmd.SenderID() == 0 {
		return nil, ErrInvalidInvocation
	}
	chatID := cmd.ChatID()
	if chatID == 0 {
		chatID = cmd.SenderID()
	}
	if chatID == 0 {
		return nil, ErrInvalidInvocation
	}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: chatID}
	decl, ok := a.catalog.FindInteraction(request.FeatureID, feature.InteractionScreen, request.ScreenID)
	if !ok {
		return nil, feature.ErrInteractionUnavailable
	}
	private := isPrivateMessageTarget(target)
	if cmd.Chat != nil && cmd.Chat.Type != "" {
		private = cmd.Chat.Type == "private"
	}
	if err := feature.AdmitInteraction(decl, execution.SourceUserbot, cmd.SenderID(), private, a.perms); err != nil {
		return nil, err
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.engine.Begin(ctx, orchestration.BeginRequest{
		FeatureID: request.FeatureID,
		ActorID:   cmd.SenderID(),
		State:     append([]byte(nil), request.State...),
		TTL:       request.TTL,
		Target:    target,
		View:      request.View,
	})
}

// RegisterAction binds one userbot action to the shared a2 dispatcher. The
// current feature policy is revalidated immediately before feature code runs.
func (a *Adapter) RegisterAction(
	scope tasks.ScopeIdentity,
	featureID, actionID string,
	handler orchestration.Handler,
) (*rootinteraction.HandlerRegistration, error) {
	if a == nil || a.engine == nil || handler == nil {
		return nil, rootinteraction.ErrHandlerUnavailable
	}
	if err := a.requireNativeAction(featureID, actionID); err != nil {
		return nil, err
	}
	return a.engine.RegisterAction(scope, featureID, actionID, func(ctx *orchestration.Context) error {
		if err := a.admitAction(featureID, actionID, ctx); err != nil {
			return err
		}
		return handler(ctx)
	})
}

// RegisterPreparedAction is the resource-aware variant of RegisterAction. The
// preparer stays side-effect-free and the action policy is revalidated again in
// the execution handler immediately before mutation.
func (a *Adapter) RegisterPreparedAction(
	scope tasks.ScopeIdentity,
	featureID, actionID string,
	preparer rootinteraction.ActionPreparer,
	handler orchestration.Handler,
) (*rootinteraction.HandlerRegistration, error) {
	if a == nil || a.engine == nil || preparer == nil || handler == nil {
		return nil, rootinteraction.ErrHandlerUnavailable
	}
	if err := a.requireNativeAction(featureID, actionID); err != nil {
		return nil, err
	}
	return a.engine.RegisterPreparedAction(scope, featureID, actionID, preparer, func(ctx *orchestration.Context) error {
		if err := a.admitAction(featureID, actionID, ctx); err != nil {
			return err
		}
		return handler(ctx)
	})
}

func (a *Adapter) requireNativeAction(featureID, actionID string) error {
	decl, ok := a.catalog.FindInteraction(featureID, feature.InteractionAction, actionID)
	if !ok || !decl.Surfaces.Supports(execution.SourceUserbot) {
		return feature.ErrInteractionUnavailable
	}
	return nil
}

func (a *Adapter) admitAction(featureID, actionID string, ctx *orchestration.Context) error {
	if a == nil || ctx == nil {
		return ErrInvalidInvocation
	}
	decl, ok := a.catalog.FindInteraction(featureID, feature.InteractionAction, actionID)
	if !ok {
		return feature.ErrInteractionUnavailable
	}
	session := ctx.Session()
	target := ctx.Target()
	switch typed := target.(type) {
	case presentationtelegram.MessageTarget:
		return feature.AdmitInteraction(
			decl,
			execution.SourceUserbot,
			session.Binding.ActorID,
			isPrivateMessageTarget(typed),
			a.perms,
		)
	case presentationtelegram.InlineTarget:
		return feature.AdmitInteractionIdentity(decl, execution.SourceInline, session.Binding.ActorID, a.perms)
	default:
		return ErrInvalidInvocation
	}
}

// HandleCallback claims only the a2 protocol namespace. A malformed a2 token is
// still considered handled so it can never fall through into legacy v1 state.
func (a *Adapter) HandleCallback(ctx context.Context, event *core.CallbackQueryEvent) (bool, error) {
	if event == nil || !rootinteraction.OwnsCallbackData(event.Data) {
		return false, nil
	}
	if a == nil || a.engine == nil || a.tasks == nil || a.port == nil {
		return true, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := callbackTarget(event)
	if err != nil {
		a.answerDirect(ctx, event.QueryID, "This interaction is no longer available. Reopen the menu.", false)
		return true, err
	}
	prepared, err := a.engine.PrepareCallback(ctx, orchestration.CallbackRequest{
		Data:    append([]byte(nil), event.Data...),
		ActorID: event.UserID,
		QueryID: event.QueryID,
		Target:  target,
	})
	if err != nil {
		a.answerDirect(ctx, event.QueryID, "This interaction is no longer available. Reopen the menu.", false)
		return true, err
	}
	if !a.port.beginQuery(event.QueryID) {
		a.answerDirect(ctx, event.QueryID, "Interaction service is busy. Please try again.", false)
		return true, ErrUnavailable
	}

	profile := tasks.ExecutionProfile{
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		ExecutionTimeout: defaultCallbackTimeout,
	}
	if aware, ok := prepared.(orchestration.ExecutionProfilePreparedCallback); ok {
		profile = aware.ExecutionProfile().WithDefaults(profile)
	} else if aware, ok := prepared.(orchestration.ResourcePreparedCallback); ok {
		profile.Resources = aware.Resources()
	}
	if aware, ok := prepared.(orchestration.AckPreparedCallback); ok && aware.AckPolicy() == rootinteraction.AckImmediate {
		_ = a.port.Answer(ctx, presentation.Answer{QueryID: event.QueryID})
	}

	var queueDeadline time.Time
	if profile.QueueTimeout > 0 {
		queueDeadline = time.Now().Add(profile.QueueTimeout)
	}
	_, submitErr := a.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               callbackTaskID(event),
		Scope:            prepared.Scope(),
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:user:%d", event.UserID)),
		Pool:             profile.Pool,
		Class:            profile.Class,
		OrderingKey:      callbackOrderingKey(event),
		QueueDeadline:    queueDeadline,
		ExecutionTimeout: profile.ExecutionTimeout,
		Resources:        append([]tasks.ResourceRequirement(nil), profile.Resources...),
		Handler: func(taskCtx context.Context) error {
			return prepared.Dispatch(taskCtx)
		},
		OnComplete: func(result tasks.TaskResult) {
			a.completeCallback(event.QueryID, result)
		},
	})
	if submitErr != nil {
		answered := a.port.finishQuery(event.QueryID)
		if !answered {
			a.answerDirect(ctx, event.QueryID, "Interaction service is busy. Please try again.", false)
		}
		return true, submitErr
	}
	return true, nil
}

func (a *Adapter) completeCallback(queryID int64, result tasks.TaskResult) {
	if a == nil || a.port == nil || queryID == 0 {
		return
	}
	if a.port.finishQuery(queryID) {
		return
	}
	answerCtx, cancel := context.WithTimeout(context.Background(), callbackAnswerTimeout)
	defer cancel()
	if result.IsSuccess() {
		a.answerDirect(answerCtx, queryID, "", false)
		return
	}
	a.answerDirect(answerCtx, queryID, "Action failed. Reopen the menu and try again.", false)
}

func (a *Adapter) answerDirect(ctx context.Context, queryID int64, text string, alert bool) {
	if a == nil || a.port == nil || queryID == 0 {
		return
	}
	svc := a.port.serviceNow()
	if svc == nil {
		return
	}
	_ = svc.AnswerCallbackQuery(ctx, queryID, text, alert)
}

func callbackTarget(event *core.CallbackQueryEvent) (presentation.Target, error) {
	if event == nil {
		return nil, ErrInvalidInvocation
	}
	if event.IsInline() {
		if event.Target.InlineID == nil {
			return nil, ErrInvalidInvocation
		}
		return presentationtelegram.InlineTarget{
			MessageID: event.Target.InlineID,
			BindingID: inlineBindingID(event.Target.InlineID),
		}, nil
	}
	chatID := event.ChatID
	messageID := event.Target.MessageID
	if messageID <= 0 {
		messageID = event.MsgID
	}
	if event.Target.Peer == nil || chatID == 0 || messageID <= 0 {
		return nil, ErrInvalidInvocation
	}
	return presentationtelegram.MessageTarget{
		Peer:      event.Target.Peer,
		ChatID:    chatID,
		MessageID: messageID,
	}, nil
}

func callbackTaskID(event *core.CallbackQueryEvent) tasks.TaskID {
	if event != nil && event.IsInline() {
		return tasks.TaskID(fmt.Sprintf("native:a2:inline:%d", event.QueryID))
	}
	if event == nil {
		return tasks.TaskID("native:a2:callback:invalid")
	}
	return tasks.TaskID(fmt.Sprintf("native:a2:callback:%d", event.QueryID))
}

func callbackOrderingKey(event *core.CallbackQueryEvent) string {
	if event == nil {
		return ""
	}
	if !event.IsInline() {
		if event.ChatID != 0 && event.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d:%d", event.ChatID, event.MsgID)
		}
		return fmt.Sprintf("callback:%d", event.QueryID)
	}
	if event.Target.InlineID != nil {
		switch id := event.Target.InlineID.(type) {
		case *tg.InputBotInlineMessageID:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		case *tg.InputBotInlineMessageID64:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		}
	}
	if event.ChatInstance != 0 {
		return fmt.Sprintf("callback:instance:%d", event.ChatInstance)
	}
	return fmt.Sprintf("inline_callback:%d", event.QueryID)
}

func inlineBindingID(messageID tg.InputBotInlineMessageIDClass) string {
	if messageID == nil {
		return ""
	}
	return fmt.Sprintf("%T:%v", messageID, messageID)
}

func isPrivateMessageTarget(target presentationtelegram.MessageTarget) bool {
	switch target.Peer.(type) {
	case *tg.InputPeerUser, *tg.InputPeerSelf:
		return true
	default:
		return false
	}
}

type telegramPort struct {
	service ServiceProvider

	mu      sync.Mutex
	pending map[int64]bool
}

func newTelegramPort(service ServiceProvider) *telegramPort {
	return &telegramPort{service: service, pending: make(map[int64]bool)}
}

func (p *telegramPort) serviceNow() core.TelegramServicer {
	if p == nil || p.service == nil {
		return nil
	}
	return p.service()
}

func (p *telegramPort) bridge() (*presentationtelegram.Bridge, error) {
	svc := p.serviceNow()
	if svc == nil {
		return nil, core.ErrUnavailable
	}
	return presentationtelegram.NewBridge(svc), nil
}

func (p *telegramPort) beginQuery(queryID int64) bool {
	if p == nil || queryID == 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.pending[queryID]; exists || len(p.pending) >= maxPendingAnswers {
		return false
	}
	p.pending[queryID] = false
	return true
}

func (p *telegramPort) finishQuery(queryID int64) bool {
	if p == nil || queryID == 0 {
		return false
	}
	p.mu.Lock()
	answered := p.pending[queryID]
	delete(p.pending, queryID)
	p.mu.Unlock()
	return answered
}

func (p *telegramPort) Send(ctx context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	bridge, err := p.bridge()
	if err != nil {
		return nil, err
	}
	return bridge.Send(ctx, target, view)
}

func (p *telegramPort) Edit(ctx context.Context, target presentation.Target, view presentation.CompiledView) error {
	bridge, err := p.bridge()
	if err != nil {
		return err
	}
	return bridge.Edit(ctx, target, view)
}

func (p *telegramPort) Answer(ctx context.Context, answer presentation.Answer) error {
	if p == nil || answer.QueryID == 0 {
		return ErrInvalidInvocation
	}
	p.mu.Lock()
	answered, tracked := p.pending[answer.QueryID]
	p.mu.Unlock()
	if tracked && answered {
		return nil
	}
	bridge, err := p.bridge()
	if err != nil {
		return err
	}
	if err := bridge.Answer(ctx, answer); err != nil {
		return err
	}
	if tracked {
		p.mu.Lock()
		if _, exists := p.pending[answer.QueryID]; exists {
			p.pending[answer.QueryID] = true
		}
		p.mu.Unlock()
	}
	return nil
}

func (p *telegramPort) DeliverMedia(ctx context.Context, target presentation.Target, media presentation.Media) error {
	bridge, err := p.bridge()
	if err != nil {
		return err
	}
	return bridge.DeliverMedia(ctx, target, media)
}

func (p *telegramPort) Delete(ctx context.Context, target presentation.Target) error {
	bridge, err := p.bridge()
	if err != nil {
		return err
	}
	return bridge.Delete(ctx, target)
}

var _ presentation.Port = (*telegramPort)(nil)
var _ presentation.MediaDeliverer = (*telegramPort)(nil)
var _ presentation.Deleter = (*telegramPort)(nil)
