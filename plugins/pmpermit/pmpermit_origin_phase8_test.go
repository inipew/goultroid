package pmpermit_test

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

func TestPhase8PMPermitManualCrossPeerMessageIDCollision(t *testing.T) {
	db := setupTestDB(t)
	telegram := &mockTelegram{botSentPeerID: 7777, botSentIDs: map[int]bool{99: true}}
	svc := pmpermitSvc.NewService(pmpermit.NewSQLiteRepository(db), telegram, 12345, core.NewPermissions(12345, nil), zap.NewNop())
	p := pmpermit.New(svc)
	entities := tg.Entities{Users: map[int64]*tg.User{8888: {ID: 8888, AccessHash: 222}}}
	msg := &tg.Message{ID: 99, Out: true, PeerID: &tg.PeerUser{UserID: 8888}, Message: "manual outgoing"}
	if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
		t.Fatal(err)
	}
	approved, err := svc.IsApproved(context.Background(), 8888)
	if err != nil || !approved {
		t.Fatalf("manual outgoing with colliding bot ID did not approve: approved=%v err=%v", approved, err)
	}
}
