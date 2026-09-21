package interaction

import (
	"context"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type sessionEntry struct {
	session Session
	ctx     context.Context
	cancel  context.CancelCauseFunc
	expiry  *expiryItem
}

// Runtime owns bounded, cancellable interaction sessions. It intentionally has
// no ticker or background goroutine; expired sessions are reclaimed lazily by
// normal operations or explicitly through PruneExpired.
type Runtime struct {
	mu sync.Mutex

	catalog Catalog
	config  Config
	now     func() time.Time
	newID   func() (string, error)

	rootCtx    context.Context
	rootCancel context.CancelCauseFunc
	closed     bool

	sessions    map[string]*sessionEntry
	byScope     map[tasks.ScopeIdentity]map[string]struct{}
	actorCounts map[int64]int
	expiries    expiryHeap
	stateBytes  int

	expiredCount          uint64
	canceledCount         uint64
	staleCount            uint64
	capacityRejectedCount uint64
}

// NewRuntime constructs an immediately usable interaction runtime.
func NewRuntime(catalog Catalog, config Config) (*Runtime, error) {
	if catalog == nil {
		return nil, ErrCatalogRequired
	}
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	rootCtx, rootCancel := context.WithCancelCause(context.Background())
	return &Runtime{
		catalog:     catalog,
		config:      normalized,
		now:         time.Now,
		newID:       newSessionID,
		rootCtx:     rootCtx,
		rootCancel:  rootCancel,
		sessions:    make(map[string]*sessionEntry),
		byScope:     make(map[tasks.ScopeIdentity]map[string]struct{}),
		actorCounts: make(map[int64]int),
	}, nil
}

// Close rejects new work, cancels all session contexts, and releases retained state.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.rootCancel(ErrClosed)
	r.sessions = make(map[string]*sessionEntry)
	r.byScope = make(map[tasks.ScopeIdentity]map[string]struct{})
	r.actorCounts = make(map[int64]int)
	r.expiries = nil
	r.stateBytes = 0
	r.mu.Unlock()
	return nil
}

// Create allocates a generation-bound session. Live sessions are never evicted
// to make room: expired sessions are reclaimed first, then capacity fails closed.
func (r *Runtime) Create(ctx context.Context, request CreateRequest) (Resolved, error) {
	if r == nil {
		return Resolved{}, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return Resolved{}, ErrClosed
	}
	featureID := normalizeFeatureID(request.FeatureID)
	if !validIdentifier(featureID) {
		return Resolved{}, ErrInvalidFeature
	}
	request.Binding = request.Binding.normalized()
	if err := request.Binding.Validate(); err != nil {
		return Resolved{}, err
	}
	if len(request.State) > r.config.MaxStateBytes {
		return Resolved{}, ErrStateTooLarge
	}
	ttl, err := r.normalizeTTL(request.TTL)
	if err != nil {
		return Resolved{}, err
	}
	scope, ok := r.catalog.FeatureScope(featureID)
	if !ok || scope.IsZero() {
		return Resolved{}, ErrInvalidFeature
	}

	now := r.now()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Resolved{}, ErrClosed
	}
	r.pruneExpiredLocked(now)
	if err := r.checkCapacityLocked(scope, request.Binding.ActorID, len(request.State)); err != nil {
		r.capacityRejectedCount++
		r.mu.Unlock()
		return Resolved{}, err
	}
	id, err := r.uniqueIDLocked()
	if err != nil {
		r.mu.Unlock()
		return Resolved{}, err
	}
	expiresAt := now.Add(ttl)
	sessionCtx, cancel := context.WithCancelCause(r.rootCtx)
	snapshot := Session{
		ID:        id,
		FeatureID: featureID,
		Scope:     scope,
		Binding:   request.Binding,
		State:     append([]byte(nil), request.State...),
		Revision:  1,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	entry := &sessionEntry{session: snapshot, ctx: sessionCtx, cancel: cancel}
	r.sessions[id] = entry
	r.indexSessionLocked(entry)
	r.scheduleExpiryLocked(entry, expiresAt)
	r.mu.Unlock()

	// Re-check after publication. Feature cleanup removes the catalog entry
	// before sweeping its sessions, which closes the create-vs-disable race.
	currentScope, current := r.catalog.FeatureScope(featureID)
	if !current || currentScope != scope {
		r.cancelIfScope(id, scope, ErrScopeStale)
		return Resolved{}, ErrScopeStale
	}
	return Resolved{Session: cloneSession(snapshot), Context: sessionCtx}, nil
}
