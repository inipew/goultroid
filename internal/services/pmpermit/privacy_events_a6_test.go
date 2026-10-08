package pmpermit

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestA6PMPermitEventRedactsReasonsAndTelegramErrors(t *testing.T) {
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	svc := NewService(nil, nil, 1001, nil, zap.NewNop())
	svc.SetEventBus(bus)

	received := make(chan *core.PMPermitEvent, 4)
	unsubscribe := bus.Subscribe(core.EventTypePMPermit, func(event core.Event) {
		if action, ok := event.(*core.PMPermitEvent); ok {
			received <- action
		}
	})
	defer unsubscribe()

	const privateData = "private-telegram-token-never-publish"
	for _, tc := range []struct {
		action     string
		reason     string
		errorText  string
		wantReason string
		wantError  string
	}{
		{action: "block", reason: privateData, errorText: privateData, wantReason: "blocked", wantError: "operation_failed"},
		{action: "approve", reason: privateData, wantReason: "approved"},
		{action: "warn", reason: privateData, wantReason: "warning"},
	} {
		svc.publishEvent(tc.action, 500, privateData, 2, tc.reason, tc.errorText == "", tc.errorText)
		select {
		case event := <-received:
			if event.Action != tc.action || event.UserID != 500 || event.WarnCount != 2 {
				t.Fatalf("lost non-sensitive context: %+v", event)
			}
			if event.Reason != tc.wantReason || event.Error != tc.wantError || event.TargetName != "" {
				t.Fatalf("event contains sensitive data or wrong diagnostic: %+v", event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %s not delivered to active subscriber", tc.action)
		}
	}
}
