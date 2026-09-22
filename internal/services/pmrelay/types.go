package pmrelay

import (
	"errors"
	"strings"
	"time"
)

const (
	DefaultMappingRetention  = 30 * 24 * time.Hour
	MaxMappingRetention      = 90 * 24 * time.Hour
	DefaultDeliveryRetention = 30 * 24 * time.Hour
	MaxDeliveryRetention     = 90 * 24 * time.Hour
	DefaultAudienceRetention = 180 * 24 * time.Hour
	MaxAudienceRetention     = 365 * 24 * time.Hour
	DeliveryClaimTTL         = 5 * time.Minute
	MaxPruneBatch            = 256
	MaxClaimIDBytes          = 64
	MaxDeliveryErrorBytes    = 1024

	DefaultMaxMappings   = 16_384
	DefaultMaxDeliveries = 32_768
	DefaultMaxAudience   = 10_000
	DefaultMaxBlocked    = 10_000

	HardMaxMappings   = 65_536
	HardMaxDeliveries = 131_072
	HardMaxAudience   = 50_000
	HardMaxBlocked    = 50_000

	MaxBlockReasonBytes = 512
)

var (
	ErrUnavailable       = errors.New("pmrelay: repository unavailable")
	ErrInvalidMapping    = errors.New("pmrelay: invalid mapping")
	ErrMappingNotFound   = errors.New("pmrelay: mapping not found")
	ErrMappingConflict   = errors.New("pmrelay: mapping conflict")
	ErrMappingCapacity   = errors.New("pmrelay: mapping capacity exhausted")
	ErrInvalidDelivery   = errors.New("pmrelay: invalid delivery")
	ErrDeliveryNotFound  = errors.New("pmrelay: delivery not found")
	ErrDeliveryConflict  = errors.New("pmrelay: delivery conflict")
	ErrDeliveryClaimed   = errors.New("pmrelay: delivery already claimed")
	ErrDeliveryCompleted = errors.New("pmrelay: delivery already completed")
	ErrDeliveryExpired   = errors.New("pmrelay: delivery expired")
	ErrDeliveryCapacity  = errors.New("pmrelay: delivery capacity exhausted")
	ErrInvalidAudience   = errors.New("pmrelay: invalid audience member")
	ErrAudienceNotFound  = errors.New("pmrelay: audience member not found")
	ErrAudienceCapacity  = errors.New("pmrelay: audience capacity exhausted")
	ErrVisitorBlocked    = errors.New("pmrelay: visitor blocked")
	ErrInvalidBlock      = errors.New("pmrelay: invalid visitor block")
	ErrBlockNotFound     = errors.New("pmrelay: visitor block not found")
	ErrBlockCapacity     = errors.New("pmrelay: visitor block capacity exhausted")
)

type Limits struct {
	Mappings   int
	Deliveries int
	Audience   int
	Blocked    int
}

func DefaultLimits() Limits {
	return Limits{
		Mappings:   DefaultMaxMappings,
		Deliveries: DefaultMaxDeliveries,
		Audience:   DefaultMaxAudience,
		Blocked:    DefaultMaxBlocked,
	}
}

func (l Limits) normalized() Limits {
	if l.Mappings <= 0 || l.Mappings > HardMaxMappings {
		l.Mappings = DefaultMaxMappings
	}
	if l.Deliveries <= 0 || l.Deliveries > HardMaxDeliveries {
		l.Deliveries = DefaultMaxDeliveries
	}
	if l.Audience <= 0 || l.Audience > HardMaxAudience {
		l.Audience = DefaultMaxAudience
	}
	if l.Blocked <= 0 || l.Blocked > HardMaxBlocked {
		l.Blocked = DefaultMaxBlocked
	}
	return l
}

