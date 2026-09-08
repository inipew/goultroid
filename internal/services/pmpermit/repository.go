package pmpermit

import (
	"context"
	"time"
)

// PMPermitRecord represents the security permit state of a Telegram user in PM.
type PMPermitRecord struct {
	UserID      int64      `json:"user_id"`
	Status      string     `json:"status"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	WarnCount   int        `json:"warn_count"`
}

// Repository defines the persistence interface required by PMPermit service.
type Repository interface {
	GetPMRecord(ctx context.Context, userID int64) (*PMPermitRecord, error)
	SetPMStatus(ctx context.Context, userID int64, status string, reason string, expiresAt *time.Time) error
	IncrementPMWarn(ctx context.Context, userID int64) (int, error)
	ResetPMWarn(ctx context.Context, userID int64) error
	GetWarnMsgIDs(ctx context.Context, userID int64) ([]int, error)
	AddWarnMsgID(ctx context.Context, userID int64, msgID int) error
	ClearWarnMsgIDs(ctx context.Context, userID int64) error
	ListPMRecords(ctx context.Context, status string, limit, offset int) ([]*PMPermitRecord, error)
	CountPMRecords(ctx context.Context) (pending, approved, blocked int, err error)
}
