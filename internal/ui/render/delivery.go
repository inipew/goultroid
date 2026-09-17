package render

import (
	"errors"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"go.uber.org/zap"
)

// DeliveryMode controls whether delivery attempts to edit the triggering message or send a reply.
type DeliveryMode uint8

const (
	// DeliveryAuto automatically edits if triggered as an outgoing userbot command, otherwise replies.
	DeliveryAuto DeliveryMode = iota
	// DeliveryEdit forces an edit of the current/last response message.
	DeliveryEdit
	// DeliveryReply forces sending a new reply message.
	DeliveryReply
)

// DeliveryConfig configures presentation delivery behavior.
type DeliveryConfig struct {
	Mode   DeliveryMode
	Logger *zap.Logger
}

// DeliveryOption mutates DeliveryConfig.
type DeliveryOption func(*DeliveryConfig)

// WithDeliveryMode sets an explicit delivery mode.
func WithDeliveryMode(mode DeliveryMode) DeliveryOption {
	return func(c *DeliveryConfig) {
		c.Mode = mode
	}
}

// WithLogger sets the logger to record markup delivery failures.
func WithLogger(logger *zap.Logger) DeliveryOption {
	return func(c *DeliveryConfig) {
		c.Logger = logger
	}
}

// DeliverHandoff delivers a presentation handoff result to Telegram via the provided context.
// It first attempts to deliver interactive reply markup. If Telegram rejects the markup
// (for example, on userbot accounts which cannot attach bot inline keyboards), it logs the failure
// without token leakage and falls back to safe text with a clickable assistant deep link.
func DeliverHandoff(ctx *core.Context, res presentation.HandoffResult, title, message string, opts ...DeliveryOption) error {
	if ctx == nil {
		return errors.New("context is nil")
	}

	cfg := DeliveryConfig{Mode: DeliveryAuto}
	for _, opt := range opts {
		opt(&cfg)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.L()
	}

	screen := res.AsScreen(title, message)
	text, markup := ToTelegram(screen)

	shouldEdit := false
	switch cfg.Mode {
	case DeliveryEdit:
		shouldEdit = true
	case DeliveryReply:
		shouldEdit = false
	case DeliveryAuto:
		if !ctx.IsAssistant() && (ctx.LastResponseID > 0 || (ctx.Message != nil && ctx.Message.ID > 0)) {
			shouldEdit = true
		}
	}

	// Regular user accounts cannot reliably attach bot reply markup. Telegram may
	// even acknowledge the RPC while silently dropping the keyboard, so waiting
	// for an error is not a sufficient fallback trigger. In automatic mode,
	// userbot surfaces therefore use the actionable text representation directly.
	attemptMarkup := markup != nil
	if cfg.Mode == DeliveryAuto && !ctx.IsAssistant() {
		attemptMarkup = false
	}

	if attemptMarkup {
		var markupErr error
		if shouldEdit {
			markupErr = ctx.EditMarkup(text, markup)
		} else {
			markupErr = ctx.ReplyMarkup(text, markup)
		}

		if markupErr == nil {
			return nil
		}

		// Log failure without exposing token!
		logger.Warn("presentation markup delivery rejected, falling back to clickable hyperlink",
			zap.String("command", ctx.Command),
			zap.String("mode", res.Mode.String()),
			zap.Error(markupErr),
		)
	}

	// Fallback text with explicit clickable URL
	fallbackText := res.FallbackText(title, message)
	if fallbackText == "" {
		fallbackText = text
	}

	if shouldEdit {
		if err := ctx.Edit(fallbackText); err == nil {
			return nil
		}
		// If edit fails (e.g., deleted message), fall back to reply
		return ctx.Reply(fallbackText)
	}

	return ctx.Reply(fallbackText)
}
