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

type p0SelfInlineServiceProvider struct {
	service core.TelegramServicer
}

func (p *p0SelfInlineServiceProvider) Service() core.TelegramServicer {
	if p == nil {
		return nil
	}
	return p.service
}

type p0AssistantIdentity struct {
	username string
}

func (p *p0AssistantIdentity) Username() string {
	if p == nil {
		return ""
	}
	return p.username
}

type p0SelfInlineService struct {
	core.MockTelegramServicer
	queryCalls   int
	sendCalls    int
	botUsername  string
	query        string
	sentResultID string
	sentQueryID  int64
}

func (s *p0SelfInlineService) QueryInlineBot(
	_ context.Context,
	botUsername string,
	_ tg.InputPeerClass,
	query string,
	_ string,
) (*tg.MessagesBotResults, error) {
	s.queryCalls++
	s.botUsername = botUsername
	s.query = query
	return &tg.MessagesBotResults{
		QueryID: 7001,
		Results: []tg.BotInlineResultClass{
			&tg.BotInlineResult{ID: "calculator", Type: "article"},
		},
	}, nil
}

func (s *p0SelfInlineService) SendInlineBotResult(
	_ context.Context,
	_ tg.InputPeerClass,
	queryID int64,
	resultID string,
	_ int64,
	_ int,
	_ int,
	_ bool,
	_ bool,
) error {
	s.sendCalls++
	s.sentQueryID = queryID
	s.sentResultID = resultID
	return nil
}

func TestP0SelfInlineWiringSurvivesTelegramServiceBecomingReadyAfterConstruction(t *testing.T) {
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

	// App composition happens before telegram.Client.Run installs Service.
	provider := &p0SelfInlineServiceProvider{}
	assistantIdentity := &p0AssistantIdentity{username: "assistant_bot"}
	wireSelfInlineRenderers(manager, provider, assistantIdentity, gate)
	if provider.Service() != nil {
		t.Fatal("precondition failed: Telegram service should not exist during construction")
	}

	// Telegram becomes ready later. The already-injected renderer must resolve
	// this current service instead of retaining the nil construction-time value.
	live := &p0SelfInlineService{}
	provider.service = live

	commands := feature.Commands()
	if len(commands) != 1 || commands[0].Name != "calc" {
		t.Fatalf("calculator commands=%+v", commands)
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		RawArgs: " 1 + 2 ",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 9, IsOutgoing: true},
		Svc:     live,
	}
	if err := commands[0].Handler(ctx); err != nil {
		t.Fatalf(".calc handler error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("self-inline query/send calls=%d/%d, want 1/1", live.queryCalls, live.sendCalls)
	}
	if live.botUsername != "assistant_bot" || live.query != "calc 1+2" {
		t.Fatalf("self-inline bot/query=%q/%q", live.botUsername, live.query)
	}
	if live.sentQueryID != 7001 || live.sentResultID != "calculator" {
		t.Fatalf("sent query/result=%d/%q", live.sentQueryID, live.sentResultID)
	}
}
