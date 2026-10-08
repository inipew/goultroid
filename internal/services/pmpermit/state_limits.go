package pmpermit

import (
	"sync"
	"time"
)

const (
	maxPMPermitApprovedCache = 2048
	maxPMPermitWarnUsers     = 1024
	maxPMPermitCooldownUsers = 2048
)

// A positive-only cache: eviction increases DB reads but never grants approval.
// Dropping a quarter at capacity amortizes cleanup under high-cardinality churn.
type boundedApprovalCache struct {
	mu      sync.Mutex
	entries map[int64]approvalCacheEntry
}

func (c *boundedApprovalCache) Load(userID int64) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[userID]
	return entry, ok
}

func (c *boundedApprovalCache) Store(userID int64, entry approvalCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]approvalCacheEntry)
	}
	if _, exists := c.entries[userID]; !exists && len(c.entries) >= maxPMPermitApprovedCache {
		// Prefer expired approvals; the remainder are safe to re-read from DB.
		now := time.Now().UTC()
		for id, cached := range c.entries {
			if !cached.expiresAt.IsZero() && !now.Before(cached.expiresAt) {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= maxPMPermitApprovedCache {
			remaining := maxPMPermitApprovedCache * 3 / 4
			for id := range c.entries {
				delete(c.entries, id)
				if len(c.entries) <= remaining {
					break
				}
			}
		}
	}
	c.entries[userID] = entry
}

func (c *boundedApprovalCache) Delete(userID int64) {
	c.mu.Lock()
	delete(c.entries, userID)
	c.mu.Unlock()
}

func (c *boundedApprovalCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (s *Service) warnInCooldown(userID int64) bool {
	cooldown := s.WarnCooldown()
	if cooldown <= 0 {
		return false
	}
	now := time.Now()
	s.warnTimeMu.Lock()
	defer s.warnTimeMu.Unlock()
	if last, tracked := s.lastWarnTime[userID]; tracked {
		if now.Sub(last) < cooldown {
			return true
		}
		s.lastWarnTime[userID] = now
		return false
	}
	if len(s.lastWarnTime) >= maxPMPermitCooldownUsers {
		for id, last := range s.lastWarnTime {
			if now.Sub(last) >= cooldown {
				delete(s.lastWarnTime, id)
			}
		}
		if len(s.lastWarnTime) >= maxPMPermitCooldownUsers {
			// All slots are active: fail closed, without a warning/RPC burst.
			return true
		}
	}
	s.lastWarnTime[userID] = now
	return false
}
