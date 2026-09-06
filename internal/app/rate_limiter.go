package app

import "github.com/inipew/goultroid/internal/services/ratelimit"

type commandRateLimiterAdapter struct {
	limiter *ratelimit.Limiter
}

func (a commandRateLimiterAdapter) Allow(key string) bool {
	return a.limiter.Allow(ratelimit.DimensionUser, key)
}
