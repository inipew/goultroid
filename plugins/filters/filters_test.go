package filters

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type mockService struct {
	core.MockTelegramServicer
	sent   string
	sentTo tg.InputPeerClass
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	m.sentTo = peer
	return &tg.Message{ID: 10, Message: text}, nil
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
	if msgID == 88 {
		return &tg.Message{ID: 88, Message: "this is replied text"}, nil
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

func TestFiltersPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	p := New(db, func() core.TelegramServicer { return svc })

	if p.Name() != "filters" {
		t.Errorf("expected name 'filters', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	chatID := int64(12345)
	peer := &tg.InputPeerChat{ChatID: chatID}

	// 1. .filter without args -> error
	ctxNoArgs := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{},
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filter"].Handler(ctxNoArgs); err == nil {
		t.Errorf("expected error for missing args, got nil")
	}

	// 2. .filter with keyword and text -> success
	ctxSave := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{"rules", "Follow", "the", "group", "guidelines!"},
		RawArgs: "rules Follow the group guidelines!",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filter"].Handler(ctxSave); err != nil {
		t.Fatalf("failed to save filter: %v", err)
	}
	if !strings.Contains(svc.sent, "rules") {
		t.Errorf("expected confirmation message, got: %s", svc.sent)
	}

	// 3. .filter with reply to a message
	ctxSaveReply := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{"replied"},
		RawArgs: "replied",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
		Message: &core.Message{
			ReplyToID: 88,
		},
	}
	if err := cmdMap["filter"].Handler(ctxSaveReply); err != nil {
		t.Fatalf("failed to save filter via reply: %v", err)
	}

	// 4. .filters -> list
	ctxList := &core.Context{
		Ctx:     context.Background(),
		Command: "filters",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filters"].Handler(ctxList); err != nil {
		t.Fatalf("failed to list filters: %v", err)
	}
	if !strings.Contains(svc.sent, "rules") || !strings.Contains(svc.sent, "replied") {
		t.Errorf("expected active filters list, got: %s", svc.sent)
	}

	// 5. Incoming message evaluation:
	// 5a. Command message -> skipped
	svc.sent = ""
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: ".filter rules",
	}, true, "filter")
	if err != nil || svc.sent != "" {
		t.Errorf("expected command message to be ignored, got sent: %s", svc.sent)
	}

	// 5b. Outgoing message -> skipped
	svc.sent = ""
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "here are the rules",
		Out:     true,
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected outgoing message to be ignored, got sent: %s", svc.sent)
	}

	// 5c. Matching non-command message -> triggers auto-reply
	svc.sent = ""
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "What are the rules please?",
	}, false, "")
	if err != nil {
		t.Fatalf("unexpected error in HandleIncomingMessage: %v", err)
	}
	if svc.sent != "Follow the group guidelines!" {
		t.Errorf("expected filter auto-reply 'Follow the group guidelines!', got '%s'", svc.sent)
	}

	// 5d. Non-matching message -> no reply
	svc.sent = ""
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "just a normal random message",
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected no auto-reply, got: %s", svc.sent)
	}

	// 6. .stop <keyword>
	ctxStop := &core.Context{
		Ctx:     context.Background(),
		Command: "stop",
		Args:    []string{"rules"},
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["stop"].Handler(ctxStop); err != nil {
		t.Fatalf("failed to stop filter: %v", err)
	}
	if !strings.Contains(svc.sent, "stopped") {
		t.Errorf("expected stop confirmation, got: %s", svc.sent)
	}

	// 7. Test matchFilter helper logic
	testCases := []struct {
		text    string
		kw      string
		matched bool
	}{
		{"hello world", "hello", true},
		{"say hello!", "hello", true},
		{"othello is great", "hello", false},
		{"good morning everyone", "good morning", true},
		{"good mornings everyone", "good morning", false},
		{"where are the RULES?", "rules", true},
	}
	for _, tc := range testCases {
		if got := matchFilter(tc.text, tc.kw); got != tc.matched {
			t.Errorf("matchFilter(%q, %q) = %v; want %v", tc.text, tc.kw, got, tc.matched)
		}
	}
}
