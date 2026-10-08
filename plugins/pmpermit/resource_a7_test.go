package pmpermit

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	pmservice "github.com/inipew/goultroid/internal/services/pmpermit"
	"go.uber.org/zap"
)

// A7 keeps resource measurements diagnostic: absolute RSS and goroutine counts
// vary between Go versions and hosts, while the persisted/cooldown bounds below
// are deterministic behavior gates.
func TestA7PMPermitSQLiteHighCardinalitySaturationAndRestart(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	svc := pmservice.NewService(repo, nil, 1, nil, zap.NewNop())
	svc.SetWarnCooldown(time.Hour)
	svc.SetMaxWarns(100)
	ctx := context.Background()
	baselineGoroutines := runtime.NumGoroutine()
	began := time.Now()

	const admitted = 2048 // Production PMPermit cooldown limit.
	const overflow = 256
	for i := 0; i < admitted+overflow; i++ {
		id := int64(i + 10000)
		handled, err := svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: id, AccessHash: 1}, id)
		if err != nil || !handled {
			t.Fatalf("user %d escaped PM security decision: handled=%v err=%v", id, handled, err)
		}
	}
	pending, approved, blocked, err := repo.CountPMRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Once the in-memory cooldown is saturated, an unseen user is suppressed
	// rather than producing another SQLite mutation or Telegram warning.
	if pending != admitted || approved != 0 || blocked != 0 {
		t.Fatalf("cooldown capacity escaped bounds: pending=%d approved=%d blocked=%d", pending, approved, blocked)
	}
	for _, id := range []int64{10000, 10000 + admitted - 1} {
		rec, err := repo.GetPMRecord(ctx, id)
		if err != nil || rec == nil || rec.WarnCount != 1 {
			t.Fatalf("persisted warning missing for user %d: rec=%+v err=%v", id, rec, err)
		}
	}
	noRow, err := repo.GetPMRecord(ctx, 10000+admitted)
	if err != nil || noRow != nil {
		t.Fatalf("overflow sender should be suppressed without durable mutation: %+v %v", noRow, err)
	}

	// Simulated service restart retains SQLite warnings but does not turn
	// capacity saturation into permanent rejection of existing senders.
	restarted := pmservice.NewService(repo, nil, 1, nil, zap.NewNop())
	restarted.SetWarnCooldown(time.Hour)
	restarted.SetMaxWarns(100)
	handled, err := restarted.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: 10000, AccessHash: 1}, 10000)
	if err != nil || !handled {
		t.Fatalf("restart lost PM security decision: handled=%v err=%v", handled, err)
	}
	rec, err := repo.GetPMRecord(ctx, 10000)
	if err != nil || rec == nil || rec.WarnCount != 2 {
		t.Fatalf("restart reset durable PM warnings: %+v %v", rec, err)
	}
	t.Logf("A7 SQLite PM admission: attempts=%d admitted=%d overflow_suppressed=%d duration=%s goroutines_before=%d goroutines_after=%d",
		admitted+overflow, admitted, overflow, time.Since(began), baselineGoroutines, runtime.NumGoroutine())
}

type a7PMPermitBlockedTransport struct {
	core.MockTelegramServicer
	stalledUser int64
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (s *a7PMPermitBlockedTransport) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	user, ok := peer.(*tg.InputPeerUser)
	if !ok {
		return nil, core.ErrInvalidArgs
	}
	if user.UserID == s.stalledUser {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &tg.Message{ID: int(user.UserID), Message: text}, nil
}

func TestA7PMPermitSQLiteFloodWaitCancellationDoesNotSerializeUnrelatedPM(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	const slowID int64 = 51001
	const fastID int64 = 51002
	svcTG := &a7PMPermitBlockedTransport{
		stalledUser: slowID,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	defer close(svcTG.release)
	svc := pmservice.NewService(repo, svcTG, 1, nil, zap.NewNop())
	svc.SetWarnCooldown(0)
	svc.SetMaxWarns(100)

	slowCtx, cancelSlow := context.WithCancel(context.Background())
	defer cancelSlow()
	slowDone := make(chan error, 1)
	go func() {
		handled, err := svc.HandleIncomingPM(slowCtx, &tg.InputPeerUser{UserID: slowID, AccessHash: 1}, slowID)
		if err == nil && !handled {
			err = core.ErrInvalidArgs
		}
		slowDone <- err
	}()
	select {
	case <-svcTG.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("injected FloodWait did not reach the Telegram transport")
	}

	// A separate sender's durable warning and Telegram response must finish
	// while the first sender remains blocked on the shared RPC boundary.
	fastDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		handled, err := svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: fastID, AccessHash: 1}, fastID)
		if err == nil && !handled {
			err = core.ErrInvalidArgs
		}
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated PM was delayed by another sender's FloodWait")
	}
	fastRecord, err := repo.GetPMRecord(context.Background(), fastID)
	if err != nil || fastRecord == nil || fastRecord.WarnCount != 1 {
		t.Fatalf("unrelated PM did not commit while FloodWait active: %+v %v", fastRecord, err)
	}
	cancelSlow()
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled Telegram FloodWait retained PM worker")
	}
	slowRecord, err := repo.GetPMRecord(context.Background(), slowID)
	if err != nil || slowRecord == nil || slowRecord.WarnCount != 1 {
		t.Fatalf("cancellation rolled back already-persisted security state: %+v %v", slowRecord, err)
	}
}
