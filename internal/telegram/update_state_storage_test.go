package telegram

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/inipew/goultroid/internal/database"
)

func newTestUpdateStateStorage(t *testing.T) (*UpdateStateStorage, *database.DB) {
	t.Helper()
	db, err := database.Open(fmt.Sprintf("file:update_state_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	storage := NewUpdateStateStorage(db)
	if err := storage.InitSchema(context.Background()); err != nil {
		t.Fatalf("init update state schema: %v", err)
	}
	return storage, db
}

func TestUpdateStateStoragePersistsUserStateAndComponentUpdates(t *testing.T) {
	ctx := context.Background()
	storage, _ := newTestUpdateStateStorage(t)
	const userID int64 = 42

	if _, found, err := storage.GetState(ctx, userID); err != nil || found {
		t.Fatalf("unexpected initial state: found=%v err=%v", found, err)
	}
	if err := storage.SetPts(ctx, userID, 1); !errors.Is(err, ErrUpdateStateNotInitialized) {
		t.Fatalf("SetPts before SetState error=%v, want ErrUpdateStateNotInitialized", err)
	}

	want := updates.State{Pts: 10, Qts: 20, Date: 30, Seq: 40}
	if err := storage.SetState(ctx, userID, want); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	got, found, err := storage.GetState(ctx, userID)
	if err != nil || !found || got != want {
		t.Fatalf("GetState=(%+v,%v,%v), want (%+v,true,nil)", got, found, err, want)
	}

	if err := storage.SetPts(ctx, userID, 11); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetQts(ctx, userID, 21); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetDate(ctx, userID, 31); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetSeq(ctx, userID, 41); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetDateSeq(ctx, userID, 32, 42); err != nil {
		t.Fatal(err)
	}
	want = updates.State{Pts: 11, Qts: 21, Date: 32, Seq: 42}
	got, found, err = storage.GetState(ctx, userID)
	if err != nil || !found || got != want {
		t.Fatalf("updated GetState=(%+v,%v,%v), want (%+v,true,nil)", got, found, err, want)
	}
}

func TestUpdateStateStoragePersistsChannelStateAndIsolatesUsers(t *testing.T) {
	ctx := context.Background()
	storage, db := newTestUpdateStateStorage(t)

	if err := storage.SetChannelPts(ctx, 1, 100, 7); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetChannelPts(ctx, 1, 200, 8); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetChannelPts(ctx, 2, 100, 99); err != nil {
		t.Fatal(err)
	}

	reopened := NewUpdateStateStorage(db)
	pts, found, err := reopened.GetChannelPts(ctx, 1, 100)
	if err != nil || !found || pts != 7 {
		t.Fatalf("GetChannelPts=(%d,%v,%v), want (7,true,nil)", pts, found, err)
	}

	var got [][2]int64
	if err := reopened.ForEachChannels(ctx, 1, func(_ context.Context, channelID int64, pts int) error {
		got = append(got, [2]int64{channelID, int64(pts)})
		return nil
	}); err != nil {
		t.Fatalf("ForEachChannels: %v", err)
	}
	want := [][2]int64{{100, 7}, {200, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels=%v, want %v", got, want)
	}
}

func TestUpdateStateStorageForEachChannelsPropagatesCallbackError(t *testing.T) {
	ctx := context.Background()
	storage, _ := newTestUpdateStateStorage(t)
	if err := storage.SetChannelPts(ctx, 1, 100, 7); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("stop")
	err := storage.ForEachChannels(ctx, 1, func(context.Context, int64, int) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("ForEachChannels error=%v, want %v", err, wantErr)
	}
}
