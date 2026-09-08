package afk

import (
	"context"
	"time"
)

// AFK represents the AFK state of a user.
type AFK struct {
	UserID int64     `json:"user_id"`
	IsAFK  bool      `json:"is_afk"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// Repository defines access methods for AFK state tracking.
type Repository interface {
	SetAFK(ctx context.Context, userID int64, isAFK bool, reason string) error
	GetAFK(ctx context.Context, userID int64) (*AFK, error)
}
