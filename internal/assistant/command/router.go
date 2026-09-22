package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

const (
	assistantSavedResponseTimeout    = 2 * time.Minute
	assistantSavedResponseMetricName = "savedresponse"
)

var (
	// ErrUnknownCommand indicates no registered handler matched the requested command.
	ErrUnknownCommand = errors.New("assistant/command: unknown command")
	// ErrTasksNotConfigured indicates a resource-bearing Assistant command cannot
	// be admitted through the shared TaskEngine execution authority.
	ErrTasksNotConfigured = errors.New("assistant/command: task client is required for command execution")
)

// Context provides the command arguments, sender coordinates, and interaction primitives.
type Context struct {
	Ctx         context.Context
	SenderID    int64
	Peer        tg.InputPeerClass
	Command     string
	Args        []string
	Interaction interaction.MessageInteraction
}

// MessageContext carries transport-derived message and chat identity into the
// canonical core.Context. Production Assistant ingress should populate Chat
// from Telegram entities so PeerChannel can be distinguished as supergroup or
// broadcast channel instead of guessing from the input-peer class alone.
type MessageContext struct {
	Chat             core.Chat
	MessageID        int
	ReplyToMessageID int
	TopicID          int
}

// Reply sends a formatted response to the command originator.
func (c *Context) Reply(text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if c.Interaction == nil || c.Peer == nil {
		return nil, interaction.ErrInvalidTarget
	}
	return c.Interaction.SendMessage(c.Ctx, c.Peer, text, markup)
}

// Handler defines the function signature for an assistant bot command handler.
type Handler func(c *Context) error

// Router is the Assistant transport dispatcher. The canonical command registry
// remains core.Router; handlers here are restricted to Assistant presentation
// commands such as /start and are not an alternate plugin command registry.
type Router struct {
	presentationHandlers map[string]Handler
	coreRouter           *core.Router
	tasks                tasks.Client
	delayedActions       core.DelayedActionScheduler
	ownerID              int64
	sudoGetter           func() []int64
	metrics              core.MetricsCollector
	savedBindings        *savedresponse.BindingService
	savedDelivery        *savedresponse.ResponseDelivery
	logger               *zap.Logger
	taskSeq              atomic.Uint64
}

// NewRouter creates an initialized command Router.
func NewRouter(logger *zap.Logger) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Router{
		presentationHandlers: make(map[string]Handler),
		logger:               logger,
	}
}

// SetTasks attaches the shared TaskEngine client used by canonical Assistant
// commands. Resource-bearing commands fail closed when this dependency is absent.
func (r *Router) SetTasks(client tasks.Client) { r.tasks = client }
func (r *Router) SetDelayedActions(scheduler core.DelayedActionScheduler) {
	r.delayedActions = scheduler
}

// SetOwner configures the owner identity and optional sudo getter for permission enforcement.
func (r *Router) SetOwner(ownerID int64, sudoGetter func() []int64) {
	r.ownerID = ownerID
	r.sudoGetter = sudoGetter
}

// SetMetricsCollector configures optional runtime metrics collection.
func (r *Router) SetMetricsCollector(m core.MetricsCollector) {
	r.metrics = m
}

// SetSavedResponseBindings installs the canonical persistent SavedResponse
// surface-binding service and shared bounded delivery lifecycle.
func (r *Router) SetSavedResponseBindings(bindings *savedresponse.BindingService, delivery *savedresponse.ResponseDelivery) {
	r.savedBindings = bindings
	r.savedDelivery = delivery
}

// OwnerID returns the configured owner ID.
func (r *Router) OwnerID() int64 {
	return r.ownerID
}

// SetCoreRouter attaches the canonical core.Router as the authoritative command source.
func (r *Router) SetCoreRouter(router *core.Router) {
	r.coreRouter = router
}

// CoreRouter returns the attached canonical core.Router.
func (r *Router) CoreRouter() *core.Router {
	return r.coreRouter
}

// Register attaches an Assistant presentation handler. It is intentionally
// separate from plugin/core command registration and should only be used for
// transport-specific UI entry points such as /start.
func (r *Router) Register(cmd string, handler Handler) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	r.presentationHandlers[cmd] = handler
}

func (r *Router) findCommand(name string) (core.Command, bool) {
	if r.coreRouter != nil {
		return r.coreRouter.Find(name)
	}
	return core.Command{}, false
}

