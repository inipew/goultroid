package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/execution"
)

// mockTelegramServicer implements TelegramServicer for unit tests.
type mockTelegramServicer struct {
	MockTelegramServicer
	sentText     string
	editedText   string
	deletedIDs   []int
	reactEmoji   string
	messageToGet *tg.Message
	getCalls     int

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
	m.getCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

type displayUserTelegramServicer struct{ mockTelegramServicer }

func (m *displayUserTelegramServicer) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return &tg.UsersUserFull{Users: []tg.UserClass{
		&tg.User{ID: 42, FirstName: "Alice &", LastName: "Bob", Username: "alice"},
	}}, nil
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

type contextTestResolver struct {
	svc TelegramServicer
}

func (r *contextTestResolver) Resolve(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	p, _, err := r.ResolveUser(ctx, ref)
	return p, err
}

func (r *contextTestResolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	if uid, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return &tg.InputPeerUser{UserID: uid, AccessHash: 12345}, uid, nil
	}
	if r.svc != nil {
		resolved, err := r.svc.ResolveUsername(ctx, strings.TrimPrefix(ref, "@"))
		if err == nil && resolved != nil {
			for _, u := range resolved.Users {
				if user, ok := u.(*tg.User); ok {
					return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, user.ID, nil
				}
			}
		}
	}
	return nil, 0, fmt.Errorf("user not found: %s", ref)
}

func (r *contextTestResolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	return &tg.InputPeerChat{ChatID: 123}, nil
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
		Svc:      mock,
		PeerID:   &tg.InputPeerChannel{ChannelID: 123},
		Resolver: &contextTestResolver{svc: mock},
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

func TestContext_DisplayUser(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Svc: &displayUserTelegramServicer{}}
	peer := &tg.InputPeerUser{UserID: 42, AccessHash: 99}

	if got := ctx.DisplayUser(peer, 42); got != `<a href="tg://user?id=42">Alice &amp; Bob</a>` {
		t.Fatalf("unexpected display user: %q", got)
	}
	if got := ctx.DisplayUser(nil, 77); got != "<code>77</code>" {
		t.Fatalf("expected numeric fallback, got %q", got)
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

func TestContext_ExecutionContext(t *testing.T) {
	mockSvc := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 200},
		Svc:    mockSvc,
		Sender: &User{
			ID:        12345,
			Username:  "tester",
			FirstName: "Alice",
		},
		Chat: &Chat{
			ID: 200,
		},
		Message: &Message{
			ID:   555,
			Text: ".test arg1",
		},
		Args:  []string{"arg1"},
		Perms: NewPermissions(12345, nil),
	}

	execCtx := ctx.ExecutionContext(execution.SourceUserbot)
	if execCtx == nil {
		t.Fatal("expected non-nil ExecutionContext")
	}

	if execCtx.Source != execution.SourceUserbot {
		t.Errorf("expected SourceUserbot, got %v", execCtx.Source)
	}
	if execCtx.Actor.UserID != 12345 || !execCtx.Actor.IsOwner {
		t.Errorf("expected owner actor with ID 12345, got %+v", execCtx.Actor)
	}
	if execCtx.Actor.Username != "tester" || execCtx.Actor.FirstName != "Alice" {
		t.Errorf("expected username tester and Alice, got %+v", execCtx.Actor)
	}
	if execCtx.ChatID != 200 {
		t.Errorf("expected chatID 200, got %d", execCtx.ChatID)
	}
	if execCtx.MessageID != 555 {
		t.Errorf("expected messageID 555, got %d", execCtx.MessageID)
	}

	// Test Response closures
	if err := execCtx.Reply("reply test"); err != nil {
		t.Errorf("unexpected error in execCtx.Reply: %v", err)
	}
	if mockSvc.sentText != "reply test" {
		t.Errorf("expected sentText 'reply test', got %q", mockSvc.sentText)
	}

	if err := execCtx.Edit("edit test"); err != nil {
		t.Errorf("unexpected error in execCtx.Edit: %v", err)
	}
	if mockSvc.editedText != "edit test" {
		t.Errorf("expected editedText 'edit test', got %q", mockSvc.editedText)
	}
}

