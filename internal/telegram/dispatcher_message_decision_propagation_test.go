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

func TestMessageHookDecisionContextPropagatesAcrossTaskEngine(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), nil, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	decision := core.NewMessageDecision(core.ExecutionAutomation)
	ctx := core.WithMessageDecision(context.Background(), decision)
	var sawSecond atomic.Bool

	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:filters-probe", Generation: 1},
		Priority: PriorityModeration,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: func(taskCtx context.Context, _ *core.MessageEnvelope) error {
			got := core.GetMessageDecision(taskCtx)
			if got != decision || got.Origin() != core.ExecutionAutomation {
				return errors.New("first decision hook lost shared automation identity")
			}
			if taskCtx.Done() == nil {
				return errors.New("worker cancellation context missing")
			}
			got.SetSuppressAFK(true)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:afk-probe", Generation: 1},
		Priority: PriorityFeature,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: func(taskCtx context.Context, _ *core.MessageEnvelope) error {
			got := core.GetMessageDecision(taskCtx)
			if got == decision && got.IsSuppressedAFK() && got.Origin() == core.ExecutionAutomation {
				sawSecond.Store(true)
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	msg := &tg.Message{ID: 301, PeerID: &tg.PeerUser{UserID: 501}, FromID: &tg.PeerUser{UserID: 501}, Message: "hello"}
	envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 0)
	handlers, _ := d.messageHandlersForEnvelope(envelope)
	if handled := d.executeDecisionHandlersEnvelope(ctx, handlers, envelope, tg.Entities{}, msg); handled {
		t.Fatal("decision handlers incorrectly intercepted the update")
	}
	if !decision.IsSuppressedAFK() || !sawSecond.Load() {
		t.Fatal("sequential decision handlers did not share suppress-AFK state")
	}
}

func TestMessageHookEventContextPropagatesAcrossTaskEngine(t *testing.T) {
	for _, tc := range []struct {
		name       string
		automation bool
	}{
		{name: "interactive"},
		{name: "automation", automation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), nil, nil, zap.NewNop())
			configureDispatcherTasks(t, d)
			origin := core.ExecutionInteractive
			if tc.automation {
				origin = core.ExecutionAutomation
			}
			decision := core.NewMessageDecision(origin)
			decision.SetSuppressAFK(true)
			ctx := core.WithMessageDecision(context.Background(), decision)
			results := make(chan bool, 1)
			if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
				Scope:    tasks.ScopeIdentity{Owner: "plugin:afk-probe", Generation: 1},
				Priority: PriorityFeature,
				Routing:  core.MessageHookRouting{Lane: core.MessageHookEvent},
				Handler: func(taskCtx context.Context, _ *core.MessageEnvelope) error {
					got := core.GetMessageDecision(taskCtx)
					results <- got == decision && got.IsSuppressedAFK() && got.Origin() == origin && taskCtx.Done() != nil
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			msg := &tg.Message{ID: 302, PeerID: &tg.PeerUser{UserID: 501}, FromID: &tg.PeerUser{UserID: 501}, Message: "hello"}
			envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 0)
			_, handlers := d.messageHandlersForEnvelope(envelope)
			d.dispatchEventHandlersEnvelope(ctx, handlers, envelope, tg.Entities{}, msg)
			select {
			case ok := <-results:
				if !ok {
					t.Fatal("event hook lost shared decision, AFK suppression, origin, or cancellation")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("event hook did not execute through TaskEngine")
			}
		})
	}
}

func TestMessageHookFilterSuppressionReachesAFKEventEndToEnd(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), nil, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	var filterRan atomic.Bool
	afkObserved := make(chan bool, 1)
	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:filters-probe", Generation: 1},
		Priority: PriorityModeration,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: func(ctx context.Context, _ *core.MessageEnvelope) error {
			decision := core.GetMessageDecision(ctx)
			if decision == nil {
				return errors.New("filter hook has no MessageDecision")
			}
			decision.SetSuppressAFK(true)
			filterRan.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:afk-probe", Generation: 1},
		Priority: PriorityFeature,
		Routing:  core.MessageHookRouting{Lane: core.MessageHookEvent},
		Handler: func(ctx context.Context, _ *core.MessageEnvelope) error {
			decision := core.GetMessageDecision(ctx)
			afkObserved <- decision != nil && decision.IsSuppressedAFK() && decision.Origin() == core.ExecutionInteractive
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{Message: &tg.Message{
		ID: 303, PeerID: &tg.PeerUser{UserID: 501}, FromID: &tg.PeerUser{UserID: 501}, Message: "hello",
	}}); err != nil {
		t.Fatal(err)
	}
	if !filterRan.Load() {
		t.Fatal("filter decision hook did not execute")
	}
	select {
	case ok := <-afkObserved:
		if !ok {
			t.Fatal("AFK event did not observe filter suppression")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AFK event did not execute")
	}
}