func legacyChatForPeer(peer tg.InputPeerClass) core.Chat {
	chat := core.Chat{ID: extractChatIDFromInputPeer(peer)}
	switch peer.(type) {
	case *tg.InputPeerUser, *tg.InputPeerSelf:
		chat.Type = string(core.ChatKindPrivate)
	case *tg.InputPeerChat:
		chat.Type = string(core.ChatKindGroup)
	case *tg.InputPeerChannel:
		// Without entity metadata InputPeerChannel is ambiguous. Treat it as a
		// broadcast channel so Assistant GroupOnly admission fails closed.
		chat.Type = string(core.ChatKindChannel)
	}
	return chat
}

func taskResultError(res tasks.TaskResult) error {
	if res.IsSuccess() {
		return nil
	}
	if res.Failure.Message != "" {
		return errors.New(res.Failure.Message)
	}
	return fmt.Errorf("assistant/command: task %s finished with outcome %s (%s)", res.TaskID, res.Outcome, res.Cause)
}

func (r *Router) executeCanonicalDirect(cmd core.Command, coreCtx *core.Context, cmdName string) error {
	handler := core.FilterMiddlewareForSource(cmd, core.ExecutionAssistant)(cmd.Handler)
	start := time.Now()
	err := handler(coreCtx)
	if r.metrics != nil {
		r.metrics.RecordCommand(cmdName, time.Since(start), err)
	}
	return err
}

func (r *Router) executeCanonicalTask(ctx context.Context, senderID int64, cmd core.Command, coreCtx *core.Context, cmdName string) error {
	if r.tasks == nil {
		// Preserve lightweight embedding/test compatibility, but never let a
		// resource-bearing command bypass TaskEngine reservations.
		if len(cmd.Resources) > 0 {
			return ErrTasksNotConfigured
		}
		return r.executeCanonicalDirect(cmd, coreCtx, cmdName)
	}

	sequence := r.taskSeq.Add(1)
	taskID := tasks.TaskID(fmt.Sprintf("assistant:%d:%d", senderID, sequence))
	correlationID := coreCtx.CorrelationID
	handler := core.FilterMiddlewareForSource(cmd, core.ExecutionAssistant)(cmd.Handler)

	ticket, err := r.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            cmd.Scope,
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("assistant:user:%d", senderID)),
		Pool:             "interactive",
		Class:            tasks.PriorityInteractive,
		OrderingKey:      correlationID,
		ExecutionTimeout: cmd.Timeout,
		Resources:        append([]tasks.ResourceRequirement(nil), cmd.Resources...),
		Handler: func(taskCtx context.Context) error {
			runCtx, cancel := context.WithCancel(taskCtx)
			defer cancel()
			stopWatching := context.AfterFunc(ctx, cancel)
			defer stopWatching()

			execCtx := *coreCtx
			execCtx.Ctx = runCtx
			start := time.Now()
			err := handler(&execCtx)
			if r.metrics != nil {
				r.metrics.RecordCommand(cmdName, time.Since(start), err)
			}
			return err
		},
	})
	if err != nil {
		return err
	}

	res, waitErr := ticket.Wait(ctx)
	if waitErr != nil {
		_, _ = r.tasks.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		return waitErr
	}
	return taskResultError(res)
}

