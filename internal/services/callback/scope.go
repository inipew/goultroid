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
// Data is intentionally any for the framework layer (StateStore is a generic short-lived session store).
// Domain code should use typed wrappers (e.g., SettingActionState) and avoid raw any assertions outside the store.
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
