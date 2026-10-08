package afk

import (
	"container/list"
	"time"
)

// afkCooldownCapacity limits active sender/chat pairs. At capacity, new
// senders are not auto-replied to; evicting active pairs would let them
// bypass their cooldown and amplify high-cardinality message floods.
const afkCooldownCapacity = 1000

type afkCooldownEntry struct {
	key    [2]int64
	sentAt time.Time
}

// removeCooldownLocked keeps both the map and expiry-order index in sync.
// The caller must hold cooldownMu.
func (p *Plugin) removeCooldownLocked(element *list.Element) {
	entry := element.Value.(afkCooldownEntry)
	delete(p.cooldownMap, entry.key)
	p.cooldownOrder.Remove(element)
}

// expireCooldownLocked removes expired pairs from oldest to newest. Each
// admitted pair is inserted once and removed once, so expiry work is amortized
// O(1) per admission rather than a full map scan for every new sender.
func (p *Plugin) expireCooldownLocked(now time.Time, duration time.Duration) {
	for element := p.cooldownOrder.Front(); element != nil; element = p.cooldownOrder.Front() {
		entry := element.Value.(afkCooldownEntry)
		if now.Sub(entry.sentAt) < duration {
			break
		}
		p.removeCooldownLocked(element)
	}
}

func (p *Plugin) isCooldownActive(chatID, senderID int64) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()

	element, ok := p.cooldownMap[[2]int64{chatID, senderID}]
	if !ok || p.cooldownDur <= 0 {
		return false
	}
	entry := element.Value.(afkCooldownEntry)
	if time.Since(entry.sentAt) < p.cooldownDur {
		return true
	}
	p.removeCooldownLocked(element)
	return false
}

func (p *Plugin) checkAndSetCooldown(chatID, senderID int64) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()

	duration := p.cooldownDur
	if duration <= 0 {
		return true
	}
	now := time.Now()
	p.expireCooldownLocked(now, duration)

	key := [2]int64{chatID, senderID}
	if _, exists := p.cooldownMap[key]; exists {
		return false
	}
	if len(p.cooldownMap) >= afkCooldownCapacity {
		return false
	}
	p.cooldownMap[key] = p.cooldownOrder.PushBack(afkCooldownEntry{key: key, sentAt: now})
	return true
}

func (p *Plugin) rollbackCooldown(chatID, senderID int64) {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if element := p.cooldownMap[[2]int64{chatID, senderID}]; element != nil {
		p.removeCooldownLocked(element)
	}
}

// Cleanup discards entries older than maxAge. The default manual cleanup
// interval is kept for compatibility; ordinary admission uses cooldownDur.
func (p *Plugin) Cleanup(maxAge time.Duration) int {
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	now := time.Now()
	removed := 0
	for element := p.cooldownOrder.Front(); element != nil; element = p.cooldownOrder.Front() {
		if now.Sub(element.Value.(afkCooldownEntry).sentAt) <= maxAge {
			break
		}
		p.removeCooldownLocked(element)
		removed++
	}
	return removed
}
