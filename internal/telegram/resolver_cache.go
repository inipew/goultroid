package telegram

import (
	"strings"
	"sync"
	"time"
)

type peerCacheKey struct {
	Kind string
	Ref  string
}

type peerCacheEntry struct {
	Prefix     string
	ID         int64
	AccessHash int64
	ExpiresAt  time.Time
	Negative   bool
}

// ResolverCacheConfig specifies capacity and TTL bounds for peer caching.
type ResolverCacheConfig struct {
	MaxEntries           int
	MaxConcurrentNetwork int
	PositiveTTL          time.Duration
	NegativeTTL          time.Duration
	Clock                Clock
}

func defaultResolverCacheConfig() ResolverCacheConfig {
	return ResolverCacheConfig{
		MaxEntries:           1000,
		MaxConcurrentNetwork: 16,
		PositiveTTL:          15 * time.Minute,
		NegativeTTL:          30 * time.Second,
	}
}

// DefaultResolverCacheConfig is retained for compatibility. Runtime defaults
// use defaultResolverCacheConfig so mutating this exported value cannot alter
// future production resolver/cache construction.
var DefaultResolverCacheConfig = defaultResolverCacheConfig()

// PeerCache is a bounded, concurrency-safe in-memory cache for resolved Telegram peers.
type PeerCache struct {
	mu      sync.RWMutex
	cfg     ResolverCacheConfig
	entries map[peerCacheKey]peerCacheEntry
	order   []peerCacheKey
	clock   Clock
}

// NewPeerCache initializes a bounded peer cache.
func NewPeerCache(cfg ResolverCacheConfig) *PeerCache {
	defaults := defaultResolverCacheConfig()
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaults.MaxEntries
	}
	if cfg.MaxConcurrentNetwork <= 0 {
		cfg.MaxConcurrentNetwork = defaults.MaxConcurrentNetwork
	}
	if cfg.PositiveTTL <= 0 {
		cfg.PositiveTTL = defaults.PositiveTTL
	}
	if cfg.NegativeTTL <= 0 {
		cfg.NegativeTTL = defaults.NegativeTTL
	}
	clock := cfg.Clock
	if clock == nil {
		clock = DefaultClock
	}

	return &PeerCache{
		cfg:     cfg,
		entries: make(map[peerCacheKey]peerCacheEntry, cfg.MaxEntries),
		order:   make([]peerCacheKey, 0, cfg.MaxEntries),
		clock:   clock,
	}
}

func normalizeRef(ref string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, "@")))
}

// Get returns the cached entry for kind and ref if present and unexpired.
func (c *PeerCache) Get(kind, ref string) (peerCacheEntry, bool) {
	norm := normalizeRef(ref)
	if norm == "" {
		return peerCacheEntry{}, false
	}
	key := peerCacheKey{Kind: kind, Ref: norm}

	c.mu.RLock()
	entry, found := c.entries[key]
	c.mu.RUnlock()

	if !found {
		return peerCacheEntry{}, false
	}

	now := c.clock.Now()
	if now.After(entry.ExpiresAt) {
		c.mu.Lock()
		if current, exists := c.entries[key]; exists && now.After(current.ExpiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return peerCacheEntry{}, false
	}

	return entry, true
}

// Set saves a positive peer resolution in the memory cache.
func (c *PeerCache) Set(kind, ref, prefix string, id, accessHash int64) {
	norm := normalizeRef(ref)
	if norm == "" {
		return
	}
	key := peerCacheKey{Kind: kind, Ref: norm}
	entry := peerCacheEntry{
		Prefix:     prefix,
		ID:         id,
		AccessHash: accessHash,
		ExpiresAt:  c.clock.Now().Add(c.cfg.PositiveTTL),
		Negative:   false,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.putUnderLock(key, entry)
}

// SetNegative saves a short-lived negative result for a non-existent entity.
func (c *PeerCache) SetNegative(kind, ref string) {
	norm := normalizeRef(ref)
	if norm == "" {
		return
	}
	key := peerCacheKey{Kind: kind, Ref: norm}
	entry := peerCacheEntry{
		ExpiresAt: c.clock.Now().Add(c.cfg.NegativeTTL),
		Negative:  true,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.putUnderLock(key, entry)
}

func (c *PeerCache) putUnderLock(key peerCacheKey, entry peerCacheEntry) {
	if _, exists := c.entries[key]; !exists {
		// Evict oldest if full
		if len(c.entries) >= c.cfg.MaxEntries {
			c.evictOneUnderLock()
		}
		c.order = append(c.order, key)
		if len(c.order) > 2*c.cfg.MaxEntries {
			c.compactOrderUnderLock()
		}
	}
	c.entries[key] = entry
}

func (c *PeerCache) evictOneUnderLock() {
	for len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, exists := c.entries[oldest]; exists {
			delete(c.entries, oldest)
			return
		}
	}
}

func (c *PeerCache) compactOrderUnderLock() {
	newOrder := make([]peerCacheKey, 0, len(c.entries))
	for _, k := range c.order {
		if _, exists := c.entries[k]; exists {
			newOrder = append(newOrder, k)
		}
	}
	c.order = newOrder
}

// Invalidate removes a cached entry by kind and ref.
func (c *PeerCache) Invalidate(kind, ref string) {
	norm := normalizeRef(ref)
	if norm == "" {
		return
	}
	key := peerCacheKey{Kind: kind, Ref: norm}

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// InvalidateID removes all entries matching a specific ID.
func (c *PeerCache) InvalidateID(id int64) {
	if id == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for k, v := range c.entries {
		if v.ID == id {
			delete(c.entries, k)
		}
	}
}

// Len returns the number of entries currently stored in the cache.
func (c *PeerCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
