package pmpermit

import (
	"context"
	"errors"
	"testing"
)

type a3WarningRepo struct {
	Repository
	ids      map[int64][]int
	addErr   error
	getErr   error
	clearErr error
	received context.Context
}

func (r *a3WarningRepo) AddWarnMsgID(ctx context.Context, userID int64, msgID int) error {
	r.received = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.addErr != nil {
		return r.addErr
	}
	if r.ids == nil {
		r.ids = make(map[int64][]int)
	}
	r.ids[userID] = append(r.ids[userID], msgID)
	return nil
}

func (r *a3WarningRepo) GetWarnMsgIDs(ctx context.Context, userID int64) ([]int, error) {
	r.received = ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.getErr != nil {
		return nil, r.getErr
	}
	return append([]int(nil), r.ids[userID]...), nil
}

func (r *a3WarningRepo) ClearWarnMsgIDs(ctx context.Context, userID int64) error {
	r.received = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.clearErr != nil {
		return r.clearErr
	}
	delete(r.ids, userID)
	return nil
}

func TestA3CWarningPersistenceFailureRetainsMemoryMarker(t *testing.T) {
	repo := &a3WarningRepo{addErr: errors.New("disk full"), getErr: errors.New("read unavailable")}
	s := NewService(repo, nil, 1, nil, nil)
	ctx := context.WithValue(context.Background(), struct{ kind string }{"test"}, "ingress")
	if err := s.addWarnID(ctx, 77, 301); err == nil {
		t.Fatal("write failure should be returned")
	}
	if repo.received != ctx {
		t.Fatal("write ignored ingress context")
	}
	found, err := s.IsWarnID(ctx, 77, 301)
	if err != nil || !found {
		t.Fatalf("failed DB write must retain bounded in-memory marker: found=%v err=%v", found, err)
	}
	s.warnMu.Lock()
	delete(s.warnIDs, 77)
	s.warnMu.Unlock()
	if found, err := s.IsWarnID(ctx, 77, 301); found || err == nil {
		t.Fatalf("missing cache plus DB failure must be unknown, not definitive miss: found=%v err=%v", found, err)
	}
}

func TestA3CClearFailureRetainsWarningMarkers(t *testing.T) {
	repo := &a3WarningRepo{clearErr: errors.New("read-only database")}
	s := NewService(repo, nil, 1, nil, nil)
	ctx := context.Background()
	if err := s.addWarnID(ctx, 77, 301); err != nil {
		t.Fatal(err)
	}
	if err := s.clearWarnIDs(ctx, 77); err == nil {
		t.Fatal("failed durable clear must return error")
	}
	found, err := s.IsWarnID(ctx, 77, 301)
	if err != nil || !found {
		t.Fatalf("clear failure forgot warning: found=%v err=%v", found, err)
	}
	repo.clearErr = nil
	if err := s.clearWarnIDs(ctx, 77); err != nil {
		t.Fatal(err)
	}
	found, err = s.IsWarnID(ctx, 77, 301)
	if err != nil || found {
		t.Fatalf("successful durable clear left warning: found=%v err=%v", found, err)
	}
}

func TestA3CWarnLookupUsesCallerCancellation(t *testing.T) {
	repo := &a3WarningRepo{}
	s := NewService(repo, nil, 1, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if found, err := s.IsWarnID(ctx, 77, 302); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("warning lookup ignored cancellation: found=%v err=%v", found, err)
	}
	if repo.received != ctx {
		t.Fatal("lookup did not use caller context")
	}
	if _, err := s.getWarnIDs(ctx, 77); !errors.Is(err, context.Canceled) {
		t.Fatalf("get warning IDs ignored cancellation: %v", err)
	}
	if err := s.addWarnID(ctx, 77, 302); !errors.Is(err, context.Canceled) {
		t.Fatalf("add warning ID ignored cancellation: %v", err)
	}
	if err := s.clearWarnIDs(ctx, 77); !errors.Is(err, context.Canceled) {
		t.Fatalf("clear warning ID ignored cancellation: %v", err)
	}
}

func TestA3CWarnLookupReloadsEvictedIDs(t *testing.T) {
	repo := &a3WarningRepo{}
	s := NewService(repo, nil, 1, nil, nil)
	ctx := context.Background()
	if err := s.addWarnID(ctx, 77, 302); err != nil {
		t.Fatal(err)
	}
	s.warnMu.Lock()
	delete(s.warnIDs, 77)
	s.warnMu.Unlock()
	found, err := s.IsWarnID(ctx, 77, 302)
	if err != nil || !found {
		t.Fatalf("evicted marker must reload from DB: found=%v err=%v", found, err)
	}
	ids, err := s.getWarnIDs(ctx, 77)
	if err != nil || len(ids) != 1 || ids[0] != 302 {
		t.Fatalf("warning history reload failed: ids=%v err=%v", ids, err)
	}
}
