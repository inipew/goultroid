package ratelimit

// Allow implements the core.CommandRateLimiter contract using the user
// dimension. Keeping the adapter in this package avoids coupling core to the
// concrete rate-limit implementation.
func (l *Limiter) Allow(key string) bool {
	return l.Take(nil, DimensionUser, key, 1).Allowed
}
