package pin

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	sent     string
	pinnedID int
	silent   bool
	unpinned bool
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
	m.pinnedID = msgID
	m.silent = silent
	return nil
}
func (m *mockService) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	m.unpinned = true
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
func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (m *mockService) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (m *mockService) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}

func TestPinPlugin(t *testing.T) {
	p := New()
	if p.Name() != "pin" {
		t.Errorf("expected plugin name pin, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, ReplyToID: 42},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Pin test
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running pin: %v", err)
	}
	if svc.pinnedID != 42 || svc.silent {
		t.Errorf("expected pinned ID 42 silent=false, got %d %v", svc.pinnedID, svc.silent)
	}
	if !strings.Contains(svc.sent, "pinned") {
		t.Errorf("expected reply to mention pinned, got %s", svc.sent)
	}

	// 2. Pin silent test
	ctx.Args = []string{"silent"}
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running pin silent: %v", err)
	}
	if !svc.silent {
		t.Errorf("expected silent=true")
	}

	// 3. Unpin test
	if err := cmds[1].Handler(ctx); err != nil {
		t.Fatalf("unexpected error running unpin: %v", err)
	}
	if !svc.unpinned {
		t.Errorf("expected unpinned=true")
	}
}
