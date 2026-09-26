package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	settingssvc "github.com/inipew/goultroid/internal/settings"
)

func TestPlugin_DashboardTextOnlyAllocatesNoInteraction(t *testing.T) {
	p, svc, tgSvc := setupTestPlugin(t)
	ctx := context.Background()
	if err := svc.Set(ctx, settingssvc.ScopeGlobal, 0, "ui", "inline_buttons", "false", 12345); err != nil {
		t.Fatalf("disable inline buttons: %v", err)
	}

	commandCtx := &core.Context{
		Ctx:     ctx,
		Message: &core.Message{ID: 1, SenderID: 12345},
		Sender:  &core.User{ID: 12345},
		Chat:    &core.Chat{ID: -100123},
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
	}

	if err := p.handleSettingsCommand(commandCtx); err != nil {
		t.Fatalf("text-only home failed: %v", err)
	}
	tgSvc.mu.Lock()
	homeText := tgSvc.lastText
	homeMarkup := tgSvc.lastMarkup
	tgSvc.mu.Unlock()
	if homeMarkup != nil {
		t.Fatal("text-only home unexpectedly sent reply markup")
	}
	if !strings.Contains(homeText, "Available settings categories") || !strings.Contains(homeText, "<code>security</code>") {
		t.Fatalf("text-only home is not navigable:\n%s", homeText)
	}

	commandCtx.Args = []string{"security", "1"}
	if err := p.handleSettingsCommand(commandCtx); err != nil {
		t.Fatalf("text-only category failed: %v", err)
	}
	tgSvc.mu.Lock()
	categoryText := tgSvc.lastText
	categoryMarkup := tgSvc.lastMarkup
	tgSvc.mu.Unlock()
	if categoryMarkup != nil {
		t.Fatal("text-only category unexpectedly sent reply markup")
	}
	if !strings.Contains(categoryText, "Settings: Security") || !strings.Contains(categoryText, "Inline buttons are disabled") {
		t.Fatalf("unexpected text-only category:\n%s", categoryText)
	}
}

func TestP1F2AssistantInteractiveFailsClosedWithoutA2Runtime(t *testing.T) {
	p, _, tgSvc := setupTestPlugin(t)
	ctx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionAssistant,
		Message: &core.Message{ID: 1, SenderID: 12345},
		Sender:  &core.User{ID: 12345},
		Chat:    &core.Chat{ID: -100123},
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
	}

	if err := p.handleSettingsCommand(ctx); err == nil {
		t.Fatal("interactive Assistant settings unexpectedly fell back without a2 runtime")
	}
	tgSvc.mu.Lock()
	markup := tgSvc.lastMarkup
	tgSvc.mu.Unlock()
	if markup != nil {
		t.Fatal("unbound Assistant settings emitted legacy reply markup")
	}
}
