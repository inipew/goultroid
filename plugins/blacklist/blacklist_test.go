package blacklist

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type mockService struct {
	core.MockTelegramServicer
	sent          string
	deletedMsgIDs []int
	deleteCalled  bool
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	m.deleteCalled = true
	m.deletedMsgIDs = append(m.deletedMsgIDs, msgIDs...)
	return nil
}

func TestBlacklistPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	p := New(db, func() core.TelegramServicer { return svc })

	if p.Name() != "blacklist" {
		t.Errorf("expected name 'blacklist', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	chatID := int64(98765)
	peer := &tg.InputPeerChat{ChatID: chatID}

	// 1. .blacklist without args -> error
	ctxNoArgs := &core.Context{
		Ctx:     context.Background(),
		Command: "blacklist",
		Args:    []string{},
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["blacklist"].Handler(ctxNoArgs); err == nil {
		t.Errorf("expected error for missing args, got nil")
	}

	// 2. .blacklist with word
	ctxAdd := &core.Context{
		Ctx:     context.Background(),
		Command: "blacklist",
		Args:    []string{"scam"},
		RawArgs: "scam",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["blacklist"].Handler(ctxAdd); err != nil {
		t.Fatalf("failed to add blacklist: %v", err)
	}
	if !strings.Contains(svc.sent, "scam") {
		t.Errorf("expected confirmation message containing 'scam', got: %s", svc.sent)
	}

	// 3. .blacklist with phrase
	ctxAddPhrase := &core.Context{
		Ctx:     context.Background(),
		Command: "blacklist",
		Args:    []string{"free", "crypto"},
		RawArgs: "free crypto",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["blacklist"].Handler(ctxAddPhrase); err != nil {
		t.Fatalf("failed to add phrase blacklist: %v", err)
	}

	// 4. .blacklists list
	ctxList := &core.Context{
		Ctx:     context.Background(),
		Command: "blacklists",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["blacklists"].Handler(ctxList); err != nil {
		t.Fatalf("failed to list blacklists: %v", err)
	}
	if !strings.Contains(svc.sent, "scam") || !strings.Contains(svc.sent, "free crypto") {
		t.Errorf("expected active blacklists in output, got: %s", svc.sent)
	}

	// 5. Incoming message evaluation
	// 5a. Command message -> ignored
	svc.deleteCalled = false
	svc.deletedMsgIDs = nil
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      101,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: ".blacklist scam",
	}, true, "blacklist")
	if err != nil || svc.deleteCalled {
		t.Errorf("expected command message to be ignored, deleteCalled=%v", svc.deleteCalled)
	}

	// 5b. Outgoing message -> ignored
	svc.deleteCalled = false
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      102,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "this is scam",
		Out:     true,
	}, false, "")
	if err != nil || svc.deleteCalled {
		t.Errorf("expected outgoing message to be ignored, deleteCalled=%v", svc.deleteCalled)
	}

	// 5c. Safe incoming message -> ignored
	svc.deleteCalled = false
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      103,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "hello this is legitimate message",
	}, false, "")
	if err != nil || svc.deleteCalled {
		t.Errorf("expected safe message to be ignored, deleteCalled=%v", svc.deleteCalled)
	}

	// 5d. Blacklist violation -> auto-deleted!
	svc.deleteCalled = false
	svc.deletedMsgIDs = nil
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      104,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "Warning, this might be a SCAM alert!",
	}, false, "")
	if err != nil {
		t.Fatalf("unexpected error in HandleIncomingMessage: %v", err)
	}
	if !svc.deleteCalled || len(svc.deletedMsgIDs) == 0 || svc.deletedMsgIDs[0] != 104 {
		t.Errorf("expected message 104 to be auto-deleted, got deleteCalled=%v msgIDs=%v", svc.deleteCalled, svc.deletedMsgIDs)
	}

	// 5e. Phrase violation -> auto-deleted!
	svc.deleteCalled = false
	svc.deletedMsgIDs = nil
	err = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      105,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "Click here to get free crypto now!",
	}, false, "")
	if err != nil {
		t.Fatalf("unexpected error in HandleIncomingMessage: %v", err)
	}
	if !svc.deleteCalled || len(svc.deletedMsgIDs) == 0 || svc.deletedMsgIDs[0] != 105 {
		t.Errorf("expected message 105 to be auto-deleted, got deleteCalled=%v msgIDs=%v", svc.deleteCalled, svc.deletedMsgIDs)
	}

	// 6. .unblacklist <word>
	ctxRm := &core.Context{
		Ctx:     context.Background(),
		Command: "unblacklist",
		Args:    []string{"scam"},
		RawArgs: "scam",
		Svc:     svc,
		PeerID:  peer,
		Chat:    &core.Chat{ID: chatID},
	}
	if err := cmdMap["unblacklist"].Handler(ctxRm); err != nil {
		t.Fatalf("failed to remove from blacklist: %v", err)
	}
	if !strings.Contains(svc.sent, "Removed") {
		t.Errorf("expected remove confirmation, got: %s", svc.sent)
	}

	// Verify scam is no longer triggered
	svc.deleteCalled = false
	_ = p.HandleIncomingMessage(context.Background(), tg.Entities{}, &tg.Message{
		ID:      106,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		Message: "this is scam",
	}, false, "")
	if svc.deleteCalled {
		t.Errorf("expected scam to no longer trigger delete after removal")
	}

	// 7. Test matchBlacklist helper
	testCases := []struct {
		text    string
		word    string
		matched bool
	}{
		{"hello scammer", "scam", false},
		{"it is a scam!", "scam", true},
		{"join free crypto here", "free crypto", true},
		{"free cryptocurrencies", "free crypto", false},
		{"SCAM", "scam", true},
	}
	for _, tc := range testCases {
		if got := matchBlacklist(tc.text, tc.word); got != tc.matched {
			t.Errorf("matchBlacklist(%q, %q) = %v; want %v", tc.text, tc.word, got, tc.matched)
		}
	}
}