func (r *Router) executeSavedResponseBinding(
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	inter interaction.MessageInteraction,
	commandName string,
) (bool, error) {
	if r.savedBindings == nil {
		return false, nil
	}

	prepared, err := r.savedBindings.Prepare(ctx, savedresponse.SurfaceAssistantCommand, commandName)
	switch {
	case err == nil:
	case errors.Is(err, savedresponse.ErrBindingNotFound),
		errors.Is(err, savedresponse.ErrBindingDisabled),
		errors.Is(err, savedresponse.ErrInvalidBinding):
		return false, nil
	default:
		return true, err
	}
	if r.savedDelivery == nil {
		return true, savedresponse.ErrResponseDeliveryUnavailable
	}
	if r.tasks == nil {
		return true, ErrTasksNotConfigured
	}

	chatID := extractChatIDFromInputPeer(peer)
	if chatID == 0 {
		chatID = senderID
	}
	pool := tasks.PoolID("interactive")
	resources := make([]tasks.ResourceRequirement, 0, 1)
	if prepared.HasMedia() {
		// Media materialization/upload can occupy a worker for much longer than
		// a text response. Keep interactive priority while using the general
		// worker pool plus the shared media resource budget.
		pool = tasks.PoolID("general")
		resources = append(resources, tasks.ResourceRequirement{Name: "media", Amount: 1})
	}

	sequence := r.taskSeq.Add(1)
	taskID := tasks.TaskID(fmt.Sprintf("assistant:savedresponse:%d:%d", senderID, sequence))
	ticket, err := r.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            prepared.Scope(),
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("assistant:user:%d", senderID)),
		Pool:             pool,
		Class:            tasks.PriorityInteractive,
		OrderingKey:      fmt.Sprintf("assistant:savedresponse:%d", chatID),
		ExecutionTimeout: assistantSavedResponseTimeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) (runErr error) {
			runCtx, cancel := context.WithCancel(taskCtx)
			defer cancel()
			stopWatching := context.AfterFunc(ctx, cancel)
			defer stopWatching()

			start := time.Now()
			defer func() {
				if r.metrics != nil {
					r.metrics.RecordCommand(assistantSavedResponseMetricName, time.Since(start), runErr)
				}
			}()

			resolved, resolveErr := r.savedBindings.ResolvePrepared(runCtx, prepared)
			if resolveErr != nil {
				return resolveErr
			}
			vars := savedresponse.TemplateVars{
				UserID: senderID,
				ChatID: chatID,
				Now:    time.Now(),
			}
			stage, deliveryErr := r.savedDelivery.Deliver(
				runCtx,
				resolved.Resolved.Response,
				vars,
				savedresponse.DeliverySink{
					SendMedia: func(mediaType, path, caption string) error {
						if inter == nil || peer == nil {
							return interaction.ErrInvalidTarget
						}
						_, sendErr := inter.SendMedia(runCtx, peer, mediaType, path, caption)
						return sendErr
					},
					SendText: func(text string) error {
						if inter == nil || peer == nil {
							return interaction.ErrInvalidTarget
						}
						_, sendErr := inter.SendMessage(runCtx, peer, text, nil)
						return sendErr
					},
				},
			)
			if deliveryErr != nil {
				return fmt.Errorf("assistant/command: saved response delivery stage %d: %w", stage, deliveryErr)
			}
			return nil
		},
	})
	if err != nil {
		return true, err
	}

	res, waitErr := ticket.Wait(ctx)
	if waitErr != nil {
		_, _ = r.tasks.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		return true, waitErr
	}
	return true, taskResultError(res)
}

// Dispatch parses a command without transport message coordinates. It is kept
// for embedding/tests; production Assistant updates should use DispatchMessage
// so reply-aware canonical commands receive message identity.
func (r *Router) Dispatch(ctx context.Context, senderID int64, peer tg.InputPeerClass, messageText string, inter interaction.MessageInteraction) error {
	return r.dispatch(ctx, senderID, peer, messageText, MessageContext{Chat: legacyChatForPeer(peer)}, inter)
}

// DispatchMessage preserves Telegram message/reply identity in the canonical
// core.Context so reply-based Assistant commands (/who, relay controls, etc.)
// can use the same command registry rather than a transport-local dispatcher.
// Callers with Telegram entity metadata should prefer DispatchMessageContext.
func (r *Router) DispatchMessage(
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	messageText string,
	messageID int,
	replyToMessageID int,
	inter interaction.MessageInteraction,
) error {
	return r.dispatch(ctx, senderID, peer, messageText, MessageContext{
		Chat:             legacyChatForPeer(peer),
		MessageID:        messageID,
		ReplyToMessageID: replyToMessageID,
	}, inter)
}

// DispatchMessageContext preserves authoritative chat kind and topic metadata
// from the Assistant update boundary.
func (r *Router) DispatchMessageContext(
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	messageText string,
	messageContext MessageContext,
	inter interaction.MessageInteraction,
) error {
	if messageContext.Chat.ID == 0 {
		messageContext.Chat = legacyChatForPeer(peer)
	}
	return r.dispatch(ctx, senderID, peer, messageText, messageContext, inter)
}

