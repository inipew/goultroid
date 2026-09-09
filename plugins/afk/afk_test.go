package afk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type mockService struct {
	core.MockTelegramServicer
	mu           sync.Mutex
	sent         string
	sentTo       tg.InputPeerClass
	sentMessages []string
	deletedIDs   []int
	deleteCh     chan int
	messages     map[int]*tg.Message
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = text
	m.sentTo = peer
	m.sentMessages = append(m.sentMessages, text)
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = text
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	m.mu.Lock()
	m.deletedIDs = append(m.deletedIDs, msgIDs...)
	deleteCh := m.deleteCh
	m.mu.Unlock()
	if deleteCh != nil {
		for _, id := range msgIDs {
			select {
			case deleteCh <- id:
			default:
			}
		}
	}
	return nil
}
func (m *mockService) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.messages != nil {
		if msg, ok := m.messages[msgID]; ok {
			return msg, nil
		}
	}
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

	repo := NewSQLiteRepository(db)
	p := New(repo, ownerID, func() core.TelegramServicer { return svc })
	p.SetWelcomeDeleteDelay(10 * time.Millisecond)
	if p.Name() != "afk" {
		t.Errorf("expected name afk, got %s", p.Name())
	}
	if err := p.InitContext(context.Background()); err != nil {
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
	status, err := repo.GetAFK(ctx, ownerID)
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
	status, _ = repo.GetAFK(ctx, ownerID)
	if !status.IsAFK {
		t.Errorf("owner should still be AFK")
	}

	// 5. Normal outgoing message from owner -> deactivates AFK!
	svc.mu.Lock()
	svc.deleteCh = make(chan int, 1)
	svc.mu.Unlock()
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
	select {
	case deletedID := <-svc.deleteCh:
		if deletedID != 10 {
			t.Fatalf("deleted message ID = %d, want welcome message ID 10", deletedID)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome back message was not auto-deleted")
	}

	// Verify DB state is now deactivated
	status, _ = repo.GetAFK(ctx, ownerID)
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
	p.cooldownMu.Lock()
	p.cooldownMap[[2]int64{1, 101}] = time.Now()
	p.cooldownMap[[2]int64{1, 102}] = time.Now().Add(-20 * time.Minute)
	p.cooldownMu.Unlock()

	purged := p.Cleanup(10 * time.Minute)
	if purged != 1 {
		t.Errorf("expected 1 record purged, got %d", purged)
	}

	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	// Verify 101 remains
	if _, ok := p.cooldownMap[[2]int64{1, 101}]; !ok {
		t.Errorf("expected 101 to remain in cooldown map")
	}
	// Verify 102 was removed
	if _, ok := p.cooldownMap[[2]int64{1, 102}]; ok {
		t.Errorf("expected 102 to be purged from cooldown map")
	}
}

func TestAFKPlugin_NewCommands(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	baseCtx := &core.Context{
		Ctx:     ctx,
		Sender:  &core.User{ID: ownerID},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}
	cmd := p.Commands()[0]

	// 1. Toggle ON (no args)
	ctxToggleOn := *baseCtx
	ctxToggleOn.Args = []string{}
	if err := cmd.Handler(&ctxToggleOn); err != nil {
		t.Fatalf("toggle on error: %v", err)
	}
	if !strings.Contains(svc.sent, "AFK Mode Activated") {
		t.Errorf("expected activated message on toggle, got: %s", svc.sent)
	}
	st := p.state.Load()
	if !st.isAFK {
		t.Fatal("expected AFK to be active")
	}

	// 2. Status check
	ctxStatus := *baseCtx
	ctxStatus.Args = []string{"status"}
	svc.sent = ""
	if err := cmd.Handler(&ctxStatus); err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(svc.sent, "AFK Status: Active") {
		t.Errorf("expected active status, got: %s", svc.sent)
	}

	// 3. Explicit OFF (.afk off)
	ctxOff := *baseCtx
	ctxOff.Args = []string{"off"}
	svc.sent = ""
	if err := cmd.Handler(&ctxOff); err != nil {
		t.Fatalf("off error: %v", err)
	}
	if !strings.Contains(svc.sent, "AFK Mode Deactivated") {
		t.Errorf("expected deactivated message, got: %s", svc.sent)
	}
	st = p.state.Load()
	if st.isAFK {
		t.Fatal("expected AFK to be inactive")
	}

	// 4. Status when inactive
	svc.sent = ""
	if err := cmd.Handler(&ctxStatus); err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(svc.sent, "AFK Status: Inactive") {
		t.Errorf("expected inactive status, got: %s", svc.sent)
	}

	// 5. .afk on with custom reason
	ctxOn := *baseCtx
	ctxOn.Args = []string{"on", "eating", "lunch"}
	ctxOn.RawArgs = "on eating lunch"
	svc.sent = ""
	if err := cmd.Handler(&ctxOn); err != nil {
		t.Fatalf("on error: %v", err)
	}
	if !strings.Contains(svc.sent, "eating lunch") {
		t.Errorf("expected custom reason in activation, got: %s", svc.sent)
	}

	// 6. Toggle OFF (no args when active)
	ctxToggleOff := *baseCtx
	ctxToggleOff.Args = []string{}
	svc.sent = ""
	if err := cmd.Handler(&ctxToggleOff); err != nil {
		t.Fatalf("toggle off error: %v", err)
	}
	if !strings.Contains(svc.sent, "AFK Mode Deactivated") {
		t.Errorf("expected deactivated on toggle, got: %s", svc.sent)
	}
}

type botSentMockService struct {
	mockService
	botSentIDs map[int]bool
}

func (m *botSentMockService) IsBotSent(msgID int) bool {
	return m.botSentIDs != nil && m.botSentIDs[msgID]
}

func TestAFKPlugin_BotSentMessageDoesNotTurnOffAFK(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &botSentMockService{
		botSentIDs: map[int]bool{
			999: true, // message ID 999 was sent by Scheduler/Broadcast
		},
	}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	// Activate AFK
	p.enableAFK(ctx, "sleeping")

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			2002: {ID: 2002, AccessHash: 123},
		},
	}

	// Outgoing message sent by Scheduler/Broadcast (ID 999, IsBotSent == true)
	botMsg := &tg.Message{
		ID:      999,
		Out:     true,
		Message: "Scheduled announcement",
		PeerID:  &tg.PeerUser{UserID: 2002},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, botMsg, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected NO welcome back message for bot-sent outgoing, got: %s", svc.sent)
	}
	st := p.state.Load()
	if !st.isAFK {
		t.Fatal("expected owner to REMAIN AFK when outgoing message was sent by bot")
	}

	// Normal manual outgoing message by owner (ID 1000, IsBotSent == false)
	manualMsg := &tg.Message{
		ID:      1000,
		Out:     true,
		Message: "Hello I am back",
		PeerID:  &tg.PeerUser{UserID: 2002},
	}
	if err := p.HandleIncomingMessage(ctx, entities, manualMsg, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Welcome back") {
		t.Errorf("expected welcome back for manual owner message, got: %s", svc.sent)
	}
	st = p.state.Load()
	if st.isAFK {
		t.Fatal("expected owner to be unAFK after manual message")
	}
}

