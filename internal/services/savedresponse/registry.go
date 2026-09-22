package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrInvalidReference    = errors.New("saved response: invalid reference")
	ErrResolverRegistered  = errors.New("saved response: resolver already registered")
	ErrResolverUnavailable = errors.New("saved response: resolver unavailable")
	ErrReferencedNotFound  = errors.New("saved response: referenced response not found")
)

// Reference is a stable pointer to a SavedResponse owned by another subsystem.
// Provider identifies the owning feature; ScopeID and Key remain provider-owned
// identity and never duplicate response text/media in the binding layer.
type Reference struct {
	Provider string
	ScopeID  int64
	Key      string
}

func (r Reference) Validate() error {
	if normalizeProvider(r.Provider) == "" || strings.TrimSpace(r.Key) == "" {
		return ErrInvalidReference
	}
	return nil
}

// Resolver loads a response from its authoritative subsystem-owned repository.
type Resolver interface {
	ResolveSavedResponse(context.Context, Reference) (Response, bool, error)
}

type resolverEntry struct {
	scope    tasks.ScopeIdentity
	resolver Resolver
	token    uint64
}

// Resolved is an immutable resolved response plus the plugin generation that
// owned the lookup. Consumers can carry Scope into TaskEngine admission.
type Resolved struct {
	Reference Reference
	Response  Response
	Scope     tasks.ScopeIdentity
}

// Registry owns only provider bindings; response persistence remains in the
// provider repository (notes, filters, etc.).
type Registry struct {
	mu        sync.RWMutex
	resolvers map[string]resolverEntry
	next      uint64
}

type Registration struct {
	registry *Registry
	provider string
	token    uint64
	once     sync.Once
}

func NewRegistry() *Registry {
	return &Registry{resolvers: make(map[string]resolverEntry)}
}

func (r *Registry) Register(provider string, scope tasks.ScopeIdentity, resolver Resolver) (*Registration, error) {
	if r == nil || resolver == nil || scope.IsZero() {
		return nil, ErrResolverUnavailable
	}
	provider = normalizeProvider(provider)
	if provider == "" {
		return nil, ErrInvalidReference
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.resolvers[provider]; exists {
		return nil, fmt.Errorf("%w: %s", ErrResolverRegistered, provider)
	}
	r.next++
	token := r.next
	r.resolvers[provider] = resolverEntry{scope: scope, resolver: resolver, token: token}
	return &Registration{registry: r, provider: provider, token: token}, nil
}

func (r *Registration) Close() {
	if r == nil || r.registry == nil {
		return
	}
	r.once.Do(func() {
		r.registry.mu.Lock()
		current, ok := r.registry.resolvers[r.provider]
		if ok && current.token == r.token {
			delete(r.registry.resolvers, r.provider)
		}
		r.registry.mu.Unlock()
	})
}

func (r *Registry) Resolve(ctx context.Context, ref Reference) (Resolved, error) {
	if r == nil {
		return Resolved{}, ErrResolverUnavailable
	}
	ref.Provider = normalizeProvider(ref.Provider)
	ref.Key = strings.TrimSpace(ref.Key)
	if err := ref.Validate(); err != nil {
		return Resolved{}, err
	}
	r.mu.RLock()
	entry, ok := r.resolvers[ref.Provider]
	r.mu.RUnlock()
	if !ok || entry.resolver == nil {
		return Resolved{}, ErrResolverUnavailable
	}
	response, found, err := entry.resolver.ResolveSavedResponse(ctx, ref)
	if err != nil {
		return Resolved{}, err
	}
	if !found {
		return Resolved{}, ErrReferencedNotFound
	}
	return Resolved{
		Reference: ref,
		Response:  response.Clone(),
		Scope:     entry.scope,
	}, nil
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
