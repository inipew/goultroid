package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
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

// Router dispatches incoming bot commands to registered handlers.
type Router struct {
	handlers map[string]Handler
	logger   *zap.Logger
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

	handler, ok := r.handlers[cmdRaw]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownCommand, cmdRaw)
	}

	cmdCtx := &Context{
		Ctx:         ctx,
		SenderID:    senderID,
		Peer:        peer,
		Command:     cmdRaw,
		Args:        fields[1:],
		Interaction: inter,
	}

	r.logger.Debug("assistant: executing command",
		zap.String("command", cmdRaw),
		zap.Int64("sender_id", senderID),
	)

	return handler(cmdCtx)
}
