package pmpermit

import (
	"testing"
	"time"
)

func TestA3CStatusLocksDoNotCollideForDifferentUsers(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	releaseFirst := s.lockUserStatus(42)
	defer releaseFirst()
	done := make(chan struct{})
	go func() {
		unlock := s.lockUserStatus(42 + 128)
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("different users with the same old stripe must not serialize")
	}
}

func TestA3CSameUserStatusTransitionsSerialize(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	releaseFirst := s.lockUserStatus(77)
	done := make(chan struct{})
	go func() {
		unlock := s.lockUserStatus(77)
		unlock()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("same-user status lock was bypassed")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same-user waiter never resumed")
	}
}

func TestA3CStatusLocksBoundedUnderHighCardinality(t *testing.T) {
	s := NewService(nil, nil, 1, nil, nil)
	unlocks := make([]func(), 0, maxPMPermitActiveStatusUsers)
	for i := 0; i < maxPMPermitActiveStatusUsers; i++ {
		unlocks = append(unlocks, s.lockUserStatus(int64(i+1)))
	}
	s.statusMu.Lock()
	count := len(s.statusUsers)
	s.statusMu.Unlock()
	if count != maxPMPermitActiveStatusUsers {
		t.Fatalf("active keyed locks=%d, want %d", count, maxPMPermitActiveStatusUsers)
	}
	overflowUnlock := s.lockUserStatus(int64(maxPMPermitActiveStatusUsers + 1))
	s.statusMu.Lock()
	count = len(s.statusUsers)
	overflow := s.statusOverflow
	s.statusMu.Unlock()
	if count > maxPMPermitActiveStatusUsers || overflow != 1 {
		t.Fatalf("status locks not bounded: keyed=%d overflow=%d", count, overflow)
	}
	overflowUnlock()
	for _, unlock := range unlocks {
		unlock()
	}
	s.statusMu.Lock()
	count = len(s.statusUsers)
	overflow = s.statusOverflow
	s.statusMu.Unlock()
	if count != 0 || overflow != 0 {
		t.Fatalf("idle lock state retained: keyed=%d overflow=%d", count, overflow)
	}
}
