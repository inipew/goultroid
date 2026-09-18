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
	if !found {
		t.Fatalf("expected updated peer to be found")
	}
	if val.AccessHash != 1122334455 {
		t.Errorf("expected updated AccessHash=1122334455, got %d", val.AccessHash)
	}

	// 3b. Username metadata survives access-hash invalidation and can drive refresh.
	if err := storage.SaveEntity(ctx, "user", keyUser.ID, "alice", "", "", "", ""); err != nil {
		t.Fatalf("failed to save peer entity: %v", err)
	}
	username, usernameFound, err := storage.FindUsernameByID(ctx, "user", keyUser.ID)
	if err != nil {
		t.Fatalf("failed to find username by id: %v", err)
	}
	if !usernameFound || username != "alice" {
		t.Fatalf("expected username alice, found=%v username=%q", usernameFound, username)
	}
	if err := storage.InvalidateContext(ctx, keyUser); err != nil {
		t.Fatalf("failed to invalidate peer with caller context: %v", err)
	}
	if _, found, err := storage.Find(ctx, keyUser); err != nil {
		t.Fatalf("failed to verify invalidation: %v", err)
	} else if found {
		t.Fatal("expected access hash to be removed after invalidation")
	}
	if err := storage.Save(ctx, keyUser, peers.Value{AccessHash: 1122334455}); err != nil {
		t.Fatalf("failed to restore peer for remaining tests: %v", err)
	}

	// 4. Test Phone mapping
	phone := "+1234567890"
	_, _, pFound, err := storage.FindPhone(ctx, phone)
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

	pKey, pVal, pFound, err := storage.FindPhone(ctx, phone)
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

func TestPeerStorageInvalidateContextClearsPhoneAccessHash(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(fmt.Sprintf("file:peer_phone_invalidate_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	key := peers.Key{Prefix: "user", ID: 77}
	if err := storage.Save(ctx, key, peers.Value{AccessHash: 998877}); err != nil {
		t.Fatalf("save peer: %v", err)
	}
	if err := storage.SavePhone(ctx, "+620000077", key); err != nil {
		t.Fatalf("save phone: %v", err)
	}
	if err := storage.InvalidateContext(ctx, key); err != nil {
		t.Fatalf("invalidate peer: %v", err)
	}
	foundKey, value, found, err := storage.FindPhone(ctx, "+620000077")
	if err != nil {
		t.Fatalf("find phone after invalidation: %v", err)
	}
	if !found || foundKey != key {
		t.Fatalf("expected phone mapping to survive invalidation, found=%v key=%+v", found, foundKey)
	}
	if value.AccessHash != 0 {
		t.Fatalf("expected phone access hash to be cleared, got %d", value.AccessHash)
	}
}

func TestPeerStorageProcessCacheIsHardBounded(t *testing.T) {
	storage := NewPeerStorage(nil)

	storage.mu.Lock()
	for i := 0; i < maxPeerStorageCacheEntries+250; i++ {
		key := peers.Key{Prefix: "user", ID: int64(i + 1)}
		storage.cachePeerLocked(key, int64(i+1000))
		storage.cacheEntityLocked(fmt.Sprintf("user:%d", i+1), peerEntitySnapshot{username: fmt.Sprintf("u%d", i+1)})
	}
	peerLen := len(storage.peers)
	entityLen := len(storage.entities)
	storage.mu.Unlock()

	if peerLen > maxPeerStorageCacheEntries {
		t.Fatalf("peer cache exceeded cap: %d > %d", peerLen, maxPeerStorageCacheEntries)
	}
	if entityLen > maxPeerStorageCacheEntries {
		t.Fatalf("entity cache exceeded cap: %d > %d", entityLen, maxPeerStorageCacheEntries)
	}
}
