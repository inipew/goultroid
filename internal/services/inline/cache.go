package inline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

var _ runtime.Component = (*Cache)(nil)

type cachedEntry struct {
	results   []InlineResult
	expiresAt time.Time
	sizeBytes int64
}

// Cache provides thread-safe short-lived caching for inline results.
type Cache struct {
	mu            sync.RWMutex
	entries       map[string]cachedEntry
	retainedBytes int64
	defaultTTL    time.Duration
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

const (
	maxInlineCacheEntries = 500
	maxInlineCacheBytes   = 8 * 1024 * 1024 // 8MB budget
	maxSingleEntryBytes   = 256 * 1024      // 256KB max per entry
)

func cloneInlineResults(results []InlineResult) []InlineResult {
	if results == nil {
		return nil
	}
	cloned := make([]InlineResult, len(results))
	copy(cloned, results)
	return cloned
}

func estimateResultSize(r *InlineResult) int64 {
	return int64(len(r.ID) + len(r.Type) + len(r.Title) + len(r.Description) +
		len(r.Text) + len(r.ThumbURL) + len(r.URL) + len(r.MediaURL) +
		len(r.MediaMimeType) + len(r.GameShortName) + len(r.Address) +
		len(r.PhoneNumber) + len(r.FirstName) + len(r.LastName) + len(r.VCard) + 256)
}

func estimateResultsSliceSize(results []InlineResult) int64 {
	var total int64 = 64
	for i := range results {
		total += estimateResultSize(&results[i])
	}
	return total
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

	return cloneInlineResults(entry.results), true
}

// Set stores inline results in the cache with the default or specified TTL.
func (c *Cache) Set(query string, results []InlineResult, ttl time.Duration) {
	c.SetScoped(query, results, ttl)
}

// SetScoped stores by full scoped key.
func (c *Cache) SetScoped(key string, results []InlineResult, ttl time.Duration) {
	if key == "" {
		return
	}
	size := estimateResultsSliceSize(results)
	if size > maxSingleEntryBytes {
		return
	}
	cloned := cloneInlineResults(results)

	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if old, exists := c.entries[key]; exists {
		c.retainedBytes -= old.sizeBytes
		delete(c.entries, key)
	}

	if len(c.entries) >= maxInlineCacheEntries || c.retainedBytes+size > maxInlineCacheBytes {
		// evict expired first
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				c.retainedBytes -= e.sizeBytes
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= maxInlineCacheEntries || (len(c.entries) > 0 && c.retainedBytes+size > maxInlineCacheBytes) {
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
				c.retainedBytes -= c.entries[oldestKey].sizeBytes
				delete(c.entries, oldestKey)
			} else {
				break
			}
		}
	}

	c.entries[key] = cachedEntry{
		results:   cloned,
		expiresAt: time.Now().Add(ttl),
		sizeBytes: size,
	}
	c.retainedBytes += size
}

// Len returns entry count.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// RetainedBytes returns current retained byte size (for metrics/testing).
func (c *Cache) RetainedBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.retainedBytes
}

// Delete removes a specific query from the cache.
func (c *Cache) Delete(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, exists := c.entries[query]; exists {
		c.retainedBytes -= entry.sizeBytes
		delete(c.entries, query)
	}
}

// Prune removes all expired entries from the cache.
func (c *Cache) Prune() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	pruned := 0
	for q, entry := range c.entries {
		if now.After(entry.expiresAt) {
			c.retainedBytes -= entry.sizeBytes
			delete(c.entries, q)
			pruned++
		}
	}
	return pruned
}

// Name returns component identifier for runtime.Component.
func (c *Cache) Name() string {
	return "inline_cache"
}

// Dependencies returns prerequisite components for runtime.Component.
func (c *Cache) Dependencies() []string {
	return []string{"taskengine"}
}

// Health probes the health status of the inline cache.
func (c *Cache) Health(ctx context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// Start launches background prune loop (Fase 4 shutdown-aware).
func (c *Cache) Start(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return nil
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
	return nil
}

// Stop terminates background prune loop.
func (c *Cache) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()
		if ctx != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			<-done
		}
	}
	return nil
}
