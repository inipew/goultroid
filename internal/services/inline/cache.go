package inline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/ui"
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
	runCtx        context.Context
	cancel        context.CancelFunc
	wake          chan struct{}
	workerRunning bool
	done          chan struct{}
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
	for i := range cloned {
		cloned[i].InteractionState = append([]byte(nil), results[i].InteractionState...)
		if results[i].ActionRows != nil {
			cloned[i].ActionRows = make([]presentation.Row, len(results[i].ActionRows))
			for rowIndex, row := range results[i].ActionRows {
				cloned[i].ActionRows[rowIndex] = append(presentation.Row(nil), row...)
			}
		}
		if results[i].Markup == nil {
			continue
		}
		markup := *results[i].Markup
		markup.Rows = make([]ui.ButtonRow, len(results[i].Markup.Rows))
		for rowIndex, row := range results[i].Markup.Rows {
			markup.Rows[rowIndex] = make(ui.ButtonRow, len(row))
			copy(markup.Rows[rowIndex], row)
			for buttonIndex := range markup.Rows[rowIndex] {
				markup.Rows[rowIndex][buttonIndex].Data = append([]byte(nil), row[buttonIndex].Data...)
			}
		}
		cloned[i].Markup = &markup
	}
	return cloned
}

func estimateResultSize(r *InlineResult) int64 {
	size := int64(len(r.ID) + len(r.Type) + len(r.Title) + len(r.Description) +
		len(r.Text) + len(r.ThumbURL) + len(r.URL) + len(r.MediaURL) +
		len(r.MediaMimeType) + len(r.GameShortName) + len(r.Address) +
		len(r.PhoneNumber) + len(r.FirstName) + len(r.LastName) + len(r.VCard) + 256)
	if r.Markup != nil {
		size += 64
		for _, row := range r.Markup.Rows {
			size += 24
			for _, button := range row {
				size += int64(len(button.Text)+len(button.Data)+len(button.URL)+len(button.InlineQuery)) + 64
			}
		}
	}
	return size
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
		wake:       make(chan struct{}, 1),
	}
}

func (c *Cache) startPrunerLocked() {
	if c.workerRunning || c.cancel == nil || c.runCtx == nil || c.runCtx.Err() != nil || len(c.entries) == 0 {
		return
	}
	done := make(chan struct{})
	c.workerRunning = true
	c.done = done
	runCtx := c.runCtx
	wake := c.wake
	go c.pruneLoop(runCtx, wake, done)
}

func (c *Cache) notifyWake() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.startPrunerLocked()
	running := c.workerRunning
	wake := c.wake
	c.mu.Unlock()
	if !running || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (c *Cache) nextExpiry() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var next time.Time
	for _, entry := range c.entries {
		if next.IsZero() || entry.expiresAt.Before(next) {
			next = entry.expiresAt
		}
	}
	return next, !next.IsZero()
}

func (c *Cache) pruneLoop(ctx context.Context, wake <-chan struct{}, done chan struct{}) {
	defer func() {
		c.mu.Lock()
		if c.done == done {
			c.workerRunning = false
			c.done = nil
		}
		c.mu.Unlock()
		close(done)
	}()

	for {
		next, ok := c.nextExpiry()
		if !ok {
			// Coordinate retirement with SetScoped. If an entry appears before
			// workerRunning is cleared, the recheck keeps this generation alive;
			// otherwise SetScoped starts a successor after retirement.
			c.mu.Lock()
			if c.done != done {
				c.mu.Unlock()
				return
			}
			if len(c.entries) == 0 {
				c.workerRunning = false
				c.done = nil
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			continue
		}

		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			c.Prune()
		}
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
	c.mu.Unlock()
	c.notifyWake()
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
	if entry, exists := c.entries[query]; exists {
		c.retainedBytes -= entry.sizeBytes
		delete(c.entries, query)
	}
	c.mu.Unlock()
	c.notifyWake()
}

// Prune removes all expired entries from the cache.
func (c *Cache) Prune() int {
	c.mu.Lock()
	now := time.Now()
	pruned := 0
	for q, entry := range c.entries {
		if now.After(entry.expiresAt) {
			c.retainedBytes -= entry.sizeBytes
			delete(c.entries, q)
			pruned++
		}
	}
	c.mu.Unlock()
	if pruned > 0 {
		c.notifyWake()
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

// Start activates cache lifecycle without creating an idle prune goroutine.
// A deadline coordinator exists only while the cache contains entries.
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
	c.wake = make(chan struct{}, 1)
	c.runCtx, c.cancel = context.WithCancel(ctx)
	c.startPrunerLocked()
	running := c.workerRunning
	wake := c.wake
	c.mu.Unlock()
	if running {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Stop terminates cache lifecycle and joins the prune coordinator only when it
// is currently active.
func (c *Cache) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.runCtx = nil
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
