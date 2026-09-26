package callback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

var (
	// ErrInvalidCallbackData marks malformed residual callback input handled by
	// the temporary compatibility Router retained until P1-F4.
	ErrInvalidCallbackData = errors.New("invalid callback data format")
	// ErrHandlerNotFound marks a non-a2 callback whose legacy feature authority
	// no longer exists. P1-F3 uses it for the explicit expired/unknown policy.
	ErrHandlerNotFound = errors.New("no callback handler found for namespace")
	// ErrHandlerRegistrationChanged is retained only with the legacy Handler
	// registration contract until P1-F4 removes that surface.
	ErrHandlerRegistrationChanged = fmt.Errorf("%w: callback handler registration changed", ErrHandlerNotFound)
	// ErrHandlerPanic marks a recovered legacy callback handler panic.
	ErrHandlerPanic = fmt.Errorf("%w: callback handler panic", core.ErrInternal)
)

const (
	// ActionNoop is the only residual control payload retained after P1-F3. It
	// is not a namespace protocol: raw "noop" only clears Telegram's spinner.
	ActionNoop = "noop"
)

// failureCode categorizes compatibility callback processing rejections.
type failureCode string

const (
	failureCodeInvalidPayload  failureCode = "INVALID_PAYLOAD"
	failureCodeRateLimited     failureCode = "RATE_LIMITED"
	failureCodeHandlerNotFound failureCode = "HANDLER_NOT_FOUND"
)

// callbackFailure carries internal rejection details for feedback and metrics.
type callbackFailure struct {
	Code        failureCode
	UserAlert   string
	InternalErr error
	MetricTag   string
	IsAlert     bool
}

func (f *callbackFailure) Error() string {
	if f == nil {
		return ""
	}
	if f.InternalErr != nil {
		return fmt.Sprintf("callback failure [%s]: %v", f.Code, f.InternalErr)
	}
	return fmt.Sprintf("callback failure [%s]: %s", f.Code, f.UserAlert)
}

func (f *callbackFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.InternalErr
}

// CallbackHandlerOptions controls UX behaviour for the legacy Handler contract
// retained only until P1-F4. State requirements were removed in P1-F3.
type CallbackHandlerOptions struct {
	// AutoAnswer when true immediately answers the callback with the configured
	// toast before handler execution.
	AutoAnswer bool
	// DefaultText and DefaultAlert are used by the immediate answer.
	DefaultText  string
	DefaultAlert bool
}

// Handler is the legacy plugin callback contract retained until P1-F4. P1-F3
// no longer admits namespace payloads to registered handlers.
type Handler interface {
	Namespace() string
	HandleCallback(ctx *CallbackContext) error
}

// HandlerWithOptions is optionally implemented by legacy handlers needing
// custom acknowledgement behaviour.
type HandlerWithOptions interface {
	Handler
	CallbackOptions() CallbackHandlerOptions
}

