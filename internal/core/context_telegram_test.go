package core

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

func TestContextUsesCapabilityContainerWithoutAggregateService(t *testing.T) {
	t.Parallel()

	ctx := &Context{
		Ctx:      context.Background(),
		Telegram: TelegramCapabilities{Messages: &messageCapabilityFake{}},
		PeerID:   &tg.InputPeerSelf{},
	}
	if ctx.Svc != nil {
		t.Fatal("compatibility aggregate unexpectedly configured")
	}
	if err := ctx.Reply("hello"); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
}

func TestTelegramCapabilitiesFromDiscoversContextualExtensionsIndependently(t *testing.T) {
	t.Parallel()

	svc := &MockTelegramServicer{}
	caps := TelegramCapabilitiesFrom(svc)
	if caps.Messages == nil || caps.Admin == nil || caps.Media == nil || caps.Peers == nil || caps.Profile == nil {
		t.Fatal("aggregate service did not populate command capabilities")
	}
	if caps.ContextualMessages != nil || caps.ContextualMedia != nil {
		t.Fatal("plain command service unexpectedly gained contextual extensions")
	}
}
