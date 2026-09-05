package help

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
)

type mockService struct {
	core.MockTelegramServicer
	sent string
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

func TestHelpPlugin(t *testing.T) {
	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Description: "Check latency",
		Category:    "Utility",
	})
	_ = router.Register(core.Command{
		Name:        "ban",
		Description: "Ban user",
		Category:    "Admin",
		Permission:  core.PermissionSudo,
	})

	p := New(router)
	if p.Name() != "help" {
		t.Errorf("expected plugin name help, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}

	svc := &mockService{}
	baseCtx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Help without args -> lists categories
	ctxAll := *baseCtx
	ctxAll.Command = "help"
	if err := cmds[0].Handler(&ctxAll); err != nil {
		t.Fatalf("unexpected error running help all: %v", err)
	}

	if !strings.Contains(svc.sent, "[Admin]") || !strings.Contains(svc.sent, "[Utility]") {
		t.Errorf("expected help output to contain [Admin] and [Utility], got: %s", svc.sent)
	}

	// 2. Help for existing command
	ctxTarget := *baseCtx
	ctxTarget.Command = "help"
	ctxTarget.Args = []string{"ping"}
	if err := cmds[0].Handler(&ctxTarget); err != nil {
		t.Fatalf("unexpected error running help ping: %v", err)
	}

	if !strings.Contains(svc.sent, "Command:** `.ping`") || !strings.Contains(svc.sent, "Check latency") {
		t.Errorf("expected help target to show ping details, got: %s", svc.sent)
	}

	// 3. Help for non-existent command
	ctxUnknown := *baseCtx
	ctxUnknown.Command = "help"
	ctxUnknown.Args = []string{"nonexistent"}
	if err := cmds[0].Handler(&ctxUnknown); err != nil {
		t.Fatalf("unexpected error running help nonexistent: %v", err)
	}

	if !strings.Contains(svc.sent, "not found") {
		t.Errorf("expected not found message, got: %s", svc.sent)
	}
}
