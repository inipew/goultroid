package telegram

import (
	"time"

	"github.com/gotd/td/tg"
)

// ingressDedupeClaim owns the exact expiry generation inserted by Begin. A
// failed pre-admission command path can release only its own generation, so a
// stale release can never remove a newer replay claim.
type ingressDedupeClaim struct {
	dedupe    *ingressMessageDedupe
	key       ingressMessageKey
	expiresAt int64
	active    bool
}

func (d *ingressMessageDedupe) Begin(msg *tg.Message, now time.Time) (ingressDedupeClaim, bool) {
	if d == nil {
		return ingressDedupeClaim{}, true
	}
	key, stable := ingressMessageKeyFrom(msg)
	if !stable {
		return ingressDedupeClaim{}, true
	}
	if !d.Accept(msg, now) {
		return ingressDedupeClaim{}, false
	}
	return ingressDedupeClaim{
		dedupe:    d,
		key:       key,
		expiresAt: now.Add(d.ttl).UnixNano(),
		active:    true,
	}, true
}

func (c *ingressDedupeClaim) Release() {
	if c == nil || !c.active || c.dedupe == nil {
		return
	}
	d := c.dedupe
	d.mu.Lock()
	if currentExpiry, exists := d.entries[c.key]; exists && currentExpiry == c.expiresAt {
		delete(d.entries, c.key)
	}
	d.mu.Unlock()
	c.active = false
}
