package pmpermit

import (
	"context"
	"errors"
	"testing"
)

func TestA3CLegacyApprovalRequiresResolvedPeer(t *testing.T) {
	const userID int64 = 1042
	repo := newA3StatusRepo(userID, StatusBlocked)
	svc := NewService(repo, &a3StatusTelegram{}, 1, nil, nil)
	err := svc.Approve(context.Background(), userID, "trusted", 0)
	if !errors.Is(err, ErrResolvedApprovalPeer) {
		t.Fatalf("legacy Telegram approval without access hash must fail: %v", err)
	}
	rec, err := repo.GetPMRecord(context.Background(), userID)
	if err != nil || rec == nil || rec.Status != StatusBlocked {
		t.Fatalf("rejected legacy approval must not alter durable block: rec=%+v err=%v", rec, err)
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || approved {
		t.Fatalf("rejected legacy approval must not publish positive cache: approved=%v err=%v", approved, err)
	}
}

func TestA3CLegacyApprovalRejectsUnavailableConfiguredProvider(t *testing.T) {
	const userID int64 = 1043
	repo := newA3StatusRepo(userID, StatusPending)
	svc := NewService(repo, func() TelegramService { return nil }, 1, nil, nil)
	if err := svc.Approve(context.Background(), userID, "trusted", 0); !errors.Is(err, ErrResolvedApprovalPeer) {
		t.Fatalf("nil live Telegram provider must not bypass peer requirement: %v", err)
	}
	rec, err := repo.GetPMRecord(context.Background(), userID)
	if err != nil || rec.Status != StatusPending {
		t.Fatalf("unavailable Telegram provider unexpectedly approved: rec=%+v err=%v", rec, err)
	}
}

func TestA3CLegacyApprovalStorageOnlyAllowed(t *testing.T) {
	const userID int64 = 1044
	repo := newA3StatusRepo(userID, StatusPending)
	svc := NewService(repo, nil, 1, nil, nil)
	if err := svc.Approve(context.Background(), userID, "trusted", 0); err != nil {
		t.Fatal(err)
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || !approved {
		t.Fatalf("storage-only approval should still work: approved=%v err=%v", approved, err)
	}
}
