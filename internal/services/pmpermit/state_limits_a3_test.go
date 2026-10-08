package pmpermit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestA3ApprovalCacheBoundedUnderChurn(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	for id := int64(2); id < 2+2*maxPMPermitApprovedCache; id++ {
		s.approvedCache.Store(id, approvalCacheEntry{})
	}
	if got := s.approvedCache.Len(); got > maxPMPermitApprovedCache {
		t.Fatalf("positive approval cache grew to %d entries, max %d", got, maxPMPermitApprovedCache)
	}
	if _, ok := s.approvedCache.Load(1 + 2*maxPMPermitApprovedCache); !ok {
		t.Fatal("latest approval missing from bounded cache")
	}
}

func TestA3WarningIDsBoundedAcrossUsers(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	for id := int64(2); id < 2+2*maxPMPermitWarnUsers; id++ {
		if err := s.addWarnID(context.Background(), id, 1); err != nil {
			t.Fatal(err)
		}
	}
	s.warnMu.Lock()
	count := len(s.warnIDs)
	s.warnMu.Unlock()
	if count > maxPMPermitWarnUsers {
		t.Fatalf("warning ID cache grew to %d users, max %d", count, maxPMPermitWarnUsers)
	}
	isWarning, err := s.IsWarnID(context.Background(), 1+2*maxPMPermitWarnUsers, 1)
	if err != nil || !isWarning {
		t.Fatalf("latest tracked warning ID lost: isWarning=%v err=%v", isWarning, err)
	}
}

func TestA3WarnCooldownCapacityFailClosed(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	s.SetWarnCooldown(time.Hour)
	for id := int64(2); id < 2+maxPMPermitCooldownUsers; id++ {
		if s.warnInCooldown(id) {
			t.Fatalf("sender %d incorrectly rejected before saturation", id)
		}
	}
	if !s.warnInCooldown(2 + maxPMPermitCooldownUsers) {
		t.Fatal("new sender should be suppressed at saturated cooldown capacity")
	}
	if !s.warnInCooldown(2) {
		t.Fatal("existing sender lost its cooldown")
	}
	s.warnTimeMu.Lock()
	count := len(s.lastWarnTime)
	s.warnTimeMu.Unlock()
	if count != maxPMPermitCooldownUsers {
		t.Fatalf("cooldown entries=%d, expected %d", count, maxPMPermitCooldownUsers)
	}
}

func TestA3WarnCooldownConcurrentSetting(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				if worker%3 == 0 {
					s.SetWarnCooldown(time.Duration(n%4) * time.Millisecond)
				} else {
					_ = s.warnInCooldown(int64(n + 10*worker))
				}
			}
		}(i)
	}
	wg.Wait()
	s.warnTimeMu.Lock()
	count := len(s.lastWarnTime)
	s.warnTimeMu.Unlock()
	if count > maxPMPermitCooldownUsers {
		t.Fatalf("cooldown state escaped bound: %d", count)
	}
}
