package callback

import "time"

// StateScope describes authorization and lifecycle metadata for a callback state entry.
type StateScope struct {
	UserID    int64
	ChatID    int64
	MessageID int
	Namespace string
	SingleUse bool
	ExpiresAt time.Time
}

// StateEntry is the stored value plus scope returned to callers.
type StateEntry struct {
	Data  any
	Scope StateScope
}

// stateItem internal storage.
type stateItem struct {
	data      any
	scope     StateScope
	expiresAt time.Time
	consumed  bool
}
