package download

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
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
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		asset, err := p.Download(ctx, rawURL, store, opts)
		if err == nil {
			return asset, nil
		}
		if !retryableDownloadError(err) || attempt == maxAttempts {
			return nil, err
		}
		if err := waitRetry(ctx, opts.RetryDelay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("download attempts exhausted")
}

func retryableDownloadError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrBlockedSSRF) || errors.Is(err, ErrInvalidScheme) ||
		errors.Is(err, ErrExtractorUnavailable) || errors.Is(err, core.ErrInvalidArgs) ||
		errors.Is(err, core.ErrResourceLimit) {
		return false
	}
	return errors.Is(err, ErrDownloadFailed)
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
