package mediaregistry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type Registry struct {
	db *database.DB
}

func New(db *database.DB) *Registry {
	return &Registry{db: db}
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizeRegistration(reg AssetRegistration) (AssetRegistration, error) {
	reg.AssetID = strings.TrimSpace(reg.AssetID)
	reg.Producer = strings.TrimSpace(reg.Producer)
	reg.Owner = strings.TrimSpace(reg.Owner)
	if reg.AssetID == "" || reg.Producer == "" || reg.Owner == "" || !reg.Lifecycle.valid() {
		return AssetRegistration{}, ErrInvalidAsset
	}
	return reg, nil
}

func normalizeReference(ref Reference) (Reference, error) {
	ref.AssetID = strings.TrimSpace(ref.AssetID)
	ref.Subsystem = strings.TrimSpace(ref.Subsystem)
	ref.Kind = strings.TrimSpace(ref.Kind)
	// Reference keys are domain identifiers and may intentionally contain
	// leading/trailing whitespace. Validate emptiness without rewriting the key.
	if ref.AssetID == "" || ref.Subsystem == "" || ref.Kind == "" || strings.TrimSpace(ref.Key) == "" {
		return Reference{}, ErrInvalidReference
	}
	return ref, nil
}

func (r *Registry) RegisterAsset(ctx context.Context, reg AssetRegistration, refs ...Reference) error {
	if r == nil || r.db == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var err error
	reg, err = normalizeRegistration(reg)
	if err != nil {
		return err
	}
	normalizedRefs := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		ref, err = normalizeReference(ref)
		if err != nil {
			return err
		}
		if ref.AssetID != reg.AssetID {
			return fmt.Errorf("%w: reference asset %q does not match registration %q", ErrInvalidReference, ref.AssetID, reg.AssetID)
		}
		normalizedRefs = append(normalizedRefs, ref)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("media registry: begin registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO media_assets (
			asset_id, producer, owner, lifecycle, registered_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET
			lifecycle = excluded.lifecycle,
			updated_at = excluded.updated_at
		WHERE media_assets.producer = excluded.producer
		  AND media_assets.owner = excluded.owner
	`, reg.AssetID, reg.Producer, reg.Owner, reg.Lifecycle, now, now)
	if err != nil {
		return fmt.Errorf("media registry: register asset %q: %w", reg.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("media registry: inspect registration %q: %w", reg.AssetID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: asset %q", ErrOwnershipConflict, reg.AssetID)
	}
	for _, ref := range normalizedRefs {
		if err := upsertReference(ctx, tx, ref, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("media registry: commit registration %q: %w", reg.AssetID, err)
	}
	return nil
}

func (r *Registry) UpsertReference(ctx context.Context, ref Reference) error {
	if r == nil || r.db == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var err error
	ref, err = normalizeReference(ref)
	if err != nil {
		return err
	}
	return upsertReference(ctx, r.db, ref, time.Now().UTC())
}

func upsertReference(ctx context.Context, db database.SQLExecutor, ref Reference, now time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO media_asset_references (
			asset_id, subsystem, reference_kind, reference_key, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(asset_id, subsystem, reference_kind, reference_key)
		DO UPDATE SET updated_at = excluded.updated_at
	`, ref.AssetID, ref.Subsystem, ref.Kind, ref.Key, now, now)
	if err != nil {
		return fmt.Errorf("media registry: upsert reference for asset %q: %w", ref.AssetID, err)
	}
	return nil
}

func (r *Registry) RemoveReference(ctx context.Context, ref Reference) error {
	if r == nil || r.db == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var err error
	ref, err = normalizeReference(ref)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM media_asset_references
		WHERE asset_id = ? AND subsystem = ? AND reference_kind = ? AND reference_key = ?
	`, ref.AssetID, ref.Subsystem, ref.Kind, ref.Key); err != nil {
		return fmt.Errorf("media registry: remove reference for asset %q: %w", ref.AssetID, err)
	}
	return nil
}

// RemoveOwnedAsset removes registry metadata only when the expected owner still
// owns the asset and no durable references remain. Physical storage is outside
// this operation.
func (r *Registry) RemoveOwnedAsset(ctx context.Context, assetID, owner string) error {
	if r == nil || r.db == nil {
		return ErrNilDatabase
	}
	return RemoveOwnedAssetWithExecutor(ctx, r.db, assetID, owner)
}

func (r *Registry) Asset(ctx context.Context, assetID string) (AssetRecord, error) {
	if r == nil || r.db == nil {
		return AssetRecord{}, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return AssetRecord{}, ErrInvalidAsset
	}
	var record AssetRecord
	err := r.db.QueryRowContext(ctx, `
		SELECT asset_id, producer, owner, lifecycle, registered_at, updated_at
		FROM media_assets WHERE asset_id = ?
	`, assetID).Scan(
		&record.AssetID,
		&record.Producer,
		&record.Owner,
		&record.Lifecycle,
		&record.RegisteredAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetRecord{}, ErrAssetNotRegistered
	}
	if err != nil {
		return AssetRecord{}, fmt.Errorf("media registry: load asset %q: %w", assetID, err)
	}
	return record, nil
}

func (r *Registry) ReferenceCount(ctx context.Context, assetID string) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return 0, ErrInvalidReference
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM media_asset_references WHERE asset_id = ?
	`, assetID).Scan(&count); err != nil {
		return 0, fmt.Errorf("media registry: count references for asset %q: %w", assetID, err)
	}
	return count, nil
}
