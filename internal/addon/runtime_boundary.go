package addon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

type EventType string

const (
	EventMessageCreated  EventType = "message.created"
	EventMessageEdited   EventType = "message.edited"
	EventMessagesDeleted EventType = "messages.deleted"
	EventCallbackQuery   EventType = "callback.query"
	EventReactionUpdated EventType = "reaction.updated"
	EventInlineChosen    EventType = "inline.chosen"
)

var canonicalEventCapabilities = map[EventType]Capability{
	EventMessageCreated:  CapTelegramRead,
	EventMessageEdited:   CapTelegramRead,
	EventMessagesDeleted: CapTelegramRead,
	EventCallbackQuery:   CapTelegramRead,
	EventReactionUpdated: CapTelegramRead,
	EventInlineChosen:    CapTelegramRead,
}

func eventCapability(t EventType) (Capability, bool) {
	c, ok := canonicalEventCapabilities[t]
	return c, ok
}

type RuntimeOperation string

const (
	OperationEventHandle   RuntimeOperation = "event.handle"
	OperationCommandHandle RuntimeOperation = "command.handle"
)

var runtimeOperationCapabilities = map[RuntimeOperation]Capability{
	OperationEventHandle:   CapTelegramRead,
	OperationCommandHandle: "",
}

func RequiredCapability(operation RuntimeOperation) (Capability, bool) {
	c, ok := runtimeOperationCapabilities[operation]
	return c, ok
}

var ErrUnsupportedRuntimeOperation = errors.New("unsupported addon runtime operation")
var ErrUnsupportedEvent = errors.New("unsupported addon event")

type CanonicalEventEnvelope struct {
	Version       int             `json:"version"`
	Type          EventType       `json:"type"`
	Timestamp     time.Time       `json:"timestamp"`
	EventID       string          `json:"event_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type MessageCreatedPayload struct {
	ID         int       `json:"id"`
	ChatID     int64     `json:"chat_id"`
	SenderID   int64     `json:"sender_id,omitempty"`
	Text       string    `json:"text,omitempty"`
	Date       time.Time `json:"date,omitempty"`
	ReplyToID  int       `json:"reply_to_id,omitempty"`
	TopicID    int       `json:"topic_id,omitempty"`
	MediaType  string    `json:"media_type,omitempty"`
	Outgoing   bool      `json:"outgoing,omitempty"`
	GroupedID  int64     `json:"grouped_id,omitempty"`
}

type MessageEditedPayload struct {
	MessageID int    `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text,omitempty"`
}

type MessagesDeletedPayload struct {
	ChatID      int64 `json:"chat_id,omitempty"`
	PeerUnknown bool  `json:"peer_unknown,omitempty"`
	MessageIDs  []int `json:"message_ids"`
}

type CallbackQueryPayload struct {
	QueryID      int64  `json:"query_id"`
	UserID       int64  `json:"user_id"`
	ChatID       int64  `json:"chat_id,omitempty"`
	MessageID    int    `json:"message_id,omitempty"`
	Data         []byte `json:"data,omitempty"`
	Inline       bool   `json:"inline,omitempty"`
	ChatInstance int64  `json:"chat_instance,omitempty"`
}

