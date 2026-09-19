package telegram

import (
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

const (
	defaultIngressDedupeTTL      = 5 * time.Minute
	defaultIngressDedupeCapacity = 8192
)

type ingressPeerKind uint8

const (
	ingressPeerUser ingressPeerKind = iota + 1
	ingressPeerChat
	ingressPeerChannel
)

type ingressMessageKey struct {
	peerID    int64
	messageID int
	peerKind  ingressPeerKind
}

type ingressDedupeEntry struct {
	key       ingressMessageKey
	expiresAt int64
}

// ingressMessageDedupe is a bounded transport deduplicator.
//
// It intentionally has no background goroutine or durable storage. Telegram
// update replay is a transport concern; durable idempotency is reserved for
// recognized commands/callbacks that may create persistent side effects.
//
// The fixed ring bounds both resident keys and cleanup work. Re-inserting an
// expired key can leave an older ring slot behind, so eviction deletes a map
// entry only when the slot still owns the current expiry generation.
type ingressMessageDedupe struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[ingressMessageKey]int64
	ring       []ingressDedupeEntry
	cursor     int
}

func newIngressMessageDedupe(ttl time.Duration, maxEntries int) *ingressMessageDedupe {
	if ttl <= 0 {
		ttl = defaultIngressDedupeTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultIngressDedupeCapacity
	}
	return &ingressMessageDedupe{
		ttl:        ttl,
		maxEntries: maxEntries,
		// Grow on demand so an idle userbot does not pay the full cache capacity
		// in RSS. Once traffic warms the ring to maxEntries, replacement is O(1).
		entries: make(map[ingressMessageKey]int64),
		ring:    make([]ingressDedupeEntry, 0),
	}
}

// Accept reports whether msg has not been seen in the active ingress window.
//
// Messages without a stable Telegram peer/message identity are deliberately not
// cached. That avoids false-positive suppression for synthetic/test updates and
// any malformed update where deduplication cannot be made peer-safe.
func (d *ingressMessageDedupe) Accept(msg *tg.Message, now time.Time) bool {
	if d == nil {
		return true
	}
	key, ok := ingressMessageKeyFrom(msg)
	if !ok {
		return true
	}

	nowNanos := now.UnixNano()
	expiresAt := now.Add(d.ttl).UnixNano()

	d.mu.Lock()
	defer d.mu.Unlock()

	if currentExpiry, exists := d.entries[key]; exists && currentExpiry > nowNanos {
		return false
	}

	entry := ingressDedupeEntry{key: key, expiresAt: expiresAt}
	if len(d.ring) < d.maxEntries {
		d.ring = append(d.ring, entry)
		d.entries[key] = expiresAt
		return true
	}

	old := d.ring[d.cursor]
	if currentExpiry, exists := d.entries[old.key]; exists && currentExpiry == old.expiresAt {
		delete(d.entries, old.key)
	}
	d.ring[d.cursor] = entry
	d.cursor++
	if d.cursor == d.maxEntries {
		d.cursor = 0
	}
	d.entries[key] = expiresAt
	return true
}

func ingressMessageKeyFrom(msg *tg.Message) (ingressMessageKey, bool) {
	if msg == nil || msg.ID <= 0 || msg.PeerID == nil {
		return ingressMessageKey{}, false
	}

	key := ingressMessageKey{messageID: msg.ID}
	switch peer := msg.PeerID.(type) {
	case *tg.PeerUser:
		if peer.UserID == 0 {
			return ingressMessageKey{}, false
		}
		key.peerKind = ingressPeerUser
		key.peerID = peer.UserID
	case *tg.PeerChat:
		if peer.ChatID == 0 {
			return ingressMessageKey{}, false
		}
		key.peerKind = ingressPeerChat
		key.peerID = peer.ChatID
	case *tg.PeerChannel:
		if peer.ChannelID == 0 {
			return ingressMessageKey{}, false
		}
		key.peerKind = ingressPeerChannel
		key.peerID = peer.ChannelID
	default:
		return ingressMessageKey{}, false
	}
	return key, true
}
