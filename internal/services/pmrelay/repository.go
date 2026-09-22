package pmrelay

import (
	"context"
	"time"
)

type Repository interface {
	EnsureMapping(context.Context, Mapping) (Mapping, error)
	GetMapping(context.Context, int64, int) (Mapping, error)
	GetMappingByVisitorMessage(context.Context, int64, int) (Mapping, error)
	PruneExpiredMappings(context.Context, time.Time, int) (int, error)
	CountMappings(context.Context) (int, error)

	EnsureDelivery(context.Context, DeliveryIntent) (DeliveryIntent, error)
	GetDelivery(context.Context, DeliveryKey) (DeliveryIntent, error)
	ClaimDelivery(context.Context, DeliveryKey, time.Time, string, time.Time) (DeliveryIntent, error)
	CommitDelivery(context.Context, DeliveryKey, string, int, time.Time) (DeliveryIntent, error)
	ReleaseDelivery(context.Context, DeliveryKey, string, time.Time, string) error
	PruneExpiredDeliveries(context.Context, time.Time, int) (int, error)
	CountDeliveries(context.Context) (int, error)

	TouchAudience(context.Context, AudienceTouch) (AudienceMember, error)
	GetAudience(context.Context, int64) (AudienceMember, error)
	ListAudience(context.Context, int64, int) ([]AudienceMember, error)
	PruneAudienceBefore(context.Context, time.Time, int) (int, error)
	CountAudience(context.Context) (int, error)

	SetVisitorBlock(context.Context, VisitorBlock) (VisitorBlock, error)
	GetVisitorBlock(context.Context, int64) (VisitorBlock, error)
	DeleteVisitorBlock(context.Context, int64) (bool, error)
	ListVisitorBlocks(context.Context, int64, int) ([]VisitorBlock, error)
	CountVisitorBlocks(context.Context) (int, error)
}
