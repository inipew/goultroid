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
	lastMessage string
	msgCount    int
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.lastMessage = text
	m.msgCount++
	return &tg.Message{ID: 1, Message: text}, nil
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
