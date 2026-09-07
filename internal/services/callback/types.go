package callback

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

var (
	// ErrInvalidCallbackData indicates the callback data is malformed or invalid version.
	ErrInvalidCallbackData = errors.New("invalid callback data format")
	// ErrHandlerNotFound indicates no handler is registered for the specified namespace.
	ErrHandlerNotFound = errors.New("no callback handler found for namespace")
	// ErrUnauthorized indicates the user pressing the button is not allowed to trigger this action.
	ErrUnauthorized = errors.New("unauthorized button interaction")
	// ErrStateExpired indicates the state associated with the callback opaque id has expired.
	ErrStateExpired = errors.New("callback state has expired")
	// ErrStateNotFound indicates no state exists for the opaque id.
	ErrStateNotFound = errors.New("callback state not found")
	// ErrStateConsumed indicates a single-use callback state has already been consumed.
	ErrStateConsumed = fmt.Errorf("%w: already consumed", ErrStateNotFound)
)

const (
	// CallbackVersion1 defines standard version 1 callback payload prefix.
	CallbackVersion1 = "v1"
	// MaxCallbackDataLen is Telegram's callback data limit (64 bytes).
	MaxCallbackDataLen = 64

	// Standard callback actions.
	ActionNoop     = "noop"
	ActionNav      = "nav"
	ActionToggle   = "toggle"
	ActionSet      = "set"
	ActionReset    = "reset"
	ActionBack     = "back"
	ActionClose    = "close"
	ActionSelect   = "select"
	ActionStep     = "step"
	ActionDuration = "dur"
)

// FailureCode categorizes standard callback processing rejections.
type FailureCode string

const (
	FailureCodeInvalidPayload  FailureCode = "INVALID_PAYLOAD"
	FailureCodeRateLimited     FailureCode = "RATE_LIMITED"
	FailureCodeSessionExpired  FailureCode = "SESSION_EXPIRED"
	FailureCodeUnauthorized    FailureCode = "UNAUTHORIZED"
	FailureCodeHandlerNotFound FailureCode = "HANDLER_NOT_FOUND"
	FailureCodeInternal        FailureCode = "INTERNAL_ERROR"
)

// CallbackFailure encapsulates failure details for answering queries and reporting metrics.
type CallbackFailure struct {
	Code        FailureCode
	UserAlert   string
	InternalErr error
	MetricTag   string
	IsAlert     bool
}

func (f *CallbackFailure) Error() string {
	if f == nil {
		return ""
	}
	if f.InternalErr != nil {
		return fmt.Sprintf("callback failure [%s]: %v", f.Code, f.InternalErr)
	}
	return fmt.Sprintf("callback failure [%s]: %s", f.Code, f.UserAlert)
}

func (f *CallbackFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.InternalErr
}

// CallbackHandlerOptions controls standard UX behaviour for a handler.
type CallbackHandlerOptions struct {
	// AutoAnswer when true (default) immediately answers the callback with empty toast
	// to clear Telegram loading state before handler execution.
	AutoAnswer bool
	// DefaultText and DefaultAlert are used for the immediate answer when AutoAnswer is true.
	// Empty DefaultText means silent ack.
	DefaultText  string
	DefaultAlert bool
}

// Handler represents a domain callback query processor for a specific namespace.
type Handler interface {
	Namespace() string
	HandleCallback(ctx *CallbackContext) error
}

// HandlerWithOptions is optionally implemented by handlers needing custom ack behaviour.
type HandlerWithOptions interface {
	Handler
	CallbackOptions() CallbackHandlerOptions
}

func handlerOptions(h Handler) CallbackHandlerOptions {
	if ho, ok := h.(HandlerWithOptions); ok {
		return ho.CallbackOptions()
	}
	// Default: immediate ack, silent
	return CallbackHandlerOptions{AutoAnswer: true}
}

