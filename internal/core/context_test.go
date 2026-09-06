package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"
)

// mockTelegramServicer implements TelegramServicer for unit tests.
type mockTelegramServicer struct {
	MockTelegramServicer
	sentText     string
	editedText   string
	deletedIDs   []int
	reactEmoji   string
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

func (m *mockTelegramServicer) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockTelegramServicer) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockTelegramServicer) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 5, nil
}
func (m *mockTelegramServicer) PurgeMessagesSafe(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return m.PurgeMessages(ctx, peer, topicID, fromID, toID)
}
func (m *mockTelegramServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 777}, nil
}
func (m *mockTelegramServicer) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return &tg.UsersUserFull{}, nil
}
func (m *mockTelegramServicer) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	if username == "targetuser" {
		return &tg.ContactsResolvedPeer{
			Users: []tg.UserClass{
				&tg.User{ID: 9999, AccessHash: 55555},
			},
		}, nil
	}
	return &tg.ContactsResolvedPeer{}, nil
}
func (m *mockTelegramServicer) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return &tg.MessagesChatFull{}, nil
}
func (m *mockTelegramServicer) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return nil
}
func (m *mockTelegramServicer) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
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

	// ExecutionSource helpers
	ctx.Source = ExecutionInteractive
	if !ctx.IsInteractive() || ctx.IsScheduled() || ctx.IsAssistant() || ctx.IsAddon() || ctx.IsSystem() {
		t.Errorf("expected only IsInteractive to be true")
	}
	ctx.Source = ExecutionScheduled
	if ctx.IsInteractive() || !ctx.IsScheduled() {
		t.Errorf("expected IsScheduled to be true")
	}
	ctx.Source = ExecutionAssistant
	if !ctx.IsAssistant() {
		t.Errorf("expected IsAssistant to be true")
	}
	ctx.Source = ExecutionAddon
	if !ctx.IsAddon() {
		t.Errorf("expected IsAddon to be true")
	}
	ctx.Source = ExecutionSystem
	if !ctx.IsSystem() {
		t.Errorf("expected IsSystem to be true")
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
	if ctx.Message.ID != 10 {
		t.Errorf("expected context message ID to remain immutable (10), got %d", ctx.Message.ID)
	}
	if ctx.LastResponseID != 42 {
		t.Errorf("expected LastResponseID to be 42, got %d", ctx.LastResponseID)
	}

	// Test Edit (should edit the sent reply with ID 42)
	if err := ctx.Edit("edited text"); err != nil {
		t.Fatalf("unexpected error editing: %v", err)
	}
	if mock.editedText != "edited text" {
		t.Errorf("expected edited text 'edited text', got %q", mock.editedText)
	}

	// Test Delete (should delete the original command message with ID 10)
	if err := ctx.Delete(); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 10 {
		t.Errorf("expected deleted ID 10 for original message, got %v", mock.deletedIDs)
	}

	// Test DeleteResponse (should delete the bot reply with ID 42)
	mock.deletedIDs = nil
	if err := ctx.DeleteResponse(); err != nil {
		t.Fatalf("unexpected error deleting response: %v", err)
	}
	if len(mock.deletedIDs) != 1 || mock.deletedIDs[0] != 42 {
		t.Errorf("expected deleted ID 42 for bot response, got %v", mock.deletedIDs)
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

func TestContext_ModerationActions(t *testing.T) {
	mock := &mockTelegramServicer{
		messageToGet: &tg.Message{
			ID:      50,
			Message: "original message",
			FromID:  &tg.PeerUser{UserID: 8888},
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 50, ReplyToTopID: 42, ForumTopic: true},
		},
	}
	user := &tg.InputPeerUser{UserID: 8888}

	ctx := &Context{
		Ctx: context.Background(),
		Message: &Message{
			ID:        100,
			ReplyToID: 50,
			TopicID:   42,
		},
		Svc:    mock,
		PeerID: &tg.InputPeerChannel{ChannelID: 123},
	}

	// 1. TopicID helper
	if ctx.TopicID() != 42 {
		t.Errorf("expected TopicID 42, got %d", ctx.TopicID())
	}

	// 2. Ban & Unban
	if err := ctx.Ban(user, 0); err != nil {
		t.Errorf("unexpected error in Ban: %v", err)
	}
	if err := ctx.Unban(user); err != nil {
		t.Errorf("unexpected error in Unban: %v", err)
	}

	// 3. Kick
	if err := ctx.Kick(user); err != nil {
		t.Errorf("unexpected error in Kick: %v", err)
	}

	// 4. Mute & Unmute
	if err := ctx.Mute(user, 3600); err != nil {
		t.Errorf("unexpected error in Mute: %v", err)
	}
	if err := ctx.Unmute(user); err != nil {
		t.Errorf("unexpected error in Unmute: %v", err)
	}

	// 5. Purge
	count, err := ctx.Purge()
	if err != nil {
		t.Errorf("unexpected error in Purge: %v", err)
	}
	if count != 5 {
		t.Errorf("expected purge count 5, got %d", count)
	}

	// 6. ResolveTargetUser via args
	ctxWithArgs := *ctx
	ctxWithArgs.Args = []string{"7777"}
	p, uid, err := ctxWithArgs.ResolveTargetUser()
	if err != nil || uid != 7777 || p == nil {
		t.Errorf("failed to resolve target from args: uid=%d, err=%v", uid, err)
	}

	// 7. ResolveTargetUser via reply
	ctxWithReply := *ctx
	ctxWithReply.Args = nil
	p, uid, err = ctxWithReply.ResolveTargetUser()
	if err != nil || uid != 8888 || p == nil {
		t.Errorf("failed to resolve target from reply: uid=%d, err=%v", uid, err)
	}

	// 8. ResolveTargetUser via username
	ctxWithUsername := *ctx
	ctxWithUsername.Args = []string{"@targetuser"}
	p, uid, err = ctxWithUsername.ResolveTargetUser()
	if err != nil || uid != 9999 || p == nil {
		t.Errorf("failed to resolve target from username: uid=%d, err=%v", uid, err)
	}
	if inputUser, ok := p.(*tg.InputPeerUser); !ok || inputUser.AccessHash != 55555 {
		t.Errorf("expected InputPeerUser with access hash 55555, got: %v", p)
	}
}

func TestContext_SendMedia(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 123},
		Svc:    mock,
	}

	if err := ctx.SendFile("/tmp/test.txt", "caption"); err != nil {
		t.Errorf("SendFile failed: %v", err)
	}
	if err := ctx.SendPhoto("/tmp/test.jpg", "photo caption"); err != nil {
		t.Errorf("SendPhoto failed: %v", err)
	}
	if err := ctx.SendSticker("/tmp/test.webp"); err != nil {
		t.Errorf("SendSticker failed: %v", err)
	}
	if err := ctx.SendAudio("/tmp/test.mp3", "audio caption"); err != nil {
		t.Errorf("SendAudio failed: %v", err)
	}

	// Service nil check
	nilCtx := &Context{Ctx: context.Background(), PeerID: &tg.InputPeerChat{ChatID: 123}}
	if err := nilCtx.SendFile("a", "b"); err == nil {
		t.Errorf("expected error with nil service")
	}
	// Peer nil check
	nilPeerCtx := &Context{Ctx: context.Background(), Svc: mock}
	if err := nilPeerCtx.SendFile("a", "b"); err == nil {
		t.Errorf("expected error with nil peer")
	}
}

