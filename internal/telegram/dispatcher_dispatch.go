package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// ingressAdmission holds the lifecycle admission lease and the memory-only
// transport deduplication claim for an incoming message update.
type ingressAdmission struct {
	releaseLease func()
	ingressClaim ingressDedupeClaim
}

func (a *ingressAdmission) releaseIngressClaim() {
	a.ingressClaim.Release()
}

// messageClassification encapsulates the syntactic and contextual properties
// derived during initial message ingress before decision or command execution.
type messageClassification struct {
	chatID             int64
	commandIdentity    string
	hasCommandIdentity bool
	parsed             *core.ParsedCommand
	isCmd              bool
	cmdName            string
	cmd                core.Command
	cmdExists          bool
	origin             core.ExecutionSource
	decision           *core.MessageDecision
	envelope           *core.MessageEnvelope
	decisionHandlers   []prioritizedHandler
	eventHandlers      []prioritizedHandler
}

// Stage 1: admitMessage gates ingress admission against lifecycle shutdown and
// acquires the bounded in-memory transport deduplication claim.
func (d *Dispatcher) admitMessage(msg *tg.Message) (ingressAdmission, bool) {
	release, accepted := d.admitIngress()
	if !accepted {
		return ingressAdmission{}, false
	}

	var claim ingressDedupeClaim
	if d.ingressDedupe != nil {
		var ingressAccepted bool
		claim, ingressAccepted = d.ingressDedupe.Begin(msg, time.Now())
		if !ingressAccepted {
			release()
			return ingressAdmission{}, false
		}
	}

	return ingressAdmission{
		releaseLease: release,
		ingressClaim: claim,
	}, true
}

// Stage 2: classifyMessage validates sender consistency, parses command syntax,
// looks up command registration, classifies bot origin, sets up the decision context,
// and normalizes the message envelope.
func (d *Dispatcher) classifyMessage(ctx context.Context, e tg.Entities, msg *tg.Message) (context.Context, *messageClassification, bool) {
	if !privateMessageSenderConsistent(msg) {
		// An incoming private message with explicit mismatched or non-user
		// FromID cannot be trusted as an owner/sudo command or PM automation.
		d.logger.Warn("dispatcher: inconsistent incoming private sender; update dropped")
		return ctx, nil, false
	}

	chatID := extractChatIDFromPeer(msg.PeerID)
	commandIdentity, hasCommandIdentity := telegramMessageIdentity(msg.PeerID, msg.ID)
	parsed, isCmd, err := d.router.Parse(msg.Message)
	if err != nil {
		// Do not log the command body or err.Error(): parser diagnostics
		// can otherwise disclose PM contents or command arguments.
		reason := "invalid_syntax"
		switch {
		case errors.Is(err, core.ErrUnclosedQuote):
			reason = "unclosed_quote"
		case errors.Is(err, core.ErrTrailingEscape):
			reason = "trailing_escape"
		}
		d.logger.Warn("command parse syntax error", zap.String("reason", reason))
		// Invalid command syntax must not bypass security or message observers.
		// The message is plain text for routing; no command will be executed.
		parsed, isCmd = nil, false
	}
	cmdName := ""
	if isCmd {
		cmdName = parsed.Name
	}

	var cmd core.Command
	var cmdExists bool
	if isCmd {
		cmd, cmdExists = d.router.Find(parsed.Name)
	}

	origin := core.ExecutionInteractive
	if msg.Out {
		svc := d.getOriginTracker()
		if svc != nil {
			if tracker, ok := svc.(peerAwareBotOriginTracker); ok {
				if tracker.IsBotSentForPeer(msg.PeerID, msg.ID, d.getSelfID()) {
					origin = core.ExecutionAutomation
				}
			}
			// No ID-only fallback: Telegram message IDs overlap across peers.
		}
	}
	decision := core.NewMessageDecision(origin)
	ctx = core.WithMessageDecision(ctx, decision)

	if len(e.Users) > 0 || len(e.Channels) > 0 || len(e.Chats) > 0 {
		resolver := d.getResolver()
		if r, ok := resolver.(*Resolver); ok && r.storage != nil {
			d.enqueuePeerEntities(e)
		}
	}

	messageEnvelope := NormalizeMessageEnvelope(e, msg, isCmd, cmdName, d.getSelfID())
	decisionHandlers, eventHandlers := d.messageHandlersForEnvelope(messageEnvelope)

	return ctx, &messageClassification{
		chatID:             chatID,
		commandIdentity:    commandIdentity,
		hasCommandIdentity: hasCommandIdentity,
		parsed:             parsed,
		isCmd:              isCmd,
		cmdName:            cmdName,
		cmd:                cmd,
		cmdExists:          cmdExists,
		origin:             origin,
		decision:           decision,
		envelope:           messageEnvelope,
		decisionHandlers:   decisionHandlers,
		eventHandlers:      eventHandlers,
	}, true
}

