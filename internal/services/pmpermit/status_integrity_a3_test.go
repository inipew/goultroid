package pmpermit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type a3StatusRepo struct {
	Repository
	mu             sync.Mutex
	records        map[int64]PMPermitRecord
	failSet        string
	failReadAt     int
	readCount      int
	warnIncrements int
	startedRead    chan struct{}
	releaseRead    chan struct{}
	once           sync.Once
}

func newA3StatusRepo(userID int64, status string) *a3StatusRepo {
	return &a3StatusRepo{records: map[int64]PMPermitRecord{userID: {UserID: userID, Status: status}}}
}

func (r *a3StatusRepo) GetPMRecord(ctx context.Context, userID int64) (*PMPermitRecord, error) {
	if r.startedRead != nil {
		r.once.Do(func() { close(r.startedRead) })
		select {
		case <-r.releaseRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readCount++
	if r.failReadAt != 0 && r.readCount == r.failReadAt {
		return nil, errors.New("injected read failure")
	}
	rec, ok := r.records[userID]
	if !ok {
		return nil, nil
	}
	cp := rec
	return &cp, nil
}

func (r *a3StatusRepo) SetPMStatus(_ context.Context, userID int64, status, reason string, exp *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSet == status {
		return errors.New("injected durable transition failure")
	}
	r.records[userID] = PMPermitRecord{UserID: userID, Status: status, Reason: reason, ExpiresAt: exp}
	return nil
}

func (r *a3StatusRepo) ResetPMWarn(context.Context, int64) error            { return nil }
func (r *a3StatusRepo) GetWarnMsgIDs(context.Context, int64) ([]int, error) { return nil, nil }
func (r *a3StatusRepo) ClearWarnMsgIDs(context.Context, int64) error        { return nil }
func (r *a3StatusRepo) IncrementPMWarn(context.Context, int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnIncrements++
	return r.warnIncrements, nil
}

type a3StatusTelegram struct {
	core.MockTelegramServicer
	blockErr   error
	unblockErr error
}

func (m *a3StatusTelegram) BlockUser(context.Context, tg.InputPeerClass) error {
	return m.blockErr
}
func (m *a3StatusTelegram) UnblockUser(context.Context, tg.InputPeerClass) error {
	return m.unblockErr
}

func TestA3DisapproveDBFailurePreservesApproval(t *testing.T) {
	const userID = int64(77)
	repo := newA3StatusRepo(userID, StatusApproved)
	svc := NewService(repo, nil, 1, nil, nil)
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || !approved {
		t.Fatalf("initial approved=%v err=%v", approved, err)
	}
	repo.failSet = StatusPending
	if err := svc.Disapprove(context.Background(), userID); err == nil {
		t.Fatal("expected durable status transition failure")
	}
	approved, err = svc.IsApproved(context.Background(), userID)
	if err != nil || !approved {
		t.Fatalf("failed disapproval invalidated durable approval: approved=%v err=%v", approved, err)
	}
}

func TestA3UnblockTelegramFailurePreservesBlockedState(t *testing.T) {
	const userID = int64(78)
	repo := newA3StatusRepo(userID, StatusBlocked)
	svc := NewService(repo, &a3StatusTelegram{unblockErr: errors.New("flood wait")}, 1, nil, nil)
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 11}
	if err := svc.Unblock(context.Background(), peer, userID); err == nil {
		t.Fatal("expected Telegram unblock failure")
	}
	rec, err := repo.GetPMRecord(context.Background(), userID)
	if err != nil || rec.Status != StatusBlocked {
		t.Fatalf("failed unblock changed durable status: %+v err=%v", rec, err)
	}
}

func TestA3BlockTelegramFailureRemainsFailClosed(t *testing.T) {
	const userID = int64(79)
	repo := newA3StatusRepo(userID, StatusApproved)
	svc := NewService(repo, &a3StatusTelegram{blockErr: errors.New("flood wait")}, 1, nil, nil)
	if approved, err := svc.IsApproved(context.Background(), userID); err != nil || !approved {
		t.Fatalf("initial approved=%v err=%v", approved, err)
	}
	if err := svc.BlockWithPeer(context.Background(), &tg.InputPeerUser{UserID: userID, AccessHash: 11}, userID, "spam"); err == nil {
		t.Fatal("expected Telegram block failure to be returned")
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || approved {
		t.Fatalf("blocked user remained approved in cache: approved=%v err=%v", approved, err)
	}
	rec, _ := repo.GetPMRecord(context.Background(), userID)
	if rec.Status != StatusBlocked {
		t.Fatalf("internal blocked state lost: %+v", rec)
	}
}

func TestA3ApproveWithPeerTelegramFailurePreservesBlockedState(t *testing.T) {
	const userID = int64(80)
	repo := newA3StatusRepo(userID, StatusBlocked)
	svc := NewService(repo, &a3StatusTelegram{unblockErr: errors.New("telegram down")}, 1, nil, nil)
	if err := svc.ApproveWithPeer(context.Background(), &tg.InputPeerUser{UserID: userID, AccessHash: 11}, userID, "trusted", 0); err == nil {
		t.Fatal("expected unblock failure")
	}
	rec, _ := repo.GetPMRecord(context.Background(), userID)
	if rec.Status != StatusBlocked {
		t.Fatalf("approval failure incorrectly downgraded blocked state: %+v", rec)
	}
}

func TestA3OutgoingAutoApproveFailurePreservesPending(t *testing.T) {
	const userID = int64(81)
	repo := newA3StatusRepo(userID, StatusPending)
	svc := NewService(repo, &a3StatusTelegram{unblockErr: errors.New("telegram down")}, 1, nil, nil)
	if err := svc.AutoApproveOutgoing(context.Background(), &tg.InputPeerUser{UserID: userID, AccessHash: 11}, userID); err == nil {
		t.Fatal("expected unblock failure")
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || approved {
		t.Fatalf("failed auto-approve granted access: approved=%v err=%v", approved, err)
	}
}

func TestA3IncomingSecondDBReadFailureSuppressesWarning(t *testing.T) {
	const userID = int64(82)
	repo := newA3StatusRepo(userID, StatusPending)
	repo.failReadAt = 2
	svc := NewService(repo, &a3StatusTelegram{}, 1, nil, nil)
	peer := &tg.InputPeerUser{UserID: userID, AccessHash: 11}
	handled, err := svc.HandleIncomingPM(context.Background(), peer, userID)
	if err != nil || !handled {
		t.Fatalf("DB fault must suppress unauthorized PM: handled=%v err=%v", handled, err)
	}
	if repo.warnIncrements != 0 {
		t.Fatalf("DB fault unexpectedly incremented warning count: %d", repo.warnIncrements)
	}
}

func TestA3ApprovalReadAndBlockPublishAtomically(t *testing.T) {
	const userID = int64(83)
	repo := newA3StatusRepo(userID, StatusApproved)
	repo.startedRead = make(chan struct{})
	repo.releaseRead = make(chan struct{})
	svc := NewService(repo, nil, 1, nil, nil)
	readDone := make(chan struct{})
	go func() {
		_, _ = svc.IsApproved(context.Background(), userID)
		close(readDone)
	}()
	select {
	case <-repo.startedRead:
	case <-time.After(time.Second):
		t.Fatal("approval read did not start")
	}
	blockDone := make(chan error, 1)
	go func() { blockDone <- svc.Block(context.Background(), userID, "spam") }()
	close(repo.releaseRead)
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("approval read did not complete")
	}
	select {
	case err := <-blockDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("block did not complete")
	}
	approved, err := svc.IsApproved(context.Background(), userID)
	if err != nil || approved {
		t.Fatalf("late approval cache publication bypassed blocked state: approved=%v err=%v", approved, err)
	}
}