func handlerOptions(h Handler) CallbackHandlerOptions {
	if ho, ok := h.(HandlerWithOptions); ok {
		return ho.CallbackOptions()
	}
	return CallbackHandlerOptions{AutoAnswer: true}
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// CallbackContext is the transport convenience context retained for the legacy
// Handler API until P1-F4. P1-F3 removed namespace/action/opaque/state protocol
// fields; canonical interactive state belongs to interaction.Runtime (a2).
type CallbackContext struct {
	Ctx     context.Context
	QueryID int64
	UserID  int64
	// Deprecated: use Target.Peer / Target.MessageID. Kept for compatibility.
	ChatID int64
	// Deprecated: use Target.MessageID or Target.InlineID.
	MsgID        int
	RawData      []byte
	Service      core.TelegramServicer
	Origin       core.CallbackOrigin
	Target       core.CallbackTarget
	ChatInstance int64

	answered bool
}

// IsInline returns true when the callback originated from an inline message.
func (c *CallbackContext) IsInline() bool {
	if c == nil {
		return false
	}
	return c.Target.IsInline() || c.Origin == core.CallbackOriginInline
}

// IsAnswered returns true if an answer has already been sent for this query.
func (c *CallbackContext) IsAnswered() bool {
	return c != nil && c.answered
}

// Answer sends a response to the callback query to dismiss the loading spinner and optionally show a toast/alert.
func (c *CallbackContext) Answer(text string, alert bool) error {
	if c == nil {
		return fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	text = truncateUTF8Bytes(text, 200)
	err := c.Service.AnswerCallbackQuery(c.Ctx, c.QueryID, text, alert)
	if err == nil {
		c.answered = true
	}
	return err
}

// Edit updates the text and optional reply markup of the message where the button was pressed.
func (c *CallbackContext) Edit(text string, markup tg.ReplyMarkupClass) error {
	if c == nil {
		return fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	text = truncateUTF8Bytes(text, 4096)
	if c.IsInline() {
		if c.Target.InlineID == nil {
			return fmt.Errorf("%w: inline message id is missing", core.ErrInternal)
		}
		return c.Service.EditInlineBotMessage(c.Ctx, c.Target.InlineID, text, markup)
	}
	if c.Target.Peer == nil {
		return fmt.Errorf("%w: peer is missing for normal message edit", core.ErrInternal)
	}
	return c.Service.EditMessageMarkup(c.Ctx, c.Target.Peer, c.Target.MessageID, text, markup)
}

// EditText is a convenience wrapper for Edit without changing markup.
func (c *CallbackContext) EditText(text string) error {
	return c.Edit(text, nil)
}

// EditMarkup updates only the reply markup, preserving the message text on Telegram server.
func (c *CallbackContext) EditMarkup(markup tg.ReplyMarkupClass) error {
	if c == nil {
		return fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	if c.IsInline() {
		if c.Target.InlineID == nil {
			return fmt.Errorf("%w: inline message id is missing", core.ErrInternal)
		}
		return c.Service.EditInlineBotMessageMarkup(c.Ctx, c.Target.InlineID, markup)
	}
	if c.Target.Peer == nil {
		return fmt.Errorf("%w: peer is missing for normal message edit", core.ErrInternal)
	}
	return c.Service.EditMessageMarkupOnly(c.Ctx, c.Target.Peer, c.Target.MessageID, markup)
}

// Delete deletes the originating message. Inline targets must be edited instead.
func (c *CallbackContext) Delete() error {
	if c == nil {
		return fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	if c.IsInline() {
		return fmt.Errorf("%w: cannot delete inline-origin message, use Edit instead", core.ErrUnsupported)
	}
	if c.Target.Peer == nil {
		return fmt.Errorf("%w: peer is missing for delete", core.ErrInternal)
	}
	return c.Service.DeleteMessage(c.Ctx, c.Target.Peer, []int{c.Target.MessageID})
}

// GetMessage fetches the originating message. Inline targets are unsupported.
func (c *CallbackContext) GetMessage() (*tg.Message, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return nil, fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	if c.IsInline() {
		return nil, fmt.Errorf("%w: GetMessage not supported for inline-origin callbacks", core.ErrUnsupported)
	}
	if c.Target.Peer == nil {
		return nil, fmt.Errorf("%w: peer is missing for GetMessage", core.ErrInternal)
	}
	return c.Service.GetMessage(c.Ctx, c.Target.Peer, c.Target.MessageID)
}

// ShowProgress edits the message to show intermediate progress.
func (c *CallbackContext) ShowProgress(text string, markup tg.ReplyMarkupClass) error {
	if text == "" {
		text = "⏳ Processing..."
	} else if !strings.HasPrefix(text, "⏳") && !strings.HasPrefix(text, "⌛") {
		text = "⏳ " + text
	}
	return c.Edit(text, markup)
}

// ShowSuccess edits the message to show completion.
func (c *CallbackContext) ShowSuccess(text string, markup tg.ReplyMarkupClass) error {
	if text == "" {
		text = "✅ Done."
	}
	return c.Edit(text, markup)
}

// ShowError edits the message to a user-safe error state.
func (c *CallbackContext) ShowError(userMsg string, err error, markup tg.ReplyMarkupClass) error {
	msg := userMsg
	if msg == "" {
		msg = "❌ Action failed."
	}
	if err != nil && core.IsUserSafeText(err.Error()) && userMsg == "" {
		msg = fmt.Sprintf("❌ %v", err)
	}
	return c.Edit(msg, markup)
}

// DisableButtons removes keyboard after a terminal action while keeping text.
func (c *CallbackContext) DisableButtons(text string) error {
	if text == "" {
		if !c.IsInline() && c.Target.Peer != nil && c.Service != nil {
			if msg, err := c.GetMessage(); err == nil && msg != nil {
				text = msg.Message
			}
		}
		if text == "" {
			text = "✅ Action completed."
		}
	}
	return c.Edit(text, nil)
}

// RemoveMarkup is an alias for disabling buttons.
func (c *CallbackContext) RemoveMarkup(text string) error {
	return c.DisableButtons(text)
}

// AnswerError shows an alert for handlers with AutoAnswer=false.
func (c *CallbackContext) AnswerError(text string) error {
	if text == "" {
		text = "❌ Action failed."
	}
	return c.Answer(text, true)
}

// AnswerSuccess shows a success toast for handlers with AutoAnswer=false.
func (c *CallbackContext) AnswerSuccess(text string) error {
	if text == "" {
		text = "✅ Done"
	}
	return c.Answer(text, false)
}
