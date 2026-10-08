package pmpermit

import "sync"

const maxPMPermitActiveStatusUsers = 4096

type pmUserStatusLock struct {
	mu    sync.Mutex
	users int
}

// lockUserStatus serializes one user's read/modify/publish transaction.
// Active per-user locks are reference-counted and removed when idle, so
// unrelated users do not collide during a slow Telegram RPC. At capacity,
// a bounded striped fallback preserves admission and synchronization without
// allowing unbounded state. Unmatched users stay in fallback while any
// fallback waiter is alive, preventing the same user from entering both paths.
func (s *Service) lockUserStatus(userID int64) func() {
	s.statusMu.Lock()
	if lock := s.statusUsers[userID]; lock != nil {
		lock.users++
		s.statusMu.Unlock()
		lock.mu.Lock()
		return func() { lock.mu.Unlock(); s.releaseStatusUser(userID, lock) }
	}
	if s.statusOverflow > 0 || len(s.statusUsers) >= maxPMPermitActiveStatusUsers {
		s.statusOverflow++
		s.statusMu.Unlock()
		stripe := &s.statusFallback[uint64(userID)%uint64(len(s.statusFallback))]
		stripe.Lock()
		return func() {
			stripe.Unlock()
			s.statusMu.Lock()
			s.statusOverflow--
			s.statusMu.Unlock()
		}
	}
	if s.statusUsers == nil {
		s.statusUsers = make(map[int64]*pmUserStatusLock)
	}
	lock := &pmUserStatusLock{users: 1}
	s.statusUsers[userID] = lock
	s.statusMu.Unlock()
	lock.mu.Lock()
	return func() { lock.mu.Unlock(); s.releaseStatusUser(userID, lock) }
}

func (s *Service) releaseStatusUser(userID int64, lock *pmUserStatusLock) {
	s.statusMu.Lock()
	lock.users--
	if lock.users == 0 {
		delete(s.statusUsers, userID)
	}
	s.statusMu.Unlock()
}
