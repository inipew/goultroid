package app

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantclient "github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/plugins/calculator"
)

type p1RotatingSelfInlineService struct {
	p0SelfInlineService
	afterQuery func()
}

func (s *p1RotatingSelfInlineService) QueryInlineBot(
	ctx context.Context,
	botUsername string,
	peer tg.InputPeerClass,
	query string,
	offset string,
) (*tg.MessagesBotResults, error) {
	results, err := s.p0SelfInlineService.QueryInlineBot(ctx, botUsername, peer, query, offset)
	if s.afterQuery != nil {
		s.afterQuery()
	}
	return results, err
}

func TestP1SelfInlineUsesCurrentTelegramServiceAcrossReconnect(t *testing.T) {
	provider := &p0SelfInlineServiceProvider{}
	identity := &p0AssistantIdentity{username: "assistant_bot"}
	renderer := newSelfInlineRenderer(provider, identity)
	if renderer == nil {
		t.Fatal("newSelfInlineRenderer() returned nil")
	}

	first := &p1RotatingSelfInlineService{}
	second := &p1RotatingSelfInlineService{}
	provider.service = first
	first.afterQuery = func() {
		// Simulate telegram.Client reconnect replacing the process-visible Service
		// after query RPC completion but before send RPC admission.
		provider.service = second
	}

	request := selfinline.Request{
		Peer:     &tg.InputPeerChat{ChatID: 77},
		Query:    "calc 1+2",
		ResultID: "calculator",
	}
	if _, err := renderer.Render(context.Background(), request); err != nil {
		t.Fatalf("Render(first reconnect) error=%v", err)
	}
	if first.queryCalls != 1 || first.sendCalls != 0 {
		t.Fatalf("old service query/send=%d/%d, want 1/0", first.queryCalls, first.sendCalls)
	}
	if second.queryCalls != 0 || second.sendCalls != 1 {
		t.Fatalf("new service query/send=%d/%d, want 0/1", second.queryCalls, second.sendCalls)
	}

	if _, err := renderer.Render(context.Background(), request); err != nil {
		t.Fatalf("Render(after reconnect) error=%v", err)
	}
	if second.queryCalls != 1 || second.sendCalls != 2 {
		t.Fatalf("current service query/send=%d/%d, want 1/2", second.queryCalls, second.sendCalls)
	}
}

func TestP1SelfInlineRejectsStaleAssistantIdentityAcrossRestart(t *testing.T) {
	provider := &p0SelfInlineServiceProvider{service: &p0SelfInlineService{}}
	identity := &p0AssistantIdentity{username: "old_assistant_bot"}
	renderer := newSelfInlineRenderer(provider, identity)
	request := selfinline.Request{
		Peer:     &tg.InputPeerSelf{},
		Query:    "calc",
		ResultID: "calculator",
	}

	if _, err := renderer.Render(context.Background(), request); err != nil {
		t.Fatalf("Render(old identity) error=%v", err)
	}
	service := provider.service.(*p0SelfInlineService)
	if service.botUsername != "old_assistant_bot" || service.queryCalls != 1 {
		t.Fatalf("old identity query bot/calls=%q/%d", service.botUsername, service.queryCalls)
	}

	// During Assistant restart the previous tg.User may still exist briefly, but
	// InlineUsername must fail closed instead of reusing that stale identity.
	identity.err = assistantclient.ErrNotReady
	if _, err := renderer.Render(context.Background(), request); !errors.Is(err, assistantclient.ErrNotReady) {
		t.Fatalf("Render(restarting identity) error=%v, want %v", err, assistantclient.ErrNotReady)
	}
	if service.queryCalls != 1 {
		t.Fatalf("stale identity reached Telegram query, calls=%d", service.queryCalls)
	}

	identity.err = nil
	identity.username = "new_assistant_bot"
	if _, err := renderer.Render(context.Background(), request); err != nil {
		t.Fatalf("Render(new identity) error=%v", err)
	}
	if service.botUsername != "new_assistant_bot" || service.queryCalls != 2 {
		t.Fatalf("new identity query bot/calls=%q/%d", service.botUsername, service.queryCalls)
	}
}

func TestP1SelfInlinePluginDisableEnableReloadUsesLiveAuthorization(t *testing.T) {
	ctx := context.Background()
	router := core.NewRouter(".")
	manager := plugin.NewManager(router)
	manager.SetInlineRegistry(inlineservice.NewRegistry())
	feature := calculator.New()
	if err := manager.RegisterWithContext(ctx, feature); err != nil {
		t.Fatalf("RegisterWithContext() error=%v", err)
	}
	t.Cleanup(func() { _ = manager.ShutdownWithContext(context.Background()) })

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

	live := &p0SelfInlineService{}
	provider := &p0SelfInlineServiceProvider{service: live}
	identity := &p0AssistantIdentity{username: "assistant_bot"}
	wireSelfInlineRenderers(manager, provider, identity, gate)

	commands := feature.Commands()
	if len(commands) != 1 {
		t.Fatalf("calculator commands=%d, want 1", len(commands))
	}
	staleCommand := commands[0]
	commandCtx := &core.Context{
		Ctx:     ctx,
		RawArgs: "1+2",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 9, IsOutgoing: true},
		Svc:     live,
	}
	if err := staleCommand.Handler(commandCtx); err != nil {
		t.Fatalf("initial .calc error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("initial query/send=%d/%d, want 1/1", live.queryCalls, live.sendCalls)
	}
	firstScope, ok := manager.Scope(feature.Name())
	if !ok {
		t.Fatal("initial calculator scope missing")
	}
	firstGeneration := firstScope.Generation()

	if err := manager.Disable(ctx, feature.Name()); err != nil {
		t.Fatalf("Disable() error=%v", err)
	}
	if err := staleCommand.Handler(commandCtx); err != nil {
		t.Fatalf("stale command while disabled error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("disabled stale command reached transport: query/send=%d/%d", live.queryCalls, live.sendCalls)
	}

	// The manager's disable->enable transition is the canonical in-process
	// plugin reload: it creates a new generation and re-registers all surfaces.
	if err := manager.Enable(ctx, feature.Name()); err != nil {
		t.Fatalf("Enable() error=%v", err)
	}
	secondScope, ok := manager.Scope(feature.Name())
	if !ok {
		t.Fatal("re-enabled calculator scope missing")
	}
	if secondScope.Generation() == firstGeneration {
		t.Fatalf("reload reused generation %d", firstGeneration)
	}

	// Even a closure retained from the old command list must consult live plugin
	// authorization and current transport instead of retaining disabled state.
	if err := staleCommand.Handler(commandCtx); err != nil {
		t.Fatalf("stale command after reload error=%v", err)
	}
	if live.queryCalls != 2 || live.sendCalls != 2 {
		t.Fatalf("reloaded stale command query/send=%d/%d, want 2/2", live.queryCalls, live.sendCalls)
	}
}