func TestContext_InfoHelpers(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 123},
		Svc:    mock,
	}

	fu, err := ctx.GetFullUser(&tg.InputUserSelf{})
	if err != nil || fu == nil {
		t.Errorf("GetFullUser failed: %v", err)
	}

	rp, err := ctx.ResolveUsername("testuser")
	if err != nil || rp == nil {
		t.Errorf("ResolveUsername failed: %v", err)
	}

	fc, err := ctx.GetFullChat()
	if err != nil || fc == nil {
		t.Errorf("GetFullChat failed: %v", err)
	}

	// Service nil check
	nilCtx := &Context{Ctx: context.Background(), PeerID: &tg.InputPeerChat{ChatID: 123}}
	if _, err := nilCtx.GetFullUser(&tg.InputUserSelf{}); err == nil {
		t.Errorf("expected error with nil service")
	}
	if _, err := nilCtx.ResolveUsername("user"); err == nil {
		t.Errorf("expected error with nil service")
	}
	if _, err := nilCtx.GetFullChat(); err == nil {
		t.Errorf("expected error with nil service")
	}
}

func TestDownloadMedia_PathTraversal(t *testing.T) {
	mock := &mockTelegramServicer{}
	tmpDir := t.TempDir()

	ctx := &Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 123},
		Svc:    mock,
		Message: &Message{
			ID: 1,
			Media: &MediaInfo{
				Type:     "document",
				FileName: "../../malicious.sh",
				Location: &tg.InputDocumentFileLocation{},
			},
		},
	}

	savedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		t.Fatalf("DownloadMedia failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "malicious.sh")
	if savedPath != expectedPath {
		t.Errorf("expected sanitized path %q, got %q", expectedPath, savedPath)
	}

	// Verify fallback when cleaned name is empty or dot
	ctx.Message.Media.FileName = "../.."
	savedFallback, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		t.Fatalf("DownloadMedia fallback failed: %v", err)
	}
	if filepath.Dir(savedFallback) != tmpDir {
		t.Errorf("expected fallback path to be in %q, got %q", tmpDir, savedFallback)
	}
}

