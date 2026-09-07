package core

import (
	"context"

	"github.com/gotd/td/tg"
)

// MockTelegramServicer provides a default no-op implementation of TelegramServicer for unit tests.
// Test suites across plugins can embed this struct so interface additions never break them.
type MockTelegramServicer struct{}

var _ TelegramServicer = (*MockTelegramServicer)(nil)

func (m *MockTelegramServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return &tg.Message{ID: 1, Message: text}, nil
}
func (m *MockTelegramServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1, Message: text}, nil
}
func (m *MockTelegramServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (m *MockTelegramServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	return nil
}
func (m *MockTelegramServicer) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	return nil
}
func (m *MockTelegramServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	return nil
}
func (m *MockTelegramServicer) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	return nil
}
func (m *MockTelegramServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *MockTelegramServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}
func (m *MockTelegramServicer) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	return nil
}
func (m *MockTelegramServicer) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts InlineAnswerOptions) error {
	return nil
}
func (m *MockTelegramServicer) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *MockTelegramServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return nil, nil
}
func (m *MockTelegramServicer) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}
func (m *MockTelegramServicer) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}
func (m *MockTelegramServicer) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *MockTelegramServicer) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}
func (m *MockTelegramServicer) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *MockTelegramServicer) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *MockTelegramServicer) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, nil
}
func (m *MockTelegramServicer) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return nil
}
func (m *MockTelegramServicer) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return nil
}
func (m *MockTelegramServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}
func (m *MockTelegramServicer) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return &tg.UsersUserFull{}, nil
}
func (m *MockTelegramServicer) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return &tg.ContactsResolvedPeer{}, nil
}
func (m *MockTelegramServicer) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return &tg.MessagesChatFull{}, nil
}
func (m *MockTelegramServicer) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	return nil
}
func (m *MockTelegramServicer) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return nil
}
func (m *MockTelegramServicer) UploadProfilePhoto(ctx context.Context, filePath string) error {
	return nil
}
func (m *MockTelegramServicer) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	return 0, nil
}
func (m *MockTelegramServicer) GetDialogs(ctx context.Context, limit int) ([]*Chat, error) {
	return nil, nil
}
func (m *MockTelegramServicer) GetContacts(ctx context.Context) ([]*User, error) {
	return nil, nil
}
func (m *MockTelegramServicer) IsBotSent(msgID int) bool {
	return false
}
