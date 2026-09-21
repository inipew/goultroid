package mediaregistry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

// OwnerStorage is a composition-time wrapper for a subsystem whose managed
// assets have one explicit producer/owner/lifecycle identity. Put registers
// ownership and a prepared crash guard before returning. Delete is routed
// through P3-C and therefore requires the same registered authority plus the
// last-moment reference claim.
//
// The shared storage primitive remains unaware of media ownership. This wrapper
// must only be injected into the matching producer; it never infers ownership
// from a path, an asset ID, physical enumeration, or a zero reference count.
type OwnerStorage struct {
	base      storage.Storage
	registry  *Registry
	reclaimer *Reclaimer
	producer  string
	owner     string
	lifecycle Lifecycle
	reason    string
}

func NewOwnerStorage(
	base storage.Storage,
	registry *Registry,
	producer string,
	owner string,
	lifecycle Lifecycle,
	reason string,
) *OwnerStorage {
	return &OwnerStorage{
		base:      base,
		registry:  registry,
		reclaimer: NewReclaimer(registry, base),
		producer:  strings.TrimSpace(producer),
		owner:     strings.TrimSpace(owner),
		lifecycle: lifecycle,
		reason:    strings.TrimSpace(reason),
	}
}

func (s *OwnerStorage) Put(ctx context.Context, src io.Reader, meta storage.Metadata) (*storage.Asset, error) {
	if s == nil || s.base == nil || s.registry == nil || s.reclaimer == nil {
		return nil, ErrNilDatabase
	}
	if s.producer == "" || s.owner == "" || !s.lifecycle.valid() || s.lifecycle == LifecycleLegacy {
		return nil, ErrInvalidReclamation
	}
	asset, err := s.base.Put(ctx, src, meta)
	if err != nil {
		return nil, err
	}
	registration := AssetRegistration{
		AssetID: asset.ID, Producer: s.producer, Owner: s.owner, Lifecycle: s.lifecycle,
	}
	if err := s.registry.RegisterAsset(ctx, registration); err != nil {
		return nil, s.rollbackFreshPut(ctx, asset.ID, err)
	}
	if err := s.reclaimer.PrepareReclamation(ctx, ReclamationRequest{
		AssetID: asset.ID, Owner: s.owner, Lifecycle: s.lifecycle,
		Reason: s.reason + " crash guard", Grace: DefaultReclamationGrace,
	}); err != nil {
		return nil, s.rollbackRegisteredPut(ctx, asset.ID, err)
	}
	return asset, nil
}

func (s *OwnerStorage) rollbackFreshPut(parent context.Context, assetID string, cause error) error {
	ctx, cancel := ownerStorageCleanupContext(parent)
	defer cancel()
	deleteErr := s.base.Delete(ctx, assetID)
	if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("media registry: rollback unregistered owner asset %q: %w", assetID, deleteErr))
	}
	return cause
}

func (s *OwnerStorage) rollbackRegisteredPut(parent context.Context, assetID string, cause error) error {
	ctx, cancel := ownerStorageCleanupContext(parent)
	defer cancel()
	deleteErr := s.base.Delete(ctx, assetID)
	if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("media registry: rollback registered owner asset %q: %w", assetID, deleteErr))
	}
	metadataErr := s.registry.RemoveOwnedAsset(ctx, assetID, s.owner)
	if metadataErr != nil {
		return errors.Join(cause, metadataErr)
	}
	return cause
}

func ownerStorageCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}

func (s *OwnerStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if s == nil || s.base == nil {
		return nil, fmt.Errorf("media registry: owner storage is nil")
	}
	return s.base.Open(ctx, id)
}

func (s *OwnerStorage) Delete(ctx context.Context, id string) error {
	if s == nil || s.base == nil || s.registry == nil || s.reclaimer == nil {
		return ErrNilDatabase
	}
	record, err := s.registry.Asset(ctx, id)
	if err != nil {
		return err
	}
	if record.Producer != s.producer || record.Owner != s.owner || record.Lifecycle != s.lifecycle || record.Lifecycle == LifecycleLegacy {
		return fmt.Errorf(
			"%w: asset %q producer=%q owner=%q lifecycle=%q",
			ErrReclamationNotAllowed,
			id,
			record.Producer,
			record.Owner,
			record.Lifecycle,
		)
	}
	return s.reclaimer.ReclaimNow(ctx, ReclamationRequest{
		AssetID:   id,
		Owner:     s.owner,
		Lifecycle: s.lifecycle,
		Reason:    s.reason,
	})
}

func (s *OwnerStorage) Stat(ctx context.Context, id string) (*storage.Asset, error) {
	if s == nil || s.base == nil {
		return nil, fmt.Errorf("media registry: owner storage is nil")
	}
	return s.base.Stat(ctx, id)
}

func (s *OwnerStorage) Enumerate(ctx context.Context, opts storage.EnumerationOptions) (storage.EnumerationPage, error) {
	if s == nil || s.base == nil {
		return storage.EnumerationPage{}, fmt.Errorf("media registry: owner storage is nil")
	}
	return s.base.Enumerate(ctx, opts)
}

func (s *OwnerStorage) BasePath() string {
	if s == nil || s.base == nil {
		return ""
	}
	return s.base.BasePath()
}

var _ storage.Storage = (*OwnerStorage)(nil)
