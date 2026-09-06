package afk

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if msgID == 99 {
		return &tg.Message{ID: 99, Out: true}, nil
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

func TestAFKPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)

	p := New(db, ownerID, func() core.TelegramServicer { return svc })
	if p.Name() != "afk" {
		t.Errorf("expected name afk, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 || cmds[0].Name != "afk" {
		t.Fatalf("expected 1 command afk, got %d", len(cmds))
	}

	ctx := context.Background()
	baseCtx := &core.Context{
		Ctx:     ctx,
		Sender:  &core.User{ID: ownerID},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Activate AFK with custom reason
	ctxAfk := *baseCtx
	ctxAfk.Args = []string{"taking", "a", "nap"}
	ctxAfk.RawArgs = "taking a nap"
	if err := cmds[0].Handler(&ctxAfk); err != nil {
		t.Fatalf("unexpected error running afk command: %v", err)
	}
	if !strings.Contains(svc.sent, "taking a nap") || !strings.Contains(svc.sent, "AFK Mode Activated") {
		t.Errorf("expected activation message, got: %s", svc.sent)
	}

	// Verify DB state
	status, err := db.GetAFK(ctx, ownerID)
	if err != nil || status == nil || !status.IsAFK {
		t.Fatalf("expected owner to be AFK in DB, got: %+v (err=%v)", status, err)
	}

	// 2. Incoming DM from user 2002 -> triggers auto-reply
	dmMsg := &tg.Message{
		ID:     20,
		Out:    false,
		FromID: &tg.PeerUser{UserID: 2002},
		PeerID: &tg.PeerUser{UserID: 2002},
	}
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			2002: {ID: 2002, AccessHash: 123},
		},
	}

	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, dmMsg, false, ""); err != nil {
		t.Fatalf("unexpected error on handle message: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") || !strings.Contains(svc.sent, "taking a nap") {
		t.Errorf("expected auto-reply, got: %s", svc.sent)
	}

	// 3. Second message from same user immediately -> rate limited (no reply)
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, dmMsg, false, ""); err != nil {
		t.Fatalf("unexpected error on second handle: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected rate limiting to prevent duplicate reply, but got: %s", svc.sent)
	}

	// 4. Outgoing message from owner with .afk command -> should NOT turn off AFK
	ownerCmdMsg := &tg.Message{
		ID:      21,
		Out:     true,
		Message: ".afk another reason",
		PeerID:  &tg.PeerUser{UserID: 2002},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, ownerCmdMsg, true, "afk"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected no deactivation message on afk command, got: %s", svc.sent)
	}
	status, _ = db.GetAFK(ctx, ownerID)
	if !status.IsAFK {
		t.Errorf("owner should still be AFK")
	}

	// 5. Normal outgoing message from owner -> deactivates AFK!
	ownerChatMsg := &tg.Message{
		ID:      22,
		Out:     true,
		Message: "I am back now!",
		PeerID:  &tg.PeerUser{UserID: 2002},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, ownerChatMsg, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Welcome back") || !strings.Contains(svc.sent, "turned off") {
		t.Errorf("expected welcome back message, got: %s", svc.sent)
	}

	// Verify DB state is now deactivated
	status, _ = db.GetAFK(ctx, ownerID)
	if status.IsAFK {
		t.Errorf("expected AFK to be turned off in DB")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m 5s"},
		{3665 * time.Second, "1h 1m 5s"},
		{90000 * time.Second, "1d 1h"},
	}

	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.expected {
			t.Errorf("formatDuration(%v) = %q, expected %q", tc.d, got, tc.expected)
		}
	}
}

func TestAFKPlugin_Cleanup(t *testing.T) {
	p := New(nil, 100, nil)

	// Add recent and old timestamps
	p.cooldown.Store(int64(101), time.Now())
	p.cooldown.Store(int64(102), time.Now().Add(-20*time.Minute))

	purged := p.Cleanup(10 * time.Minute)
	if purged != 1 {
		t.Errorf("expected 1 record purged, got %d", purged)
	}

	// Verify 101 remains
	if _, ok := p.cooldown.Load(int64(101)); !ok {
		t.Errorf("expected 101 to remain in cooldown map")
	}
	// Verify 102 was removed
	if _, ok := p.cooldown.Load(int64(102)); ok {
		t.Errorf("expected 102 to be purged from cooldown map")
	}
}
