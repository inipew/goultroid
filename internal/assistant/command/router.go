package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	unifiedRegistry CommandSource
	adapter         *UnifiedCommandAdapter
	ownerID         int64
	sudoGetter      func() []int64
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
	if r.adapter != nil {
		r.adapter.SetOwner(ownerID, sudoGetter)
	}
}

// OwnerID returns the configured owner ID.
func (r *Router) OwnerID() int64 {
	return r.ownerID
}

// SetUnifiedRegistry attaches the unified command registry to enable plugin commands on Assistant.
func (r *Router) SetUnifiedRegistry(reg CommandSource) {
	r.unifiedRegistry = reg
	if reg != nil {
		adapter := NewUnifiedCommandAdapter(reg, r.logger)
		adapter.SetOwner(r.ownerID, r.sudoGetter)
		r.adapter = adapter
	} else {
		r.adapter = nil
	}
}

// UnifiedRegistry returns the attached unified command source.
func (r *Router) UnifiedRegistry() CommandSource {
	return r.unifiedRegistry
}

// Register attaches a handler to a command name (e.g. "/start", "/ping").
func (r *Router) Register(cmd string, handler Handler) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	r.handlers[cmd] = handler
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

	// 1. Primary: Unified command adapter for plugins declaring SurfaceAssistant (§0, §10, §31 bug16_1)
	if r.adapter != nil {
		handled, err := r.adapter.Execute(ctx, cmdNameClean, fields[1:], senderID, peer, inter)
		if handled {
			return err
		}
	}

	// 2. Fallback: Assistant-specific presentation handlers (e.g. /start dashboard menu)
	if handler, ok := r.handlers[cmdRaw]; ok {
		r.logger.Debug("assistant: executing local command",
			zap.String("command", cmdRaw),
			zap.Int64("sender_id", senderID),
		)
		return handler(cmdCtx)
	}

	return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
}