func (r *Router) dispatch(
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	messageText string,
	messageContext MessageContext,
	inter interaction.MessageInteraction,
) error {
	fields := strings.Fields(strings.TrimSpace(messageText))
	if len(fields) == 0 {
		return nil
	}

	cmdRaw := strings.ToLower(fields[0])
	if !strings.HasPrefix(cmdRaw, "/") {
		return nil
	}

	// Strip optional @botusername suffix (e.g. /start@GoUltroidBot -> /start)
	if atIdx := strings.Index(cmdRaw, "@"); atIdx != -1 {
		cmdRaw = cmdRaw[:atIdx]
	}

	cmdCtx := &Context{
		Ctx:         ctx,
		SenderID:    senderID,
		Peer:        peer,
		Command:     cmdRaw,
		Args:        fields[1:],
		Interaction: inter,
	}

	cmdNameClean := strings.TrimPrefix(cmdRaw, "/")

	// Primary path: canonical command lookup from core.Router.
	if cmd, ok := r.findCommand(cmdNameClean); ok && cmd.Handler != nil {
		if !cmd.IsAvailableOn(execution.SourceAssistant) {
			return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
		}

		// Permission check. The owner/sudo policy is kept explicit here until
		// command execution authorization is fully centralized in core.
		isOwner := r.ownerID != 0 && senderID != 0 && senderID == r.ownerID
		isSudo := isOwner
		var sudoList []int64
		if r.sudoGetter != nil {
			sudoList = r.sudoGetter()
			for _, s := range sudoList {
				if s != 0 && s == senderID {
					isSudo = true
					break
				}
			}
		}

		perms := core.NewPermissions(r.ownerID, sudoList)
		if !cmd.CanInvoke(core.ExecutionAssistant, senderID, false, perms) {
			r.logger.Debug("assistant: command invocation denied",
				zap.String("command", cmdNameClean),
				zap.Int64("sender_id", senderID),
				zap.String("policy", cmd.EffectiveInvocation(core.ExecutionAssistant).String()),
			)
			if inter != nil && peer != nil {
				_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command cannot be invoked by this account.</i>", nil)
			}
			return nil
		}

		if (cmd.Permission == core.PermissionOwner || cmd.Permission == core.PermissionSudo) && r.ownerID == 0 {
			r.logger.Warn("assistant: owner_id not configured, rejecting privileged command",
				zap.String("command", cmdNameClean),
				zap.Int64("sender_id", senderID),
			)
			if inter != nil && peer != nil {
				_, _ = inter.SendMessage(ctx, peer, "⛔ <i>Privileged commands are disabled: bot owner is not configured.</i>", nil)
			}
			return nil
		}

		switch cmd.Permission {
		case core.PermissionOwner:
			if !isOwner {
				r.logger.Warn("assistant: permission denied for command",
					zap.String("command", cmdNameClean),
					zap.Int64("sender_id", senderID),
				)
				if inter != nil && peer != nil {
					_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command is restricted to the bot owner.</i>", nil)
				}
				return nil
			}
		case core.PermissionSudo:
			if !isSudo {
				r.logger.Warn("assistant: sudo permission required for command",
					zap.String("command", cmdNameClean),
					zap.Int64("sender_id", senderID),
				)
				if inter != nil && peer != nil {
					_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command requires sudo privileges.</i>", nil)
				}
				return nil
			}
		}

		chat := messageContext.Chat
		if chat.ID == 0 {
			chat = legacyChatForPeer(peer)
		}
		if chat.ID == 0 {
			chat.ID = senderID
		}
		if chat.Type == "" && chat.ID == senderID {
			chat.Type = string(core.ChatKindPrivate)
		}

		principal := &core.Principal{
			UserID:  senderID,
			IsOwner: isOwner,
			IsSudo:  isSudo,
			Level:   perms.Level(senderID),
		}

		coreCtx := &core.Context{
			Ctx:           ctx,
			CorrelationID: fmt.Sprintf("asst-%d-%d", senderID, time.Now().UnixNano()),
			Source:        core.ExecutionAssistant,
			Command:       cmdNameClean,
			Args:          fields[1:],
			RawArgs:       strings.Join(fields[1:], " "),
			PeerID:        peer,
			Message: &core.Message{
				ID:        messageContext.MessageID,
				SenderID:  senderID,
				TopicID:   messageContext.TopicID,
				Text:      strings.TrimSpace(messageText),
				ReplyToID: messageContext.ReplyToMessageID,
			},
			Sender:         &core.User{ID: senderID},
			Chat:           &chat,
			Perms:          perms,
			Principal:      principal,
			Svc:            &assistantServicerAdapter{inter: inter},
			DelayedActions: r.delayedActions,
		}

		return r.executeCanonicalTask(ctx, senderID, cmd, coreCtx, cmdNameClean)
	}

	// Presentation-only fallbacks such as /start stay reserved ahead of
	// persistent dynamic aliases; this keeps shell entry points authoritative.
	if handler, ok := r.presentationHandlers[cmdRaw]; ok {
		r.logger.Debug("assistant: executing presentation command",
			zap.String("command", cmdRaw),
			zap.Int64("sender_id", senderID),
		)
		start := time.Now()
		err := handler(cmdCtx)
		if r.metrics != nil {
			r.metrics.RecordCommand(cmdNameClean, time.Since(start), err)
		}
		return err
	}

	if handled, err := r.executeSavedResponseBinding(ctx, senderID, peer, inter, cmdNameClean); handled {
		return err
	}

	return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
}
