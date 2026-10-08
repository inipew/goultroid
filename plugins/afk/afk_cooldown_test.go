package afk

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAFKCooldownCapacityFailClosedPreservesActivePairs(t *testing.T) {
	p := New(nil, 100, nil)
	for i := 0; i < afkCooldownCapacity; i++ {
		if !p.checkAndSetCooldown(77, int64(i+1)) {
			t.Fatalf("unexpected rejection of slot %d", i)
		}
	}
	for i := 0; i < 5*afkCooldownCapacity; i++ {
		if p.checkAndSetCooldown(77, int64(afkCooldownCapacity+i+1)) {
			t.Fatalf("overflow sender %d received an auto-reply admission", i)
		}
	}
	for i := 0; i < afkCooldownCapacity; i++ {
		if !p.isCooldownActive(77, int64(i+1)) {
			t.Fatalf("active cooldown for sender %d was evicted", i+1)
		}
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if len(p.cooldownMap) != afkCooldownCapacity || p.cooldownOrder.Len() != afkCooldownCapacity {
		t.Fatalf("cooldown state grew or diverged: map=%d order=%d", len(p.cooldownMap), p.cooldownOrder.Len())
	}
}

func TestAFKCooldownExpiredSlotsReclaimedInOrder(t *testing.T) {
	p := New(nil, 100, nil)
	p.SetCooldown(time.Minute)
	for i := 1; i <= afkCooldownCapacity; i++ {
		if !p.checkAndSetCooldown(77, int64(i)) {
			t.Fatalf("unable to seed cooldown %d", i)
		}
	}
	const expired = 25
	p.cooldownMu.Lock()
	old := time.Now().Add(-2 * time.Minute)
	for i := 1; i <= expired; i++ {
		element := p.cooldownMap[[2]int64{77, int64(i)}]
		element.Value = afkCooldownEntry{key: [2]int64{77, int64(i)}, sentAt: old}
	}
	p.cooldownMu.Unlock()

	for i := 0; i < expired; i++ {
		if !p.checkAndSetCooldown(88, int64(i+1)) {
			t.Fatalf("expired slot %d was not reused", i)
		}
	}
	if p.checkAndSetCooldown(88, expired+1) {
		t.Fatal("admitted above capacity while all remaining pairs were active")
	}
	if p.isCooldownActive(77, 1) || !p.isCooldownActive(77, expired+1) || !p.isCooldownActive(88, 1) {
		t.Fatal("expired cleanup evicted a live pair or retained an expired pair")
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if len(p.cooldownMap) != afkCooldownCapacity || p.cooldownOrder.Len() != afkCooldownCapacity {
		t.Fatalf("unexpected size after recycling: map=%d order=%d", len(p.cooldownMap), p.cooldownOrder.Len())
	}
}

func TestAFKCooldownRollbackAndZeroSettingResetBothIndexes(t *testing.T) {
	p := New(nil, 100, nil)
	if !p.checkAndSetCooldown(20, 30) || !p.checkAndSetCooldown(20, 31) {
		t.Fatal("initial admissions rejected")
	}
	p.rollbackCooldown(20, 30)
	if p.isCooldownActive(20, 30) || !p.isCooldownActive(20, 31) {
		t.Fatal("rollback changed wrong cooldown pair")
	}
	if !p.checkAndSetCooldown(20, 30) {
		t.Fatal("rolled back pair cannot retry")
	}
	p.SetCooldown(0)
	p.cooldownMu.Lock()
	mapSize, orderSize := len(p.cooldownMap), p.cooldownOrder.Len()
	p.cooldownMu.Unlock()
	if mapSize != 0 || orderSize != 0 {
		t.Fatalf("zero cooldown retains state: map=%d order=%d", mapSize, orderSize)
	}
	p.SetCooldown(time.Minute)
	if !p.checkAndSetCooldown(20, 30) {
		t.Fatal("reset pair should be admitted")
	}
}

func TestAFKCooldownConcurrentHighCardinalityBounded(t *testing.T) {
	p := New(nil, 100, nil)
	const workers = 16
	const perWorker = 180
	var accepted atomic.Int64
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < perWorker; i++ {
				if p.checkAndSetCooldown(100, int64(worker*perWorker+i+1)) {
					accepted.Add(1)
				}
			}
		}(worker)
	}
	group.Wait()
	if got := accepted.Load(); got != afkCooldownCapacity {
		t.Fatalf("accepted=%d, want %d", got, afkCooldownCapacity)
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if len(p.cooldownMap) != afkCooldownCapacity || p.cooldownOrder.Len() != afkCooldownCapacity {
		t.Fatalf("concurrent state diverged: map=%d order=%d", len(p.cooldownMap), p.cooldownOrder.Len())
	}
}

func TestAFKCooldownRepeatedExpiryDoesNotGrowOrder(t *testing.T) {
	p := New(nil, 100, nil)
	p.SetCooldown(time.Nanosecond)
	for i := 0; i < 10000; i++ {
		if !p.checkAndSetCooldown(77, 88) {
			t.Fatalf("expired pair rejected on iteration %d", i)
		}
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if len(p.cooldownMap) != 1 || p.cooldownOrder.Len() != 1 {
		t.Fatalf("repeated expiry grew state: map=%d order=%d", len(p.cooldownMap), p.cooldownOrder.Len())
	}
}
