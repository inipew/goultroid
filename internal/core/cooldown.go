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

type cooldownRecord struct {
	at       time.Time
	duration time.Duration
}

const (
	maxCooldownRecords            = 4096
	cooldownCapacitySweepInterval = 30 * time.Second
)

// CooldownTracker provides thread-safe rate-limiting per user and command.
type CooldownTracker struct {
	mu                sync.RWMutex
	records           map[userCommandKey]cooldownRecord
	lastCapacitySweep time.Time
}

// NewCooldownTracker creates a new CooldownTracker instance.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		records: make(map[userCommandKey]cooldownRecord),
	}
}

func (c *CooldownTracker) pruneExpiredLocked(now time.Time) {
	for key, record := range c.records {
		if record.duration <= 0 || now.Sub(record.at) >= record.duration {
			delete(c.records, key)
		}
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

	record, exists := c.records[key]
	if exists {
		elapsed := now.Sub(record.at)
		if elapsed < duration {
			return duration - elapsed, false
		}
	} else if len(c.records) >= maxCooldownRecords {
		if c.lastCapacitySweep.IsZero() || now.Sub(c.lastCapacitySweep) >= cooldownCapacitySweepInterval {
			c.pruneExpiredLocked(now)
			c.lastCapacitySweep = now
		}
		if len(c.records) >= maxCooldownRecords {
			// Fail closed rather than evicting an active cooldown and allowing
			// a new identity to bypass admission under cardinality pressure.
			return duration, false
		}
	}

	c.records[key] = cooldownRecord{at: now, duration: duration}
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
	for k, record := range c.records {
		if now.Sub(record.at) > maxAge {
			delete(c.records, k)
			purged++
		}
	}
	return purged
}
