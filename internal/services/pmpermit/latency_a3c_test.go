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

type a3SlowWarningRepo struct{ *a3StatusRepo }

func (r *a3SlowWarningRepo) AddWarnMsgID(context.Context, int64, int) error { return nil }

type a3SlowWarningTelegram struct {
	core.MockTelegramServicer
	stalledUser int64
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (s *a3SlowWarningTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	user, ok := peer.(*tg.InputPeerUser)
	if !ok {
		return nil, errors.New("not a private user peer")
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

func TestA3CSlowTelegramWarningDoesNotBlockUnrelatedPrivateUser(t *testing.T) {
	const firstID int64 = 440
	const secondID int64 = firstID + 128 // Collided under prior 128 stripes.
	repo := &a3SlowWarningRepo{a3StatusRepo: newA3StatusRepo(firstID, StatusPending)}
	repo.mu.Lock()
	repo.records[secondID] = PMPermitRecord{UserID: secondID, Status: StatusPending}
	repo.mu.Unlock()
	telegram := &a3SlowWarningTelegram{
		stalledUser: firstID,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	var release sync.Once
	free := func() { release.Do(func() { close(telegram.release) }) }
	defer free()

	svc := NewService(repo, telegram, 1, nil, nil)
	svc.SetWarnCooldown(0)
	svc.SetMaxWarns(100)
	ctx := context.Background()
	firstDone := make(chan error, 1)
	go func() {
		handled, err := svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: firstID, AccessHash: 1}, firstID)
		if err == nil && !handled {
			err = errors.New("first incoming PM bypassed permit")
		}
		firstDone <- err
	}()
	select {
	case <-telegram.entered:
	case <-time.After(time.Second):
		t.Fatal("first warning did not reach blocked Telegram RPC")
	}

	secondDone := make(chan error, 1)
	go func() {
		handled, err := svc.HandleIncomingPM(ctx, &tg.InputPeerUser{UserID: secondID, AccessHash: 1}, secondID)
		if err == nil && !handled {
			err = errors.New("second incoming PM bypassed permit")
		}
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated private user stalled behind Telegram FloodWait")
	}
	free()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first warning did not settle after Telegram resumed")
	}
}
