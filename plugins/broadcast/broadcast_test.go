package broadcast_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/plugins/broadcast"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}

func (m *mockTelegram) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	return []*core.Chat{
		{ID: 101, Title: "Group 1", Type: "group"},
		{ID: 102, Title: "Channel 1", Type: "channel"},
		{ID: 103, Title: "User 1", Type: "user"},
	}, nil
}

func TestBroadcastPlugin(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := broadcastSvc.NewService(mockTG, zap.NewNop())
	p := broadcast.New(svc)

	if p.Name() != "broadcast" {
		t.Errorf("expected plugin name broadcast, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1},
		Args:    []string{"-users", "Hello", "Users"},
		RawArgs: "-users Hello Users",
	}

	// 1. Broadcast command
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("handleBroadcast failed: %v", err)
	}

	if !strings.Contains(mockTG.edited, "Broadcast Completed") {
		t.Errorf("expected completion edit, got %s", mockTG.edited)
	}

	// 2. Cancel when no job running
	cancelCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 2},
	}
	if err := cmds[1].Handler(cancelCtx); err != nil {
		t.Fatalf("handleCancelBroadcast failed: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "No active broadcast") {
		t.Errorf("expected no active broadcast notice, got %s", mockTG.sentText)
	}
}

func TestBroadcastPlugin_EmptyArgs(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := broadcastSvc.NewService(mockTG, zap.NewNop())
	p := broadcast.New(svc)

	cmds := p.Commands()
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "Usage:") {
		t.Errorf("expected usage guide, got %s", mockTG.sentText)
	}
}
