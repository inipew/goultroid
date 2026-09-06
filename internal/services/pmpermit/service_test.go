package pmpermit_test

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/pmpermit"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	lastMessage  string
	msgCount     int
	deletedIDs   []int
	blockedIDs   []int64
	unblockedIDs []int64
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.lastMessage = text
	m.msgCount++
	return &tg.Message{ID: m.msgCount, Message: text}, nil
}

func (m *mockTelegram) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	m.deletedIDs = append(m.deletedIDs, msgIDs...)
	return nil
}

func (m *mockTelegram) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if u, ok := peer.(*tg.InputPeerUser); ok {
		m.blockedIDs = append(m.blockedIDs, u.UserID)
	}
	return nil
}

func (m *mockTelegram) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if u, ok := peer.(*tg.InputPeerUser); ok {
		m.unblockedIDs = append(m.unblockedIDs, u.UserID)
	}
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

func TestPMPermit_Bypass(t *testing.T) {
	db := setupTestDB(t)
	perms := core.NewPermissions(12345, []int64{99999})
	svc := pmpermit.NewService(db, nil, 12345, perms, zap.NewNop())

	ctx := context.Background()

	// Owner bypass
	approved, err := svc.IsApproved(ctx, 12345)
	if err != nil || !approved {
		t.Errorf("expected owner to be approved, got %v (err=%v)", approved, err)
	}

	// Sudo bypass
	approved, err = svc.IsApproved(ctx, 99999)
	if err != nil || !approved {
		t.Errorf("expected sudo user to be approved, got %v (err=%v)", approved, err)
	}

	// Random unapproved user
	approved, err = svc.IsApproved(ctx, 55555)
	if err != nil || approved {
		t.Errorf("expected random user to NOT be approved, got %v (err=%v)", approved, err)
	}
}

func TestPMPermit_ApproveAndExpire(t *testing.T) {
	db := setupTestDB(t)
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, nil, 12345, perms, zap.NewNop())

	ctx := context.Background()
	targetUser := int64(77777)

	// 1. Approve indefinitely
	if err := svc.Approve(ctx, targetUser, "friend", 0); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	approved, _ := svc.IsApproved(ctx, targetUser)
	if !approved {
		t.Errorf("expected approved true")
	}

	// 2. Disapprove
	if err := svc.Disapprove(ctx, targetUser); err != nil {
		t.Fatalf("Disapprove failed: %v", err)
	}
	approved, _ = svc.IsApproved(ctx, targetUser)
	if approved {
		t.Errorf("expected approved false after disapprove")
	}

	// 3. Approve with expired duration
	if err := svc.Approve(ctx, targetUser, "temp", -time.Minute); err != nil {
		t.Fatalf("Approve with negative duration failed: %v", err)
	}
	approved, _ = svc.IsApproved(ctx, targetUser)
	if approved {
		t.Errorf("expected expired approval to return false")
	}
}

func TestPMPermit_HandleIncomingPM(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetMaxWarns(3)

	ctx := context.Background()
	sender := int64(88888)
	peer := &tg.InputPeerUser{UserID: sender}

	// 1st unapproved message -> warning 1
	handled, err := svc.HandleIncomingPM(ctx, peer, sender)
	if err != nil || !handled {
		t.Fatalf("expected handled true, got %v, err=%v", handled, err)
	}
	if mockTG.msgCount != 1 {
		t.Errorf("expected 1 warning message sent, got %d", mockTG.msgCount)
	}

	// 2nd unapproved message -> warning 2
	handled, _ = svc.HandleIncomingPM(ctx, peer, sender)
	if !handled || mockTG.msgCount != 2 {
		t.Errorf("expected warning 2, got msgCount %d", mockTG.msgCount)
	}

	// 3rd unapproved message -> limit reached & blocked
	handled, _ = svc.HandleIncomingPM(ctx, peer, sender)
	if !handled || mockTG.msgCount != 3 {
		t.Errorf("expected block notice on 3rd warning, got msgCount %d", mockTG.msgCount)
	}

	rec, err := db.GetPMRecord(ctx, sender)
	if err != nil || rec == nil || rec.Status != pmpermit.StatusBlocked {
		t.Errorf("expected user to be blocked in database, got %+v", rec)
	}
}

