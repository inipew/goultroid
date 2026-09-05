package downloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
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
	return &tg.Message{
		ID: 42,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				ID:       999,
				MimeType: "video/mp4",
				Size:     2048576,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "sample.mp4"},
				},
			},
		},
	}, nil
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
	// Create a dummy file at dstPath to simulate successful download
	_ = os.MkdirAll(filepath.Dir(dstPath), 0755)
	return os.WriteFile(dstPath, []byte("dummy video content"), 0644)
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
func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (m *mockService) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (m *mockService) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}

func TestDownloaderPlugin(t *testing.T) {
	p := New()
	if p.Name() != "downloader" {
		t.Errorf("expected plugin name downloader, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if !cmds[0].ReplyOnly {
		t.Errorf("expected download command to have ReplyOnly=true")
	}
	if cmds[0].Cooldown != 3*time.Second {
		t.Errorf("expected 3s cooldown, got %v", cmds[0].Cooldown)
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, ReplyToID: 42},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running download: %v", err)
	}

	if !strings.Contains(svc.sent, "Downloading") {
		t.Errorf("expected initial reply to mention Downloading, got %s", svc.sent)
	}
	if !strings.Contains(svc.edited, "Download Complete") || !strings.Contains(svc.edited, "sample.mp4") {
		t.Errorf("expected edit message to report complete and filename, got %s", svc.edited)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.b)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}
