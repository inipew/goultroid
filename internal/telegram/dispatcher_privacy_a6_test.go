package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func a6AssertNoLogPayload(t *testing.T, logs *observer.ObservedLogs, secret string) {
	t.Helper()
	entries := logs.All()
	if len(entries) == 0 {
		t.Fatal("expected a diagnostic log")
	}
	for _, entry := range entries {
		if strings.Contains(entry.Message, secret) || strings.Contains(fmt.Sprint(entry.ContextMap()), secret) {
			t.Fatalf("diagnostic leaked message content: %s %v", entry.Message, entry.ContextMap())
		}
	}
}

func TestA6MalformedCommandDoesNotLogPrivateMessageContent(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
	}{
		{"unclosed quote", ".ping \"private-secret-quote", "unclosed_quote"},
		{"trailing escape", ".ping private-secret-escape\\", "trailing_escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coreLogger, observed := observer.New(zap.DebugLevel)
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(100, nil), nil, zap.New(coreLogger))
			msg := &tg.Message{ID: 44, PeerID: &tg.PeerUser{UserID: 123}, FromID: &tg.PeerUser{UserID: 123}, Message: tc.body}
			if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
				t.Fatal(err)
			}
			a6AssertNoLogPayload(t, observed, "private-secret")
			entries := observed.All()
			if len(entries) != 1 || entries[0].Message != "command parse syntax error" {
				t.Fatalf("unexpected parse log: %v", entries)
			}
			fields := entries[0].ContextMap()
			if fields["reason"] != tc.reason {
				t.Fatalf("missing safe reason %q: %v", tc.reason, fields)
			}
			if _, ok := fields["text"]; ok {
				t.Fatalf("message text field must not be logged: %v", fields)
			}
			if _, ok := fields["error"]; ok {
				t.Fatalf("raw parser errors must not be logged: %v", fields)
			}
		})
	}
}

func TestA6MessageHookErrorAndPanicAreRedactedWithoutChangingPolicy(t *testing.T) {
	const secret = "private-message-do-not-log"
	for _, tc := range []struct {
		name        string
		policy      HandlerFailurePolicy
		run         func(context.Context) error
		wantHandled bool
	}{
		{
			name:        "security error remains fail closed",
			policy:      FailurePolicyFailClosed,
			run:         func(context.Context) error { return errors.New(secret) },
			wantHandled: true,
		},
		{
			name:        "security panic remains fail closed",
			policy:      FailurePolicyFailClosed,
			run:         func(context.Context) error { panic(secret) },
			wantHandled: true,
		},
		{
			name:        "observer error remains fail open",
			policy:      FailurePolicyFailOpen,
			run:         func(context.Context) error { return errors.New(secret) },
			wantHandled: false,
		},
		{
			name:        "observer panic remains fail open",
			policy:      FailurePolicyFailOpen,
			run:         func(context.Context) error { panic(secret) },
			wantHandled: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coreLogger, observed := observer.New(zap.DebugLevel)
			d := NewDispatcher(core.NewRouter("."), nil, nil, zap.New(coreLogger))
			handled := d.safeExecuteMessageHook(context.Background(), tc.policy, tc.run)
			if handled != tc.wantHandled {
				t.Fatalf("failure policy changed: handled=%v want=%v", handled, tc.wantHandled)
			}
			a6AssertNoLogPayload(t, observed, secret)
			fields := observed.All()[0].ContextMap()
			if _, ok := fields["panic"]; ok {
				t.Fatalf("raw panic value leaked: %v", fields)
			}
			if _, ok := fields["error"]; ok {
				t.Fatalf("raw hook error leaked: %v", fields)
			}
			if _, ok := fields["panic_type"]; !ok && strings.Contains(tc.name, "panic") {
				t.Fatalf("missing panic category: %v", fields)
			}
			if _, ok := fields["error_type"]; !ok && strings.Contains(tc.name, "error") {
				t.Fatalf("missing hook error category: %v", fields)
			}
		})
	}
}

func TestA6MessageHookStateGatePanicDoesNotLogPayload(t *testing.T) {
	const secret = "private-message-do-not-log"
	coreLogger, observed := observer.New(zap.WarnLevel)
	d := NewDispatcher(core.NewRouter("."), nil, nil, zap.New(coreLogger))
	registered := prioritizedHandler{stateGate: func(int64) bool { panic(secret) }, id: 17}
	if !d.messageHookStateInterested(registered, 500) {
		t.Fatal("state gate panic must preserve existing fail-open policy")
	}
	a6AssertNoLogPayload(t, observed, secret)
	fields := observed.All()[0].ContextMap()
	if fields["panic_type"] != "string" {
		t.Fatalf("missing sanitized panic type: %v", fields)
	}
}
