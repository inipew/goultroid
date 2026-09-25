package app

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/plugins/calculator"
)

func TestP0SelfInlineFailsClosedUntilAssistantIdentityIsReady(t *testing.T) {
	manager := plugin.NewManager(core.NewRouter("."))
	manager.SetInlineRegistry(inlineservice.NewRegistry())
	feature := calculator.New()
	if err := manager.RegisterWithContext(context.Background(), feature); err != nil {
		t.Fatalf("RegisterWithContext() error=%v", err)
	}
	t.Cleanup(func() {
		if manager.IsEnabled(feature.Name()) {
			_ = manager.ShutdownWithContext(context.Background())
		}
	})

	gate := plugin.NewCapabilityGate()
	if err := gate.RegisterManifest(plugin.Manifest{
		ID:      feature.Name(),
		Name:    feature.Name(),
		Version: "1",
		Capabilities: []string{
			plugin.CapTelegramRead,
			plugin.CapTelegramSendMessage,
		},
	}); err != nil {
		t.Fatalf("RegisterManifest() error=%v", err)
	}

	provider := &p0SelfInlineServiceProvider{}
	identity := &p0AssistantIdentity{}
	wireSelfInlineRenderers(manager, provider, identity, gate)
	live := &p0SelfInlineService{}
	provider.service = live

	commands := feature.Commands()
	if len(commands) != 1 || commands[0].Name != "calc" {
		t.Fatalf("calculator commands=%+v", commands)
	}
	beforeReady := &core.Context{
		Ctx:     context.Background(),
		RawArgs: "1 + 2",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 8, IsOutgoing: true},
		Svc:     live,
	}
	if err := commands[0].Handler(beforeReady); err != nil {
		t.Fatalf(".calc before Assistant readiness error=%v", err)
	}
	if live.queryCalls != 0 || live.sendCalls != 0 {
		t.Fatalf("self-inline reached Telegram before Assistant identity readiness: query/send=%d/%d", live.queryCalls, live.sendCalls)
	}

	identity.username = "assistant_bot"
	afterReady := &core.Context{
		Ctx:     context.Background(),
		RawArgs: "1 + 2",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 9, IsOutgoing: true},
		Svc:     live,
	}
	if err := commands[0].Handler(afterReady); err != nil {
		t.Fatalf(".calc after Assistant readiness error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("self-inline query/send calls after readiness=%d/%d, want 1/1", live.queryCalls, live.sendCalls)
	}
	if live.botUsername != "assistant_bot" {
		t.Fatalf("self-inline bot username=%q, want assistant_bot", live.botUsername)
	}
}