func TestContext_PeerResolver(t *testing.T) {
	mockResolver := &MockPeerResolver{
		UserID:   999888,
		UserPeer: &tg.InputPeerUser{UserID: 999888, AccessHash: 77777},
		ChatPeer: &tg.InputPeerChannel{ChannelID: 555444, AccessHash: 33333},
	}

	ctx := &Context{
		Ctx:      context.Background(),
		Resolver: mockResolver,
		Args:     []string{"@alice"},
	}

	// 1. ResolveUser
	uPeer, uid, err := ctx.ResolveUser("@alice")
	if err != nil {
		t.Fatalf("ResolveUser failed: %v", err)
	}
	if uid != 999888 {
		t.Errorf("expected uid 999888, got %d", uid)
	}
	if up, ok := uPeer.(*tg.InputPeerUser); !ok || up.AccessHash != 77777 {
		t.Errorf("expected user peer with access hash 77777, got %+v", uPeer)
	}

	// 2. ResolveChat
	cPeer, err := ctx.ResolveChat("-100555444")
	if err != nil {
		t.Fatalf("ResolveChat failed: %v", err)
	}
	if cp, ok := cPeer.(*tg.InputPeerChannel); !ok || cp.AccessHash != 33333 {
		t.Errorf("expected channel peer with access hash 33333, got %+v", cPeer)
	}

	// 3. ResolveTargetUser with username using resolver
	resolvedTarget, tid, err := ctx.ResolveTargetUser()
	if err != nil {
		t.Fatalf("ResolveTargetUser failed: %v", err)
	}
	if tid != 999888 {
		t.Errorf("expected target ID 999888, got %d", tid)
	}
	if targetUser, ok := resolvedTarget.(*tg.InputPeerUser); !ok || targetUser.AccessHash != 77777 {
		t.Errorf("expected target user peer with access hash 77777, got %+v", resolvedTarget)
	}

	// 4. ResolveTargetUser with numeric ID using resolver
	ctx.Args = []string{"999888"}
	resolvedNumeric, nid, err := ctx.ResolveTargetUser()
	if err != nil {
		t.Fatalf("ResolveTargetUser with numeric ID failed: %v", err)
	}
	if nid != 999888 {
		t.Errorf("expected target ID 999888, got %d", nid)
	}
	if targetUser, ok := resolvedNumeric.(*tg.InputPeerUser); !ok || targetUser.AccessHash != 77777 {
		t.Errorf("expected target user peer with access hash 77777, got %+v", resolvedNumeric)
	}
}

func TestMessage_MentionsAndURLs(t *testing.T) {
	// 1. Mentions and URLs from Telegram Entities
	msg := &Message{
		Text: "Hello @alice and @bob check https://example.com and click here!",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 6, Length: 6},                              // @alice
			&tg.MessageEntityMentionName{Offset: 17, Length: 4, UserID: 123456},         // @bob / user ID 123456
			&tg.MessageEntityURL{Offset: 28, Length: 19},                                // https://example.com
			&tg.MessageEntityTextURL{Offset: 52, Length: 10, URL: "https://golang.org"}, // click here
		},
	}
	ctx := &Context{Message: msg}

	mentions := ctx.Mentions()
	if len(mentions) != 2 || mentions[0] != "@alice" || mentions[1] != "123456" {
		t.Errorf("unexpected mentions: %v", mentions)
	}

	urls := ctx.URLs()
	if len(urls) != 2 || urls[0] != "https://example.com" || urls[1] != "https://golang.org" {
		t.Errorf("unexpected urls: %v", urls)
	}

	// 2. Plain-text fallback when Entities is empty
	plainMsg := &Message{
		Text: "Hey @charlie go to https://google.com or http://test.org now",
	}
	plainCtx := &Context{Message: plainMsg}

	plainMentions := plainCtx.Mentions()
	if len(plainMentions) != 1 || plainMentions[0] != "@charlie" {
		t.Errorf("unexpected plain mentions: %v", plainMentions)
	}

	plainURLs := plainCtx.URLs()
	if len(plainURLs) != 2 || plainURLs[0] != "https://google.com" || plainURLs[1] != "http://test.org" {
		t.Errorf("unexpected plain urls: %v", plainURLs)
	}
}

