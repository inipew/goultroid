package filters

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/telegram"
)

func handleMessageEvent(p *Plugin, ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
	return p.HandleMessageEvent(ctx, telegram.NormalizeMessageEnvelope(e, msg, isCmd, cmdName, 0))
}

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
	if msgID == 88 {
		return &tg.Message{ID: 88, Message: "this is replied text"}, nil
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

func TestFiltersPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatalf("filters migrations failed: %v", err)
	}
	svc := &mockService{}
	p := New(NewSQLiteRepository(db), func() core.TelegramServicer { return svc })

	if p.Name() != "filters" {
		t.Errorf("expected name 'filters', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	chatID := int64(12345)
	peer := &tg.InputPeerChat{ChatID: chatID}

	// 1. .filter without args -> error
	ctxNoArgs := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{},
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filter"].Handler(ctxNoArgs); err == nil {
		t.Errorf("expected error for missing args, got nil")
	}

	// 2. .filter with keyword and text -> success
	ctxSave := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{"rules", "Follow", "the", "group", "guidelines!"},
		RawArgs: "rules Follow the group guidelines!",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filter"].Handler(ctxSave); err != nil {
		t.Fatalf("failed to save filter: %v", err)
	}
	if !strings.Contains(svc.sent, "rules") {
		t.Errorf("expected confirmation message, got: %s", svc.sent)
	}

	// 3. .filter with reply to a message
	ctxSaveReply := &core.Context{
		Ctx:     context.Background(),
		Command: "filter",
		Args:    []string{"replied"},
		RawArgs: "replied",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
		Message: &core.Message{
			ReplyToID: 88,
		},
	}
	if err := cmdMap["filter"].Handler(ctxSaveReply); err != nil {
		t.Fatalf("failed to save filter via reply: %v", err)
	}

	// 4. .filters -> list
	ctxList := &core.Context{
		Ctx:     context.Background(),
		Command: "filters",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["filters"].Handler(ctxList); err != nil {
		t.Fatalf("failed to list filters: %v", err)
	}
	if !strings.Contains(svc.sent, "rules") || !strings.Contains(svc.sent, "replied") {
		t.Errorf("expected active filters list, got: %s", svc.sent)
	}

	// 5. template variables are rendered from the triggering sender/chat.
	ctxTemplate := &core.Context{
		Ctx: context.Background(), Command: "filter",
		Args:    []string{"welcome", "Hi", "{mention}", "in", "{chat}"},
		RawArgs: "welcome Hi {mention} in {chat}",
		Svc:     svc, PeerID: peer, Chat: &core.Chat{ID: chatID, Title: "Rules Room"},
	}
	if err := cmdMap["filter"].Handler(ctxTemplate); err != nil {
		t.Fatalf("failed to save template filter: %v", err)
	}
	svc.sent = ""
	templateEntities := tg.Entities{Users: map[int64]*tg.User{7777: {ID: 7777, FirstName: "Alice"}}}
	if err := handleMessageEvent(p, context.Background(), templateEntities, &tg.Message{
		PeerID: &tg.PeerChat{ChatID: chatID}, FromID: &tg.PeerUser{UserID: 7777}, Message: "welcome",
	}, false, ""); err != nil {
		t.Fatalf("template filter trigger failed: %v", err)
	}
	if !strings.Contains(svc.sent, `<a href="tg://user?id=7777">Alice</a>`) {
		t.Fatalf("template mention not rendered: %s", svc.sent)
	}

	// 6. Incoming message evaluation:
	// 6a. Command message -> skipped
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: ".filter rules",
	}, true, "filter")
	if err != nil || svc.sent != "" {
		t.Errorf("expected command message to be ignored, got sent: %s", svc.sent)
	}

	// 6b. Outgoing message -> skipped
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "here are the rules",
		Out:     true,
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected outgoing message to be ignored, got sent: %s", svc.sent)
	}

	// 6c. Matching non-command message -> triggers auto-reply
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "What are the rules please?",
	}, false, "")
	if err != nil {
		t.Fatalf("unexpected error in HandleIncomingMessage: %v", err)
	}
	if svc.sent != "Follow the group guidelines!" {
		t.Errorf("expected filter auto-reply 'Follow the group guidelines!', got '%s'", svc.sent)
	}

	// 6d. Non-matching message -> no reply
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), tg.Entities{}, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "just a normal random message",
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected no auto-reply, got: %s", svc.sent)
	}

	// 7. .stop <keyword>
	ctxStop := &core.Context{
		Ctx:     context.Background(),
		Command: "stop",
		Args:    []string{"rules"},
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["stop"].Handler(ctxStop); err != nil {
		t.Fatalf("failed to stop filter: %v", err)
	}
	if !strings.Contains(svc.sent, "stopped") {
		t.Errorf("expected stop confirmation, got: %s", svc.sent)
	}

	// 8. Test matchFilter helper logic
	testCases := []struct {
		text    string
		kw      string
		matched bool
	}{
		{"hello world", "hello", true},
		{"say hello!", "hello", true},
		{"othello is great", "hello", false},
		{"good morning everyone", "good morning", true},
		{"good mornings everyone", "good morning", false},
		{"where are the RULES?", "rules", true},
	}
	for _, tc := range testCases {
		if got := matchFilter(tc.text, tc.kw); got != tc.matched {
			t.Errorf("matchFilter(%q, %q) = %v; want %v", tc.text, tc.kw, got, tc.matched)
		}
	}

	// 9. Bot loop prevention
	botEntities := tg.Entities{
		Users: map[int64]*tg.User{
			9999: {ID: 9999, Bot: true},
		},
	}
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), botEntities, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		FromID:  &tg.PeerUser{UserID: 9999},
		Message: "replied to this filter keyword",
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected bot sender to be ignored, got sent: %s", svc.sent)
	}

	// 10. Cooldown test: first reply works, second within 5s is dropped
	humanEntities := tg.Entities{
		Users: map[int64]*tg.User{
			8888: {ID: 8888, Bot: false},
		},
	}
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), humanEntities, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		FromID:  &tg.PeerUser{UserID: 8888},
		Message: "replied to this filter keyword",
	}, false, "")
	if err != nil || svc.sent == "" {
		t.Fatalf("expected first human trigger to reply, got sent: %s", svc.sent)
	}
	svc.sent = ""
	err = handleMessageEvent(p, context.Background(), humanEntities, &tg.Message{
		PeerID:  &tg.PeerChat{ChatID: chatID},
		FromID:  &tg.PeerUser{UserID: 8888},
		Message: "replied to this filter keyword again immediately",
	}, false, "")
	if err != nil || svc.sent != "" {
		t.Errorf("expected cooldown to drop immediate repeat reply, got sent: %s", svc.sent)
	}
}

