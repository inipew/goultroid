package pmpermit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestPMPermitPlugin(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermitSvc.NewService(db, mockTG, 12345, perms, zap.NewNop())

	p := pmpermit.New(svc)
	if p.Name() != "pmpermit" {
		t.Errorf("expected plugin name pmpermit, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(cmds))
	}

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerUser{UserID: 88888},
		Message: &core.Message{ID: 1, IsOutgoing: true},
		Args:    []string{"88888"},
	}

	// 1. Approve command
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Approved") {
		t.Errorf("expected approval message, got %s", mockTG.edited)
	}

	// 2. Disapprove command
	if err := cmds[1].Handler(ctx); err != nil {
		t.Fatalf("disapprove failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Revoked approval") {
		t.Errorf("expected revoke message, got %s", mockTG.edited)
	}

	// 3. Block command
	if err := cmds[2].Handler(ctx); err != nil {
		t.Fatalf("block failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Blocked") {
		t.Errorf("expected block message, got %s", mockTG.edited)
	}

	// 4. Toggle command
	toggleCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerUser{UserID: 88888},
		Message: &core.Message{ID: 1, IsOutgoing: true},
		Args:    []string{"off"},
	}
	if err := cmds[3].Handler(toggleCtx); err != nil {
		t.Fatalf("toggle failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "DISABLED") {
		t.Errorf("expected disabled toggle, got %s", mockTG.edited)
	}
	if svc.IsEnabled() {
		t.Errorf("expected svc to be disabled")
	}
}

func TestPMPermitPlugin_HandleIncomingMessage(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermitSvc.NewService(db, mockTG, 12345, perms, zap.NewNop())
	p := pmpermit.New(svc)

	ctx := context.Background()
	e := tg.Entities{
		Users: map[int64]*tg.User{
			9999: {ID: 9999, AccessHash: 123456},
		},
	}

	// Outgoing message should be skipped
	outMsg := &tg.Message{
		ID:     1,
		Out:    true,
		PeerID: &tg.PeerUser{UserID: 9999},
	}
	if err := p.HandleIncomingMessage(ctx, e, outMsg, false, ""); err != nil {
		t.Errorf("expected outgoing message skipped, got %v", err)
	}

	// Group message should be skipped
	groupMsg := &tg.Message{
		ID:     2,
		Out:    false,
		PeerID: &tg.PeerChannel{ChannelID: 100},
	}
	if err := p.HandleIncomingMessage(ctx, e, groupMsg, false, ""); err != nil {
		t.Errorf("expected group message skipped, got %v", err)
	}

	// Incoming PM from unapproved user should trigger warning
	inMsg := &tg.Message{
		ID:     3,
		Out:    false,
		PeerID: &tg.PeerUser{UserID: 9999},
		FromID: &tg.PeerUser{UserID: 9999},
	}
	if err := p.HandleIncomingMessage(ctx, e, inMsg, false, ""); err != nil {
		t.Errorf("unexpected error handling incoming PM: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "Warning") {
		t.Errorf("expected warning sent, got: %s", mockTG.sentText)
	}
}
