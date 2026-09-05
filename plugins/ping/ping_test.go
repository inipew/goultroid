package ping

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
)

type mockService struct {
	sent   string
	edited string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
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
	return nil
}
func (m *mockService) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}
func (m *mockService) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, nil
}
func (m *mockService) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}

func TestPingPlugin(t *testing.T) {
	p := New()
	if p.Name() != "ping" {
		t.Errorf("expected plugin name ping, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "ping" {
		t.Errorf("expected command name ping, got %s", cmds[0].Name)
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Command: "ping",
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running ping: %v", err)
	}

	if svc.sent != "🏓 ..." {
		t.Errorf("expected initial reply '🏓 ...', got %q", svc.sent)
	}

	if !strings.Contains(svc.edited, "Pong!") {
		t.Errorf("expected edit message to contain 'Pong!', got %q", svc.edited)
	}
}
