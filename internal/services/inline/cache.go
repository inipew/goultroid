package inline

import (
	"context"
	"fmt"
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
	cancel     context.CancelFunc
	wg         sync.WaitGroup
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

// ScopedKey builds a cache key incorporating query, offset, handler, and optional scopes.
func ScopedKey(handlerPattern, query, offset string, policy CachePolicy, userID int64) string {
	return ScopedKeyEx(handlerPattern, query, offset, policy, userID, 0, "", "")
}

// ScopedKeyEx includes locale, chat and version dimensions per audit: handler/version + query + offset + user/chat/locale
func ScopedKeyEx(handlerPattern, query, offset string, policy CachePolicy, userID int64, chatID int64, locale, version string) string {
	if policy == CacheNone {
		return ""
	}
	base := handlerPattern
	if version != "" {
		base = fmt.Sprintf("%s|v:%s", handlerPattern, version)
	}
	base = fmt.Sprintf("%s|%s|%s", base, query, offset)
	switch policy {
	case CachePerUser:
		base = fmt.Sprintf("%s|u:%d", base, userID)
	case CachePerChat:
		if chatID != 0 {
			base = fmt.Sprintf("%s|c:%d", base, chatID)
		}
		// if chatID 0 fallback to global behavior but still distinct from PerUser
	}
	if locale != "" {
		base = fmt.Sprintf("%s|l:%s", base, locale)
	}
	return base
}

// HandlerVersion extracts version string if handler implements Version() string.
func HandlerVersion(h InlineHandler) string {
	if v, ok := h.(interface{ Version() string }); ok {
		return v.Version()
	}
	return ""
}

// Get retrieves cached inline results for a query if not expired.
func (c *Cache) Get(query string) ([]InlineResult, bool) {
	return c.GetScoped(query)
}

// GetScoped retrieves by full scoped key.
func (c *Cache) GetScoped(key string) ([]InlineResult, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()

	if !exists {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		c.Delete(key)
		return nil, false
	}

	return entry.results, true
}

// Set stores inline results in the cache with the default or specified TTL.
func (c *Cache) Set(query string, results []InlineResult, ttl time.Duration) {
	c.SetScoped(query, results, ttl)
}

const maxInlineCacheEntries = 500

// SetScoped stores by full scoped key.
func (c *Cache) SetScoped(key string, results []InlineResult, ttl time.Duration) {
	if key == "" {
		return
	}
	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= maxInlineCacheEntries {
		// evict expired first
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxInlineCacheEntries {
			// evict oldest
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, e := range c.entries {
				if first || e.expiresAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = e.expiresAt
					first = false
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
	}

	c.entries[key] = cachedEntry{
		results:   results,
		expiresAt: time.Now().Add(ttl),
	}
}

// Len returns entry count.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
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

// Start launches background prune loop (Fase 4 shutdown-aware).
func (c *Cache) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.Prune()
			case <-runCtx.Done():
				return
			}
		}
	}()
}

// Stop terminates background prune loop.
func (c *Cache) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		c.wg.Wait()
	}
}