// Stage 3: runMessageDecision executes synchronous interceptors/decision handlers
// under the shared 5-second deadline. Returns true if the message was handled,
// suppressed, or dropped by a security fail-closed decision.
func (d *Dispatcher) runMessageDecision(ctx context.Context, e tg.Entities, msg *tg.Message, c *messageClassification) bool {
	if d.executeDecisionHandlersEnvelope(ctx, c.decisionHandlers, c.envelope, e, msg) {
		return true
	}
	if c.decision.IsHandled() || c.decision.IsSuppressedCommands() {
		return true
	}
	return false
}

type commandCandidate struct {
	isExecutable bool
	commandClaim dispatcherCommandClaim
}

func (c *commandCandidate) IsExecutable() bool {
	return c != nil && c.isExecutable
}

// Stage 4: authorizeAndClaimCommand evaluates invocation policy before acquiring
// durable idempotency claims in SQLite. Non-commands and unknown commands return
// an non-executable candidate without touching durable storage.
func (d *Dispatcher) authorizeAndClaimCommand(
	ctx context.Context,
	msg *tg.Message,
	c *messageClassification,
	admission *ingressAdmission,
) (*commandCandidate, bool) {
	if !c.cmdExists {
		return &commandCandidate{isExecutable: false}, true
	}

	// Invocation is a cheap security admission gate and must run before durable
	// idempotency, peer resolution, or TaskEngine submission. Permission remains
	// a separate authorization check inside CommandExecutor.
	if !c.hasCommandIdentity {
		d.logger.Warn("dispatcher: command has no stable telegram message identity")
		return nil, false
	}
	senderID := invocationSenderID(msg, d.getSelfID())
	if !c.cmd.CanInvoke(core.ExecutionInteractive, senderID, msg.Out, d.perms) {
		d.logger.Debug("dispatcher: command invocation denied",
			zap.String("command", c.cmdName),
			zap.Int64("sender_id", senderID),
			zap.String("policy", c.cmd.EffectiveInvocation(core.ExecutionInteractive).String()),
		)
		return nil, false
	}

	// Durability belongs to execution candidates, not transport ingress. Only a
	// recognized command that survived the synchronous decision pipeline may
	// claim durable idempotency. This keeps ordinary traffic, unknown commands,
	// and suppressed commands off the SQLite write path while preserving the
	// fail-closed guarantee before command/event side effects.
	var commandClaim dispatcherCommandClaim
	if d.idempotencyMgr != nil {
		key := "msg:" + c.commandIdentity
		legacyKey := fmt.Sprintf("msg:%d:%d", c.chatID, msg.ID)
		if key != legacyKey && time.Now().Before(d.legacyCommandIdentityUntil) {
			legacyProcessed, legacyErr := d.idempotencyMgr.IsProcessedContext(ctx, legacyKey)
			if legacyErr != nil {
				admission.releaseIngressClaim()
				d.logger.Error("dispatcher: legacy command idempotency lookup failed",
					zap.String("key", legacyKey),
					zap.String("command", c.cmdName),
					zap.String("error_type", fmt.Sprintf("%T", legacyErr)),
				)
				return nil, false
			}
			if legacyProcessed {
				d.logger.Debug("dispatcher: duplicate command matched legacy idempotency key", zap.String("key", legacyKey))
				return nil, false
			}
		}
		var isNew bool
		var claimErr error
		commandClaim, isNew, claimErr = d.beginCommandClaim(ctx, key)
		if claimErr != nil {
			admission.releaseIngressClaim()
			d.logger.Error("dispatcher: command idempotency claim failed",
				zap.String("key", key),
				zap.String("command", c.cmdName),
				zap.String("error_type", fmt.Sprintf("%T", claimErr)),
			)
			return nil, false
		}
		if !isNew {
			d.logger.Debug("dispatcher: duplicate command dropped by idempotency manager", zap.String("key", key))
			return nil, false
		}
	}

	return &commandCandidate{
		isExecutable: true,
		commandClaim: commandClaim,
	}, true
}

