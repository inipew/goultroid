package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestPeerCache_BoundedEviction(t *testing.T) {
	clock := NewFakeClock(time.Now())
	cache := NewPeerCache(ResolverCacheConfig{
		MaxEntries:  3,
		PositiveTTL: 10 * time.Minute,
		Clock:       clock,
	})

	cache.Set("user", "alice", "user", 1, 100)
	cache.Set("user", "bob", "user", 2, 200)
	cache.Set("user", "charlie", "user", 3, 300)

	if cache.Len() != 3 {
		t.Fatalf("expected len 3, got %d", cache.Len())
	}

	// 4th entry evicts oldest (alice)
	cache.Set("user", "david", "user", 4, 400)

	if cache.Len() != 3 {
		t.Fatalf("expected len bounded at 3, got %d", cache.Len())
	}

	if _, hit := cache.Get("user", "alice"); hit {
		t.Fatal("expected alice to be evicted")
	}
	if _, hit := cache.Get("user", "bob"); !hit {
		t.Fatal("expected bob to remain")
	}
	if _, hit := cache.Get("user", "david"); !hit {
		t.Fatal("expected david to be present")
	}
}

func TestPeerCache_Expiration(t *testing.T) {
	clock := NewFakeClock(time.Now())
	cache := NewPeerCache(ResolverCacheConfig{
		MaxEntries:  10,
		PositiveTTL: 5 * time.Minute,
		Clock:       clock,
	})

	cache.Set("user", "alice", "user", 1, 100)

	if _, hit := cache.Get("user", "alice"); !hit {
		t.Fatal("expected hit before expiration")
	}

	// Advance clock past TTL
	clock.Advance(6 * time.Minute)

	if _, hit := cache.Get("user", "alice"); hit {
		t.Fatal("expected miss after expiration")
	}
}

func TestPeerCache_NegativeCaching(t *testing.T) {
	clock := NewFakeClock(time.Now())
	cache := NewPeerCache(ResolverCacheConfig{
		MaxEntries:  10,
		NegativeTTL: 30 * time.Second,
		Clock:       clock,
	})

	cache.SetNegative("user", "unknown_user")

	entry, hit := cache.Get("user", "unknown_user")
	if !hit {
		t.Fatal("expected negative cache hit")
	}
	if !entry.Negative {
		t.Fatal("expected entry to be marked negative")
	}

	clock.Advance(31 * time.Second)
	if _, hit := cache.Get("user", "unknown_user"); hit {
		t.Fatal("expected negative entry to expire")
	}
}

func TestResolver_MemoryHit_ZeroStorageAndRPC(t *testing.T) {
	resolver := NewResolver(nil, nil)
	resolver.cache.Set("user", "alice", "user", 12345, 99999)

	peer, id, err := resolver.ResolveUser(context.Background(), "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 12345 {
		t.Fatalf("expected id 12345, got %d", id)
	}
	uPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || uPeer.AccessHash != 99999 {
		t.Fatalf("expected InputPeerUser with access hash 99999, got %+v", peer)
	}
}

func TestResolver_PersistentHit_PopulatesMemoryCache(t *testing.T) {
	db, err := database.Open(fmt.Sprintf("file:peer_res_test_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	ctx := context.Background()
	_ = storage.SaveEntity(ctx, "user", 55555, "bob", "", "", "", "")
	_ = storage.Save(ctx, peers.Key{Prefix: "user", ID: 55555}, peers.Value{AccessHash: 77777})

	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)

	// 1. First resolve hits storage and populates memory cache
	peer, id, err := resolver.ResolveUser(ctx, "bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 55555 {
		t.Fatalf("expected id 55555, got %d", id)
	}
	uPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || uPeer.AccessHash != 77777 {
		t.Fatalf("expected InputPeerUser with access hash 77777, got %+v", peer)
	}

	// 2. Memory cache should now have bob
	entry, hit := resolver.cache.Get("user", "bob")
	if !hit || entry.ID != 55555 || entry.AccessHash != 77777 {
		t.Fatalf("expected memory cache to be populated, got hit=%v entry=%+v", hit, entry)
	}
}

func TestResolver_NegativeHit_ReturnsNotFoundWithoutRPC(t *testing.T) {
	resolver := NewResolver(nil, nil)
	resolver.cache.SetNegative("user", "nobody")

	_, _, err := resolver.ResolveUser(context.Background(), "nobody")
	if err == nil || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expected core.ErrNotFound, got %v", err)
	}
}