func TestExtractMediaFromTG_WebPageAndPaidMediaAndStory(t *testing.T) {
	// 1. WebPage with embedded audio document
	wpAudio := &tg.MessageMediaWebPage{
		Webpage: &tg.WebPage{
			URL: "https://example.com/audio",
			Document: &tg.Document{
				ID:       101,
				MimeType: "audio/mpeg",
				Size:     2048,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeAudio{
						Duration: 180,
						Title:    "Track 1",
					},
					&tg.DocumentAttributeFilename{
						FileName: "song.mp3",
					},
				},
			},
		},
	}
	infoAudio := ExtractMediaFromTG(wpAudio)
	if infoAudio == nil {
		t.Fatal("expected non-nil MediaInfo for webpage with audio document")
	}
	if infoAudio.Type != "audio" || infoAudio.FileName != "song.mp3" || infoAudio.WebURL != "https://example.com/audio" {
		t.Errorf("unexpected MediaInfo for audio webpage: %+v", infoAudio)
	}
	if infoAudio.Location == nil {
		t.Error("expected non-nil Location for audio webpage document")
	}

	// 2. WebPage with embedded photo
	wpPhoto := &tg.MessageMediaWebPage{
		Webpage: &tg.WebPage{
			URL: "https://example.com/pic",
			Photo: &tg.Photo{
				ID: 202,
				Sizes: []tg.PhotoSizeClass{
					&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 50000},
				},
			},
		},
	}
	infoPhoto := ExtractMediaFromTG(wpPhoto)
	if infoPhoto == nil {
		t.Fatal("expected non-nil MediaInfo for webpage with photo")
	}
	if infoPhoto.Type != "photo" || infoPhoto.WebURL != "https://example.com/pic" {
		t.Errorf("unexpected MediaInfo for photo webpage: %+v", infoPhoto)
	}

	// 3. WebPage without file attachment but with URL
	wpURL := &tg.MessageMediaWebPage{
		Webpage: &tg.WebPage{
			URL: "https://example.com/post",
		},
	}
	infoURL := ExtractMediaFromTG(wpURL)
	if infoURL == nil {
		t.Fatal("expected non-nil MediaInfo for webpage with URL")
	}
	if infoURL.Type != "webpage" || infoURL.WebURL != "https://example.com/post" || infoURL.Location != nil {
		t.Errorf("unexpected MediaInfo for URL webpage: %+v", infoURL)
	}

	// 4. Paid media with purchased document
	paidMedia := &tg.MessageMediaPaidMedia{
		ExtendedMedia: []tg.MessageExtendedMediaClass{
			&tg.MessageExtendedMedia{
				Media: &tg.MessageMediaDocument{
					Document: &tg.Document{
						ID:       303,
						MimeType: "video/mp4",
						Size:     1048576,
						Attributes: []tg.DocumentAttributeClass{
							&tg.DocumentAttributeVideo{
								Duration: 60,
								W:        1920,
								H:        1080,
							},
						},
					},
				},
			},
		},
	}
	infoPaid := ExtractMediaFromTG(paidMedia)
	if infoPaid == nil {
		t.Fatal("expected non-nil MediaInfo for paid media")
	}
	if infoPaid.Type != "video" || infoPaid.Size != 1048576 {
		t.Errorf("unexpected MediaInfo for paid media: %+v", infoPaid)
	}

	// 5. Story media
	storyMedia := &tg.MessageMediaStory{
		Story: &tg.StoryItem{
			ID: 404,
			Media: &tg.MessageMediaPhoto{
				Photo: &tg.Photo{
					ID: 405,
					Sizes: []tg.PhotoSizeClass{
						&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 15000},
					},
				},
			},
		},
	}
	infoStory := ExtractMediaFromTG(storyMedia)
	if infoStory == nil {
		t.Fatal("expected non-nil MediaInfo for story media")
	}
	if infoStory.Type != "photo" || infoStory.Size != 15000 {
		t.Errorf("unexpected MediaInfo for story media: %+v", infoStory)
	}
}

func TestContext_WithContextAndWithMedia(t *testing.T) {
	ctx1 := context.Background()
	ctx2, cancel := context.WithCancel(ctx1)
	defer cancel()

	orig := &Context{
		Ctx:           ctx1,
		CorrelationID: "corr-123",
		Command:       "download",
		Message: &Message{
			ID:   1,
			Text: ".download",
		},
	}

	// WithContext
	updatedCtx := orig.WithContext(ctx2)
	if updatedCtx == orig {
		t.Fatal("expected shallow copy from WithContext")
	}
	if updatedCtx.Ctx != ctx2 {
		t.Errorf("expected updated context %v, got %v", ctx2, updatedCtx.Ctx)
	}
	if updatedCtx.CorrelationID != "corr-123" || updatedCtx.Command != "download" {
		t.Errorf("fields corrupted after WithContext: %+v", updatedCtx)
	}

	// WithMedia
	media := &MediaInfo{
		Type:     "audio",
		FileName: "song.mp3",
		Size:     1024,
	}
	withMediaCtx := orig.WithMedia(media)
	if withMediaCtx.Message.Media != media {
		t.Errorf("expected message media to be set, got %+v", withMediaCtx.Message.Media)
	}
	if withMediaCtx.Message.MediaType != "audio" {
		t.Errorf("expected media type audio, got %s", withMediaCtx.Message.MediaType)
	}
	// Verify original is untouched
	if orig.Message.Media != nil {
		t.Error("original message media was modified")
	}
}

