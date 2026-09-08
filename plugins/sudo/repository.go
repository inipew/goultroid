package sudo

import (
	"context"
	"time"
)

// User represents a registered sudo user.
type User struct {
	UserID  int64     `json:"user_id"`
	AddedAt time.Time `json:"added_at"`
	AddedBy int64     `json:"added_by"`
}

// SudoUser is an alias for User for backward compatibility.
type SudoUser = User

// Repository defines access methods for sudo user management.
type Repository interface {
	GetSudoUsers(ctx context.Context) ([]User, error)
	AddSudoUser(ctx context.Context, userID, addedBy int64) error
	RemoveSudoUser(ctx context.Context, userID int64) error
	IsSudoUser(ctx context.Context, userID int64) (bool, error)
}