type Mapping struct {
	OwnerChatID      int64
	OwnerMessageID   int
	VisitorUserID    int64
	VisitorMessageID int
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

func (m Mapping) Normalize() (Mapping, error) {
	m.CreatedAt = m.CreatedAt.UTC()
	m.ExpiresAt = m.ExpiresAt.UTC()
	if m.OwnerChatID <= 0 || m.OwnerMessageID <= 0 || m.VisitorUserID <= 0 || m.VisitorMessageID <= 0 || m.CreatedAt.IsZero() || m.ExpiresAt.IsZero() {
		return Mapping{}, ErrInvalidMapping
	}
	if !m.ExpiresAt.After(m.CreatedAt) || m.ExpiresAt.Sub(m.CreatedAt) > MaxMappingRetention {
		return Mapping{}, ErrInvalidMapping
	}
	return m, nil
}

func (m Mapping) Expired(now time.Time) bool {
	return !now.UTC().Before(m.ExpiresAt.UTC())
}

type DeliveryDirection string

const (
	DeliveryVisitorToOwner DeliveryDirection = "visitor_to_owner"
	DeliveryOwnerToVisitor DeliveryDirection = "owner_to_visitor"
)

func (d DeliveryDirection) valid() bool {
	return d == DeliveryVisitorToOwner || d == DeliveryOwnerToVisitor
}

type DeliveryKey struct {
	Direction       DeliveryDirection
	SourceChatID    int64
	SourceMessageID int
}

func (k DeliveryKey) Normalize() (DeliveryKey, error) {
	k.Direction = DeliveryDirection(strings.ToLower(strings.TrimSpace(string(k.Direction))))
	if !k.Direction.valid() || k.SourceChatID <= 0 || k.SourceMessageID <= 0 {
		return DeliveryKey{}, ErrInvalidDelivery
	}
	return k, nil
}

type DeliveryIntent struct {
	DeliveryKey
	TargetChatID    int64
	RandomID        int64
	TargetMessageID int
	ClaimID         string
	ClaimExpiresAt  *time.Time
	Attempts        int
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeliveredAt     *time.Time
	ExpiresAt       time.Time
}

func (d DeliveryIntent) Normalize() (DeliveryIntent, error) {
	key, err := d.DeliveryKey.Normalize()
	if err != nil {
		return DeliveryIntent{}, err
	}
	d.DeliveryKey = key
	d.ClaimID = strings.TrimSpace(d.ClaimID)
	d.CreatedAt = d.CreatedAt.UTC()
	d.UpdatedAt = d.UpdatedAt.UTC()
	d.ExpiresAt = d.ExpiresAt.UTC()
	if d.TargetChatID <= 0 || d.RandomID == 0 || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() || d.ExpiresAt.IsZero() || d.Attempts < 0 {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	if d.UpdatedAt.Before(d.CreatedAt) || !d.ExpiresAt.After(d.CreatedAt) || d.ExpiresAt.Sub(d.CreatedAt) > MaxDeliveryRetention {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	if len(d.ClaimID) > MaxClaimIDBytes || len(d.LastError) > MaxDeliveryErrorBytes {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	if d.ClaimExpiresAt != nil {
		claimExpiry := d.ClaimExpiresAt.UTC()
		d.ClaimExpiresAt = &claimExpiry
		if d.ClaimID == "" || !claimExpiry.After(d.UpdatedAt) || claimExpiry.Sub(d.UpdatedAt) > DeliveryClaimTTL {
			return DeliveryIntent{}, ErrInvalidDelivery
		}
	} else if d.ClaimID != "" {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	if d.DeliveredAt != nil {
		deliveredAt := d.DeliveredAt.UTC()
		d.DeliveredAt = &deliveredAt
		if d.TargetMessageID <= 0 || deliveredAt.Before(d.CreatedAt) || d.ClaimID != "" || d.ClaimExpiresAt != nil {
			return DeliveryIntent{}, ErrInvalidDelivery
		}
	} else if d.TargetMessageID != 0 {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	return d, nil
}

func (d DeliveryIntent) Completed() bool { return d.DeliveredAt != nil }

func (d DeliveryIntent) Expired(now time.Time) bool {
	return !now.UTC().Before(d.ExpiresAt.UTC())
}

type AudienceSource uint8

const (
	AudienceSourceStart AudienceSource = 1 << iota
	AudienceSourceRelay
	AudienceSourceInline
	AudienceSourceDeepLink

	audienceSourceAll = AudienceSourceStart | AudienceSourceRelay | AudienceSourceInline | AudienceSourceDeepLink
)

func (s AudienceSource) valid() bool {
	return s != 0 && s&^audienceSourceAll == 0
}

type AudienceTouch struct {
	UserID int64
	Source AudienceSource
	SeenAt time.Time
}

func (t AudienceTouch) Normalize() (AudienceTouch, error) {
	t.SeenAt = t.SeenAt.UTC()
	if t.UserID <= 0 || !t.Source.valid() || t.SeenAt.IsZero() {
		return AudienceTouch{}, ErrInvalidAudience
	}
	return t, nil
}

type AudienceMember struct {
	UserID      int64
	Sources     AudienceSource
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

func (m AudienceMember) Normalize() (AudienceMember, error) {
	m.FirstSeenAt = m.FirstSeenAt.UTC()
	m.LastSeenAt = m.LastSeenAt.UTC()
	if m.UserID <= 0 || !m.Sources.valid() || m.FirstSeenAt.IsZero() || m.LastSeenAt.IsZero() || m.LastSeenAt.Before(m.FirstSeenAt) {
		return AudienceMember{}, ErrInvalidAudience
	}
	return m, nil
}

type VisitorBlock struct {
	VisitorUserID int64
	BlockedAt     time.Time
	Reason        string
}

func (b VisitorBlock) Normalize() (VisitorBlock, error) {
	b.BlockedAt = b.BlockedAt.UTC()
	b.Reason = strings.TrimSpace(b.Reason)
	if b.VisitorUserID <= 0 || b.BlockedAt.IsZero() || len(b.Reason) > MaxBlockReasonBytes {
		return VisitorBlock{}, ErrInvalidBlock
	}
	return b, nil
}
