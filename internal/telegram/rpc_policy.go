package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tgerr"
)

type RPCErrorClass uint8

const (
	RPCUnknown RPCErrorClass = iota
	RPCSuccess
	RPCTransient
	RPCFloodWait
	RPCStalePeer
	RPCPermission
	RPCAuth
	RPCInvalidRequest
)

func (c RPCErrorClass) String() string {
	switch c {
	case RPCSuccess:
		return "success"
	case RPCTransient:
		return "transient"
	case RPCFloodWait:
		return "flood_wait"
	case RPCStalePeer:
		return "stale_peer"
	case RPCPermission:
		return "permission"
	case RPCAuth:
		return "auth"
	case RPCInvalidRequest:
		return "invalid_request"
	default:
		return "unknown"
	}
}

type RPCPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func defaultRPCPolicy() RPCPolicy {
	return RPCPolicy{
		MaxAttempts: 3,
		BaseDelay:   250 * time.Millisecond,
		MaxDelay:    30 * time.Second,
	}
}

// DefaultRPCPolicy is retained for compatibility only. Production runtime code
// must use the shared RPCExecutor; compatibility callers should prefer an
// explicit RPCPolicy instead of mutating this package-global value.
var DefaultRPCPolicy = defaultRPCPolicy()

func ClassifyRPCError(err error) RPCErrorClass {
	if err == nil {
		return RPCSuccess
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
	if tgerr.Is(err, "RPC_CALL_FAIL") {
		return RPCTransient
	}
	text := strings.ToUpper(err.Error())
	if strings.Contains(text, "TIMEOUT") || strings.Contains(text, "CONNECTION RESET") || strings.Contains(text, "TEMPORAR") || strings.Contains(text, "EOF") {
		return RPCTransient
	}
	return RPCUnknown
}

func IsStalePeerError(err error) bool { return ClassifyRPCError(err) == RPCStalePeer }

// RetryRPC is a compatibility helper for legacy tests/adapters. Production
// runtime code must use the shared RPCExecutor so limiter, metrics, FloodWait
// penalties, and retry state remain process-wide.
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

	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter: NewHierarchicalRPCLimiter(DefaultHierarchicalLimiterConfig()),
		DefaultPolicy: RetryPolicy{
			MaxAttempts:        policy.MaxAttempts,
			BaseDelay:          policy.BaseDelay,
			MaxDelay:           policy.MaxDelay,
			MaxElapsed:         time.Duration(policy.MaxAttempts+1) * (policy.MaxDelay + policy.BaseDelay),
			InlineFloodWaitMax: policy.MaxDelay,
			JitterFraction:     0.0,
		},
	})
	if err != nil {
		return err
	}

	err = exec.Do(ctx, RPCMeta{
		Method: "legacy.RetryRPC",
		Kind:   RPCReadOnly,
	}, op)
	if err != nil {
		var failure *RPCFailure
		if errors.As(err, &failure) {
			return failure.Err
		}
		return err
	}
	return nil
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
