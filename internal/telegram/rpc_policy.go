package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
)

type RPCErrorClass uint8

const (
	RPCUnknown RPCErrorClass = iota
	RPCTransient
	RPCFloodWait
	RPCStalePeer
	RPCPermission
	RPCAuth
	RPCInvalidRequest
)

type RPCPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

var DefaultRPCPolicy = RPCPolicy{
	MaxAttempts: 3,
	BaseDelay:   250 * time.Millisecond,
	MaxDelay:    30 * time.Second,
}

func ClassifyRPCError(err error) RPCErrorClass {
	if err == nil {
		return RPCUnknown
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return RPCFloodWait
	}
	if tgerr.Is(err, "PEER_ID_INVALID", "USER_ID_INVALID", "CHANNEL_INVALID", "CHAT_ID_INVALID", "INPUT_USER_DEACTIVATED") {
		return RPCStalePeer
	}
	if tgerr.Is(err, "CHAT_ADMIN_REQUIRED", "CHAT_WRITE_FORBIDDEN", "USER_BANNED_IN_CHANNEL", "USER_BANNED", "CHANNEL_PRIVATE", "USER_NOT_MUTUAL_CONTACT", "RIGHT_FORBIDDEN") {
		return RPCPermission
	}
	if tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN") {
		return RPCAuth
	}
	if tgerr.Is(err, "MESSAGE_ID_INVALID", "MESSAGE_TOO_LONG", "USERNAME_INVALID", "USERNAME_NOT_OCCUPIED", "USER_NOT_FOUND") {
		return RPCInvalidRequest
	}
	text := strings.ToUpper(err.Error())
	if strings.Contains(text, "TIMEOUT") || strings.Contains(text, "CONNECTION RESET") || strings.Contains(text, "TEMPORAR") || strings.Contains(text, "EOF") {
		return RPCTransient
	}
	return RPCUnknown
}

func IsStalePeerError(err error) bool { return ClassifyRPCError(err) == RPCStalePeer }

func RetryRPC(ctx context.Context, policy RPCPolicy, op func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = 250 * time.Millisecond
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = 30 * time.Second
	}

	var last error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(ctx)
		if err == nil {
			return nil
		}
		last = err

		switch ClassifyRPCError(err) {
		case RPCFloodWait:
			wait, _ := tgerr.AsFloodWait(err)
			if wait <= 0 {
				return err
			}
			if wait > policy.MaxDelay {
				return core.NewRateLimitError(wait, err)
			}
			if err := sleepContext(ctx, wait); err != nil {
				return err
			}
		case RPCTransient:
			if attempt == policy.MaxAttempts {
				break
			}
			delay := policy.BaseDelay * time.Duration(1<<(attempt-1))
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
			if err := sleepContext(ctx, delay); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return last
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func WrapRPCError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

func IsPermanentRPCError(err error) bool {
	c := ClassifyRPCError(err)
	return c == RPCPermission || c == RPCAuth || c == RPCInvalidRequest
}
