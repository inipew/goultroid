package deeplink

import (
	"time"

	"github.com/inipew/goultroid/internal/presentation"
)

// Purpose describes the functional goal of a generated deep link.
type Purpose string

const (
	PurposeOpenScreen Purpose = "open_screen"
	PurposeResumeFlow Purpose = "resume_flow"
	PurposeStoredFile Purpose = "stored_file"
)

const (
	DefaultTokenTTL    = 10 * time.Minute
	MaxTokenTTL        = 24 * time.Hour
	MaxPayloadSize     = 8 * 1024 // 8 KiB bound
	DefaultPrunePeriod = 15 * time.Minute
)

// IssueRequest carries the parameters to mint a new deep link.
type IssueRequest struct {
	Purpose        Purpose
	Owner          string
	Generation     uint64
	UserID         int64
	SourceChatID   int64
	Screen         presentation.ScreenKey
	PayloadType    string
	PayloadVersion uint16
	Payload        []byte
	SingleUse      bool
	TTL            time.Duration
}

// Claim represents verified token information returned upon inspection or consumption.
type Claim struct {
	ID             string
	Purpose        Purpose
	Owner          string
	Generation     uint64
	UserID         int64
	SourceChatID   int64
	Screen         presentation.ScreenKey
	PayloadType    string
	PayloadVersion uint16
	Payload        []byte
	IssuedAt       time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	ConsumedBy     int64
}

// Record models the persistent schema stored in SQLite.
type Record struct {
	ID             string
	TokenHash      []byte
	Purpose        Purpose
	Owner          string
	Generation     uint64
	UserID         int64
	SourceChatID   int64
	Screen         presentation.ScreenKey
	PayloadType    string
	PayloadVersion uint16
	Payload        []byte
	SingleUse      bool
	IssuedAt       time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	ConsumedBy     int64
}

// ToClaim maps a database Record into a domain Claim.
func (r Record) ToClaim() Claim {
	return Claim{
		ID:             r.ID,
		Purpose:        r.Purpose,
		Owner:          r.Owner,
		Generation:     r.Generation,
		UserID:         r.UserID,
		SourceChatID:   r.SourceChatID,
		Screen:         r.Screen,
		PayloadType:    r.PayloadType,
		PayloadVersion: r.PayloadVersion,
		Payload:        r.Payload,
		IssuedAt:       r.IssuedAt,
		ExpiresAt:      r.ExpiresAt,
		ConsumedAt:     r.ConsumedAt,
		ConsumedBy:     r.ConsumedBy,
	}
}
