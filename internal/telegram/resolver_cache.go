package telegram

import (
	"container/list"
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
	mu         sync.RWMutex
	cfg        ResolverCacheConfig
	entries    map[peerCacheKey]peerCacheEntry
	order      list.List
	orderIndex map[peerCacheKey]*list.Element
	clock      Clock
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
		cfg:   cfg,
		clock: clock,
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
			c.deleteUnderLock(key)
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
	if c.entries == nil {
		// Keep an unused resolver cache allocation-free. Do not pre-size to
		// MaxEntries on first use either; most processes touch only a small
		// fraction of the configured bound and Go's map grows incrementally.
		c.entries = make(map[peerCacheKey]peerCacheEntry)
	}
	if _, exists := c.entries[key]; exists {
		c.entries[key] = entry
		return
	}
	if len(c.entries) >= c.cfg.MaxEntries {
		c.evictOneUnderLock()
	}
	if c.orderIndex == nil {
		c.orderIndex = make(map[peerCacheKey]*list.Element)
	}
	elem := c.order.PushBack(key)
	c.orderIndex[key] = elem
	c.entries[key] = entry
}

func (c *PeerCache) evictOneUnderLock() {
	front := c.order.Front()
	if front == nil {
		return
	}
	key := front.Value.(peerCacheKey)
	c.order.Remove(front)
	delete(c.orderIndex, key)
	delete(c.entries, key)
}

func (c *PeerCache) deleteUnderLock(key peerCacheKey) {
	delete(c.entries, key)
	if elem, ok := c.orderIndex[key]; ok {
		c.order.Remove(elem)
		delete(c.orderIndex, key)
	}
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
	c.deleteUnderLock(key)
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
			c.deleteUnderLock(k)
		}
	}
}

// Len returns the number of entries currently stored in the cache.
func (c *PeerCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
