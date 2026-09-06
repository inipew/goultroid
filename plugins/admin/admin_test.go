package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent         string
	errToReturn  error
	banCalled    bool
	unbanCalled  bool
	kickCalled   bool
	muteCalled   bool
	unmuteCalled bool
	purgeCalled  bool
	purgeTopicID int
}

func (m *mockService) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return &tg.Message{ID: 999}, nil
}
func (m *mockService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	m.sent = text
	return nil
}
func (m *mockService) GetMessage(_ context.Context, _ tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if msgID != 50 {
		return nil, nil
	}
	return &tg.Message{
		ID:     50,
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 50, ReplyToTopID: 42, ForumTopic: true},
	}, nil
}
func (m *mockService) BanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	m.banCalled = true
	return m.errToReturn
}
func (m *mockService) UnbanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	m.unbanCalled = true
	return m.errToReturn
}
func (m *mockService) KickUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	m.kickCalled = true
	return m.errToReturn
}
func (m *mockService) MuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	m.muteCalled = true
	return m.errToReturn
}
func (m *mockService) UnmuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	m.unmuteCalled = true
	return m.errToReturn
}
func (m *mockService) PurgeMessages(_ context.Context, _ tg.InputPeerClass, topicID, _, _ int) (int, error) {
	m.purgeCalled = true
	m.purgeTopicID = topicID
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}
	return 12, nil
}
func (m *mockService) PurgeMessagesSafe(ctx context.Context, peer tg.InputPeerClass, topicID, fromID, toID int) (int, error) {
	return m.PurgeMessages(ctx, peer, topicID, fromID, toID)
}

func newAdminTestContext(svc *mockService) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: 2002},
		Chat:    &core.Chat{ID: -100123456, Type: "supergroup"},
		Message: &core.Message{ID: 100, IsOutgoing: true},
		Perms:   core.NewPermissions(1001, []int64{2002}),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 123456},
	}
}

func TestAdminPlugin(t *testing.T) {
	p := New()
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cmdMap := make(map[string]core.Command)
	for _, cmd := range p.Commands() {
		cmdMap[cmd.Name] = cmd
	}
	if len(cmdMap) != 11 {
		t.Fatalf("expected 11 commands, got %d", len(cmdMap))
	}

	svc := &mockService{}
	ctx := newAdminTestContext(svc)
	ctx.Args = []string{"5555", "spamming"}
	if err := cmdMap["ban"].Handler(ctx); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if !svc.banCalled || !strings.Contains(svc.sent, "Banned user") {
		t.Fatalf("ban was not handled correctly: called=%v response=%q", svc.banCalled, svc.sent)
	}

	ctx.Args = []string{"5555"}
	if err := cmdMap["unban"].Handler(ctx); err != nil || !svc.unbanCalled {
		t.Fatalf("unban failed: err=%v called=%v", err, svc.unbanCalled)
	}
	if err := cmdMap["kick"].Handler(ctx); err != nil || !svc.kickCalled {
		t.Fatalf("kick failed: err=%v called=%v", err, svc.kickCalled)
	}
	ctx.Args = []string{"5555", "10m"}
	if err := cmdMap["mute"].Handler(ctx); err != nil || !svc.muteCalled {
		t.Fatalf("mute failed: err=%v called=%v", err, svc.muteCalled)
	}
	ctx.Args = []string{"5555"}
	if err := cmdMap["unmute"].Handler(ctx); err != nil || !svc.unmuteCalled {
		t.Fatalf("unmute failed: err=%v called=%v", err, svc.unmuteCalled)
	}
}

func TestAdminPlugin_PurgeForumTopic(t *testing.T) {
	p := New()
	cmdMap := make(map[string]core.Command)
	for _, cmd := range p.Commands() {
		cmdMap[cmd.Name] = cmd
	}

	svc := &mockService{}
	ctx := newAdminTestContext(svc)
	ctx.Message = &core.Message{ID: 200, ReplyToID: 50, TopicID: 42, IsOutgoing: true}

	if err := cmdMap["purge"].Handler(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if !svc.purgeCalled || svc.purgeTopicID != 42 {
		t.Fatalf("expected safe topic purge, called=%v topic=%d", svc.purgeCalled, svc.purgeTopicID)
	}
	if !strings.Contains(svc.sent, "Purged 12 messages") {
		t.Fatalf("unexpected purge response: %q", svc.sent)
	}
}

func TestAdminPlugin_GuardsAndErrors(t *testing.T) {
	p := New()
	cmdMap := make(map[string]core.Command)
	for _, cmd := range p.Commands() {
		cmdMap[cmd.Name] = cmd
	}

	svc := &mockService{}
	ctx := newAdminTestContext(svc)
	ctx.Chat = &core.Chat{ID: 123, Type: "private"}
	ctx.Args = []string{"2002"}
	if err := cmdMap["ban"].Handler(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported in private chat, got %v", err)
	}

	ctx.Chat = &core.Chat{ID: -100123456, Type: "supergroup"}
	ctx.Message = &core.Message{ID: 10, IsOutgoing: true}
	if err := cmdMap["purge"].Handler(ctx); err == nil {
		t.Fatal("expected purge without reply to fail")
	}

	svc.errToReturn = core.ErrPermissionDenied
	if err := cmdMap["ban"].Handler(ctx); err == nil {
		t.Fatal("expected permission error")
	}
}