type preparedMessage struct {
	coreMsg *core.Message
}

// Stage 5: prepareMessageEffects checks EventBus subscribers, conditionally
// materializes core.Message, buffers albums, and publishes the MessageCreated event.
func (d *Dispatcher) prepareMessageEffects(
	msg *tg.Message,
	c *messageClassification,
) *preparedMessage {
	bus := d.getEventBus()
	publishMessageEvent := bus != nil &&
		bus.HasSubscribersAtPriority(core.EventTypeMessageCreated, core.PriorityNormal)

	// core.Message performs media extraction and is materially heavier than the
	// ingress envelope. Build it only when a command, album aggregation, or an
	// actual EventBus subscriber needs it.
	needsCoreMessage := c.cmdExists || msg.GroupedID != 0 || publishMessageEvent
	var coreMsg *core.Message
	if needsCoreMessage {
		coreMsg = extractCoreMessage(msg)
	}
	if coreMsg != nil && coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		d.albumBuffer.Add(coreMsg)
	}
	if publishMessageEvent {
		if evt := canonicalMessageCreatedEvent(msg, coreMsg, time.Now()); evt != nil {
			bus.Publish(evt)
		}
	}

	return &preparedMessage{
		coreMsg: coreMsg,
	}
}

// dispatchObservers dispatches event hooks asynchronously via TaskEngine.
func (d *Dispatcher) dispatchObservers(
	ctx context.Context,
	e tg.Entities,
	msg *tg.Message,
	c *messageClassification,
) {
	d.dispatchEventHandlersEnvelope(ctx, c.eventHandlers, c.envelope, e, msg)
}

type preparedCommand struct {
	execCtx       context.Context
	cancel        context.CancelFunc
	coreCtx       *core.Context
	cmd           core.Command
	cmdName       string
	taskID        string
	taskOwner     string
	correlationID string
	commandClaim  dispatcherCommandClaim
	chatID        int64
}