type ReactionUpdatedPayload struct {
	MessageID int    `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	Reaction  string `json:"reaction,omitempty"`
}

type InlineChosenPayload struct {
	UserID   int64  `json:"user_id"`
	Query    string `json:"query,omitempty"`
	ResultID string `json:"result_id"`
}

type CommandInvocation struct {
	Version       int      `json:"version"`
	Command       string   `json:"command"`
	Args          []string `json:"args,omitempty"`
	RawArgs       string   `json:"raw_args,omitempty"`
	Source        string   `json:"source"`
	CorrelationID string   `json:"correlation_id,omitempty"`
}

type CommandResult struct {
	Disposition     execution.Disposition `json:"disposition,omitempty"`
	Code            string                `json:"code,omitempty"`
	Reply           string                `json:"reply,omitempty"`
	Error           string                `json:"error,omitempty"`
	RetryAfterMillis int64                 `json:"retry_after_ms,omitempty"`
}

func (r CommandResult) semantics() (execution.Semantics, error) {
	disposition := r.Disposition
	if disposition == "" {
		disposition = execution.DispositionSuccess
	}
	switch disposition {
	case execution.DispositionSuccess,
		execution.DispositionHandled,
		execution.DispositionRejected,
		execution.DispositionRetryable,
		execution.DispositionPermanent,
		execution.DispositionCancelled,
		execution.DispositionInternal:
	default:
		return execution.Semantics{}, fmt.Errorf("%w: unsupported command disposition %q", ErrUnsupportedRuntimeOperation, disposition)
	}
	if r.RetryAfterMillis < 0 {
		return execution.Semantics{}, fmt.Errorf("%w: retry_after_ms cannot be negative", ErrUnsupportedRuntimeOperation)
	}
	const maxRetryAfterMillis = int64((1<<63 - 1) / int64(time.Millisecond))
	if r.RetryAfterMillis > maxRetryAfterMillis {
		return execution.Semantics{}, fmt.Errorf("%w: retry_after_ms overflows duration", ErrUnsupportedRuntimeOperation)
	}
	return execution.Semantics{
		Disposition: disposition,
		Code:        r.Code,
		RetryAfter:  time.Duration(r.RetryAfterMillis) * time.Millisecond,
	}, nil
}

func (r CommandResult) executionError() error {
	semantics, err := r.semantics()
	if err != nil {
		return runtimeBoundaryError(err)
	}
	if semantics.IsSuccess() {
		return nil
	}
	message := r.Error
	if message == "" {
		message = semantics.Code
	}
	if message == "" {
		message = string(semantics.Disposition)
	}
	return execution.WithSemantics(errors.New(message), semantics)
}

func CanonicalizeEvent(event core.Event) (CanonicalEventEnvelope, error) {
	if event == nil {
		return CanonicalEventEnvelope{}, fmt.Errorf("%w: nil event", ErrUnsupportedEvent)
	}
	var eventType EventType
	var payload any
	switch e := event.(type) {
	case *core.MessageCreatedEvent:
		eventType = EventMessageCreated
		p := MessageCreatedPayload{ChatID: e.ChatID}
		if e.Message != nil {
			p.ID = e.Message.ID
			p.SenderID = e.Message.SenderID
			p.Text = e.Message.Text
			p.Date = e.Message.Date
			p.ReplyToID = e.Message.ReplyToID
			p.TopicID = e.Message.TopicID
			p.MediaType = e.Message.MediaType
			p.Outgoing = e.Message.IsOutgoing
			p.GroupedID = e.Message.GroupedID
		}
		payload = p
	case *core.MessageEditedEvent:
		eventType = EventMessageEdited
		payload = MessageEditedPayload{MessageID: e.MsgID, ChatID: e.ChatID, Text: e.Text}
	case *core.MessagesDeletedEvent:
		eventType = EventMessagesDeleted
		payload = MessagesDeletedPayload{ChatID: e.ChatID, PeerUnknown: e.PeerUnknown, MessageIDs: append([]int(nil), e.MsgIDs...)}
	case *core.CallbackQueryEvent:
		eventType = EventCallbackQuery
		payload = CallbackQueryPayload{
			QueryID: e.QueryID, UserID: e.UserID, ChatID: e.ChatID, MessageID: e.MsgID,
			Data: append([]byte(nil), e.Data...), Inline: e.IsInline(), ChatInstance: e.ChatInstance,
		}
	case *core.ReactionUpdatedEvent:
		eventType = EventReactionUpdated
		payload = ReactionUpdatedPayload{MessageID: e.MsgID, ChatID: e.ChatID, Reaction: e.Reaction}
	case *core.InlineResultChosenEvent:
		eventType = EventInlineChosen
		payload = InlineChosenPayload{UserID: e.UserID, Query: e.Query, ResultID: e.ResultID}
	default:
		return CanonicalEventEnvelope{}, fmt.Errorf("%w: %s", ErrUnsupportedEvent, event.Type())
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return CanonicalEventEnvelope{}, fmt.Errorf("marshal canonical addon event: %w", err)
	}
	meta := event.Meta()
	return CanonicalEventEnvelope{
		Version: AddonProtocolVersion, Type: eventType, Timestamp: event.Timestamp(),
		EventID: meta.ID, CorrelationID: meta.CorrelationID, CausationID: meta.CausationID,
		Payload: data,
	}, nil
}

func runtimeBoundaryError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionCancelled, Code: "addon_runtime_cancelled"})
	case errors.Is(err, context.DeadlineExceeded):
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionRetryable, Code: "addon_runtime_timeout"})
	case errors.Is(err, ErrUnauthorizedCapability):
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionRejected, Code: "addon_capability_denied"})
	case errors.Is(err, ErrAddonDisabled):
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionCancelled, Code: "addon_runtime_disabled"})
	case errors.Is(err, ErrUnsupportedRuntimeOperation), errors.Is(err, ErrUnsupportedEvent):
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionPermanent, Code: "addon_contract_invalid"})
	default:
		return execution.WithSemantics(err, execution.Semantics{Disposition: execution.DispositionInternal, Code: "addon_runtime_failure"})
	}
}
