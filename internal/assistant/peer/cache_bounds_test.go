package peer

import (
	"testing"
	"time"
)

func TestMemoryCacheBoundedAndTTLReclaimed(t *testing.T) {
	c := NewMemoryCache()
	for i := 0; i < maxMemoryCacheEntries+128; i++ {
		c.Put(PeerRecord{ID: int64(i + 1), Kind: PeerKindUser, AccessHash: int64(i + 100)})
	}
	if got := c.Len(); got > maxMemoryCacheEntries {
		t.Fatalf("peer cache grew past cap: got=%d cap=%d", got, maxMemoryCacheEntries)
	}

	expiredID := int64(1_000_000)
	c.Put(PeerRecord{
		ID: expiredID, Kind: PeerKindUser, AccessHash: 42,
		UpdatedAt: time.Now().Add(-memoryCacheTTL - time.Second),
	})
	if _, ok := c.Get(PeerKindUser, expiredID); ok {
		t.Fatal("expired peer cache entry survived lazy TTL reclamation")
	}
	if got := c.Len(); got > maxMemoryCacheEntries {
		t.Fatalf("peer cache grew past cap after TTL reclamation: got=%d cap=%d", got, maxMemoryCacheEntries)
	}
}