func TestAFKPlugin_SilentUnAFKInGroups(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetWelcomePrivateOnly(true)
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "in meeting")

	// Manual message by owner in a public group chat
	groupMsg := &tg.Message{
		ID:      50,
		Out:     true,
		Message: "I agree with this plan",
		PeerID:  &tg.PeerChannel{ChannelID: 7777},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, tg.Entities{}, groupMsg, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify AFK was deactivated silently without spamming the group
	if svc.sent != "" {
		t.Errorf("expected silent unAFK in group, but sent: %s", svc.sent)
	}
	st := p.state.Load()
	if st.isAFK {
		t.Fatal("expected AFK to be deactivated")
	}
}

func TestAFKPlugin_CompoundCooldown(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetCooldown(30 * time.Second)
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "busy")

	senderID := int64(5555)
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			senderID: {ID: senderID, AccessHash: 111},
		},
		Channels: map[int64]*tg.Channel{
			100: {ID: 100, AccessHash: 222},
			200: {ID: 200, AccessHash: 333},
		},
	}

	// 1. Mention in Group 100 -> auto-reply triggered
	msgGroup1 := &tg.Message{
		ID:        1,
		Mentioned: true,
		PeerID:    &tg.PeerChannel{ChannelID: 100},
		FromID:    &tg.PeerUser{UserID: senderID},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, msgGroup1, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected auto-reply in Group 100, got: %s", svc.sent)
	}

	// 2. Second mention immediately in Group 100 -> rate-limited (no reply)
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, msgGroup1, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected rate-limiting in Group 100, got: %s", svc.sent)
	}

	// 3. Mention by SAME user in Group 200 -> should reply because cooldown is per chat!
	msgGroup2 := &tg.Message{
		ID:        2,
		Mentioned: true,
		PeerID:    &tg.PeerChannel{ChannelID: 200},
		FromID:    &tg.PeerUser{UserID: senderID},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, msgGroup2, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Errorf("expected auto-reply in Group 200 (compound cooldown), got: %s", svc.sent)
	}
}

