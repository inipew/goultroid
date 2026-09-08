package notes

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
	sent string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
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
	if msgID == 77 {
		return &tg.Message{ID: 77, Message: "Content from replied message"}, nil
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

func TestNotesPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	p := New(repo)
	if p.Name() != "notes" {
		t.Errorf("expected name notes, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	svc := &mockService{}
	baseCtx := &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: 1001},
		Chat:    &core.Chat{ID: -100999},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: -100999},
	}

	// 1. notes list initially empty
	ctxList := *baseCtx
	if err := cmdMap["notes"].Handler(&ctxList); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "No notes saved") {
		t.Errorf("expected empty notes message, got: %s", svc.sent)
	}

	// 2. save without args -> error
	ctxNoArg := *baseCtx
	if err := cmdMap["save"].Handler(&ctxNoArg); err == nil {
		t.Errorf("expected error on save with no args")
	}

	// 3. save with inline args
	ctxSaveInline := *baseCtx
	ctxSaveInline.Args = []string{"rules", "Be", "kind", "to", "all."}
	ctxSaveInline.RawArgs = "rules Be kind to all."
	if err := cmdMap["save"].Handler(&ctxSaveInline); err != nil {
		t.Fatalf("unexpected error on save: %v", err)
	}
	if !strings.Contains(svc.sent, "saved successfully") {
		t.Errorf("expected success message, got: %s", svc.sent)
	}

	// 4. get saved note
	ctxGet := *baseCtx
	ctxGet.Args = []string{"rules"}
	if err := cmdMap["get"].Handler(&ctxGet); err != nil {
		t.Fatalf("unexpected error on get: %v", err)
	}
	if svc.sent != "Be kind to all." {
		t.Errorf("expected note content, got: %s", svc.sent)
	}

	// 5. get non-existent note
	ctxGetMissing := *baseCtx
	ctxGetMissing.Args = []string{"unknown"}
	if err := cmdMap["get"].Handler(&ctxGetMissing); err == nil {
		t.Errorf("expected error getting non-existent note")
	}

	// 6. save via reply
	ctxSaveReply := *baseCtx
	ctxSaveReply.Args = []string{"faq"}
	ctxSaveReply.RawArgs = "faq"
	ctxSaveReply.Message = &core.Message{ID: 2, ReplyToID: 77}
	if err := cmdMap["save"].Handler(&ctxSaveReply); err != nil {
		t.Fatalf("unexpected error saving via reply: %v", err)
	}
	if !strings.Contains(svc.sent, "saved successfully") {
		t.Errorf("expected success message, got: %s", svc.sent)
	}

	// 7. get note saved via reply
	ctxGetReply := *baseCtx
	ctxGetReply.Args = []string{"faq"}
	if err := cmdMap["get"].Handler(&ctxGetReply); err != nil {
		t.Fatalf("unexpected error getting faq: %v", err)
	}
	if svc.sent != "Content from replied message" {
		t.Errorf("expected content from replied message, got: %s", svc.sent)
	}

	// 8. list notes (now has 2 notes)
	if err := cmdMap["notes"].Handler(&ctxList); err != nil {
		t.Fatalf("unexpected error listing notes: %v", err)
	}
	if !strings.Contains(svc.sent, "rules") || !strings.Contains(svc.sent, "faq") {
		t.Errorf("expected notes to contain rules and faq, got: %s", svc.sent)
	}

	// 9. clear note
	ctxClear := *baseCtx
	ctxClear.Args = []string{"rules"}
	if err := cmdMap["clear"].Handler(&ctxClear); err != nil {
		t.Fatalf("unexpected error clearing note: %v", err)
	}
	if !strings.Contains(svc.sent, "deleted") {
		t.Errorf("expected deleted message, got: %s", svc.sent)
	}

	// 10. clear non-existent
	if err := cmdMap["clear"].Handler(&ctxClear); err == nil {
		t.Errorf("expected error clearing non-existent note")
	}
}
