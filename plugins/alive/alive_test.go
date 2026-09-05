package alive

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
)

type mockService struct {
	sent string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 1, Message: text}, nil
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

func TestAlivePlugin(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Hour)
	p := New(startTime)

	if p.Name() != "alive" {
		t.Errorf("expected plugin name alive, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "alive" {
		t.Errorf("expected command name alive, got %s", cmds[0].Name)
	}
	if cmds[0].Cooldown != 3*time.Second {
		t.Errorf("expected 3s cooldown, got %v", cmds[0].Cooldown)
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Command: "alive",
		Message: &core.Message{ID: 1},
		Perms:   core.NewPermissions(123456, nil),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running alive handler: %v", err)
	}

	if !strings.Contains(svc.sent, "GoUltroid is Alive") {
		t.Errorf("expected output to contain 'GoUltroid is Alive', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Uptime:**") {
		t.Errorf("expected output to contain uptime, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "123456") {
		t.Errorf("expected output to contain owner ID 123456, got: %s", svc.sent)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{5*time.Minute + 12*time.Second, "5m 12s"},
		{3*time.Hour + 20*time.Minute + 10*time.Second, "3h 20m 10s"},
		{2*24*time.Hour + 4*time.Hour + 5*time.Minute + 1*time.Second, "2d 4h 5m 1s"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
