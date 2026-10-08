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

func a6DRequireSanitizedAdmission(t *testing.T, logs *observer.ObservedLogs, marker string, failClosed bool) {
	t.Helper()
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected one admission diagnostic, got %d: %v", len(entries), entries)
	}
	entry := entries[0]
	if strings.Contains(entry.Message, marker) || strings.Contains(fmt.Sprint(entry.ContextMap()), marker) {
		t.Fatalf("admission log leaked private payload: %s %v", entry.Message, entry.ContextMap())
	}
	fields := entry.ContextMap()
	if _, ok := fields["error"]; ok {
		t.Fatalf("raw error field is forbidden: %v", fields)
	}
	if fields["error_type"] == nil {
		t.Fatalf("missing bounded error type diagnostic: %v", fields)
	}
	if value, ok := fields["fail_closed"]; ok && value != failClosed {
		t.Fatalf("wrong admission failure policy in log: %v", fields)
	}
}

func TestA6DDecisionAdmissionFailureIsPrivateAndRetainsPolicy(t *testing.T) {
	const secret = "private-command-or-message-token"
	for _, tc := range []struct {
		name     string
		priority HandlerPriority
		wait     bool
		handled  bool
	}{
		{name: "security submit", priority: PrioritySecurity, handled: true},
		{name: "feature submit", priority: PriorityFeature, handled: false},
		{name: "security wait", priority: PrioritySecurity, wait: true, handled: true},
		{name: "feature wait", priority: PriorityFeature, wait: true, handled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, observed := observer.New(zap.DebugLevel)
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.New(logger))
			failure := errors.New("TaskEngine admission: " + secret)
			client := &decisionPolicyTaskClient{submitErr: failure}
			if tc.wait {
				client = &decisionPolicyTaskClient{waitErr: failure}
			}
			d.SetTasks(client)
			handler, envelope, msg := decisionPolicyFixture(tc.priority, func(context.Context, *core.MessageEnvelope) error {
				t.Fatal("handler must not run on admission failure")
				return nil
			})
			envelope.Text = secret
			msg.Message = secret
			got := d.executeDecisionHandlersEnvelope(context.Background(), []prioritizedHandler{handler}, envelope, tg.Entities{}, msg)
			if got != tc.handled {
				t.Fatalf("decision changed: got=%v want=%v", got, tc.handled)
			}
			a6DRequireSanitizedAdmission(t, observed, secret, tc.handled)
		})
	}
}

func TestA6DObserverAdmissionFailureIsPrivateAndNonBlocking(t *testing.T) {
	const secret = "private-observer-task-token"
	logger, observed := observer.New(zap.DebugLevel)
	d := NewDispatcher(core.NewRouter("."), nil, nil, zap.New(logger))
	d.SetTasks(&decisionPolicyTaskClient{submitErr: errors.New("observer rejected: " + secret)})
	handler, envelope, msg := decisionPolicyFixture(PriorityObservability, func(context.Context, *core.MessageEnvelope) error {
		t.Fatal("rejected observer task must not execute")
		return nil
	})
	handler.routing = core.MessageHookRouting{Lane: core.MessageHookEvent}
	envelope.Text = secret
	msg.Message = secret
	d.dispatchEventHandlersEnvelope(context.Background(), []prioritizedHandler{handler}, envelope, tg.Entities{}, msg)
	a6DRequireSanitizedAdmission(t, observed, secret, false)
}
