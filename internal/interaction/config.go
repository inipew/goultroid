package interaction

import (
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

const (
	DefaultMaxSessions         = 4096
	DefaultMaxSessionsPerScope = 512
	DefaultMaxSessionsPerActor = 64
	DefaultMaxStateBytes       = 64 * 1024
	DefaultMaxTotalStateBytes  = 8 * 1024 * 1024
	DefaultTTL                 = 15 * time.Minute
	DefaultMaxTTL              = 24 * time.Hour
)

var (
	ErrCatalogRequired  = errors.New("interaction: feature catalog is required")
	ErrClosed           = errors.New("interaction: runtime is closed")
	ErrInvalidConfig    = errors.New("interaction: invalid config")
	ErrInvalidFeature   = errors.New("interaction: invalid feature")
	ErrInvalidBinding   = errors.New("interaction: invalid binding")
	ErrInvalidTTL       = errors.New("interaction: invalid ttl")
	ErrStateTooLarge    = errors.New("interaction: state exceeds per-session limit")
	ErrCapacity         = errors.New("interaction: session capacity exhausted")
	ErrNotFound         = errors.New("interaction: session not found")
	ErrExpired          = errors.New("interaction: session expired")
	ErrCanceled         = errors.New("interaction: session canceled")
	ErrScopeStale       = errors.New("interaction: feature generation is stale")
	ErrBindingMismatch  = errors.New("interaction: binding mismatch")
	ErrRevisionConflict = errors.New("interaction: revision conflict")
	ErrTargetBound      = errors.New("interaction: target already bound")
	ErrActionNotFound   = errors.New("interaction: action not found")
	ErrTokenMismatch    = errors.New("interaction: callback token does not match session")
	ErrStaleToken       = errors.New("interaction: callback token revision is stale")
)

// Catalog is the minimal P0 feature-catalog view needed by the session runtime.
// It deliberately avoids exposing registry mutation to the interaction layer.
type Catalog interface {
	FeatureScope(featureID string) (tasks.ScopeIdentity, bool)
	HasAction(featureID, actionID string) bool
}

// Config bounds retained interaction state. Zero values select conservative defaults.
type Config struct {
	MaxSessions         int
	MaxSessionsPerScope int
	MaxSessionsPerActor int
	MaxStateBytes       int
	MaxTotalStateBytes  int
	DefaultTTL          time.Duration
	MaxTTL              time.Duration
}

func (c Config) normalized() (Config, error) {
	if c.MaxSessions < 0 || c.MaxSessionsPerScope < 0 || c.MaxSessionsPerActor < 0 || c.MaxStateBytes < 0 || c.MaxTotalStateBytes < 0 || c.DefaultTTL < 0 || c.MaxTTL < 0 {
		return Config{}, ErrInvalidConfig
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = DefaultMaxSessions
	}
	if c.MaxSessionsPerScope == 0 {
		c.MaxSessionsPerScope = DefaultMaxSessionsPerScope
	}
	if c.MaxSessionsPerActor == 0 {
		c.MaxSessionsPerActor = DefaultMaxSessionsPerActor
	}
	if c.MaxStateBytes == 0 {
		c.MaxStateBytes = DefaultMaxStateBytes
	}
	if c.MaxTotalStateBytes == 0 {
		c.MaxTotalStateBytes = DefaultMaxTotalStateBytes
	}
	if c.DefaultTTL == 0 {
		c.DefaultTTL = DefaultTTL
	}
	if c.MaxTTL == 0 {
		c.MaxTTL = DefaultMaxTTL
	}
	if c.DefaultTTL > c.MaxTTL {
		return Config{}, fmt.Errorf("%w: default ttl exceeds max ttl", ErrInvalidConfig)
	}
	if c.MaxStateBytes > c.MaxTotalStateBytes {
		return Config{}, fmt.Errorf("%w: per-session state limit exceeds total state limit", ErrInvalidConfig)
	}
	return c, nil
}
