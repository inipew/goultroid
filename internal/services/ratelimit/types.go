package ratelimit

import "time"

// Dimension represents the scope for rate-limiting.
type Dimension string

const (
	DimensionUser      Dimension = "user"
	DimensionChat      Dimension = "chat"
	DimensionCommand   Dimension = "command"
	DimensionOperation Dimension = "operation"
	DimensionMedia     Dimension = "media"
	DimensionScheduler Dimension = "scheduler"
)

// Policy defines token-bucket rate-limiting parameters.
type Policy struct {
	// Limit is the number of tokens added per Window.
	Limit int `json:"limit"`
	// Window is the duration over which Limit tokens are generated.
	Window time.Duration `json:"window"`
	// Burst is the maximum token bucket capacity.
	Burst int `json:"burst"`
}

// Result describes the outcome of a rate-limit check.
type Result struct {
	// Allowed indicates whether the operation was permitted.
	Allowed bool `json:"allowed"`
	// Remaining is the number of tokens left in the bucket.
	Remaining int `json:"remaining"`
	// RetryAfter indicates the duration to wait before trying again if denied.
	RetryAfter time.Duration `json:"retry_after"`
	// ResetAfter indicates when the bucket will be completely refilled.
	ResetAfter time.Duration `json:"reset_after"`
}