func TestContext_SubFacades(t *testing.T) {
	mock := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Svc:    mock,
	}

	// 1. MessagesFacade
	if err := ctx.Messages().Reply("hello from facade"); err != nil {
		t.Errorf("unexpected error in Messages().Reply: %v", err)
	}
	if mock.sentText != "hello from facade" {
		t.Errorf("expected sentText 'hello from facade', got %q", mock.sentText)
	}
	if ctx.LastResponseID != 42 {
		t.Errorf("expected LastResponseID 42, got %d", ctx.LastResponseID)
	}

	if err := ctx.Messages().Edit("edited from facade"); err != nil {
		t.Errorf("unexpected error in Messages().Edit: %v", err)
	}
	if mock.editedText != "edited from facade" {
		t.Errorf("expected editedText 'edited from facade', got %q", mock.editedText)
	}

	// 2. AdminFacade
	if err := ctx.Admin().Ban(&tg.InputPeerUser{UserID: 999}, 0); err != nil {
		t.Errorf("unexpected error in Admin().Ban: %v", err)
	}
	if err := ctx.Admin().Kick(&tg.InputPeerUser{UserID: 999}); err != nil {
		t.Errorf("unexpected error in Admin().Kick: %v", err)
	}

	// 3. MediaFacade
	if err := ctx.Media().SendPhoto("fake.jpg", "caption"); err != nil {
		t.Errorf("unexpected error in Media().SendPhoto: %v", err)
	}

	// 4. PeerFacade with nil resolver returns ErrUnsupported
	_, _, err := ctx.Peer().ResolveUser("123")
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("expected ErrUnsupported, got %v", err)
	}

	// 5. Zero-value Context should not panic
	emptyCtx := &Context{}
	if err := emptyCtx.Messages().Reply("test"); err == nil {
		t.Errorf("expected error from empty context, got nil")
	}
	if err := emptyCtx.Admin().Ban(&tg.InputPeerUser{UserID: 1}, 0); err == nil {
		t.Errorf("expected error from empty context, got nil")
	}
	if _, err := emptyCtx.Media().DownloadMedia("/tmp"); err == nil {
		t.Errorf("expected error from empty context, got nil")
	}
}

type mockLocalizer struct{}

func (m *mockLocalizer) T(key string, args ...any) string {
	if key == "hello" {
		return "Hello World"
	}
	return key
}

func TestContext_LocalizationAndMarkup(t *testing.T) {
	// 1. Nil localizer fallback
	ctx := &Context{}
	if val := ctx.T("raw.key"); val != "raw.key" {
		t.Errorf("expected raw key fallback, got: %s", val)
	}

	// 2. Active localizer
	ctx.Localizer = &mockLocalizer{}
	if val := ctx.T("hello"); val != "Hello World" {
		t.Errorf("expected localized text, got: %s", val)
	}

	// 3. Markup reply
	mockSvc := &MockTelegramServicer{}
	ctx.Svc = mockSvc
	ctx.PeerID = &tg.InputPeerSelf{}

	if err := ctx.ReplyMarkup("hello", &tg.ReplyInlineMarkup{}); err != nil {
		t.Errorf("unexpected error in ReplyMarkup: %v", err)
	}
	if ctx.LastResponseID != 1 {
		t.Errorf("expected LastResponseID to be 1, got %d", ctx.LastResponseID)
	}
	if err := ctx.EditMarkup("updated", &tg.ReplyInlineMarkup{}); err != nil {
		t.Errorf("unexpected error in EditMarkup: %v", err)
	}
}