func TestFilterMediaManagementUX(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	chatID := int64(54321)

	if err := repo.SaveFilter(context.Background(), chatID, "hello", savedresponse.NewHTML("Hi {mention}")); err != nil {
		t.Fatal(err)
	}
	media := savedresponse.NewPlainText("literal")
	media.Media = &savedresponse.MediaRef{
		AssetID: "asset-sticker", MediaType: "sticker", Name: "wave.webp", MIMEType: "image/webp",
	}
	if err := repo.SaveFilter(context.Background(), chatID, "wave", media); err != nil {
		t.Fatal(err)
	}

	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	ctx := &core.Context{
		Ctx: context.Background(), Chat: &core.Chat{ID: chatID},
		Svc: svc, PeerID: &tg.InputPeerChat{ChatID: chatID},
	}
	if err := p.handleList(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svc.sent, "[text]") || !strings.Contains(svc.sent, "[sticker]") || !strings.Contains(svc.sent, ".filterinfo") {
		t.Fatalf("filter list missing response indicators: %s", svc.sent)
	}

	ctx.Args = []string{"wave"}
	if err := p.handleInfo(ctx); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Filter Info", "sticker", "plain", "wave.webp", "image/webp", "Template variables:", "none"} {
		if !strings.Contains(svc.sent, want) {
			t.Fatalf("filter info missing %q: %s", want, svc.sent)
		}
	}

	ctx.Args = []string{"hello"}
	if err := p.handleInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svc.sent, "{mention}") || !strings.Contains(svc.sent, "Type:</b> <code>text") {
		t.Fatalf("text filter info missing template/type metadata: %s", svc.sent)
	}
}


type filterListCaptureService struct {
	mockService
	messages []string
}

func (s *filterListCaptureService) SendMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	s.messages = append(s.messages, text)
	return &tg.Message{ID: len(s.messages), Message: text}, nil
}

func (s *filterListCaptureService) EditMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	msgID int,
	text string,
) error {
	s.messages = append(s.messages, text)
	return nil
}

func TestFilterListDeliveryIsChunkedToTelegramLimit(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	chatID := int64(98765)
	for i := 0; i < 700; i++ {
		keyword := fmt.Sprintf("filter-%04d-%s", i, strings.Repeat("x", 8))
		if err := repo.SaveFilter(context.Background(), chatID, keyword, savedresponse.NewText("value")); err != nil {
			t.Fatal(err)
		}
	}

	svc := &filterListCaptureService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	ctx := &core.Context{
		Ctx: context.Background(), Chat: &core.Chat{ID: chatID},
		Svc: svc, PeerID: &tg.InputPeerChat{ChatID: chatID},
	}
	if err := p.handleList(ctx); err != nil {
		t.Fatal(err)
	}
	if len(svc.messages) < 2 {
		t.Fatalf("expected chunked filter list, got %d message", len(svc.messages))
	}
	for i, message := range svc.messages {
		if runes := utf8.RuneCountInString(message); runes > filtersTelegramMessageRunes {
			t.Fatalf("chunk %d has %d runes, max %d", i, runes, filtersTelegramMessageRunes)
		}
	}
}
