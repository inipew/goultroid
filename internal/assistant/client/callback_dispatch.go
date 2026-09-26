package client

import (
	"sync"
	"time"
)

const (
	assistantCallbackDedupTTL = time.Hour
	assistantCallbackDedupMax = 4096
)

type callbackSeenEntry struct {
	id int64
	at time.Time
}

// callbackQueryDeduper keeps Telegram callback delivery deduplication at the
// transport ingress for canonical a2 and explicit unknown-callback handling.
type callbackQueryDeduper struct {
	mu    sync.Mutex
	seen  map[int64]time.Time
	order []callbackSeenEntry
	head  int
}

func newCallbackQueryDeduper() *callbackQueryDeduper {
	return &callbackQueryDeduper{seen: make(map[int64]time.Time)}
}

func (d *callbackQueryDeduper) Admit(id int64, now time.Time) bool {
	if d == nil || id == 0 {
		return true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.pruneExpired(now)
	if at, ok := d.seen[id]; ok && now.Sub(at) < assistantCallbackDedupTTL {
		return false
	}

	for len(d.seen) >= assistantCallbackDedupMax {
		if !d.evictOldest() {
			break
		}
	}

	d.seen[id] = now
	d.order = append(d.order, callbackSeenEntry{id: id, at: now})
	d.compact()
	return true
}

func (d *callbackQueryDeduper) pruneExpired(now time.Time) {
	for d.head < len(d.order) {
		entry := d.order[d.head]
		if now.Sub(entry.at) < assistantCallbackDedupTTL {
			break
		}
		if current, ok := d.seen[entry.id]; ok && current.Equal(entry.at) {
			delete(d.seen, entry.id)
		}
		d.order[d.head] = callbackSeenEntry{}
		d.head++
	}
	d.compact()
}

func (d *callbackQueryDeduper) evictOldest() bool {
	for d.head < len(d.order) {
		entry := d.order[d.head]
		d.order[d.head] = callbackSeenEntry{}
		d.head++
		if current, ok := d.seen[entry.id]; ok && current.Equal(entry.at) {
			delete(d.seen, entry.id)
			d.compact()
			return true
		}
	}
	d.compact()
	return false
}

func (d *callbackQueryDeduper) compact() {
	if d.head == 0 {
		return
	}
	if d.head < 1024 && d.head*2 < len(d.order) {
		return
	}
	copy(d.order, d.order[d.head:])
	d.order = d.order[:len(d.order)-d.head]
	d.head = 0
}
