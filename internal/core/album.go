package core

import (
	"sort"
	"sync"
	"time"
)

// AlbumEntry stores grouped media messages for a specific Telegram album.
type AlbumEntry struct {
	GroupedID int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []*Message
}

// AlbumBuffer aggregates and caches incoming messages that belong to the same Telegram album.
type AlbumBuffer struct {
	mu      sync.RWMutex
	albums  map[int64]*AlbumEntry
	ttl     time.Duration
	maxSize int
}

// NewAlbumBuffer creates a new thread-safe AlbumBuffer.
// If ttl <= 0, a default of 10 minutes is used.
func NewAlbumBuffer(ttl time.Duration) *AlbumBuffer {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &AlbumBuffer{
		albums:  make(map[int64]*AlbumEntry),
		ttl:     ttl,
		maxSize: 500, // keep at most 500 active albums
	}
}

// Add appends a message to its corresponding album entry if msg.GroupedID != 0.
// Messages within an album are maintained sorted by their message ID.
func (b *AlbumBuffer) Add(msg *Message) {
	if b == nil || msg == nil || msg.GroupedID == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entry, exists := b.albums[msg.GroupedID]
	now := time.Now()

	if !exists {
		// Prune expired entries first, then enforce maxSize as a hard bound.
		if len(b.albums) >= b.maxSize {
			b.pruneLocked(now)
			for len(b.albums) >= b.maxSize {
				if !b.evictOldestLocked() {
					break
				}
			}
		}

		b.albums[msg.GroupedID] = &AlbumEntry{
			GroupedID: msg.GroupedID,
			CreatedAt: now,
			UpdatedAt: now,
			Messages:  []*Message{msg},
		}
		return
	}

	// Avoid duplicate messages
	for _, m := range entry.Messages {
		if m.ID == msg.ID {
			return
		}
	}

	entry.Messages = append(entry.Messages, msg)
	sort.Slice(entry.Messages, func(i, j int) bool {
		return entry.Messages[i].ID < entry.Messages[j].ID
	})
	entry.UpdatedAt = now
}

// Get returns a shallow copy of messages belonging to the given album.
// Returns nil if no album exists for this groupedID or if expired.
func (b *AlbumBuffer) Get(groupedID int64) []*Message {
	if b == nil || groupedID == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entry, exists := b.albums[groupedID]
	if !exists {
		return nil
	}
	if time.Since(entry.UpdatedAt) > b.ttl {
		delete(b.albums, groupedID)
		return nil
	}

	result := make([]*Message, len(entry.Messages))
	copy(result, entry.Messages)
	return result
}

// Len returns the current number of tracked albums.
func (b *AlbumBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.albums)
}

// Prune removes albums older than the configured TTL.
func (b *AlbumBuffer) Prune() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now())
}

func (b *AlbumBuffer) pruneLocked(now time.Time) {
	for id, entry := range b.albums {
		if now.Sub(entry.UpdatedAt) > b.ttl {
			delete(b.albums, id)
		}
	}
}

func (b *AlbumBuffer) evictOldestLocked() bool {
	if len(b.albums) == 0 {
		return false
	}
	var (
		oldestID int64
		oldestAt time.Time
		found    bool
	)
	for id, entry := range b.albums {
		if entry == nil {
			oldestID = id
			found = true
			break
		}
		if !found || entry.UpdatedAt.Before(oldestAt) {
			oldestID = id
			oldestAt = entry.UpdatedAt
			found = true
		}
	}
	if !found {
		return false
	}
	delete(b.albums, oldestID)
	return true
}
