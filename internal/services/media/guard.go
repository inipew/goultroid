package media

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/storage"
)

// ResourceGuard enforces size, dimension, duration, disk space, and concurrency boundaries.
type ResourceGuard struct {
	MaxInputSize      int64
	MaxOutputSize     int64
	MaxDuration       time.Duration
	MaxWidth          int
	MaxHeight         int
	DiskFreeThreshold int64
	sem               chan struct{}
}

// NewResourceGuard creates a new guard configured with safe defaults.
func NewResourceGuard(maxConcurrent int, diskFreeThreshold int64) *ResourceGuard {
	if maxConcurrent <= 0 {
		maxConcurrent = 2 // Default: 2 parallel heavy media tasks
	}
	if diskFreeThreshold <= 0 {
		diskFreeThreshold = 100 * 1024 * 1024 // 100 MB free required
	}
	return &ResourceGuard{
		MaxInputSize:      500 * 1024 * 1024, // 500 MB
		MaxOutputSize:     500 * 1024 * 1024, // 500 MB
		MaxDuration:       2 * time.Hour,
		MaxWidth:          4096,
		MaxHeight:         4096,
		DiskFreeThreshold: diskFreeThreshold,
		sem:               make(chan struct{}, maxConcurrent),
	}
}

// ValidateInput ensures the input asset does not exceed safe operating parameters.
func (g *ResourceGuard) ValidateInput(asset *storage.Asset) error {
	if asset == nil {
		return fmt.Errorf("%w: input asset is nil", core.ErrInvalidArgs)
	}

	if asset.Size > g.MaxInputSize {
		return fmt.Errorf("%w: media size %d exceeds maximum allowed %d", core.ErrResourceLimit, asset.Size, g.MaxInputSize)
	}

	if asset.Duration > g.MaxDuration {
		return fmt.Errorf("%w: media duration %v exceeds maximum %v", core.ErrResourceLimit, asset.Duration, g.MaxDuration)
	}

	if asset.Width > g.MaxWidth || asset.Height > g.MaxHeight {
		return fmt.Errorf("%w: media resolution (%dx%d) exceeds maximum (%dx%d)", core.ErrResourceLimit, asset.Width, asset.Height, g.MaxWidth, g.MaxHeight)
	}

	return nil
}

// CheckDisk ensures that at least requiredBytes + DiskFreeThreshold is available on the disk.
func (g *ResourceGuard) CheckDisk(dirPath string, requiredBytes int64) error {
	need := requiredBytes + g.DiskFreeThreshold
	return core.CheckDiskSpace(dirPath, need)
}

// Acquire acquires a slot in the concurrency semaphore or waits until ctx is canceled.
// Returns a release function to be called in defer.
func (g *ResourceGuard) Acquire(ctx context.Context) (func(), error) {
	select {
	case g.sem <- struct{}{}:
		return func() { <-g.sem }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: timed out waiting for media worker slot: %v", core.ErrTimeout, ctx.Err())
	}
}
