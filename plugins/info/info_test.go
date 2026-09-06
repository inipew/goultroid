package info

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent          string
	resolvedUsers []tg.UserClass
	fullUser      *tg.UsersUserFull
	fullChat      *tg.MessagesChatFull
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
	if msgID == 77 {
		return &tg.Message{
			ID:     77,
			FromID: &tg.PeerUser{UserID: 8888},
		}, nil
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
	return &tg.Message{ID: 200}, nil
}
func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	if m.fullUser != nil {
		return m.fullUser, nil
	}
	return &tg.UsersUserFull{
		FullUser: tg.UserFull{
			About:            "Default Bio",
			CommonChatsCount: 2,
		},
		Users: []tg.UserClass{
			&tg.User{
				ID:        12345,
				FirstName: "Alice",
				LastName:  "Wonderland",
				Username:  "alice",
				Premium:   true,
				Verified:  true,
			},
		},
	}, nil
}
func (m *mockService) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return &tg.ContactsResolvedPeer{
		Users: m.resolvedUsers,
	}, nil
}
func (m *mockService) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return m.fullChat, nil
}

func TestInfoPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "info" {
		t.Errorf("expected name info, got %s", p.Name())
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
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "whois" {
		t.Errorf("expected whois, got %s", cmds[0].Name)
	}
	if cmds[0].Permission != core.PermissionEveryone {
		t.Errorf("expected whois to be PermissionEveryone, got %v", cmds[0].Permission)
	}
	if cmds[1].Name != "chatinfo" {
		t.Errorf("expected chatinfo, got %s", cmds[1].Name)
	}
	if cmds[1].Permission != core.PermissionSudo {
		t.Errorf("expected chatinfo to be PermissionSudo, got %v", cmds[1].Permission)
	}
	if cmds[2].Name != "id" {
		t.Errorf("expected id, got %s", cmds[2].Name)
	}
	if cmds[2].Permission != core.PermissionEveryone {
		t.Errorf("expected id to be PermissionEveryone, got %v", cmds[2].Permission)
	}
}

func TestWhois_Self(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".whois"},
		Svc:     svc,
	}

	err := p.handleWhois(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "Alice") {
		t.Errorf("expected Alice in sent message, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "12345") {
		t.Errorf("expected ID 12345, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "@alice") {
		t.Errorf("expected username @alice, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Premium") {
		t.Errorf("expected Premium flag, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Default Bio") {
		t.Errorf("expected Bio, got: %s", svc.sent)
	}
}

func TestWhois_Reply(t *testing.T) {
	p := New()
	svc := &mockService{
		fullUser: &tg.UsersUserFull{
			FullUser: tg.UserFull{About: "Replied Bio"},
			Users: []tg.UserClass{
				&tg.User{ID: 8888, FirstName: "Bob", Bot: true},
			},
		},
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, ReplyToID: 77},
		Svc:     svc,
	}

	err := p.handleWhois(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Bob") || !strings.Contains(svc.sent, "8888") {
		t.Errorf("expected Bob (8888), got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Is Bot") {
		t.Errorf("expected Is Bot, got: %s", svc.sent)
	}
}

func TestWhois_UsernameArg(t *testing.T) {
	p := New()
	svc := &mockService{
		resolvedUsers: []tg.UserClass{
			&tg.User{ID: 9999, AccessHash: 111, Username: "charlie"},
		},
		fullUser: &tg.UsersUserFull{
			FullUser: tg.UserFull{About: "Charlie Bio"},
			Users: []tg.UserClass{
				&tg.User{ID: 9999, FirstName: "Charlie", Username: "charlie"},
			},
		},
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".whois @charlie"},
		Args:    []string{"@charlie"},
		Svc:     svc,
	}

	err := p.handleWhois(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Charlie") {
		t.Errorf("expected Charlie, got: %s", svc.sent)
	}
}

func TestChatInfo_Channel(t *testing.T) {
	p := New()
	svc := &mockService{
		fullChat: &tg.MessagesChatFull{
			FullChat: &tg.ChannelFull{
				ParticipantsCount: 500,
				AdminsCount:       10,
				SlowmodeSeconds:   30,
				About:             "The official channel",
			},
		},
	}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChannel{ChannelID: 12345},
		Chat: &core.Chat{
			ID:    -10012345,
			Title: "Gophers Supergroup",
			Type:  "supergroup",
		},
		Svc: svc,
	}

	err := p.handleChatInfo(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "Gophers Supergroup") {
		t.Errorf("expected title, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Members</b>: 500") {
		t.Errorf("expected 500 members, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Admins</b>: 10") {
		t.Errorf("expected 10 admins, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Slowmode</b>: 30s") {
		t.Errorf("expected 30s slowmode, got: %s", svc.sent)
	}
}

func TestEscapeHTML(t *testing.T) {
	in := "<script>alert('test & fun')</script>"
	out := core.EscapeHTML(in)
	if out != "&lt;script&gt;alert('test &amp; fun')&lt;/script&gt;" {
		t.Errorf("unexpected escape result: %s", out)
	}
}

func TestHandleID(t *testing.T) {
	p := New()
	svc := &mockService{}

	ctx := &core.Context{
		Ctx: context.Background(),
		Chat: &core.Chat{
			ID:    -100123456789,
			Type:  "supergroup",
			Title: "Test Supergroup",
		},
		Sender: &core.User{
			ID: 55555,
		},
		Message: &core.Message{
			ID:        100,
			ReplyToID: 77,
		},
		PeerID: &tg.InputPeerChannel{ChannelID: 123456789},
		Svc:    svc,
	}

	if err := p.handleID(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "-100123456789") {
		t.Errorf("expected chat id in output, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "supergroup") {
		t.Errorf("expected chat type in output, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "55555") {
		t.Errorf("expected sender id in output, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "8888") {
		t.Errorf("expected reply sender id in output, got: %s", svc.sent)
	}
}