func TestMessage_URLs_WebURL(t *testing.T) {
	msg := &Message{
		Text: "check out this link",
		Media: &MediaInfo{
			Type:   "webpage",
			WebURL: "https://example.com/webpage",
		},
	}

	urls := msg.URLs()
	if len(urls) != 1 || urls[0] != "https://example.com/webpage" {
		t.Fatalf("expected [https://example.com/webpage], got %v", urls)
	}

	// Deduplication when entity has same URL
	msg.Text = "https://example.com/webpage"
	msg.Entities = []tg.MessageEntityClass{
		&tg.MessageEntityURL{Offset: 0, Length: 27},
	}
	urlsDedup := msg.URLs()
	if len(urlsDedup) != 1 {
		t.Fatalf("expected 1 deduplicated URL, got %v", urlsDedup)
	}
}

func TestDownloadMedia_PropagatesGetReplyError(t *testing.T) {
	deadCtx, cancel := context.WithCancel(context.Background())
	cancel()

	mockSvc := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:    deadCtx,
		Svc:    mockSvc,
		PeerID: &tg.InputPeerSelf{},
		Message: &Message{
			ID:        1,
			ReplyToID: 42,
		},
	}

	_, err := ctx.DownloadMedia(t.TempDir())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled error to be propagated, got: %v", err)
	}
}

func TestGetReplyMemoizesSuccessfulLookupAcrossMediaDownload(t *testing.T) {
	mock := &mockTelegramServicer{
		messageToGet: &tg.Message{
			ID:      42,
			Message: "image",
			Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{
				ID: 9001,
				Sizes: []tg.PhotoSizeClass{
					&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 1024},
				},
			}},
		},
	}
	ctx := &Context{
		Ctx:    context.Background(),
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
		Message: &Message{
			ID:        1,
			ReplyToID: 42,
		},
	}

	reply, err := ctx.GetReply()
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.Media == nil {
		t.Fatalf("expected replied media, got %+v", reply)
	}
	if _, err := ctx.DownloadMedia(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if mock.getCalls != 1 {
		t.Fatalf("GetMessage calls=%d, want 1 across GetReply + DownloadMedia", mock.getCalls)
	}
}

func TestGetReplyMemoSharedAfterWithContext(t *testing.T) {
	mock := &mockTelegramServicer{messageToGet: &tg.Message{ID: 42, Message: "cached"}}
	ctx := &Context{
		Ctx:    context.Background(),
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
		Message: &Message{ID: 1, ReplyToID: 42},
	}
	if _, err := ctx.GetReply(); err != nil {
		t.Fatal(err)
	}

	child := ctx.WithContext(context.WithValue(context.Background(), struct{}{}, "child"))
	reply, err := child.GetReply()
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.Text != "cached" {
		t.Fatalf("unexpected child reply: %+v", reply)
	}
	if mock.getCalls != 1 {
		t.Fatalf("GetMessage calls=%d, want shared memo after WithContext", mock.getCalls)
	}
}

func TestGetReplyDoesNotMemoizeTransientFailure(t *testing.T) {
	mock := &mockTelegramServicer{errToGet: context.DeadlineExceeded}
	ctx := &Context{
		Ctx:    context.Background(),
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
		Message: &Message{ID: 1, ReplyToID: 42},
	}

	if _, err := ctx.GetReply(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first GetReply error=%v, want deadline exceeded", err)
	}
	mock.errToGet = nil
	mock.messageToGet = &tg.Message{ID: 42, Message: "retry succeeded"}
	reply, err := ctx.GetReply()
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.Text != "retry succeeded" {
		t.Fatalf("unexpected reply after transient retry: %+v", reply)
	}
	if mock.getCalls != 2 {
		t.Fatalf("GetMessage calls=%d, want transient failure retried", mock.getCalls)
	}
}

func TestGetReplyMemoizesAuthoritativeAbsence(t *testing.T) {
	mock := &mockTelegramServicer{errToGet: ErrNotFound}
	ctx := &Context{
		Ctx:    context.Background(),
		Svc:    mock,
		PeerID: &tg.InputPeerSelf{},
		Message: &Message{ID: 1, ReplyToID: 42},
	}

	if reply, err := ctx.GetReply(); err != nil || reply != nil {
		t.Fatalf("first GetReply=(%+v,%v), want nil,nil", reply, err)
	}
	mock.errToGet = nil
	mock.messageToGet = &tg.Message{ID: 42, Message: "must stay absent in invocation"}
	if reply, err := ctx.GetReply(); err != nil || reply != nil {
		t.Fatalf("memoized absent GetReply=(%+v,%v), want nil,nil", reply, err)
	}
	if mock.getCalls != 1 {
		t.Fatalf("GetMessage calls=%d, want authoritative absence cached", mock.getCalls)
	}
}