func TestAFKPlugin_MentionDetectionComplete(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetOwnerUsername("dhimas")
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "coding")

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			2002: {ID: 2002, AccessHash: 111},
		},
		Channels: map[int64]*tg.Channel{
			300: {ID: 300, AccessHash: 222},
		},
	}

	// Case A: standard @username MessageEntityMention
	textMsg := "hello @dhimas please review this PR"
	usernameMsg := &tg.Message{
		ID:      10,
		Message: textMsg,
		PeerID:  &tg.PeerChannel{ChannelID: 300},
		FromID:  &tg.PeerUser{UserID: 2002},
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 6, Length: 7}, // "@dhimas"
		},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, usernameMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Errorf("expected @username mention to trigger AFK reply, got: %s", svc.sent)
	}
}

func TestAFKPlugin_BotDMIgnored(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "sleeping")

	botID := int64(8888)
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			botID: {ID: botID, AccessHash: 111, Bot: true},
		},
	}

	botMsg := &tg.Message{
		ID:     1,
		PeerID: &tg.PeerUser{UserID: botID},
		FromID: &tg.PeerUser{UserID: botID},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, botMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected bot PM to be ignored, but got: %s", svc.sent)
	}
}

func TestAFKPlugin_ArbitrationSuppressed(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "away")

	// Create context with SuppressAFK = true (e.g. Filter or PMPermit already handled it)
	decision := core.NewMessageDecision(core.ExecutionInteractive)
	decision.SetSuppressAFK(true)
	arbCtx := core.WithMessageDecision(ctx, decision)

	dmMsg := &tg.Message{
		ID:     1,
		PeerID: &tg.PeerUser{UserID: 2002},
		FromID: &tg.PeerUser{UserID: 2002},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(arbCtx, tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 1}}}, dmMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc.sent != "" {
		t.Errorf("expected AFK reply to be suppressed by arbitration, but got: %s", svc.sent)
	}
}

func TestAFKPlugin_ConcurrentOutgoingAtomicCAS(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetWelcomePrivateOnly(false) // allow private welcome
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "racing")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(msgID int) {
			defer wg.Done()
			m := &tg.Message{
				ID:      msgID,
				Out:     true,
				Message: fmt.Sprintf("message %d", msgID),
				PeerID:  &tg.PeerUser{UserID: 2002},
			}
			_ = p.HandleIncomingMessage(ctx, tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 1}}}, m, false, "")
		}(i + 1)
	}
	wg.Wait()

	welcomeCount := 0
	svc.mu.Lock()
	for _, text := range svc.sentMessages {
		if strings.Contains(text, "Welcome back") {
			welcomeCount++
		}
	}
	svc.mu.Unlock()

	if welcomeCount != 1 {
		t.Errorf("expected exactly 1 welcome back message across 20 concurrent messages, got %d", welcomeCount)
	}
	st := p.state.Load()
	if st.isAFK {
		t.Fatal("expected AFK to be inactive")
	}
}

func TestAFKPlugin_MentionDetectionUTF16Emojis(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetOwnerUsername("dhimas")
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "testing emojis")

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			2002: {ID: 2002, AccessHash: 111},
		},
		Channels: map[int64]*tg.Channel{
			300: {ID: 300, AccessHash: 222},
		},
	}

	// 🚀 is U+1F680 (2 UTF-16 code units), 👋 is U+1F44B (2 UTF-16 code units).
	// "🚀👋 " = 2+2+1 = 5 UTF-16 code units.
	// But in runes: '🚀' (1), '👋' (1), ' ' (1) = 3 runes!
	// Telegram sends Offset in UTF-16 code units: Offset: 5, Length: 7 ("@dhimas").
	textMsg := "🚀👋 @dhimas please look"
	msg := &tg.Message{
		ID:      10,
		Message: textMsg,
		PeerID:  &tg.PeerChannel{ChannelID: 300},
		FromID:  &tg.PeerUser{UserID: 2002},
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 5, Length: 7}, // "@dhimas"
		},
	}

	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, msg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Errorf("expected @username mention with preceding surrogate pairs to trigger AFK, got: %s", svc.sent)
	}
}

