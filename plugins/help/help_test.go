package help

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
)

type mockService struct {
	core.MockTelegramServicer
	sent       string
	edited     string
	lastMarkup tg.ReplyMarkupClass
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	m.lastMarkup = nil
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	m.lastMarkup = nil
	return nil
}
func (m *mockService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.edited = text
	m.lastMarkup = markup
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

	// 1. Help without args -> compact category overview (bold category names, command list)
	ctxAll := *baseCtx
	ctxAll.Command = "help"
	if err := cmds[0].Handler(&ctxAll); err != nil {
		t.Fatalf("unexpected error running help all: %v", err)
	}

	if !strings.Contains(svc.edited, "Admin") || !strings.Contains(svc.edited, "Utility") {
		t.Errorf("expected help output to contain Admin and Utility categories, got: %s", svc.edited)
	}
	// Compact view: command names listed inline, no expandable blockquotes in summary
	if !strings.Contains(svc.edited, ".ban") || !strings.Contains(svc.edited, ".ping") {
		t.Errorf("expected help overview to list command names, got: %s", svc.edited)
	}

	// 2. Help for existing command
	ctxTarget := *baseCtx
	ctxTarget.Command = "help"
	ctxTarget.Args = []string{"ping"}
	if err := cmds[0].Handler(&ctxTarget); err != nil {
		t.Fatalf("unexpected error running help ping: %v", err)
	}

	if !strings.Contains(svc.edited, "Command: .ping") || !strings.Contains(svc.edited, "Check latency") {
		t.Errorf("expected help target to show ping details, got: %s", svc.edited)
	}

	// 3. Help for module/category
	ctxCat := *baseCtx
	ctxCat.Command = "help"
	ctxCat.Args = []string{"admin"}
	if err := cmds[0].Handler(&ctxCat); err != nil {
		t.Fatalf("unexpected error running help admin: %v", err)
	}

	if !strings.Contains(svc.edited, "Module: Admin") || !strings.Contains(svc.edited, ".ban") {
		t.Errorf("expected module help to show admin commands, got: %s", svc.edited)
	}

	// 4. Help for non-existent command/module
	ctxUnknown := *baseCtx
	ctxUnknown.Command = "help"
	ctxUnknown.Args = []string{"nonexistent"}
	if err := cmds[0].Handler(&ctxUnknown); err != nil {
		t.Fatalf("unexpected error running help nonexistent: %v", err)
	}

	if !strings.Contains(svc.edited, "not found") {
		t.Errorf("expected not found message, got: %s", svc.edited)
	}
}

func TestHelpPlugin_Interactive(t *testing.T) {
	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name:        "ping",
		Description: "Check latency",
		Category:    "Utility",
	})
	_ = router.Register(core.Command{
		Name:        "ban",
		Description: "Ban user",
		Category:    "Admin",
	})

	p := New(router)
	store := callback.NewStateStore()
	p.SetStateStore(store)

	if p.Namespace() != "help" {
		t.Errorf("expected namespace 'help', got %s", p.Namespace())
	}

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, SenderID: 12345},
		Sender:  &core.User{ID: 12345},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Run help command with stateStore enabled -> should attach markup with category buttons
	cmds := p.Commands()
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("help handler failed: %v", err)
	}

	if svc.lastMarkup == nil {
		t.Fatal("expected inline markup with category buttons")
	}

	// 2. Simulate clicking "Admin" category callback
	st := helpMenuState{
		Category: "Admin",
		UserID:   12345,
	}
	oid := store.Store(st, 12345, 10*time.Minute)

	cbCtx := &callback.CallbackContext{
		Ctx:      context.Background(),
		QueryID:  101,
		UserID:   12345,
		Action:   "cat",
		OpaqueID: oid,
		State:    st,
		Service:  svc,
		Target:   core.CallbackTarget{Peer: &tg.InputPeerSelf{}, MessageID: 1},
	}

	if err := p.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback cat failed: %v", err)
	}

	if !strings.Contains(svc.edited, "Module: Admin") || !strings.Contains(svc.edited, ".ban") {
		t.Errorf("expected module Admin details after callback, got: %s", svc.edited)
	}
	if svc.lastMarkup == nil {
		t.Fatal("expected back button markup in category view")
	}

	// 3. Simulate clicking "Back" (home)
	cbCtxHome := &callback.CallbackContext{
		Ctx:     context.Background(),
		QueryID: 102,
		UserID:  12345,
		Action:  "home",
		Service: svc,
		Target:  core.CallbackTarget{Peer: &tg.InputPeerSelf{}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxHome); err != nil {
		t.Fatalf("HandleCallback home failed: %v", err)
	}
	if !strings.Contains(svc.edited, "GoUltroid Help") {
		t.Errorf("expected overview text on home callback, got: %s", svc.edited)
	}

	// 4. Simulate clicking "Close"
	cbCtxClose := &callback.CallbackContext{
		Ctx:     context.Background(),
		QueryID: 103,
		UserID:  12345,
		Action:  "close",
		Service: svc,
		Target:  core.CallbackTarget{Peer: &tg.InputPeerSelf{}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxClose); err != nil {
		t.Fatalf("HandleCallback close failed: %v", err)
	}
	if !strings.Contains(svc.edited, "Help menu closed") {
		t.Errorf("expected closed text on close callback, got: %s", svc.edited)
	}
}
