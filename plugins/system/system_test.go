package system

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	sent         string
	mediaSent    bool
	mediaType    string
	mediaCaption string
	mediaPath    string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 100, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.sent = text
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
	m.mediaSent = true
	m.mediaType = mediaType
	m.mediaPath = filePath
	m.mediaCaption = caption
	return &tg.Message{ID: 200}, nil
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

func TestSystemPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "system" {
		t.Errorf("expected name system, got %s", p.Name())
	}
	if p.Description() == "" {
		t.Errorf("expected non-empty description")
	}
	if err := p.Init(); err != nil {
		t.Errorf("Init failed: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	for _, c := range cmds {
		if c.Permission != core.PermissionOwner {
			t.Errorf("expected command %s to have PermissionOwner permission", c.Name)
		}
	}
}

func TestExec_EmptyArgs(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec"},
		Args:    nil,
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "Usage:") {
		t.Errorf("expected usage message, got: %s", svc.sent)
	}
}

func TestExec_ShortOutput(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec echo Hello GoUltroid"},
		Args:    []string{"echo", "Hello", "GoUltroid"},
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "Hello GoUltroid") {
		t.Errorf("expected 'Hello GoUltroid' in output, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Shell Execution") {
		t.Errorf("expected header 'Shell Execution', got: %s", svc.sent)
	}
}

func TestExec_LongOutput(t *testing.T) {
	p := New()
	svc := &mockService{}
	// Generates 4000 chars
	cmdStr := "head -c 4000 < /dev/zero | tr '\\0' 'A'"
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec " + cmdStr},
		Args:    []string{"head", "-c", "4000", "<", "/dev/zero", "|", "tr", "'\\0'", "'A'"},
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !svc.mediaSent {
		t.Errorf("expected long output to be uploaded as file")
	}
	if svc.mediaType != "file" {
		t.Errorf("expected mediaType file, got %s", svc.mediaType)
	}
}

func TestRestart_CustomHandler(t *testing.T) {
	var calledState RestartState
	var called bool

	p := NewWithCustomRestart("", func(state RestartState) error {
		called = true
		calledState = state
		return nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChannel{ChannelID: 777, AccessHash: 888},
		Message: &core.Message{ID: 42, Text: ".restart"},
		Svc:     svc,
	}

	err := p.handleRestart(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !called {
		t.Errorf("expected custom restart handler to be called")
	}
	if calledState.ChatID != 777 || !calledState.IsChannel || calledState.AccessHash != 888 || calledState.MsgID != 100 {
		t.Errorf("unexpected restart state captured: %+v", calledState)
	}
	if !strings.Contains(svc.sent, "Restarting GoUltroid") {
		t.Errorf("expected restart response, got: %s", svc.sent)
	}
}
