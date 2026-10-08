package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestMalformedCommandStillReachesMessageSecurityAndObservers(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "unclosed quote", body: `.ping "unterminated`},
		{name: "trailing escape", body: ".ping trailing\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := core.NewRouter(".")
			var commandCalls atomic.Int32
			if err := router.Register(core.Command{Name: "ping", Handler: func(*core.Context) error {
				commandCalls.Add(1)
				return nil
			}}); err != nil {
				t.Fatal(err)
			}

			d := NewDispatcher(router, core.NewPermissions(123, nil), nil, zap.NewNop())
			configureDispatcherTasks(t, d)
			d.SetSelfID(123)

			var decisionCalls atomic.Int32
			if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
				Priority: PrioritySecurity,
				Scope:    tasks.ScopeIdentity{Owner: "plugin:security-probe", Generation: 1},
				Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision, Interests: []core.MessageHookInterest{{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate}}},
				Handler: func(_ context.Context, message *core.MessageEnvelope) error {
					if message.IsCommand || message.CommandName != "" || message.Text != tc.body {
						return errors.New("malformed message lost plain-text security semantics")
					}
					decisionCalls.Add(1)
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			observed := make(chan *core.MessageEnvelope, 1)
			if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
				Priority: PriorityObservability,
				Scope:    tasks.ScopeIdentity{Owner: "plugin:observer-probe", Generation: 1},
				Routing:  core.MessageHookRouting{Lane: core.MessageHookEvent, Interests: []core.MessageHookInterest{{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate}}},
				Handler: func(_ context.Context, message *core.MessageEnvelope) error {
					observed <- message
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := d.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{Message: &tg.Message{
				ID: 41, PeerID: &tg.PeerUser{UserID: 123}, FromID: &tg.PeerUser{UserID: 123}, Message: tc.body,
			}}); err != nil {
				t.Fatal(err)
			}
			if got := decisionCalls.Load(); got != 1 {
				t.Fatalf("security decision calls=%d, want 1", got)
			}
			if got := commandCalls.Load(); got != 0 {
				t.Fatalf("malformed command executed %d times", got)
			}
			select {
			case message := <-observed:
				if message.IsCommand || message.CommandName != "" || message.Text != tc.body {
					t.Fatalf("observer received unexpected envelope: %+v", message)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("malformed command bypassed message observer")
			}
		})
	}
}

func TestMalformedCommandSecurityShortCircuitStillBlocksObserver(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(123, nil), nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	var securityCalls atomic.Int32
	var observerCalls atomic.Int32

	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Priority: PrioritySecurity,
		Scope:    tasks.ScopeIdentity{Owner: "plugin:security-probe", Generation: 1},
		Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: func(context.Context, *core.MessageEnvelope) error {
			securityCalls.Add(1)
			return core.ErrInterceptHandled
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Priority: PriorityObservability,
		Scope:    tasks.ScopeIdentity{Owner: "plugin:observer-probe", Generation: 1},
		Routing:  core.MessageHookRouting{Lane: core.MessageHookEvent},
		Handler: func(context.Context, *core.MessageEnvelope) error {
			observerCalls.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{Message: &tg.Message{
		ID: 42, PeerID: &tg.PeerUser{UserID: 123}, FromID: &tg.PeerUser{UserID: 123}, Message: `.ping "unterminated`,
	}}); err != nil {
		t.Fatal(err)
	}
	if got := securityCalls.Load(); got != 1 {
		t.Fatalf("security decision calls=%d, want 1", got)
	}
	if got := observerCalls.Load(); got != 0 {
		t.Fatalf("observer ran %d times after security interception", got)
	}
}
