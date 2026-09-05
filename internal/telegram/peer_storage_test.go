package telegram

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/inipew/goultroid/internal/database"
)

func TestPeerStorage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(fmt.Sprintf("file:peer_test_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)

	// 1. Test Find on non-existent peer
	keyUser := peers.Key{Prefix: "user", ID: 12345}
	val, found, err := storage.Find(ctx, keyUser)
	if err != nil {
		t.Fatalf("unexpected error finding non-existent peer: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for non-existent peer")
	}

	// 2. Test Save & Find peer
	err = storage.Save(ctx, keyUser, peers.Value{AccessHash: 987654321})
	if err != nil {
		t.Fatalf("failed to save peer: %v", err)
	}

	val, found, err = storage.Find(ctx, keyUser)
	if err != nil {
		t.Fatalf("failed to find saved peer: %v", err)
	}
	if !found || val.AccessHash != 987654321 {
		t.Errorf("expected found=true and AccessHash=987654321, got found=%v, val=%+v", found, val)
	}

	// 3. Test Save overwrite/upsert
	err = storage.Save(ctx, keyUser, peers.Value{AccessHash: 1122334455})
	if err != nil {
		t.Fatalf("failed to update peer: %v", err)
	}
	val, found, err = storage.Find(ctx, keyUser)
	if err != nil {
		t.Fatalf("failed to find updated peer: %v", err)
	}
	if val.AccessHash != 1122334455 {
		t.Errorf("expected updated AccessHash=1122334455, got %d", val.AccessHash)
	}

	// 4. Test Phone mapping
	phone := "+1234567890"
	pKey, pVal, pFound, err := storage.FindPhone(ctx, phone)
	if err != nil {
		t.Fatalf("unexpected error finding phone: %v", err)
	}
	if pFound {
		t.Fatalf("expected found=false for non-existent phone")
	}

	err = storage.SavePhone(ctx, phone, keyUser)
	if err != nil {
		t.Fatalf("failed to save phone: %v", err)
	}

	pKey, pVal, pFound, err = storage.FindPhone(ctx, phone)
	if err != nil {
		t.Fatalf("failed to find saved phone: %v", err)
	}
	if !pFound {
		t.Fatalf("expected phone to be found")
	}
	if pKey != keyUser {
		t.Errorf("expected key %+v, got %+v", keyUser, pKey)
	}
	if pVal.AccessHash != 1122334455 {
		t.Errorf("expected val AccessHash=1122334455, got %d", pVal.AccessHash)
	}

	// 5. Test Contacts hash
	hash, err := storage.GetContactsHash(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting contacts hash: %v", err)
	}
	if hash != 0 {
		t.Errorf("expected initial contacts hash=0, got %d", hash)
	}

	err = storage.SaveContactsHash(ctx, 424242)
	if err != nil {
		t.Fatalf("failed to save contacts hash: %v", err)
	}

	hash, err = storage.GetContactsHash(ctx)
	if err != nil {
		t.Fatalf("failed to get contacts hash: %v", err)
	}
	if hash != 424242 {
		t.Errorf("expected contacts hash=424242, got %d", hash)
	}
}
