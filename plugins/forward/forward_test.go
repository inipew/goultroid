package forward

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	sent         string
	forwardedIDs []int
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return nil, nil
}
func (m *mockService) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}
func (m *mockService) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}
func (m *mockService) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	m.forwardedIDs = msgIDs
	return nil
}
func (m *mockService) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}

func TestForwardPlugin(t *testing.T) {
	p := New()
	if p.Name() != "forward" {
		t.Errorf("expected plugin name forward, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if !cmds[0].ReplyOnly {
		t.Errorf("expected forward command to have ReplyOnly=true")
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, ReplyToID: 777},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running forward: %v", err)
	}

	if len(svc.forwardedIDs) != 1 || svc.forwardedIDs[0] != 777 {
		t.Errorf("expected forwarded ID 777, got %v", svc.forwardedIDs)
	}
	if !strings.Contains(svc.sent, "Saved Messages") {
		t.Errorf("expected reply to mention Saved Messages, got %s", svc.sent)
	}
}
