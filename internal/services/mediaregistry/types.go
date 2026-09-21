package mediaregistry

import (
	"errors"
	"time"
)

var (
	ErrNilDatabase           = errors.New("media registry: database is nil")
	ErrInvalidAsset          = errors.New("media registry: invalid asset registration")
	ErrInvalidReference      = errors.New("media registry: invalid reference")
	ErrOwnershipConflict     = errors.New("media registry: asset ownership conflict")
	ErrAssetNotRegistered    = errors.New("media registry: asset is not registered")
	ErrIncompleteSchema      = errors.New("media registry: incomplete schema")
	ErrInvalidReclamation    = errors.New("media registry: invalid reclamation request")
	ErrReclamationNotAllowed = errors.New("media registry: reclamation is not authorized")
	ErrAssetReferenced       = errors.New("media registry: asset has durable references")
	ErrReclamationInProgress = errors.New("media registry: reclamation is in progress")
	ErrReclamationIntentGone = errors.New("media registry: reclamation intent disappeared")
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

// ReclamationState is the durable state of an owner-authorized delete intent.
// prepared means bytes exist but the producer has not yet made an explicit
// deletion decision; pending means the owner authorized reclamation; deleting
// is a short claim lease that blocks new durable references until the physical
// delete finishes or the claim is released.
type ReclamationState string

const (
	ReclamationPrepared ReclamationState = "prepared"
	ReclamationPending  ReclamationState = "pending"
	ReclamationDeleting ReclamationState = "deleting"
)

// ReclamationRequest is an explicit owner authorization to reclaim one
// registered asset. Expected owner/lifecycle are snapshots of the authority
// being exercised; they must still match immediately before physical delete.
type ReclamationRequest struct {
	AssetID   string
	Owner     string
	Lifecycle Lifecycle
	Reason    string
	Grace     time.Duration
}

// ReclamationPolicy is an explicit owner/lifecycle policy used to discover
// reclaimable registered assets. A policy authorizes a class of assets; zero
// references remains only a safety precondition and never becomes authority on
// its own. Retained and legacy assets should not be supplied as auto policies.
type ReclamationPolicy struct {
	Owner      string
	Lifecycle  Lifecycle
	MinimumAge time.Duration
	Reason     string
}

// ReclamationIntent is the durable representation of one global delete intent.
type ReclamationIntent struct {
	AssetID           string
	ExpectedOwner     string
	ExpectedLifecycle Lifecycle
	Reason            string
	State             ReclamationState
	Attempts          int
	LastError         string
	NotBefore         time.Time
	NextAttemptAt     time.Time
	ClaimToken        string
	ClaimedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ReclamationStats summarizes one bounded reconciliation pass.
type ReclamationStats struct {
	Scanned         int
	Claimed         int
	Deleted         int
	AlreadyMissing  int
	Deferred        int
	UnsafeCancelled int
	RecoveredClaims int
}
