package pmpermit

// Status stripes serialize each user's durable state, cache publication,
// and side effects without creating a per-user mutex registry. Different
// users in different stripes remain independent; memory stays constant.
func (s *Service) lockUserStatus(userID int64) func() {
	stripe := uint64(userID) % uint64(len(s.statusLocks))
	mu := &s.statusLocks[stripe]
	mu.Lock()
	return mu.Unlock
}
