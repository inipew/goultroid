package pmpermit_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmpermit"
	"go.uber.org/zap"
)

func TestPMPermitHandleIncomingPMWithoutPeerUsesStatusBeforeTransport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		handled bool
	}{
		{name: "approved", status: pmpermit.StatusApproved, handled: false},
		{name: "pending", status: pmpermit.StatusPending, handled: true},
		{name: "blocked", status: pmpermit.StatusBlocked, handled: true},
		{name: "unknown", handled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := setupTestDB(t)
			const senderID int64 = 2002
			if tc.status != "" {
				if err := repo.SetPMStatus(ctx, senderID, tc.status, "test setup", nil); err != nil {
					t.Fatal(err)
				}
			}
			telegram := &mockTelegram{}
			svc := pmpermit.NewService(repo, telegram, 1001, core.NewPermissions(1001, nil), zap.NewNop())
			svc.SetWarnCooldown(0)
			handled, err := svc.HandleIncomingPM(ctx, nil, senderID)
			if err != nil || handled != tc.handled {
				t.Fatalf("handled=%v, err=%v; want handled=%v", handled, err, tc.handled)
			}
			if telegram.msgCount != 0 || len(telegram.blockedIDs) != 0 {
				t.Fatalf("nil peer performed transport operations: messages=%d blocks=%v", telegram.msgCount, telegram.blockedIDs)
			}
			rec, err := repo.GetPMRecord(ctx, senderID)
			if err != nil {
				t.Fatal(err)
			}
			if rec != nil && rec.WarnCount != 0 {
				t.Fatalf("nil peer incremented warn count: %d", rec.WarnCount)
			}
		})
	}
}

func TestPMPermitHandleIncomingPMNilPeerExpiredApprovalFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := setupTestDB(t)
	expired := time.Now().UTC().Add(-time.Minute)
	if err := repo.SetPMStatus(ctx, 2002, pmpermit.StatusApproved, "temporary", &expired); err != nil {
		t.Fatal(err)
	}
	telegram := &mockTelegram{}
	svc := pmpermit.NewService(repo, telegram, 1001, core.NewPermissions(1001, nil), zap.NewNop())
	handled, err := svc.HandleIncomingPM(ctx, nil, 2002)
	if err != nil || !handled {
		t.Fatalf("expired approval should be intercepted: handled=%v err=%v", handled, err)
	}
	rec, err := repo.GetPMRecord(ctx, 2002)
	if err != nil || rec == nil || rec.Status != pmpermit.StatusPending {
		t.Fatalf("expired approval not invalidated: %+v err=%v", rec, err)
	}
	if telegram.msgCount != 0 || len(telegram.blockedIDs) != 0 {
		t.Fatal("expired approval attempted transport without a peer")
	}
}

func TestPMPermitHandleIncomingPMNilPeerMissingRepositoryFailsClosed(t *testing.T) {
	svc := pmpermit.NewService(nil, &mockTelegram{}, 1001, core.NewPermissions(1001, nil), zap.NewNop())
	handled, err := svc.HandleIncomingPM(context.Background(), nil, 2002)
	if err != nil || !handled {
		t.Fatalf("missing DB unexpectedly granted approval: handled=%v err=%v", handled, err)
	}
}
