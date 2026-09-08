package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
)

func TestClassifyRPCError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want RPCErrorClass
	}{
		{name: "flood", err: tgerr.New(420, "FLOOD_WAIT_1"), want: RPCFloodWait},
		{name: "stale", err: tgerr.New(400, "PEER_ID_INVALID"), want: RPCStalePeer},
		{name: "permission", err: tgerr.New(400, "CHAT_WRITE_FORBIDDEN"), want: RPCPermission},
		{name: "auth", err: tgerr.New(401, "AUTH_KEY_UNREGISTERED"), want: RPCAuth},
		{name: "invalid", err: tgerr.New(400, "MESSAGE_ID_INVALID"), want: RPCInvalidRequest},
		{name: "unknown", err: errors.New("something else"), want: RPCUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyRPCError(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRetryRPCTransient(t *testing.T) {
	var attempts int
	err := RetryRPC(context.Background(), RPCPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    2 * time.Millisecond,
	}, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary timeout")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestRetryRPCDoesNotRetryPermission(t *testing.T) {
	var attempts int
	err := RetryRPC(context.Background(), DefaultRPCPolicy, func(context.Context) error {
		attempts++
		return tgerr.New(400, "CHAT_WRITE_FORBIDDEN")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestRetryRPCCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RetryRPC(ctx, DefaultRPCPolicy, func(context.Context) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
