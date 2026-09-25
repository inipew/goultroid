package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantclient "github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/plugins/calculator"
)

func TestP2CalculatorFallsBackUntilAssistantIdentityIsReady(t *testing.T) {
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
	if !strings.Contains(live.edited, "<code>1+2</code>") || !strings.Contains(live.edited, "<b>3</b>") {
		t.Fatalf("native calculator fallback before Assistant readiness=%q", live.edited)
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

func TestP0SelfInlineMapsAssistantCapabilityPreflightBeforeTransport(t *testing.T) {
	provider := &p0SelfInlineServiceProvider{service: &p0SelfInlineService{}}
	identity := &p0AssistantIdentity{username: "assistant_bot", err: assistantclient.ErrInlineDisabled}
	renderer := newSelfInlineRenderer(provider, identity)
	if renderer == nil {
		t.Fatal("newSelfInlineRenderer() returned nil")
	}
	_, err := renderer.Render(context.Background(), selfinline.Request{
		Peer: &tg.InputPeerSelf{}, Query: "calc",
	})
	if !errors.Is(err, selfinline.ErrInlineDisabled) {
		t.Fatalf("Render() error=%v, want %v", err, selfinline.ErrInlineDisabled)
	}
	live := provider.service.(*p0SelfInlineService)
	if live.queryCalls != 0 || live.sendCalls != 0 {
		t.Fatalf("capability preflight reached Telegram: query/send=%d/%d", live.queryCalls, live.sendCalls)
	}
}
