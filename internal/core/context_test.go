package core

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

// mockTelegramServicer implements TelegramServicer for unit tests.
type mockTelegramServicer struct {
	sentText    string
	editedText  string
	deletedIDs  []int
	reactEmoji  string
	messageToGet *tg.Message

	errToSend   error
	errToEdit   error
	errToDelete error
	errToReact  error
	errToGet    error
}

func (m *mockTelegramServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if m.errToSend != nil {
		return nil, m.errToSend
	}
	m.sentText = text
	return &tg.Message{ID: 42, Message: text}, nil
}

func (m *mockTelegramServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if m.errToEdit != nil {
		return m.errToEdit
	}
	m.editedText = text
	return nil
}

func (m *mockTelegramServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if m.errToDelete != nil {
		return m.errToDelete
	}
	m.deletedIDs = msgIDs
	return nil
}

func (m *mockTelegramServicer) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	if m.errToReact != nil {
		return m.errToReact
	}
	m.reactEmoji = emoji
	return nil
}

func (m *mockTelegramServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if m.errToGet != nil {
		return nil, m.errToGet
	}
	return m.messageToGet, nil
}

func (m *mockTelegramServicer) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}

func (m *mockTelegramServicer) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}

func (m *mockTelegramServicer) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return nil
}

func (m *mockTelegramServicer) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}

func TestContext_Helpers(t *testing.T) {
	ctx := &Context{
		Chat:   &Chat{Type: "private"},
		Sender: &User{ID: 100},
		Perms:  NewPermissions(100, []int64{200}),
	}

	if !ctx.IsPrivate() {
		t.Errorf("expected private chat")
	}
	if ctx.IsGroup() {
		t.Errorf("expected not group")
	}
	if ctx.IsChannel() {
		t.Errorf("expected not channel")
	}
	if !ctx.IsOwner() {
		t.Errorf("expected owner")
	}
	if !ctx.IsSudo() {
		t.Errorf("expected sudo")
	}

	ctx.Chat.Type = "supergroup"
	if !ctx.IsGroup() {
		t.Errorf("expected group")
	}

	ctx.Chat.Type = "channel"
	if !ctx.IsChannel() {
		t.Errorf("expected channel")
	}

	ctx.Sender.ID = 200
	if ctx.IsOwner() {
		t.Errorf("sudo should not be owner")
	}
	if !ctx.IsSudo() {
		t.Errorf("expected sudo")
	}

	ctx.Sender.ID = 300
	if ctx.IsOwner() || ctx.IsSudo() {
		t.Errorf("normal user should not be owner or sudo")
	}
}

func TestContext_Actions(t *testing.T) {
	mock := &mockTelegramServicer{}
	peer := &tg.InputPeerSelf{}

	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 10, ReplyToID: 5},
		Svc:     mock,
		PeerID:  peer,
	}

	// Test Reply
	if err := ctx.Reply("hello world"); err != nil {
		t.Fatalf("unexpected error replying: %v", err)
	}
	if mock.sentText != "hello world" {
		t.Errorf("expected sent text 'hello world', got %q", mock.sentText)
	}
	if ctx.Message.ID != 42 {
		t.Errorf("expected context message ID to update to 42, got %d", ctx.Message.ID)
	}

	// Test Edit
	if err := ctx.Edit("edited text"); err != nil {
		t.Fatalf("unexpected error editing: %v", err)
	}
	if mock.editedText != "edited text" {
		t.Errorf("expected edited text 'edited text', got %q", mock.editedText)
	}

	// Test Delete
	if err := ctx.Delete(); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 42 {
		t.Errorf("expected deleted ID 42, got %v", mock.deletedIDs)
	}

	// Test React
	if err := ctx.React("🔥"); err != nil {
		t.Fatalf("unexpected error reacting: %v", err)
	}
	if mock.reactEmoji != "🔥" {
		t.Errorf("expected react emoji '🔥', got %q", mock.reactEmoji)
	}

	// Test GetReply
	mock.messageToGet = &tg.Message{
		ID:      5,
		Message: "original message",
		Date:    1700000000,
	}
	repliedMsg, err := ctx.GetReply()
	if err != nil {
		t.Fatalf("unexpected error in GetReply: %v", err)
	}
	if repliedMsg == nil || repliedMsg.ID != 5 || repliedMsg.Text != "original message" {
		t.Errorf("unexpected replied msg: %+v", repliedMsg)
	}

	// Test nil Svc or Peer error handling
	ctxNilSvc := &Context{Ctx: context.Background(), PeerID: peer}
	if err := ctxNilSvc.Reply("test"); err == nil {
		t.Errorf("expected error when Svc is nil")
	}
	ctxNilPeer := &Context{Ctx: context.Background(), Svc: mock}
	if err := ctxNilPeer.Reply("test"); err == nil {
		t.Errorf("expected error when PeerID is nil")
	}
}

func TestContext_ActionErrors(t *testing.T) {
	mock := &mockTelegramServicer{
		errToSend: errors.New("send failed"),
		errToEdit: errors.New("edit failed"),
	}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 10},
		Svc:     mock,
		PeerID:  &tg.InputPeerSelf{},
	}

	if err := ctx.Reply("test"); err == nil {
		t.Errorf("expected error when send fails")
	}
	if err := ctx.Edit("test"); err == nil {
		t.Errorf("expected error when edit fails")
	}
}

func TestContext_ExtendedActions(t *testing.T) {
	mock := &mockTelegramServicer{}
	peer := &tg.InputPeerSelf{}

	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID: 10,
			Media: &MediaInfo{
				Type:     "photo",
				FileName: "test.jpg",
				Location: &tg.InputPhotoFileLocation{ID: 123},
			},
		},
		Svc:    mock,
		PeerID: peer,
	}

	// 1. Pin & Unpin
	if err := ctx.Pin(true); err != nil {
		t.Errorf("unexpected error in Pin: %v", err)
	}
	if err := ctx.Unpin(); err != nil {
		t.Errorf("unexpected error in Unpin: %v", err)
	}

	// 2. Forward & ForwardToSelf
	if err := ctx.Forward(peer); err != nil {
		t.Errorf("unexpected error in Forward: %v", err)
	}
	if err := ctx.ForwardToSelf(); err != nil {
		t.Errorf("unexpected error in ForwardToSelf: %v", err)
	}

	// 3. DownloadMedia
	tmpDir := t.TempDir()
	path, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		t.Errorf("unexpected error in DownloadMedia: %v", err)
	}
	if path == "" {
		t.Errorf("expected non-empty downloaded path")
	}

	// 4. HasMedia
	if !ctx.Message.HasMedia() {
		t.Errorf("expected HasMedia to be true")
	}
}

