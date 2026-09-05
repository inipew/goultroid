package core

import (
	"sync"
	"time"
)

// userCommandKey identifies a command execution by a specific user.
type userCommandKey struct {
	userID  int64
	cmdName string
}

// CooldownTracker provides thread-safe rate-limiting per user and command.
type CooldownTracker struct {
	mu      sync.RWMutex
	records map[userCommandKey]time.Time
}

// NewCooldownTracker creates a new CooldownTracker instance.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		records: make(map[userCommandKey]time.Time),
	}
}

// CheckAndRecord determines whether a command execution is allowed under cooldown rules.
// If on cooldown, it returns (remaining, false).
// If execution is permitted, it registers the timestamp and returns (0, true).
func (c *CooldownTracker) CheckAndRecord(userID int64, cmdName string, duration time.Duration) (time.Duration, bool) {
	if c == nil || duration <= 0 {
		return 0, true
	}

	key := userCommandKey{
		userID:  userID,
		cmdName: cmdName,
	}

	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	lastTime, exists := c.records[key]
	if exists {
		elapsed := now.Sub(lastTime)
		if elapsed < duration {
			return duration - elapsed, false
		}
	}

	c.records[key] = now
	return 0, true
}

// Cleanup purges cooldown records that are older than maxAge.
// Returns the number of purged records.
func (c *CooldownTracker) Cleanup(maxAge time.Duration) int {
	if c == nil || maxAge <= 0 {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	purged := 0
	for k, t := range c.records {
		if now.Sub(t) > maxAge {
			delete(c.records, k)
			purged++
		}
	}
	return purged
}

