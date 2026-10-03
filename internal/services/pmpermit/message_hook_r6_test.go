package pmpermit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmpermit"
	"go.uber.org/zap"
)

func TestR6IncomingDecisionDefersTelegramPresentationEffect(t *testing.T) {
	repo := newMockRepo()
	transport := &mockTelegram{}
	svc := pmpermit.NewService(repo, transport, 12345, core.NewPermissions(12345, nil), zap.NewNop())
	svc.SetWarnCooldown(0)
	svc.SetMaxWarns(3)

	const userID int64 = 9001
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 77}
	decision, err := svc.DecideIncomingPM(context.Background(), userID, pmpermit.PMActor{UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Handled || decision.Effect != pmpermit.IncomingPMEffectWarning {
		t.Fatalf("decision=%+v, want handled warning", decision)
	}
	if transport.msgCount != 0 || len(transport.blockedIDs) != 0 {
		t.Fatalf("Telegram effect ran in decision phase: messages=%d blocked=%v", transport.msgCount, transport.blockedIDs)
	}
	rec, err := repo.GetPMRecord(context.Background(), userID)
	if err != nil || rec == nil || rec.WarnCount != 1 {
		t.Fatalf("decision did not persist warning state: rec=%+v err=%v", rec, err)
	}

	if err := svc.ApplyIncomingPMEffect(context.Background(), peer, decision); err != nil {
		t.Fatal(err)
	}
	if transport.msgCount != 1 || !strings.Contains(transport.lastMessage, "Warning 1/3") {
		t.Fatalf("warning effect output count=%d text=%q", transport.msgCount, transport.lastMessage)
	}
}

func TestR6IncomingEffectRevalidatesStateBeforeSending(t *testing.T) {
	repo := newMockRepo()
	transport := &mockTelegram{}
	svc := pmpermit.NewService(repo, transport, 12345, core.NewPermissions(12345, nil), zap.NewNop())
	svc.SetWarnCooldown(0)

	const userID int64 = 9002
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 88}
	decision, err := svc.DecideIncomingPM(context.Background(), userID, pmpermit.PMActor{UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Handled || decision.Effect != pmpermit.IncomingPMEffectWarning {
		t.Fatalf("decision=%+v, want warning effect", decision)
	}
	if err := svc.Approve(context.Background(), userID, "approved before warning effect", 0); err != nil {
		t.Fatal(err)
	}
	before := transport.msgCount
	if err := svc.ApplyIncomingPMEffect(context.Background(), peer, decision); err != nil {
		t.Fatal(err)
	}
	if transport.msgCount != before {
		t.Fatalf("stale warning effect sent after approval: before=%d after=%d", before, transport.msgCount)
	}
}

func TestR6AutoApproveCommitsStateBeforeTelegramCleanupAndFencesStaleEffect(t *testing.T) {
	repo := newMockRepo()
	transport := &mockTelegram{}
	svc := pmpermit.NewService(repo, transport, 12345, core.NewPermissions(12345, nil), zap.NewNop())

	const userID int64 = 9003
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 99}
	if err := repo.SetPMStatus(context.Background(), userID, pmpermit.StatusBlocked, "seed", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWarnMsgID(context.Background(), userID, 41); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWarnMsgID(context.Background(), userID, 42); err != nil {
		t.Fatal(err)
	}

	effect, changed, err := svc.PrepareAutoApproveOutgoing(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("outgoing auto-approve did not commit state transition")
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || !approved {
		t.Fatalf("approved=%v err=%v after prepare", approved, err)
	}
	if len(transport.deletedIDs) != 0 || len(transport.unblockedIDs) != 0 {
		t.Fatalf("Telegram cleanup ran in synchronous prepare: deleted=%v unblocked=%v", transport.deletedIDs, transport.unblockedIDs)
	}
	if len(effect.WarnIDs) != 2 {
		t.Fatalf("captured warning ids=%v, want two", effect.WarnIDs)
	}
	retained, err := repo.GetWarnMsgIDs(context.Background(), userID)
	if err != nil || len(retained) != 2 {
		t.Fatalf("prepare lost warning cleanup authority: ids=%v err=%v", retained, err)
	}

	if err := svc.ApplyAutoApproveOutgoingEffect(context.Background(), peer, effect); err != nil {
		t.Fatal(err)
	}
	if len(transport.deletedIDs) != 2 || len(transport.unblockedIDs) != 1 {
		t.Fatalf("cleanup effect deleted=%v unblocked=%v", transport.deletedIDs, transport.unblockedIDs)
	}
	retained, err = repo.GetWarnMsgIDs(context.Background(), userID)
	if err != nil || len(retained) != 0 {
		t.Fatalf("successful cleanup retained warning ids=%v err=%v", retained, err)
	}

	effect2 := pmpermit.AutoApproveEffect{UserID: userID, WarnIDs: []int{99}}
	if err := svc.Block(context.Background(), userID, "reblocked before stale cleanup"); err != nil {
		t.Fatal(err)
	}
	transport.deletedIDs = nil
	transport.unblockedIDs = nil
	if err := svc.ApplyAutoApproveOutgoingEffect(context.Background(), peer, effect2); err != nil {
		t.Fatal(err)
	}
	if len(transport.deletedIDs) != 0 || len(transport.unblockedIDs) != 0 {
		t.Fatalf("stale auto-approve effect crossed newer blocked state: deleted=%v unblocked=%v", transport.deletedIDs, transport.unblockedIDs)
	}
}


type r6DeleteFailTelegram struct {
	mockTelegram
	deleteErr error
}

func (m *r6DeleteFailTelegram) DeleteMessage(
	context.Context,
	tg.InputPeerClass,
	[]int,
) error {
	return m.deleteErr
}

func TestR6AutoApproveRetainsWarningIDsWhenTelegramDeleteFails(t *testing.T) {
	repo := newMockRepo()
	transport := &r6DeleteFailTelegram{deleteErr: errors.New("delete unavailable")}
	svc := pmpermit.NewService(repo, transport, 12345, core.NewPermissions(12345, nil), zap.NewNop())

	const userID int64 = 9004
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 100}
	if err := repo.SetPMStatus(context.Background(), userID, pmpermit.StatusBlocked, "seed", nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{51, 52} {
		if err := repo.AddWarnMsgID(context.Background(), userID, id); err != nil {
			t.Fatal(err)
		}
	}

	effect, changed, err := svc.PrepareAutoApproveOutgoing(context.Background(), userID)
	if err != nil || !changed {
		t.Fatalf("prepare changed=%v err=%v", changed, err)
	}
	if err := svc.ApplyAutoApproveOutgoingEffect(context.Background(), peer, effect); err != nil {
		t.Fatal(err)
	}
	retained, err := repo.GetWarnMsgIDs(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 2 || retained[0] != 51 || retained[1] != 52 {
		t.Fatalf("failed Telegram delete lost warning cleanup authority: %v", retained)
	}
}
