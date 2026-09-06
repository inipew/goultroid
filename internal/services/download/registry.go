package download

import (
	"context"
	"fmt"
	"sync"

	"github.com/inipew/goultroid/internal/services/storage"
)

// Registry manages and dispatches download requests across registered Providers.
type Registry struct {
	providers []Provider
	mu        sync.RWMutex
}

// NewRegistry creates a new download provider registry.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{
		providers: make([]Provider, 0, len(providers)),
	}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register registers a new download provider.
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

// Resolve returns the first provider that matches rawURL.
func (r *Registry) Resolve(rawURL string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.providers {
		if p.Match(rawURL) {
			return p
		}
	}
	return nil
}

// Download finds the appropriate provider and downloads the media into storage.
func (r *Registry) Download(ctx context.Context, rawURL string, store storage.Storage, opts DownloadOptions) (*storage.Asset, error) {
	p := r.Resolve(rawURL)
	if p == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoMatchingProvider, rawURL)
	}
	return p.Download(ctx, rawURL, store, opts)
}
