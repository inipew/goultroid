package pmpermit_test

import (
	"context"
	"sync"
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
	svc.SetWarnCooldown(0)

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
	svc.SetWarnCooldown(0)

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
	svc.SetWarnCooldown(0)

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

func TestPMPermit_SilentDropOnBlocked(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetWarnCooldown(0)

	ctx := context.Background()
	user := int64(77777)
	peer := &tg.InputPeerUser{UserID: user}

	// Explicitly block user
	_ = svc.Block(ctx, user, "bad actor")
	initialMsgs := mockTG.msgCount

	// Blocked user sends another message -> must be silently dropped (handled=true, no reply)
	handled, err := svc.HandleIncomingPM(ctx, peer, user)
	if err != nil || !handled {
		t.Fatalf("expected handled true on blocked user, got %v (err=%v)", handled, err)
	}
	if mockTG.msgCount != initialMsgs {
		t.Errorf("expected 0 reply messages sent to blocked user, got %d", mockTG.msgCount-initialMsgs)
	}
}

func TestPMPermit_ActorBypass(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetWarnCooldown(0)

	ctx := context.Background()

	// 1. Bot bypass
	botID := int64(1001)
	handled, err := svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: botID}, botID, pmpermit.PMActor{
		UserID: botID,
		IsBot:  true,
	})
	if err != nil || handled {
		t.Errorf("expected bot to bypass pmpermit (handled=false), got %v", handled)
	}

	// 2. Verified user bypass
	verifiedID := int64(1002)
	handled, err = svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: verifiedID}, verifiedID, pmpermit.PMActor{
		UserID:   verifiedID,
		Verified: true,
	})
	if err != nil || handled {
		t.Errorf("expected verified user to bypass pmpermit (handled=false), got %v", handled)
	}

	// 3. Self bypass
	selfID := int64(12345)
	handled, err = svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: selfID}, selfID, pmpermit.PMActor{
		UserID: selfID,
		IsSelf: true,
	})
	if err != nil || handled {
		t.Errorf("expected self to bypass pmpermit (handled=false), got %v", handled)
	}
}

func TestPMPermit_WarnCooldown_Burst(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetMaxWarns(4)
	svc.SetWarnCooldown(500 * time.Millisecond) // 500ms cooldown

	ctx := context.Background()
	user := int64(333444)
	peer := &tg.InputPeerUser{UserID: user}

	// Message 1: triggers warning 1
	handled, _ := svc.HandleIncomingPM(ctx, peer, user)
	if !handled || mockTG.msgCount != 1 {
		t.Fatalf("expected msg 1 to send warning, got count=%d", mockTG.msgCount)
	}

	// Message 2 immediate burst (in cooldown): silently dropped, does NOT trigger warning 2
	handled, _ = svc.HandleIncomingPM(ctx, peer, user)
	if !handled || mockTG.msgCount != 1 {
		t.Fatalf("expected burst msg 2 to be in cooldown, got count=%d", mockTG.msgCount)
	}

	// Wait for cooldown to expire
	time.Sleep(550 * time.Millisecond)

	// Message 3: triggers warning 2
	handled, _ = svc.HandleIncomingPM(ctx, peer, user)
	if !handled || mockTG.msgCount != 2 {
		t.Fatalf("expected msg 3 to send warning 2, got count=%d", mockTG.msgCount)
	}
}

func TestPMPermit_UnblockAndStats(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetWarnCooldown(0)

	ctx := context.Background()
	userA := int64(111)
	userB := int64(222)
	userC := int64(333)

	_ = svc.Approve(ctx, userA, "friend", 0)
	_ = svc.Block(ctx, userB, "spam")
	_, _ = db.IncrementPMWarn(ctx, userC)

	pending, approved, blocked, err := svc.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if approved != 1 || blocked != 1 || pending != 1 {
		t.Errorf("expected stats 1/1/1, got pending=%d, approved=%d, blocked=%d", pending, approved, blocked)
	}

	apprList, _ := svc.ListApproved(ctx, 10, 0)
	if len(apprList) != 1 || apprList[0].UserID != userA {
		t.Errorf("expected approved userA in list, got %v", apprList)
	}

	// Unblock userB
	if err := svc.Unblock(ctx, nil, userB); err != nil {
		t.Fatalf("Unblock failed: %v", err)
	}
	rec, _ := db.GetPMRecord(ctx, userB)
	if rec == nil || rec.Status != pmpermit.StatusPending {
		t.Errorf("expected userB status pending after unblock, got %+v", rec)
	}
}

func TestPMPermit_EventBus(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	perms := core.NewPermissions(12345, nil)
	svc := pmpermit.NewService(db, mockTG, 12345, perms, zap.NewNop())
	svc.SetWarnCooldown(0)

	bus := core.NewEventBus()
	defer bus.Close()
	svc.SetEventBus(bus)

	var lastAction string
	var lastUserID int64
	var mu sync.Mutex
	bus.Subscribe(core.EventTypePMPermit, func(evt core.Event) {
		mu.Lock()
		defer mu.Unlock()
		if pmEvt, ok := evt.(*core.PMPermitEvent); ok {
			lastAction = pmEvt.Action
			lastUserID = pmEvt.UserID
		}
	})

	ctx := context.Background()
	_ = svc.Approve(ctx, 9988, "test", 0)

	for i := 0; i < 20; i++ {
		mu.Lock()
		act := lastAction
		mu.Unlock()
		if act == "approve" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if lastAction != "approve" || lastUserID != 9988 {
		t.Errorf("expected event approve for 9988, got action=%s, user=%d", lastAction, lastUserID)
	}
}
