package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent          string
	banCalled     bool
	unbanCalled   bool
	kickCalled    bool
	muteCalled    bool
	unmuteCalled  bool
	purgeCalled   bool
	purgeTopicID  int
	purgeCount    int
	promoteCalled bool
	promoteTitle  string
	demoteCalled  bool
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 999, Message: text}, nil
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
	if msgID == 50 {
		return &tg.Message{ID: 50, FromID: &tg.PeerUser{UserID: 8888}}, nil
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
	m.banCalled = true
	return nil
}
func (m *mockService) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.unbanCalled = true
	return nil
}
func (m *mockService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.kickCalled = true
	return nil
}
func (m *mockService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	m.muteCalled = true
	return nil
}
func (m *mockService) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.unmuteCalled = true
	return nil
}
func (m *mockService) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	m.purgeCalled = true
	m.purgeTopicID = topicID
	m.purgeCount = 12
	return 12, nil
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
func (m *mockService) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	m.promoteCalled = true
	m.promoteTitle = title
	return nil
}
func (m *mockService) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.demoteCalled = true
	return nil
}

func TestAdminPlugin(t *testing.T) {
	p := New()
	if p.Name() != "admin" {
		t.Errorf("expected name admin, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 8 {
		t.Fatalf("expected 8 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	ownerID := int64(1001)
	perms := core.NewPermissions(ownerID, []int64{2002})
	svc := &mockService{}

	baseCtx := &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: 2002},
		Chat:    &core.Chat{ID: -100123456, Type: "supergroup"},
		Message: &core.Message{ID: 100},
		Perms:   perms,
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 123456},
	}

	// 1. Ban by arg
	ctxBan := *baseCtx
	ctxBan.Args = []string{"5555", "spamming"}
	if err := cmdMap["ban"].Handler(&ctxBan); err != nil {
		t.Fatalf("unexpected error running ban: %v", err)
	}
	if !svc.banCalled || !strings.Contains(svc.sent, "Banned user") || !strings.Contains(svc.sent, "spamming") {
		t.Errorf("expected ban called and message, got: %s", svc.sent)
	}

	// 2. Ban owner rejected
	ctxBanOwner := *baseCtx
	ctxBanOwner.Args = []string{"1001"}
	if err := cmdMap["ban"].Handler(&ctxBanOwner); err == nil {
		t.Errorf("expected error when banning owner")
	}

	// 3. Unban
	ctxUnban := *baseCtx
	ctxUnban.Args = []string{"5555"}
	if err := cmdMap["unban"].Handler(&ctxUnban); err != nil {
		t.Fatalf("unexpected error running unban: %v", err)
	}
	if !svc.unbanCalled || !strings.Contains(svc.sent, "Unbanned user") {
		t.Errorf("expected unban called, got: %s", svc.sent)
	}

	// 4. Kick
	ctxKick := *baseCtx
	ctxKick.Args = []string{"5555"}
	if err := cmdMap["kick"].Handler(&ctxKick); err != nil {
		t.Fatalf("unexpected error running kick: %v", err)
	}
	if !svc.kickCalled || !strings.Contains(svc.sent, "Kicked user") {
		t.Errorf("expected kick called, got: %s", svc.sent)
	}

	// 5. Mute with duration (10m)
	ctxMute := *baseCtx
	ctxMute.Args = []string{"5555", "10m"}
	if err := cmdMap["mute"].Handler(&ctxMute); err != nil {
		t.Fatalf("unexpected error running mute: %v", err)
	}
	if !svc.muteCalled || !strings.Contains(svc.sent, "Muted user") || !strings.Contains(svc.sent, "10m") {
		t.Errorf("expected mute called with duration, got: %s", svc.sent)
	}

	// 6. Unmute
	ctxUnmute := *baseCtx
	ctxUnmute.Args = []string{"5555"}
	if err := cmdMap["unmute"].Handler(&ctxUnmute); err != nil {
		t.Fatalf("unexpected error running unmute: %v", err)
	}
	if !svc.unmuteCalled || !strings.Contains(svc.sent, "Unmuted user") {
		t.Errorf("expected unmute called, got: %s", svc.sent)
	}

	// 7. Purge in Forum Topic
	ctxPurge := *baseCtx
	ctxPurge.Message = &core.Message{
		ID:        200,
		ReplyToID: 50,
		TopicID:   42, // forum topic 42!
	}
	if err := cmdMap["purge"].Handler(&ctxPurge); err != nil {
		t.Fatalf("unexpected error running purge: %v", err)
	}
	if !svc.purgeCalled || svc.purgeTopicID != 42 {
		t.Errorf("expected purge in topic 42, got called=%v topic=%d", svc.purgeCalled, svc.purgeTopicID)
	}
	if !strings.Contains(svc.sent, "Purged 12 messages") || !strings.Contains(svc.sent, "42") {
		t.Errorf("expected topic purge confirmation, got: %s", svc.sent)
	}

	// 8. Promote with title
	ctxPromote := *baseCtx
	ctxPromote.Args = []string{"5555", "Mod", "Captain"}
	if err := cmdMap["promote"].Handler(&ctxPromote); err != nil {
		t.Fatalf("unexpected error running promote: %v", err)
	}
	if !svc.promoteCalled || svc.promoteTitle != "Mod Captain" || !strings.Contains(svc.sent, "Promoted user") {
		t.Errorf("expected promote called with title 'Mod Captain', got called=%v title=%q sent=%s", svc.promoteCalled, svc.promoteTitle, svc.sent)
	}

	// 9. Demote
	ctxDemote := *baseCtx
	ctxDemote.Args = []string{"5555"}
	if err := cmdMap["demote"].Handler(&ctxDemote); err != nil {
		t.Fatalf("unexpected error running demote: %v", err)
	}
	if !svc.demoteCalled || !strings.Contains(svc.sent, "Demoted admin") {
		t.Errorf("expected demote called, got: %s", svc.sent)
	}

	// 10. Demote owner rejected
	ctxDemoteOwner := *baseCtx
	ctxDemoteOwner.Args = []string{"1001"}
	if err := cmdMap["demote"].Handler(&ctxDemoteOwner); err == nil {
		t.Errorf("expected error when demoting owner")
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in  string
		exp time.Duration
	}{
		{"10m", 10 * time.Minute},
		{"2h", 2 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseDuration(c.in)
		if err != nil || got != c.exp {
			t.Errorf("parseDuration(%q) = %v (err=%v), expected %v", c.in, got, err, c.exp)
		}
	}
}