// CallbackContext encapsulates the execution environment and metadata of an incoming callback query.
// ChatID and MsgID are deprecated (C): use Origin/Target. They are kept populated from Target for compatibility.
type CallbackContext struct {
	Ctx     context.Context
	QueryID int64
	UserID  int64
	// Deprecated: use Target.Peer / Target.MessageID. Kept populated from Target for backward compat.
	ChatID int64
	// Deprecated: use Target.MessageID or Target.InlineID.
	MsgID        int
	RawData      []byte
	Namespace    string
	Action       string
	OpaqueID     string
	State        any // Retrieved from StateStore if associated
	Service      core.TelegramServicer
	Origin       core.CallbackOrigin
	Target       core.CallbackTarget
	ChatInstance int64

	answered bool
}

// IsInline returns true when the callback originated from an inline message,
// using Target.IsInline() as the single source of truth.
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
	// Telegram answer length limit ~200 chars
	if len(text) > 200 {
		text = text[:200]
	}
	err := c.Service.AnswerCallbackQuery(c.Ctx, c.QueryID, text, alert)
	if err == nil {
		c.answered = true
	}
	return err
}

// Edit updates the text and optional reply markup of the message where the button was pressed.
// It dispatches to EditMessageMarkup for normal messages and EditInlineBotMessage for inline messages.
func (c *CallbackContext) Edit(text string, markup tg.ReplyMarkupClass) error {
	if c == nil {
		return fmt.Errorf("%w: callback context is nil", core.ErrInternal)
	}
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	// Bounded serialized payload: truncate to Telegram max (4096 chars)
	if len(text) > 4096 {
		text = text[:4096]
	}
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
// For inline messages it calls EditInlineBotMessageMarkup; for normal messages it calls EditMessageMarkupOnly.
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

// Delete deletes the originating message. For inline-origin callbacks it returns an error
// because inline messages have no normal peer/message and must be edited, not deleted.
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

// GetMessage fetches the originating message. For inline it returns ErrUnsupported.
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

// --- Standard UX helpers (Phase 3) ---

// ShowProgress edits the message to show intermediate progress. Keeps existing markup unless new one provided.
func (c *CallbackContext) ShowProgress(text string, markup tg.ReplyMarkupClass) error {
	if text == "" {
		text = "⏳ Processing..."
	} else if !strings.HasPrefix(text, "⏳") && !strings.HasPrefix(text, "⌛") {
		text = "⏳ " + text
	}
	return c.Edit(text, markup)
}

// ShowSuccess edits the message to show completion. Intended to be called after long-running work.
func (c *CallbackContext) ShowSuccess(text string, markup tg.ReplyMarkupClass) error {
	if text == "" {
		text = "✅ Done."
	}
	return c.Edit(text, markup)
}

// ShowError edits the message to show a user-safe error state plus optional toast.
// It sanitizes raw error text via core.IsUserSafeText before exposing.
func (c *CallbackContext) ShowError(userMsg string, err error, markup tg.ReplyMarkupClass) error {
	msg := userMsg
	if msg == "" {
		msg = "❌ Action failed."
	}
	if err != nil && core.IsUserSafeText(err.Error()) {
		// Prefer explicit userMsg; don't leak raw err unless safe and userMsg empty
		if userMsg == "" {
			msg = fmt.Sprintf("❌ %v", err)
		}
	}
	// Toast already consumed by AutoAnswer; error is durable via Edit
	return c.Edit(msg, markup)
}

// DisableButtons removes keyboard after single-use destructive action while keeping text.
func (c *CallbackContext) DisableButtons(text string) error {
	if text == "" {
		// Try to fetch current text via GetMessage for normal messages
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

// RemoveMarkup is alias for disabling buttons.
func (c *CallbackContext) RemoveMarkup(text string) error {
	return c.DisableButtons(text)
}

// AnswerError is convenience for handlers with AutoAnswer=false that need to show alert on failure.
func (c *CallbackContext) AnswerError(text string) error {
	if text == "" {
		text = "❌ Action failed."
	}
	return c.Answer(text, true)
}

// AnswerSuccess is convenience for toast on success when AutoAnswer=false.
func (c *CallbackContext) AnswerSuccess(text string) error {
	if text == "" {
		text = "✅ Done"
	}
	return c.Answer(text, false)
}

// EncodeCallbackData serializes namespace, action, and opaque id into standard versioned format:
// v1:<namespace>:<action>:<opaque-id>
func EncodeCallbackData(namespace, action, opaqueID string) []byte {
	b, _ := EncodeCallbackDataChecked(namespace, action, opaqueID)
	return b
}

// EncodeCallbackDataChecked validates and serializes; returns error if payload invalid.
func EncodeCallbackDataChecked(namespace, action, opaqueID string) ([]byte, error) {
	if err := validateCallbackField(namespace, "namespace"); err != nil {
		return nil, err
	}
	if err := validateCallbackField(action, "action"); err != nil {
		return nil, err
	}
	if opaqueID == "" {
		return nil, fmt.Errorf("%w: opaque id cannot be empty", ErrInvalidCallbackData)
	}
	if len(opaqueID) > 32 || !isHexID(opaqueID) {
		// opaqueID from StateStore is hex16, but allow up to 32 hex chars for future signed mode
		// for now enforce hex; "noop" is handled earlier by router, not via validation here
		if opaqueID != "noop" && !isValidOpaqueID(opaqueID) {
			return nil, fmt.Errorf("%w: invalid opaque id %q", ErrInvalidCallbackData, opaqueID)
		}
	}
	raw := fmt.Sprintf("%s:%s:%s:%s", CallbackVersion1, namespace, action, opaqueID)
	if len(raw) > MaxCallbackDataLen {
		return nil, fmt.Errorf("%w: callback data exceeds %d bytes (%d)", ErrInvalidCallbackData, MaxCallbackDataLen, len(raw))
	}
	return []byte(raw), nil
}

// ParseCallbackData deserializes callback payload into (namespace, action, opaqueID).
func ParseCallbackData(data []byte) (namespace, action, opaqueID string, err error) {
	if len(data) == 0 || len(data) > MaxCallbackDataLen {
		return "", "", "", ErrInvalidCallbackData
	}
	str := string(data)
	parts := strings.SplitN(str, ":", 4)
	if len(parts) != 4 || parts[0] != CallbackVersion1 {
		return "", "", "", ErrInvalidCallbackData
	}
	ns, act, oid := parts[1], parts[2], parts[3]
	if err := validateCallbackField(ns, "namespace"); err != nil {
		return "", "", "", err
	}
	if err := validateCallbackField(act, "action"); err != nil {
		return "", "", "", err
	}
	if oid == "" {
		return "", "", "", ErrInvalidCallbackData
	}
	if !isValidOpaqueID(oid) {
		return "", "", "", ErrInvalidCallbackData
	}
	return ns, act, oid, nil
}

func validateCallbackField(s, field string) error {
	if s == "" {
		return fmt.Errorf("%w: %s cannot be empty", ErrInvalidCallbackData, field)
	}
	if len(s) > 32 {
		return fmt.Errorf("%w: %s too long (%d > 32)", ErrInvalidCallbackData, field, len(s))
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: %s contains invalid character %q", ErrInvalidCallbackData, field, r)
	}
	return nil
}

func isHexID(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func isValidOpaqueID(s string) bool {
	if s == "noop" {
		return true
	}
	if len(s) > 32 {
		return false
	}
	// allow hex or base64url-like without padding for future; for now hex or alnum_-.
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// NewActionData constructs standard callback data encoded as v1:namespace:action:opaqueID.
func NewActionData(namespace, action, opaqueID string) ([]byte, error) {
	return EncodeCallbackDataChecked(namespace, action, opaqueID)
}
