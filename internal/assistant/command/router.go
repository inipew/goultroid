package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

var (
	// ErrUnknownCommand indicates no registered handler matched the requested command.
	ErrUnknownCommand = errors.New("assistant/command: unknown command")
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

// Reply sends a formatted response to the command originator.
func (c *Context) Reply(text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if c.Interaction == nil || c.Peer == nil {
		return nil, interaction.ErrInvalidTarget
	}
	return c.Interaction.SendMessage(c.Ctx, c.Peer, text, markup)
}

// Handler defines the function signature for an assistant bot command handler.
type Handler func(c *Context) error

// CommandSource defines capability to query commands for a surface.
type CommandSource interface {
	FindForSurface(name string, source execution.Source) (core.Command, bool)
	CommandsForSurface(source execution.Source) []core.Command
}

// Router dispatches incoming bot commands to registered handlers.
type Router struct {
	handlers        map[string]Handler
	coreRouter      *core.Router
	unifiedRegistry CommandSource
	ownerID         int64
	sudoGetter      func() []int64
	metrics         core.MetricsCollector
	logger          *zap.Logger
}

// NewRouter creates an initialized command Router.
func NewRouter(logger *zap.Logger) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Router{
		handlers: make(map[string]Handler),
		logger:   logger,
	}
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

// SetUnifiedRegistry attaches a command source.
func (r *Router) SetUnifiedRegistry(reg CommandSource) {
	r.unifiedRegistry = reg
	if cr, ok := reg.(*core.Router); ok {
		r.coreRouter = cr
	}
}

// UnifiedRegistry returns the attached unified command source.
func (r *Router) UnifiedRegistry() CommandSource {
	if r.coreRouter != nil {
		return r.coreRouter
	}
	return r.unifiedRegistry
}

// Register attaches a handler to a command name (e.g. "/start").
func (r *Router) Register(cmd string, handler Handler) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	r.handlers[cmd] = handler
}

func (r *Router) findCommand(name string) (core.Command, bool) {
	if r.coreRouter != nil {
		return r.coreRouter.Find(name)
	}
	if r.unifiedRegistry != nil {
		return r.unifiedRegistry.FindForSurface(name, execution.SourceAssistant)
	}
	return core.Command{}, false
}

// Dispatch parses the message text, extracts the command, and invokes the matching handler.
func (r *Router) Dispatch(ctx context.Context, senderID int64, peer tg.InputPeerClass, messageText string, inter interaction.MessageInteraction) error {
	fields := strings.Fields(strings.TrimSpace(messageText))
	if len(fields) == 0 {
		return nil
	}

	cmdRaw := strings.ToLower(fields[0])
	if !strings.HasPrefix(cmdRaw, "/") {
		return nil // Not a bot command
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

	// 1. Primary: Canonical command lookup from core.Router (or fallback unified source)
	if cmd, ok := r.findCommand(cmdNameClean); ok && cmd.Handler != nil {
		if !cmd.IsAvailableOn(execution.SourceAssistant) {
			return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
		}

		// Permission check
		isOwner := r.ownerID != 0 && senderID == r.ownerID
		isSudo := isOwner
		var sudoList []int64
		if r.sudoGetter != nil {
			sudoList = r.sudoGetter()
			for _, s := range sudoList {
				if s == senderID {
					isSudo = true
					break
				}
			}
		}

		switch cmd.Permission {
		case core.PermissionOwner:
			if !isOwner {
				r.logger.Warn("assistant: permission denied for command",
					zap.String("command", cmdNameClean),
					zap.Int64("sender_id", senderID),
				)
				if inter != nil {
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
				if inter != nil {
					_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command requires sudo privileges.</i>", nil)
				}
				return nil
			}
		}

		r.logger.Debug("assistant: executing canonical command",
			zap.String("command", cmdNameClean),
			zap.Int64("sender_id", senderID),
		)

		chatID := extractChatIDFromInputPeer(peer)
		if chatID == 0 {
			chatID = senderID
		}

		perms := core.NewPermissions(r.ownerID, sudoList)
		principal := &core.Principal{
			UserID:  senderID,
			IsOwner: isOwner,
			IsSudo:  isSudo,
		}

		coreCtx := &core.Context{
			Ctx:           ctx,
			CorrelationID: fmt.Sprintf("asst-%d-%d", senderID, time.Now().UnixNano()),
			Source:        core.ExecutionAssistant,
			Command:       cmdNameClean,
			Args:          fields[1:],
			RawArgs:       strings.Join(fields[1:], " "),
			PeerID:        peer,
			Message:       &core.Message{SenderID: senderID, Text: "/" + cmdNameClean + " " + strings.Join(fields[1:], " ")},
			Sender:        &core.User{ID: senderID},
			Chat:          &core.Chat{ID: chatID},
			Perms:         perms,
			Principal:     principal,
			Svc:           &assistantServicerAdapter{inter: inter},
		}

		start := time.Now()
		err := cmd.Handler(coreCtx)
		if r.metrics != nil {
			r.metrics.RecordCommand(cmdNameClean, time.Since(start), err)
		}
		return err
	}

	// 2. Fallback: Assistant-specific presentation handlers (e.g. /start dashboard menu)
	if handler, ok := r.handlers[cmdRaw]; ok {
		r.logger.Debug("assistant: executing local command",
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

	return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
}
