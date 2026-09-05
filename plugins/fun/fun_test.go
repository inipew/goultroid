package fun

import (
	"context"
	"strings"
	"testing"

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
	if msgID == 77 {
		return &tg.Message{ID: 77, Message: "reply text to mock"}, nil
	}
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

func TestFunPlugin(t *testing.T) {
	p := New()
	if p.Name() != "fun" {
		t.Errorf("expected name 'fun', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 5 {
		t.Fatalf("expected 5 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	svc := &mockService{}
	peer := &tg.InputPeerChat{ChatID: 1234}

	// 1. .roll default (1-6)
	ctxRoll := &core.Context{
		Ctx:     context.Background(),
		Command: "roll",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["roll"].Handler(ctxRoll); err != nil {
		t.Fatalf("roll failed: %v", err)
	}
	if !strings.Contains(svc.edited, "You rolled a") || !strings.Contains(svc.edited, "1-6") {
		t.Errorf("unexpected roll output: %s", svc.edited)
	}

	// 2. .roll with max (1-20)
	ctxRoll20 := &core.Context{
		Ctx:     context.Background(),
		Command: "roll",
		Args:    []string{"20"},
		RawArgs: "20",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["roll"].Handler(ctxRoll20); err != nil {
		t.Fatalf("roll 20 failed: %v", err)
	}
	if !strings.Contains(svc.edited, "1-20") {
		t.Errorf("unexpected roll 20 output: %s", svc.edited)
	}

	// 3. .shrug
	ctxShrug := &core.Context{
		Ctx:     context.Background(),
		Command: "shrug",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["shrug"].Handler(ctxShrug); err != nil {
		t.Fatalf("shrug failed: %v", err)
	}
	if svc.edited != `¯\_(ツ)_/¯` {
		t.Errorf("unexpected shrug output: %s", svc.edited)
	}

	// 4. .tableflip
	ctxTableflip := &core.Context{
		Ctx:     context.Background(),
		Command: "tableflip",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["tableflip"].Handler(ctxTableflip); err != nil {
		t.Fatalf("tableflip failed: %v", err)
	}
	if svc.edited != `(╯°□°)╯︵ ┻━┻` {
		t.Errorf("unexpected tableflip output: %s", svc.edited)
	}

	// 5. .unflip
	ctxUnflip := &core.Context{
		Ctx:     context.Background(),
		Command: "unflip",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["unflip"].Handler(ctxUnflip); err != nil {
		t.Fatalf("unflip failed: %v", err)
	}
	if svc.edited != `┬─┬ノ( º _ ºノ)` {
		t.Errorf("unexpected unflip output: %s", svc.edited)
	}

	// 6. .mock with args
	ctxMock := &core.Context{
		Ctx:     context.Background(),
		Command: "mock",
		Args:    []string{"hello", "world"},
		RawArgs: "hello world",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["mock"].Handler(ctxMock); err != nil {
		t.Fatalf("mock failed: %v", err)
	}
	if svc.edited != "hElLo WoRlD" {
		t.Errorf("expected 'hElLo WoRlD', got '%s'", svc.edited)
	}

	// 7. .mock with reply
	ctxMockReply := &core.Context{
		Ctx:     context.Background(),
		Command: "mock",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{
			ID:        1,
			ReplyToID: 77,
		},
	}
	if err := cmdMap["mock"].Handler(ctxMockReply); err != nil {
		t.Fatalf("mock via reply failed: %v", err)
	}
	if svc.edited != "rEpLy TeXt To MoCk" {
		t.Errorf("expected 'rEpLy TeXt To MoCk', got '%s'", svc.edited)
	}

	// 8. .mock without args or reply
	ctxMockEmpty := &core.Context{
		Ctx:     context.Background(),
		Command: "mock",
		Svc:     svc,
		PeerID:  peer,
		Message: &core.Message{ID: 1},
	}
	if err := cmdMap["mock"].Handler(ctxMockEmpty); err == nil {
		t.Errorf("expected error for empty mock, got nil")
	}

	// 9. mockText unit tests
	if got := mockText("abc def"); got != "aBc DeF" {
		t.Errorf("mockText('abc def') = %s; want 'aBc DeF'", got)
	}
}
