package database

import (
	"context"
	"testing"
	"time"
)

func TestCloneRepositoryRoundTrip(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	want := CloneState{
		OwnerID:       123,
		OriginalFirst: "Original",
		OriginalLast:  "User",
		OriginalBio:   "original bio",
		OriginalPhoto: "data/clone/original.jpg",
		ClonedPhoto:   true,
		Active:        true,
		UpdatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := db.SaveCloneState(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCloneState(context.Background(), want.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected clone state")
	}
	if got.OwnerID != want.OwnerID || got.OriginalFirst != want.OriginalFirst || got.OriginalLast != want.OriginalLast || got.OriginalBio != want.OriginalBio || got.OriginalPhoto != want.OriginalPhoto || got.ClonedPhoto != want.ClonedPhoto || !got.Active {
		t.Fatalf("unexpected state: %#v", got)
	}
	if err := db.ClearCloneState(context.Background(), want.OwnerID); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetCloneState(context.Background(), want.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected state to be cleared: %#v", got)
	}
}