func TestPMPermit_DeleteWarnMessagesOnApprove(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetMaxWarns(4)

	ctx := context.Background()
	sender := int64(66666)
	peer := &tg.InputPeerUser{UserID: sender}

	// Send 2 unapproved incoming messages, creating 2 warnings
	_, _ = svc.HandleIncomingPM(ctx, peer, sender)
	_, _ = svc.HandleIncomingPM(ctx, peer, sender)

	if mockTG.msgCount != 2 {
		t.Fatalf("expected 2 warning messages sent, got %d", mockTG.msgCount)
	}

	// Now approve the user
	if err := svc.Approve(ctx, sender, "verified", 0); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	// Verify that warning messages were deleted
	if len(mockTG.deletedIDs) < 2 {
		t.Errorf("expected at least 2 messages deleted, got %d (%v)", len(mockTG.deletedIDs), mockTG.deletedIDs)
	}

	// Verify DB warn_msg_ids is cleared
	ids, err := db.GetWarnMsgIDs(ctx, sender)
	if err != nil || len(ids) != 0 {
		t.Errorf("expected DB warn_msg_ids to be empty, got %v (err=%v)", ids, err)
	}
}

func TestPMPermit_AutoApproveOutgoing_CleansWarningsAndUnblocks(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetMaxWarns(2)

	ctx := context.Background()
	user := int64(54321)
	peer := &tg.InputPeerUser{UserID: user}

	// User sends 2 messages and gets blocked
	_, _ = svc.HandleIncomingPM(ctx, peer, user)
	_, _ = svc.HandleIncomingPM(ctx, peer, user)

	// User is blocked
	rec, _ := db.GetPMRecord(ctx, user)
	if rec == nil || rec.Status != pmpermit.StatusBlocked {
		t.Fatalf("expected user to be blocked")
	}

	// Now owner sends outgoing message to user -> AutoApproveOutgoing
	if err := svc.AutoApproveOutgoing(ctx, peer, user); err != nil {
		t.Fatalf("AutoApproveOutgoing failed: %v", err)
	}

	// Check approved
	approved, _ := svc.IsApproved(ctx, user)
	if !approved {
		t.Errorf("expected user to be approved after outgoing chat")
	}

	// Check UnblockUser was called
	foundUnblock := false
	for _, id := range mockTG.unblockedIDs {
		if id == user {
			foundUnblock = true
			break
		}
	}
	if !foundUnblock {
		t.Errorf("expected UnblockUser to be called for %d, got %v", user, mockTG.unblockedIDs)
	}
}

func TestPMPermit_BlockUser_MTProto(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetMaxWarns(1)

	ctx := context.Background()
	spammer := int64(111222)
	peer := &tg.InputPeerUser{UserID: spammer}

	// 1 message reaches maxWarns=1 -> blocked via MTProto
	_, _ = svc.HandleIncomingPM(ctx, peer, spammer)

	foundBlock := false
	for _, id := range mockTG.blockedIDs {
		if id == spammer {
			foundBlock = true
			break
		}
	}
	if !foundBlock {
		t.Errorf("expected BlockUser (MTProto) to be called for %d, got %v", spammer, mockTG.blockedIDs)
	}
}

func TestPMPermit_DisapproveResetsWarnCount(t *testing.T) {
	db := setupTestDB(t)
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, nil, 12345, perms, zap.NewNop())

	ctx := context.Background()
	user := int64(9999)

	// User has 2 warns
	_, _ = db.IncrementPMWarn(ctx, user)
	_, _ = db.IncrementPMWarn(ctx, user)
	rec, _ := db.GetPMRecord(ctx, user)
	if rec.WarnCount != 2 {
		t.Fatalf("expected warn count 2, got %d", rec.WarnCount)
	}

	// Disapprove user
	if err := svc.Disapprove(ctx, user); err != nil {
		t.Fatalf("Disapprove failed: %v", err)
	}

	// Verify warn count is reset to 0
	rec, _ = db.GetPMRecord(ctx, user)
	if rec.WarnCount != 0 {
		t.Errorf("expected warn count to be reset to 0 after disapprove, got %d", rec.WarnCount)
	}
	if rec.Status != pmpermit.StatusPending {
		t.Errorf("expected status pending, got %s", rec.Status)
	}
}


