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
)

const (
	// CallbackVersion1 defines standard version 1 callback payload prefix.
	CallbackVersion1 = "v1"
)

// Handler represents a domain callback query processor for a specific namespace.
type Handler interface {
	Namespace() string
	HandleCallback(ctx *CallbackContext) error
}

// CallbackContext encapsulates the execution environment and metadata of an incoming callback query.
type CallbackContext struct {
	Ctx       context.Context
	QueryID   int64
	UserID    int64
	ChatID    int64
	MsgID     int
	RawData   []byte
	Namespace string
	Action    string
	OpaqueID  string
	State     any // Retrieved from StateStore if associated
	Service   core.TelegramServicer

	answered bool
}

// Answer sends a response to the callback query to dismiss the loading spinner and optionally show a toast/alert.
func (c *CallbackContext) Answer(text string, alert bool) error {
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	c.answered = true
	return c.Service.AnswerCallbackQuery(c.Ctx, c.QueryID, text, alert)
}

// Edit updates the text and optional reply markup of the message where the button was pressed.
func (c *CallbackContext) Edit(text string, markup tg.ReplyMarkupClass) error {
	if c.Service == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	peer := &tg.InputPeerChat{ChatID: c.ChatID}
	if c.ChatID > 0 {
		peer = &tg.InputPeerChat{ChatID: c.ChatID}
	}
	return c.Service.EditMessageMarkup(c.Ctx, peer, c.MsgID, text, markup)
}

// IsAnswered returns true if an answer has already been sent for this query.
func (c *CallbackContext) IsAnswered() bool {
	return c.answered
}

// EncodeCallbackData serializes namespace, action, and opaque id into standard versioned format:
// v1:<namespace>:<action>:<opaque-id>
func EncodeCallbackData(namespace, action, opaqueID string) []byte {
	return []byte(fmt.Sprintf("%s:%s:%s:%s", CallbackVersion1, namespace, action, opaqueID))
}

// ParseCallbackData deserializes callback payload into (namespace, action, opaqueID).
func ParseCallbackData(data []byte) (namespace, action, opaqueID string, err error) {
	str := string(data)
	parts := strings.SplitN(str, ":", 4)
	if len(parts) != 4 || parts[0] != CallbackVersion1 {
		return "", "", "", ErrInvalidCallbackData
	}
	return parts[1], parts[2], parts[3], nil
}