// Stage 6: prepareCommandContext resolves peer and chat entities, sets up sender
// and localizer metadata, constructs core.Context, and derives stable task/correlation IDs.
func (d *Dispatcher) prepareCommandContext(
	ctx context.Context,
	e tg.Entities,
	msg *tg.Message,
	classified *messageClassification,
	candidate *commandCandidate,
	prepared *preparedMessage,
	admission *ingressAdmission,
) (*preparedCommand, bool) {
	peerInput := d.resolveDispatchPeer(ctx, e, msg)
	chat := d.resolveDispatchChat(e, msg)
	d.logger.Debug("dispatch: peer resolved",
		zap.String("peerType", fmt.Sprintf("%T", msg.PeerID)),
		zap.String("chatType", chat.Type),
		zap.Int64("chatID", chat.ID),
		zap.Bool("msgOut", msg.Out),
		zap.String("command", classified.cmdName),
	)
	if peerInput == nil && msg.Out {
		peerInput = &tg.InputPeerSelf{}
	}
	if peerInput == nil && msg.PeerID != nil {
		d.releaseCommandClaim(ctx, classified.cmdName, candidate.commandClaim)
		admission.releaseIngressClaim()
		d.logger.Warn("dispatch: peer unresolvable without access hash, command execution skipped",
			zap.Int64("chatID", chat.ID),
			zap.String("command", classified.cmdName),
		)
		return nil, false
	}

	sender := &core.User{}
	if msg.Out {
		sender.ID = d.getSelfID()
	} else if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			sender.ID = u.UserID
			if ue, ok := e.Users[u.UserID]; ok {
				sender.FirstName = ue.FirstName
				sender.LastName = ue.LastName
				sender.Username = ue.Username
				sender.IsBot = ue.Bot
			}
		}
	}
	coreMsg := prepared.coreMsg
	if coreMsg == nil {
		coreMsg = extractCoreMessage(msg)
	}
	coreMsg.SenderID = sender.ID

	root := d.getRootContext()
	if root == nil {
		d.logger.Warn("dispatcher: root context is nil, falling back to background - lifecycle not correctly wired")
		root = context.Background()
	}
	execCtx, cancel := context.WithCancel(root)
	var album []*core.Message
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		album = d.albumBuffer.Get(coreMsg.GroupedID)
	}
	localizer := d.getLocalizer()
	if resolve := d.getLocalizerResolver(); resolve != nil {
		if bound := resolve(execCtx, sender.ID, chat.ID); bound != nil {
			localizer = bound
		}
	}
	coreCtx := &core.Context{
		Ctx:       execCtx,
		Command:   classified.parsed.Name,
		Args:      classified.parsed.Args,
		RawArgs:   classified.parsed.RawArgs,
		Message:   coreMsg,
		Album:     album,
		Chat:      chat,
		Sender:    sender,
		Perms:     d.perms,
		Telegram:  d.getCommandCapabilities(),
		PeerID:    peerInput,
		Resolver:  d.getResolver(),
		Localizer: localizer,
		EventBus:  d.getEventBus(),
	}

	taskOwner := fmt.Sprintf("telegram:user:%d", sender.ID)
	if sender.ID == 0 {
		taskOwner = "telegram:unknown"
	}
	taskID := "cmd:" + classified.commandIdentity
	correlationID := "msg:" + classified.commandIdentity

	return &preparedCommand{
		execCtx:       execCtx,
		cancel:        cancel,
		coreCtx:       coreCtx,
		cmd:           classified.cmd,
		cmdName:       classified.cmdName,
		taskID:        taskID,
		taskOwner:     taskOwner,
		correlationID: correlationID,
		commandClaim:  candidate.commandClaim,
		chatID:        chat.ID,
	}, true
}

// Stage 7: submitCommand submits the prepared command context to TaskEngine,
// committing the durable claim upon admission or releasing claims upon failure.
func (d *Dispatcher) submitCommand(
	ctx context.Context,
	command *preparedCommand,
	admission *ingressAdmission,
) error {
	if err := d.submitInteractiveCommandWithAdmission(
		command.execCtx,
		command.cancel,
		command.coreCtx,
		command.cmd,
		command.taskID,
		command.taskOwner,
		command.correlationID,
		func() error { return d.acceptCommandClaim(ctx, command.cmdName, command.commandClaim) },
	); err != nil {
		d.releaseCommandClaim(ctx, command.cmdName, command.commandClaim)
		admission.releaseIngressClaim()
		d.logger.Warn("interactive command admission rejected",
			zap.String("command", command.cmdName),
			zap.Int64("chat_id", command.chatID),
			zap.String("error_type", fmt.Sprintf("%T", err)),
		)
		return err
	}
	return nil
}

// dispatch is the linear pipeline coordinator for incoming Telegram message updates.
func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	admission, ok := d.admitMessage(msg)
	if !ok {
		return nil
	}
	defer admission.releaseLease()

	ctx, classified, ok := d.classifyMessage(ctx, e, msg)
	if !ok {
		return nil
	}

	if d.runMessageDecision(ctx, e, msg, classified) {
		return nil
	}

	candidate, ok := d.authorizeAndClaimCommand(ctx, msg, classified, &admission)
	if !ok {
		return nil
	}

	prepared := d.prepareMessageEffects(msg, classified)

	if !candidate.IsExecutable() {
		d.dispatchObservers(ctx, e, msg, classified)
		return nil
	}

	command, ok := d.prepareCommandContext(ctx, e, msg, classified, candidate, prepared, &admission)
	if !ok {
		return nil
	}

	if err := d.submitCommand(ctx, command, &admission); err != nil {
		return nil
	}

	d.dispatchObservers(ctx, e, msg, classified)
	return nil
}

