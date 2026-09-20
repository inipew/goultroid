package mediaregistry

import (
	"errors"
	"time"
)

var (
	ErrNilDatabase        = errors.New("media registry: database is nil")
	ErrInvalidAsset       = errors.New("media registry: invalid asset registration")
	ErrInvalidReference   = errors.New("media registry: invalid reference")
	ErrOwnershipConflict  = errors.New("media registry: asset ownership conflict")
	ErrAssetNotRegistered = errors.New("media registry: asset is not registered")
)

// Lifecycle describes who is allowed to decide when an asset can be reclaimed.
// A zero reference count is not, by itself, a deletion decision.
type Lifecycle string

const (
	LifecyclePersistent Lifecycle = "persistent"
	LifecycleRetained   Lifecycle = "retained"
	LifecycleTransient  Lifecycle = "transient"
	LifecycleLegacy     Lifecycle = "legacy"
)

func (l Lifecycle) valid() bool {
	switch l {
	case LifecyclePersistent, LifecycleRetained, LifecycleTransient, LifecycleLegacy:
		return true
	default:
		return false
	}
}

// AssetRegistration records provenance and lifecycle authority for one
// physical asset. Producer identifies who created the bytes; Owner identifies
// the subsystem responsible for lifecycle decisions.
type AssetRegistration struct {
	AssetID   string
	Producer  string
	Owner     string
	Lifecycle Lifecycle
}

// AssetRecord is the durable registry representation of an asset.
type AssetRecord struct {
	AssetRegistration
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// Reference is one durable domain reference to an asset. Reference rows, not a
// mutable ref_count column, are the source of truth for reachability.
type Reference struct {
	AssetID   string
	Subsystem string
	Kind      string
	Key       string
}
