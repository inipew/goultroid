package peer

import (
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

// Cache stores entity access hashes to resolve peers without repeating RPC lookups.
type Cache interface {
	Get(kind PeerKind, id int64) (PeerRecord, bool)
	Put(rec PeerRecord)
	Invalidate(kind PeerKind, id int64)
	CacheEntities(entities tg.Entities)
	Len() int
}

// MemoryCache implements a concurrent in-memory Peer cache.
type MemoryCache struct {
	mu      sync.RWMutex
	records map[uint64]PeerRecord // key: (uint64(kind) << 56) | (uint64(id) & 0x00FFFFFFFFFFFFFF)
}

const (
	maxMemoryCacheEntries = 4096
	memoryCacheTTL        = 30 * time.Minute
)

// NewMemoryCache creates an initialized in-memory cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		records: make(map[uint64]PeerRecord),
	}
}

func cacheKey(kind PeerKind, id int64) uint64 {
	// 8 bits for kind, remaining for unsigned positive portion of id
	return (uint64(kind) << 56) | (uint64(id) & 0x00FFFFFFFFFFFFFF)
}

func (c *MemoryCache) pruneExpiredLocked(now time.Time) {
	for key, rec := range c.records {
		if !rec.UpdatedAt.IsZero() && now.Sub(rec.UpdatedAt) >= memoryCacheTTL {
			delete(c.records, key)
		}
	}
}

func (c *MemoryCache) ensureCapacityLocked(now time.Time) {
	if len(c.records) < maxMemoryCacheEntries {
		return
	}
	c.pruneExpiredLocked(now)
	if len(c.records) < maxMemoryCacheEntries {
		return
	}

	var oldestKey uint64
	var oldest time.Time
	found := false
	for key, rec := range c.records {
		if !found || rec.UpdatedAt.Before(oldest) {
			oldestKey = key
			oldest = rec.UpdatedAt
			found = true
		}
	}
	if found {
		delete(c.records, oldestKey)
	}
}

func (c *MemoryCache) putLocked(rec PeerRecord, now time.Time) {
	key := cacheKey(rec.Kind, rec.ID)
	if _, exists := c.records[key]; !exists {
		c.ensureCapacityLocked(now)
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	c.records[key] = rec
}

// Get retrieves a cached peer record.
func (c *MemoryCache) Get(kind PeerKind, id int64) (PeerRecord, bool) {
	key := cacheKey(kind, id)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.records[key]
	if ok && !rec.UpdatedAt.IsZero() && now.Sub(rec.UpdatedAt) >= memoryCacheTTL {
		delete(c.records, key)
		return PeerRecord{}, false
	}
	return rec, ok
}

// Put adds or updates a peer record in the cache.
func (c *MemoryCache) Put(rec PeerRecord) {
	if rec.ID == 0 {
		return
	}
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(rec, now)
}

// Invalidate removes a cached record for a given entity kind and ID.
func (c *MemoryCache) Invalidate(kind PeerKind, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.records, cacheKey(kind, id))
}

// CacheEntities extracts user and channel access hashes from a batch of received Telegram entities.
func (c *MemoryCache) CacheEntities(entities tg.Entities) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, u := range entities.Users {
		if u != nil && u.AccessHash != 0 {
			c.putLocked(PeerRecord{
				ID:         id,
				Kind:       PeerKindUser,
				AccessHash: u.AccessHash,
				UpdatedAt:  now,
			}, now)
		}
	}

	for id, ch := range entities.Channels {
		if ch != nil && ch.AccessHash != 0 {
			c.putLocked(PeerRecord{
				ID:         id,
				Kind:       PeerKindChannel,
				AccessHash: ch.AccessHash,
				UpdatedAt:  now,
			}, now)
		}
	}
}

// Len returns the total number of cached records.
func (c *MemoryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.records)
}
