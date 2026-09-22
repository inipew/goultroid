package deeplink

import (
	"context"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

const (
	TokenVersion      = "d1"
	DefaultTTL        = 24 * time.Hour
	MaxTTL            = 30 * 24 * time.Hour
	MaxPayloadBytes   = 1024
	MaxKindBytes      = 32
	MaxRetainedTokens = 4096
	ClaimLeaseTTL     = 5 * time.Minute
)

var (
	ErrInvalidToken        = errors.New("assistant/deeplink: invalid token")
	ErrTokenNotFound       = errors.New("assistant/deeplink: token not found")
	ErrTokenExpired        = errors.New("assistant/deeplink: token expired")
	ErrTokenConsumed       = errors.New("assistant/deeplink: token already consumed")
	ErrTokenClaimed        = errors.New("assistant/deeplink: token execution already in progress")
	ErrTokenUnauthorized   = errors.New("assistant/deeplink: token actor mismatch")
	ErrTokenExists         = errors.New("assistant/deeplink: token already exists")
	ErrInvalidKind         = errors.New("assistant/deeplink: invalid kind")
	ErrInvalidPayload      = errors.New("assistant/deeplink: invalid payload")
	ErrProviderRegistered  = errors.New("assistant/deeplink: provider already registered")
	ErrProviderUnavailable = errors.New("assistant/deeplink: provider unavailable")
	ErrProviderStale       = errors.New("assistant/deeplink: provider registration is stale")
	ErrCapacity            = errors.New("assistant/deeplink: token capacity exhausted")
)

// Token is immutable durable routing state. Payload is opaque to the router.
type Token struct {
	ID         string
	Kind       string
	Payload    string
	ActorID    int64
	SingleUse  bool
	ConsumedAt *time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// IssueRequest creates an opaque /start payload. ActorID=0 means public.
type IssueRequest struct {
	Kind      string
	Payload   string
	ActorID   int64
	SingleUse bool
	TTL       time.Duration
}

// PreparedTarget is provider-owned admission state. Scope and resources are
// copied into TaskEngine before provider execution.
type PreparedTarget struct {
	Scope     tasks.ScopeIdentity
	Resources []tasks.ResourceRequirement
	State     any
}

// Delivery is the transport-neutral subset needed by deep-link providers.
type Delivery struct {
	ActorID   int64
	ChatID    int64
	SendText  func(string) error
	SendMedia func(mediaType, path, caption string) error
}

// Provider resolves one typed token payload and executes it after admission.
type Provider interface {
	Prepare(context.Context, string, int64) (PreparedTarget, error)
	Execute(context.Context, PreparedTarget, Delivery) error
}

// Repository persists opaque token records.
type Repository interface {
	Create(context.Context, Token) error
	Get(context.Context, string) (Token, error)
	Claim(context.Context, string, int64, time.Time, string, time.Time) (Token, error)
	CommitClaim(context.Context, string, string, time.Time) error
	ReleaseClaim(context.Context, string, string) error
	PruneExpired(context.Context, time.Time, int) (int, error)
	CountRetained(context.Context) (int, error)
}