func TestResolver_Invalidate_RemovesMemoryAndPersistent(t *testing.T) {
	db, err := database.Open(fmt.Sprintf("file:peer_inv_test_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	ctx := context.Background()
	_ = storage.Save(ctx, peers.Key{Prefix: "user", ID: 123}, peers.Value{AccessHash: 456})

	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)
	resolver.cache.Set("user", "123", "user", 123, 456)

	err = resolver.Invalidate(ctx, &tg.InputPeerUser{UserID: 123, AccessHash: 456})
	if err != nil {
		t.Fatalf("unexpected invalidate error: %v", err)
	}

	// Verify memory cache invalidated
	if _, hit := resolver.cache.Get("user", "123"); hit {
		t.Fatal("expected memory cache to be invalidated")
	}

	// Verify persistent storage invalidated
	val, found, err := storage.Find(ctx, peers.Key{Prefix: "user", ID: 123})
	if err != nil || (found && val.AccessHash != 0) {
		t.Fatalf("expected persistent storage to be deleted/empty, found=%v val=%+v", found, val)
	}
}

func TestResolver_Singleflight_ConcurrentRequests(t *testing.T) {
	resolver := NewResolver(nil, nil)

	var rpcCalls atomic.Int32
	// Simulated shared singleflight channel
	group := &resolver.group
	const callers = 10
	var wg sync.WaitGroup
	wg.Add(callers)

	type userRes struct {
		id int64
	}

	results := make([]int64, callers)
	for i := 0; i < callers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			ch := group.DoChan("test:concurrent", func() (any, error) {
				rpcCalls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return userRes{id: 999}, nil
			})
			res := <-ch
			if res.Err != nil {
				t.Errorf("caller %d failed: %v", idx, res.Err)
				return
			}
			results[idx] = res.Val.(userRes).id
		}()
	}

	wg.Wait()

	if rpcCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 underlying RPC call, got %d", rpcCalls.Load())
	}
	for i, id := range results {
		if id != 999 {
			t.Errorf("caller %d got id %d, want 999", i, id)
		}
	}
}

func TestResolver_Singleflight_OneCancelDoesNotCancelOthers(t *testing.T) {
	resolver := NewResolver(nil, nil)
	group := &resolver.group

	var rpcFinished atomic.Bool

	// Start slow operation
	opFunc := func() (any, error) {
		time.Sleep(50 * time.Millisecond)
		rpcFinished.Store(true)
		return "done", nil
	}

	// Caller 1 cancels early
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel1()
	ch1 := group.DoChan("test:cancel", opFunc)

	// Caller 2 waits fully
	ch2 := group.DoChan("test:cancel", opFunc)

	// Wait on Caller 1
	select {
	case <-ctx1.Done():
		// Canceled as expected
	case <-ch1:
		t.Fatal("caller 1 should have timed out before result")
	}

	// Caller 2 should successfully receive result
	select {
	case <-time.After(200 * time.Millisecond):
		t.Fatal("caller 2 timed out waiting for shared result")
	case res2 := <-ch2:
		if res2.Err != nil {
			t.Fatalf("caller 2 unexpected error: %v", res2.Err)
		}
		if res2.Val != "done" {
			t.Fatalf("caller 2 unexpected value: %v", res2.Val)
		}
	}

	if !rpcFinished.Load() {
		t.Fatal("underlying operation should have finished")
	}
}

func TestResolver_Len(t *testing.T) {
	cache := NewPeerCache(ResolverCacheConfig{MaxEntries: 5})
	for i := 1; i <= 3; i++ {
		cache.Set("user", strconv.Itoa(i), "user", int64(i), int64(i*10))
	}
	if cache.Len() != 3 {
		t.Fatalf("expected len 3, got %d", cache.Len())
	}
}

func TestPeerCache_TombstoneCompaction(t *testing.T) {
	cache := NewPeerCache(ResolverCacheConfig{MaxEntries: 2})

	// Set 2 entries
	cache.Set("user", "1", "user", 1, 100)
	cache.Set("user", "2", "user", 2, 200)

	// Invalidate both entries (leaves tombstones in order slice)
	cache.Invalidate("user", "1")
	cache.Invalidate("user", "2")

	// Now add new entries to trigger compaction (len(order) > 2 * MaxEntries)
	cache.Set("user", "3", "user", 3, 300)
	cache.Set("user", "4", "user", 4, 400)
	cache.Set("user", "5", "user", 5, 500)

	cache.mu.RLock()
	orderLen := len(cache.order)
	cacheLen := len(cache.entries)
	cache.mu.RUnlock()

	if cacheLen > 2 {
		t.Fatalf("expected entries bounded to MaxEntries (2), got %d", cacheLen)
	}
	if orderLen > 2*cache.cfg.MaxEntries {
		t.Fatalf("expected order slice bounded by compaction, got %d", orderLen)
	}
}


func TestPeerCache_IdleStorageIsLazy(t *testing.T) {
	cache := NewPeerCache(ResolverCacheConfig{MaxEntries: 1000})
	if cache.entries != nil {
		t.Fatalf("idle cache eagerly allocated entries map with len=%d", len(cache.entries))
	}
	if cache.order != nil {
		t.Fatalf("idle cache eagerly allocated order slice with len=%d cap=%d", len(cache.order), cap(cache.order))
	}
	if cache.Len() != 0 {
		t.Fatalf("idle cache len=%d, want 0", cache.Len())
	}
	if _, ok := cache.Get("user", "alice"); ok {
		t.Fatal("idle cache unexpectedly returned a hit")
	}

	cache.Set("user", "alice", "user", 1, 2)
	if cache.entries == nil {
		t.Fatal("first cache write did not initialize entries")
	}
	if got := cache.Len(); got != 1 {
		t.Fatalf("cache len=%d after first write, want 1", got)
	}
	if cap(cache.order) >= cache.cfg.MaxEntries {
		t.Fatalf("first write preallocated full order capacity: cap=%d max=%d", cap(cache.order), cache.cfg.MaxEntries)
	}
}
