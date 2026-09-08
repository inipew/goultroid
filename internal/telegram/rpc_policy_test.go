package telegram

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyRPCError(t *testing.T) {
	cases := []struct { name, err string; want RPCErrorClass }{
		{"flood", "FLOOD_WAIT_12", RPCFloodWait},
		{"slowmode", "SLOWMODE_WAIT_4", RPCFloodWait},
		{"peer", "PEER_ID_INVALID", RPCStalePeer},
		{"permission", "CHAT_ADMIN_REQUIRED", RPCPermission},
		{"auth", "AUTH_KEY_UNREGISTERED", RPCAuth},
		{"invalid", "MESSAGE_ID_INVALID", RPCInvalidRequest},
		{"transient", "connection reset by peer", RPCTransient},
	}
	for _, tc := range cases { t.Run(tc.name, func(t *testing.T) { if got := ClassifyRPCError(errors.New(tc.err)); got != tc.want { t.Fatalf("got %v, want %v", got, tc.want) } }) }
}

func TestFloodWaitDuration(t *testing.T) {
	got, ok := FloodWaitDuration(errors.New("FLOOD_WAIT_17"))
	if !ok || got != 17*time.Second { t.Fatalf("got %v, %v", got, ok) }
}

func TestRPCPolicyRetriesTransient(t *testing.T) {
	p := RPCPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	attempts := 0
	err := p.Do(context.Background(), func(context.Context) error { attempts++; if attempts < 3 { return errors.New("temporary unavailable") }; return nil }, nil)
	if err != nil { t.Fatal(err) }
	if attempts != 3 { t.Fatalf("attempts = %d", attempts) }
}

func TestRPCPolicyStopsOnPermission(t *testing.T) {
	attempts := 0
	err := DefaultRPCPolicy.Do(context.Background(), func(context.Context) error { attempts++; return errors.New("CHAT_ADMIN_REQUIRED") }, nil)
	if err == nil { t.Fatal("expected error") }
	if attempts != 1 { t.Fatalf("permission error retried %d times", attempts) }
}
