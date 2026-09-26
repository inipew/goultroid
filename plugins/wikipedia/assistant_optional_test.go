package wikipedia

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

type p6WikipediaService struct {
	core.MockTelegramServicer
	edited string
	sent   string
}

func (s *p6WikipediaService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	s.edited = text
	return nil
}

func (s *p6WikipediaService) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.sent = text
	return &tg.Message{ID: 100, Message: text}, nil
}

func TestP6WikipediaNativeCommandDoesNotRequireInline(t *testing.T) {
	p := New()
	commands := p.Commands()
	if len(commands) != 1 || commands[0].Name != "wiki" {
		t.Fatalf("wikipedia commands=%+v", commands)
	}
	if !commands[0].IsAvailableOn(execution.SourceUserbot) {
		t.Fatalf("wiki command surfaces=%v, want userbot", commands[0].Surfaces)
	}

	svc := &p6WikipediaService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionInteractive,
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 9, IsOutgoing: true},
		Svc:     svc,
	}
	if err := commands[0].Handler(ctx); err != nil {
		t.Fatalf("native .wiki usage error=%v", err)
	}
	output := svc.edited + svc.sent
	if !strings.Contains(output, "Usage: .wiki") {
		t.Fatalf("native .wiki usage=%q", output)
	}
	if len(p.InlineBindings()) != 1 {
		t.Fatalf("inline enhancement bindings=%d, want 1", len(p.InlineBindings()))
	}
}
