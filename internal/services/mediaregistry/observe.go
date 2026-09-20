package mediaregistry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/services/storage"
)

type ObservationClass string

const (
	ObservationTrackedReferenced   ObservationClass = "tracked_referenced"
	ObservationTrackedUnreferenced ObservationClass = "tracked_unreferenced"
	ObservationUntrackedReferenced ObservationClass = "untracked_referenced"
	ObservationLegacyUntracked     ObservationClass = "legacy_untracked"
	ObservationMalformed           ObservationClass = "malformed"
)

type ObservationEntry struct {
	Physical       storage.EnumerationEntry
	Class          ObservationClass
	Registration   *AssetRecord
	ReferenceCount int
}

type ObservationStats struct {
	PhysicalEntries     int
	TrackedReferenced   int
	TrackedUnreferenced int
	UntrackedReferenced int
	LegacyUntracked     int
	Malformed           int
}

type ObservationPage struct {
	Entries    []ObservationEntry
	NextCursor string
	Stats      ObservationStats
}

// ObserveStorage classifies one bounded physical page without changing the
// storage backend or registry. Reclamation is deliberately outside this API.
func (r *Registry) ObserveStorage(
	ctx context.Context,
	store storage.Storage,
	opts storage.EnumerationOptions,
) (ObservationPage, error) {
	if r == nil || r.db == nil {
		return ObservationPage{}, ErrNilDatabase
	}
	if store == nil {
		return ObservationPage{}, fmt.Errorf("media registry: storage is nil")
	}
	ctx = normalizeContext(ctx)
	physical, err := store.Enumerate(ctx, opts)
	if err != nil {
		return ObservationPage{}, fmt.Errorf("media registry: enumerate storage: %w", err)
	}

	page := ObservationPage{
		Entries:    make([]ObservationEntry, 0, len(physical.Entries)),
		NextCursor: physical.NextCursor,
	}
	for _, entry := range physical.Entries {
		observed, err := r.classify(ctx, entry)
		if err != nil {
			return ObservationPage{}, err
		}
		page.Entries = append(page.Entries, observed)
		page.Stats.PhysicalEntries++
		switch observed.Class {
		case ObservationTrackedReferenced:
			page.Stats.TrackedReferenced++
		case ObservationTrackedUnreferenced:
			page.Stats.TrackedUnreferenced++
		case ObservationUntrackedReferenced:
			page.Stats.UntrackedReferenced++
		case ObservationLegacyUntracked:
			page.Stats.LegacyUntracked++
		case ObservationMalformed:
			page.Stats.Malformed++
		}
	}
	return page, nil
}

func (r *Registry) classify(ctx context.Context, entry storage.EnumerationEntry) (ObservationEntry, error) {
	observed := ObservationEntry{Physical: entry}
	switch entry.State {
	case storage.EnumerationMalformed:
		observed.Class = ObservationMalformed
		return observed, nil
	case storage.EnumerationUnmanaged:
		observed.Class = ObservationLegacyUntracked
		return observed, nil
	case storage.EnumerationManaged:
	default:
		observed.Class = ObservationMalformed
		return observed, nil
	}
	if entry.Asset == nil || entry.Asset.ID == "" {
		observed.Class = ObservationMalformed
		return observed, nil
	}

	var record AssetRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT asset_id, producer, owner, lifecycle, registered_at, updated_at
		FROM media_assets WHERE asset_id = ?
	`, entry.Asset.ID).Scan(
		&record.AssetID,
		&record.Producer,
		&record.Owner,
		&record.Lifecycle,
		&record.RegisteredAt,
		&record.UpdatedAt,
	)
	registered := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ObservationEntry{}, fmt.Errorf("media registry: inspect asset %q: %w", entry.Asset.ID, err)
	}
	if registered {
		observed.Registration = &record
	}

	if err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM media_asset_references WHERE asset_id = ?
	`, entry.Asset.ID).Scan(&observed.ReferenceCount); err != nil {
		return ObservationEntry{}, fmt.Errorf("media registry: inspect references for asset %q: %w", entry.Asset.ID, err)
	}

	switch {
	case registered && observed.ReferenceCount > 0:
		observed.Class = ObservationTrackedReferenced
	case registered:
		observed.Class = ObservationTrackedUnreferenced
	case observed.ReferenceCount > 0:
		observed.Class = ObservationUntrackedReferenced
	default:
		observed.Class = ObservationLegacyUntracked
	}
	return observed, nil
}
