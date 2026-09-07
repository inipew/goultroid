package callback

import (
	"errors"
)

var (
	// ErrPayloadTooLong occurs when callback data exceeds 64 bytes.
	ErrPayloadTooLong = errors.New("assistant/callback: payload exceeds 64 bytes limit")

	// ErrMalformedPayload occurs when callback data does not conform to required formatting.
	ErrMalformedPayload = errors.New("assistant/callback: malformed callback payload")

	// ErrEmptyField occurs when a required field in callback data is blank.
	ErrEmptyField = errors.New("assistant/callback: required field is empty")

	// ErrUnknownAction indicates no action handler was registered for the target action.
	ErrUnknownAction = errors.New("assistant/callback: no handler registered for action")

	// ErrSessionExpired indicates the menu instance has exceeded its lifespan.
	ErrSessionExpired = errors.New("assistant/callback: menu session expired")

	// ErrUnauthorized occurs when an unprivileged user triggers a protected action.
	ErrUnauthorized = errors.New("assistant/callback: unauthorized actor")

	// ErrDuplicateCallback indicates a callback query ID that has already been admitted for execution.
	// Telegram callback deliveries are treated as at-least-once, so duplicate query IDs must not
	// execute the same side effect twice.
	ErrDuplicateCallback = errors.New("assistant/callback: duplicate callback query")

	// ErrTimeout indicates a callback transaction exceeded its execution deadline.
	ErrTimeout = errors.New("assistant/callback: transaction execution timeout")
)
