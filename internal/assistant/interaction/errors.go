package interaction

import (
	"errors"
	"strings"

	"github.com/gotd/td/tgerr"
)

const (
	// MaxPeerRecoveryAttempts defines the bounded retry limit for stale access hash recovery.
	MaxPeerRecoveryAttempts = 1
)

var (
	// ErrInvalidTarget occurs when an interaction is dispatched with an invalid or unresolvable target.
	ErrInvalidTarget = errors.New("assistant/interaction: target is invalid or missing required coordinates")

	// ErrMessageNotFound indicates the target message does not exist on Telegram.
	ErrMessageNotFound = errors.New("assistant/interaction: message not found")

	// ErrMessageAlreadyDeleted indicates the target message is already deleted or absent.
	ErrMessageAlreadyDeleted = errors.New("assistant/interaction: message already deleted")

	// ErrPeerResolution occurs when a peer cannot be mapped to an InputPeer.
	ErrPeerResolution = errors.New("assistant/interaction: unable to resolve peer")

	// ErrAccessHashStale occurs when an access hash is rejected by Telegram RPC.
	ErrAccessHashStale = errors.New("assistant/interaction: access hash is invalid or stale")

	// ErrCallbackExpired indicates the callback query is too old for Telegram to acknowledge.
	ErrCallbackExpired = errors.New("assistant/interaction: callback query expired")

	// ErrCallbackAlreadyAnswered indicates the callback query has already been answered.
	ErrCallbackAlreadyAnswered = errors.New("assistant/interaction: callback query already answered")

	// ErrUnauthorized occurs when the caller lacks permissions for the action.
	ErrUnauthorized = errors.New("assistant/interaction: unauthorized actor")

	// ErrUnsupportedTarget indicates an operation cannot be performed on the given target type (e.g. Delete on InlineTarget).
	ErrUnsupportedTarget = errors.New("assistant/interaction: operation not supported on this target kind")
)

// ClassifyRPCError maps raw Telegram MTProto RPC errors to domain interaction errors.
func ClassifyRPCError(err error) error {
	if err == nil {
		return nil
	}

	if tgerr.Is(err, "MESSAGE_ID_INVALID") || tgerr.Is(err, "MESSAGE_EMPTY") {
		return ErrMessageAlreadyDeleted
	}
	if tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
		return nil // Non-destructive; screen is already at desired state
	}
	if tgerr.Is(err, "CHANNEL_INVALID") || tgerr.Is(err, "CHANNEL_PRIVATE") || tgerr.Is(err, "CHAT_ID_INVALID") {
		return ErrInvalidTarget
	}
	if tgerr.Is(err, "ACCESS_HASH_INVALID") || tgerr.Is(err, "PEER_ID_INVALID") {
		return ErrAccessHashStale
	}
	if tgerr.Is(err, "QUERY_ID_INVALID") || tgerr.Is(err, "TIMEOUT") {
		return ErrCallbackExpired
	}

	// Fallback substring checks for wrapped errors
	msg := err.Error()
	if strings.Contains(msg, "MESSAGE_ID_INVALID") {
		return ErrMessageAlreadyDeleted
	}
	if strings.Contains(msg, "ACCESS_HASH_INVALID") {
		return ErrAccessHashStale
	}
	if strings.Contains(msg, "QUERY_ID_INVALID") {
		return ErrCallbackExpired
	}

	return err
}