// executeDecisionHandlers preserves the synchronous security/moderation
// decision contract while routing plugin code through TaskEngine. One shared
// deadline bounds total update-loop latency regardless of handler count.
func (d *Dispatcher) executeDecisionHandlers(ctx context.Context, handlers []prioritizedHandler, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) bool {
	message := NormalizeMessageEnvelope(e, msg, isCmd, cmdName, d.getSelfID())
	return d.executeDecisionHandlersEnvelope(ctx, handlers, message, e, msg)
}

func (d *Dispatcher) executeDecisionHandlersEnvelope(ctx context.Context, handlers []prioritizedHandler, message *core.MessageEnvelope, e tg.Entities, msg *tg.Message) bool {
	if message == nil || msg == nil {
		return false
	}
	if len(handlers) == 0 {
		return false
	}
	chatID := message.ChatID
	decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, _ := decisionCtx.Deadline()
	// TaskEngine workers have engine-owned cancellation and lifecycle. Bound
	// their queueing and execution to this one ingress deadline rather than
	// granting each handler another full timeout.
	sharedDecision := core.GetMessageDecision(ctx)
	for _, registered := range handlers {
		if !d.messageHookStateInterested(registered, chatID) {
			continue
		}
		// Once the shared budget expires, a security decision that could not
		// run is a denial. Never submit further tasks on an expired context.
		if decisionCtx.Err() != nil {
			if registered.failurePolicy == FailurePolicyFailClosed {
				return true
			}
			continue
		}
		if registered.scope.IsZero() { // compatibility for local/test handlers
			handled := d.safeExecuteRegisteredInterceptor(decisionCtx, registered, e, msg, message)
			if decisionCtx.Err() != nil && registered.failurePolicy == FailurePolicyFailClosed {
				return true
			}
			if handled {
				return true
			}
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if registered.failurePolicy == FailurePolicyFailClosed {
				return true
			}
			continue
		}
		client := d.taskClient()
		if client == nil {
			failClosed := registered.failurePolicy == FailurePolicyFailClosed
			d.logger.Warn("decision handler execution unavailable",
				zap.Uint64("handler_id", registered.id),
				zap.Bool("fail_closed", failClosed),
				zap.String("error_code", "tasks_unavailable"),
			)
			if failClosed {
				return true
			}
			continue
		}
		var handled atomic.Bool
		d.inFlight.Add(1)
		ticket, err := client.Submit(decisionCtx, tasks.WorkSpec{
			ID:               messageHookTaskID("decision", registered.id, message.Peer, msg.ID),
			Scope:            registered.scope,
			QuotaOwner:       tasks.OwnerID(registered.scope.Owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      messageHookDecisionOrderingKey(message.Peer),
			QueueDeadline:    deadline,
			ExecutionTimeout: remaining,
			Handler: func(taskCtx context.Context) error {
				// Admission can outlive an ingress cancellation. A queued
				// handler must not run after the decision was abandoned.
				if err := decisionCtx.Err(); err != nil {
					return err
				}
				hookCtx, hookCancel := context.WithDeadline(taskCtx, deadline)
				defer hookCancel()
				if err := hookCtx.Err(); err != nil {
					return err
				}
				if sharedDecision != nil {
					hookCtx = core.WithMessageDecision(hookCtx, sharedDecision)
				}
				handled.Store(d.safeExecuteRegisteredInterceptor(hookCtx, registered, e, msg, message))
				return nil
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			failClosed := registered.failurePolicy == FailurePolicyFailClosed
			d.logger.Warn("decision handler admission rejected",
				zap.Uint64("handler_id", registered.id),
				zap.Bool("fail_closed", failClosed),
				zap.String("error_type", fmt.Sprintf("%T", err)),
			)
			if failClosed {
				return true
			}
			continue
		}
		result, waitErr := ticket.Wait(decisionCtx)
		if waitErr != nil || !result.IsSuccess() {
			failClosed := registered.failurePolicy == FailurePolicyFailClosed
			d.logger.Warn("decision handler task did not complete successfully",
				zap.Uint64("handler_id", registered.id),
				zap.Bool("fail_closed", failClosed),
				zap.String("outcome", string(result.Outcome)),
				zap.String("error_type", fmt.Sprintf("%T", waitErr)),
			)
			if failClosed {
				return true
			}
			continue
		}
		if decisionCtx.Err() != nil && registered.failurePolicy == FailurePolicyFailClosed {
			return true
		}
		if handled.Load() {
			return true
		}
	}
	return false
}

func (d *Dispatcher) messageHookStateInterested(registered prioritizedHandler, chatID int64) (interested bool) {
	if registered.stateGate == nil {
		return true
	}
	defer func() {
		if r := recover(); r != nil {
			d.logger.Warn("message hook state gate panicked; failing open",
				zap.Uint64("handler_id", registered.id),
				zap.String("panic_type", fmt.Sprintf("%T", r)),
			)
			interested = true
		}
	}()
	return registered.stateGate(chatID)
}

func (d *Dispatcher) dispatchEventHandlersEnvelope(ctx context.Context, handlers []prioritizedHandler, message *core.MessageEnvelope, e tg.Entities, msg *tg.Message) {
	if len(handlers) == 0 || message == nil || msg == nil {
		return
	}
	client := d.taskClient()
	if client == nil {
		d.logger.Warn("observer execution unavailable", zap.String("error_code", "tasks_unavailable"))
		return
	}
	chatID := message.ChatID
	sharedDecision := core.GetMessageDecision(ctx)
	for _, registered := range handlers {
		registered := registered
		if !d.messageHookStateInterested(registered, chatID) {
			continue
		}
		d.inFlight.Add(1)
		owner := tasks.OwnerID("telegram:feature")
		class := tasks.PriorityNormal
		if registered.priority >= PriorityObservability {
			owner = "telegram:observability"
			class = tasks.PriorityBackground
		}
		if !registered.scope.IsZero() {
			owner = tasks.OwnerID(registered.scope.Owner)
		}
		_, err := client.Submit(ctx, tasks.WorkSpec{
			ID:               messageHookTaskID("hook", registered.id, message.Peer, msg.ID),
			Scope:            registered.scope,
			QuotaOwner:       owner,
			Pool:             "general",
			Class:            class,
			OrderingKey:      messageHookEventOrderingKey(registered.scope, message.Peer),
			ExecutionTimeout: 10 * time.Second,
			Handler: func(taskCtx context.Context) error {
				hookCtx := taskCtx
				if sharedDecision != nil {
					hookCtx = core.WithMessageDecision(taskCtx, sharedDecision)
				}
				_ = d.safeExecuteRegisteredInterceptor(hookCtx, registered, e, msg, message)
				return nil
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Debug("message hook admission rejected", zap.Uint64("handler_id", registered.id), zap.String("error_type", fmt.Sprintf("%T", err)))
		}
	}
}

func (d *Dispatcher) resolveDispatchChat(e tg.Entities, msg *tg.Message) *core.Chat {
	chat := &core.Chat{}
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		chat.ID = p.UserID
		chat.Type = "private"
		if u, ok := e.Users[p.UserID]; ok {
			chat.Username = u.Username
			chat.Title = u.FirstName + " " + u.LastName
			chat.AccessHash = u.AccessHash
		}
	case *tg.PeerChat:
		chat.ID = p.ChatID
		chat.Type = "group"
		if c, ok := e.Chats[p.ChatID]; ok {
			chat.Title = c.Title
		}
	case *tg.PeerChannel:
		chat.ID = p.ChannelID
		// Without channel metadata, fail closed as broadcast rather than
		// inventing group membership. Canonical hooks use the same default.
		chat.Type = "channel"
		if ch, ok := e.Channels[p.ChannelID]; ok {
			chat.Title = ch.Title
			chat.Username = ch.Username
			if ch.Megagroup {
				chat.Type = "supergroup"
			} else {
				chat.Type = "channel"
			}
			chat.AccessHash = ch.AccessHash
		}
	}
	return chat
}

func (d *Dispatcher) resolveDispatchPeer(ctx context.Context, e tg.Entities, msg *tg.Message) tg.InputPeerClass {
	r := d.getResolver()
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		if p.UserID == d.getSelfID() {
			return &tg.InputPeerSelf{}
		}
		if u, ok := e.Users[p.UserID]; ok && u.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		}
		if r != nil {
			if peer, _, err := r.ResolveUser(ctx, strconv.FormatInt(p.UserID, 10)); err == nil {
				if ip, ok := peer.(*tg.InputPeerUser); ok && ip.AccessHash != 0 {
					return ip
				}
			}
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if ch, ok := e.Channels[p.ChannelID]; ok && ch.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
		if r != nil {
			if peer, err := r.ResolveChat(ctx, fmt.Sprintf("-100%d", p.ChannelID)); err == nil {
				if ip, ok := peer.(*tg.InputPeerChannel); ok && ip.AccessHash != 0 {
					return ip
				}
			}
		}
	}
	return nil
}

func extractCoreMessage(msg *tg.Message) *core.Message {
	coreMsg := &core.Message{
		ID:         msg.ID,
		Text:       msg.Message,
		Date:       time.Unix(int64(msg.Date), 0),
		IsOutgoing: msg.Out,
		GroupedID:  msg.GroupedID,
		Entities:   msg.Entities,
	}
	if msg.Media != nil {
		coreMsg.Media = core.ExtractMediaFromTG(msg.Media)
		if coreMsg.Media != nil {
			coreMsg.MediaType = coreMsg.Media.Type
		}
	}
	if msg.ReplyTo != nil {
		if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			coreMsg.ReplyToID = header.ReplyToMsgID
			if header.ForumTopic || header.ReplyToTopID != 0 {
				if header.ReplyToTopID != 0 {
					coreMsg.TopicID = header.ReplyToTopID
				} else {
					coreMsg.TopicID = header.ReplyToMsgID
				}
			}
		}
	}
	return coreMsg
}

func telegramMessageIdentity(peer tg.PeerClass, messageID int) (string, bool) {
	if peer == nil || messageID <= 0 {
		return "", false
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		if p == nil || p.UserID == 0 {
			return "", false
		}
		return fmt.Sprintf("user:%d:%d", p.UserID, messageID), true
	case *tg.PeerChat:
		if p == nil || p.ChatID == 0 {
			return "", false
		}
		return fmt.Sprintf("chat:%d:%d", p.ChatID, messageID), true
	case *tg.PeerChannel:
		if p == nil || p.ChannelID == 0 {
			return "", false
		}
		return fmt.Sprintf("channel:%d:%d", p.ChannelID, messageID), true
	default:
		return "", false
	}
}

func invocationSenderID(msg *tg.Message, selfID int64) int64 {
	if msg == nil {
		return 0
	}
	if msg.Out {
		return selfID
	}
	if from, ok := msg.FromID.(*tg.PeerUser); ok && from != nil {
		return from.UserID
	}
	if msg.FromID == nil {
		if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer != nil {
			return peer.UserID
		}
	}
	return 0
}

// privateMessageSenderConsistent guards authorization before hook dispatch.
// Only Telegram's omitted private FromID may fall back to the dialog peer.
func privateMessageSenderConsistent(msg *tg.Message) bool {
	if msg == nil {
		return false
	}
	if msg.Out {
		return true
	}
	peer, isPrivate := msg.PeerID.(*tg.PeerUser)
	if !isPrivate {
		return true
	}
	if peer == nil || peer.UserID == 0 {
		return false
	}
	if msg.FromID == nil {
		return true
	}
	from, isUser := msg.FromID.(*tg.PeerUser)
	return isUser && from != nil && from.UserID == peer.UserID
}
