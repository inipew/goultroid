package settings

import (
	"context"
	"testing"
)

func TestResolveCacheRemainsBounded(t *testing.T) {
	svc := NewService(newMockRepo(), NewRegistry(), nil)
	ctx := context.Background()

	for i := 0; i < maxResolveCacheEntries+128; i++ {
		if _, err := svc.Resolve(ctx, int64(i+1), int64(10000+i), "missing", "key"); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}

	svc.cacheMu.RLock()
	accounted := svc.cacheEntries
	actual := 0
	for _, byKey := range svc.cache {
		actual += len(byKey)
	}
	svc.cacheMu.RUnlock()

	if accounted > maxResolveCacheEntries {
		t.Fatalf("resolve cache retained %d entries, cap=%d", accounted, maxResolveCacheEntries)
	}
	if actual != accounted {
		t.Fatalf("resolve cache accounting mismatch: actual=%d accounted=%d", actual, accounted)
	}

	svc.invalidate("missing", "key")
	svc.cacheMu.RLock()
	defer svc.cacheMu.RUnlock()
	if svc.cacheEntries != 0 || len(svc.cache) != 0 {
		t.Fatalf("specific invalidation did not release cache: entries=%d keys=%d", svc.cacheEntries, len(svc.cache))
	}
}
