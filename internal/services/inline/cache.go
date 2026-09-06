package inline

import (
	"sync"
	"time"
)

type cachedEntry struct {
	results   []InlineResult
	expiresAt time.Time
}

// Cache provides thread-safe short-lived caching for inline results.
type Cache struct {
	mu         sync.RWMutex
	entries    map[string]cachedEntry
	defaultTTL time.Duration
}

// NewCache creates an initialized Cache.
func NewCache(defaultTTL time.Duration) *Cache {
	if defaultTTL <= 0 {
		defaultTTL = 30 * time.Second
	}
	return &Cache{
		entries:    make(map[string]cachedEntry),
		defaultTTL: defaultTTL,
	}
}

// Get retrieves cached inline results for a query if not expired.
func (c *Cache) Get(query string) ([]InlineResult, bool) {
	c.mu.RLock()
	entry, exists := c.entries[query]
	c.mu.RUnlock()

	if !exists {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		c.Delete(query)
		return nil, false
	}

	return entry.results, true
}

// Set stores inline results in the cache with the default or specified TTL.
func (c *Cache) Set(query string, results []InlineResult, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[query] = cachedEntry{
		results:   results,
		expiresAt: time.Now().Add(ttl),
	}
}

// Delete removes a specific query from the cache.
func (c *Cache) Delete(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, query)
}

// Prune removes all expired entries from the cache.
func (c *Cache) Prune() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	pruned := 0
	for q, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, q)
			pruned++
		}
	}
	return pruned
}
