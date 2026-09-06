package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/moderation"
)

type mockService struct {
	core.MockTelegramServicer
	sent          string
	errToReturn   error
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
		return &tg.Message{
			ID:     50,
			FromID: &tg.PeerUser{UserID: 8888},
			ReplyTo: &tg.MessageReplyHeader{
				ReplyToMsgID: 50,
				ReplyToTopID: 42,
				ForumTopic:   true,
			},
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
	m.banCalled = true
	return m.errToReturn
}
func (m *mockService) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.unbanCalled = true
	return m.errToReturn
}
func (m *mockService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.kickCalled = true
	return m.errToReturn
}
func (m *mockService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	m.muteCalled = true
	return m.errToReturn
}
func (m *mockService) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.unmuteCalled = true
	return m.errToReturn
}
func (m *mockService) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	m.purgeCalled = true
	m.purgeTopicID = topicID
	m.purgeCount = 12
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}
	return m.purgeCount, nil
}
func (m *mockService) PurgeMessagesSafe(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return m.PurgeMessages(ctx, peer, topicID, fromID, toID)
}
func (m *mockService) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	m.promoteCalled = true
	m.promoteTitle = title
	return m.errToReturn
}
func (m *mockService) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	m.demoteCalled = true
	return m.errToReturn
}
func (m *mockService) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return m.errToReturn
}

// ... rest of test file unchanged ...