func TestAFKPlugin_ForumTopicHandling(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{
		messages: map[int]*tg.Message{
			100: {ID: 100, Out: true, Message: "Topic Root Header"},
			150: {ID: 150, Out: true, Message: "Owner message in topic"},
		},
	}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "busy in forum")

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			2002: {ID: 2002, AccessHash: 111},
			3003: {ID: 3003, AccessHash: 222},
		},
		Channels: map[int64]*tg.Channel{
			500: {ID: 500, AccessHash: 333},
		},
	}

	// 1. Regular post inside forum topic (ReplyToMsgID points to topic root 100, ReplyToTopID == 100)
	// Even though topic root 100 was posted by owner, this should NOT trigger AFK reply!
	topicPostMsg := &tg.Message{
		ID:      201,
		Message: "Just chatting inside forum topic",
		PeerID:  &tg.PeerChannel{ChannelID: 500},
		FromID:  &tg.PeerUser{UserID: 2002},
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToMsgID: 100,
			ReplyToTopID: 100,
		},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, topicPostMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc.sent != "" {
		t.Fatalf("expected topic root message to NOT trigger AFK auto-reply, got: %s", svc.sent)
	}

	// 2. Legitimate reply inside the forum topic targeting owner message 150
	replyInTopicMsg := &tg.Message{
		ID:      202,
		Message: "Replying directly to owner message 150",
		PeerID:  &tg.PeerChannel{ChannelID: 500},
		FromID:  &tg.PeerUser{UserID: 3003},
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToMsgID: 150,
			ReplyToTopID: 100,
		},
	}
	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, replyInTopicMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected direct reply inside forum topic to trigger AFK auto-reply, got: %s", svc.sent)
	}
}

func TestAFKPlugin_AnonymousChannelSender(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	p.SetCooldown(30 * time.Second)
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "sleeping")

	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			500: {ID: 500, AccessHash: 333},
			777: {ID: 777, AccessHash: 444},
		},
		Users: map[int64]*tg.User{
			888: {ID: 888, AccessHash: 555},
		},
	}

	// Anonymous admin sending as channel 777 in group 500 with mention
	chanSenderMsg := &tg.Message{
		ID:        1,
		Message:   "hello",
		PeerID:    &tg.PeerChannel{ChannelID: 500},
		FromID:    &tg.PeerChannel{ChannelID: 777},
		Mentioned: true,
	}

	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, chanSenderMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected anonymous channel sender mention to trigger AFK reply, got: %s", svc.sent)
	}

	// Second message from same channel 777 should be cooldown rate-limited
	svc.sent = ""
	chanSenderMsg2 := &tg.Message{
		ID:        2,
		Message:   "hello again",
		PeerID:    &tg.PeerChannel{ChannelID: 500},
		FromID:    &tg.PeerChannel{ChannelID: 777},
		Mentioned: true,
	}
	if err := p.HandleIncomingMessage(ctx, entities, chanSenderMsg2, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc.sent != "" {
		t.Fatalf("expected second message from channel 777 to be rate-limited, got: %s", svc.sent)
	}

	// But a message from user 888 in the same chat 500 should NOT be blocked
	svc.sent = ""
	userSenderMsg := &tg.Message{
		ID:        3,
		Message:   "hello from user",
		PeerID:    &tg.PeerChannel{ChannelID: 500},
		FromID:    &tg.PeerUser{UserID: 888},
		Mentioned: true,
	}
	if err := p.HandleIncomingMessage(ctx, entities, userSenderMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected user 888 to trigger AFK reply (not affected by channel 777 cooldown), got: %s", svc.sent)
	}
}

func TestAFKPlugin_AutoDiscoverOwnerUsername(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	// Owner username is initially empty (not configured via SetOwnerUsername)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "working")

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			ownerID: {ID: ownerID, Username: "botmaster", AccessHash: 111},
			2002:    {ID: 2002, AccessHash: 222},
		},
		Channels: map[int64]*tg.Channel{
			300: {ID: 300, AccessHash: 333},
		},
	}

	msg := &tg.Message{
		ID:      1,
		Message: "hey @botmaster take a look",
		PeerID:  &tg.PeerChannel{ChannelID: 300},
		FromID:  &tg.PeerUser{UserID: 2002},
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 4, Length: 10}, // "@botmaster"
		},
	}

	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, msg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected auto-discovered username to match mention and trigger AFK, got: %s", svc.sent)
	}
	if p.getOwnerUsername() != "botmaster" {
		t.Errorf("expected ownerUsername to be 'botmaster', got %q", p.getOwnerUsername())
	}
}

func TestAFKPlugin_IncomingDMOmittedFromID(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	ownerID := int64(1001)
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	_ = p.Init()

	ctx := context.Background()
	p.enableAFK(ctx, "sleeping")

	otherUserID := int64(5555)
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			otherUserID: {ID: otherUserID, AccessHash: 123},
		},
	}

	// Incoming DM where Telegram omits FromID (FromID is nil)
	dmMsg := &tg.Message{
		ID:      1,
		Message: "hey are you there?",
		PeerID:  &tg.PeerUser{UserID: otherUserID},
		FromID:  nil, // Omitted by Telegram MTProto!
		Out:     false,
	}

	svc.sent = ""
	if err := p.HandleIncomingMessage(ctx, entities, dmMsg, false, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("expected AFK reply even when FromID is nil in incoming DM, got: %q", svc.sent)
	}
}
