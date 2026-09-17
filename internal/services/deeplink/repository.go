package deeplink

import (
	"context"
	"time"
)

// Repository defines the persistent storage contract for deep-link tokens.
type Repository interface {
	Create(ctx context.Context, record Record) error
	FindByHash(ctx context.Context, tokenHash []byte) (Record, error)
	ConsumeAtomic(ctx context.Context, tokenHash []byte, consumedBy int64, now time.Time, validate func(rec Record) error) (Record, error)
	RevokeOwner(ctx context.Context, owner string, generation uint64) (int64, error)
	Prune(ctx context.Context, olderThan time.Time, limit int) (int64, error)
}
