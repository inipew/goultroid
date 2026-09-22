package savedresponse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type SQLiteSurfaceBindingRepository struct {
	db *database.DB
}

func NewSQLiteSurfaceBindingRepository(db *database.DB) *SQLiteSurfaceBindingRepository {
	return &SQLiteSurfaceBindingRepository{db: db}
}

func (r *SQLiteSurfaceBindingRepository) CreateBinding(ctx context.Context, binding SurfaceBinding) (SurfaceBinding, error) {
	if r == nil || r.db == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	normalized, err := binding.Normalize()
	if err != nil {
		return SurfaceBinding{}, err
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO saved_response_surface_bindings (
			surface, alias, provider, provider_scope_id, provider_key,
			enabled, revision, incarnation, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 1, lower(hex(randomblob(16))), ?, ?)
	`,
		string(normalized.Surface),
		normalized.Alias,
		normalized.Reference.Provider,
		normalized.Reference.ScopeID,
		normalized.Reference.Key,
		normalized.Enabled,
		now,
		now,
	)
	if err != nil {
		if bindingExists(ctx, r.db, normalized.Surface, normalized.Alias) {
			return SurfaceBinding{}, ErrBindingExists
		}
		return SurfaceBinding{}, fmt.Errorf("create saved response surface binding: %w", err)
	}
	created, err := r.GetBinding(ctx, normalized.Surface, normalized.Alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	if created == nil {
		return SurfaceBinding{}, ErrBindingNotFound
	}
	return *created, nil
}

func (r *SQLiteSurfaceBindingRepository) GetBinding(ctx context.Context, surface Surface, alias string) (*SurfaceBinding, error) {
	if r == nil || r.db == nil {
		return nil, ErrResolverUnavailable
	}
	surface, alias, err := normalizeSurfaceAlias(surface, alias)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT surface, alias, provider, provider_scope_id, provider_key,
		       enabled, revision, incarnation, created_at, updated_at
		FROM saved_response_surface_bindings
		WHERE surface = ? AND alias = ?
	`, string(surface), alias)
	binding, err := scanSurfaceBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get saved response surface binding: %w", err)
	}
	return &binding, nil
}

func (r *SQLiteSurfaceBindingRepository) ListBindings(ctx context.Context, surface Surface, includeDisabled bool, limit int) ([]SurfaceBinding, error) {
	if r == nil || r.db == nil {
		return nil, ErrResolverUnavailable
	}
	surface = Surface(strings.ToLower(strings.TrimSpace(string(surface))))
	if !validSurface(surface) {
		return nil, ErrInvalidBinding
	}
	if limit <= 0 || limit > MaxBindingList {
		limit = MaxBindingList
	}
	query := `
		SELECT surface, alias, provider, provider_scope_id, provider_key,
		       enabled, revision, incarnation, created_at, updated_at
		FROM saved_response_surface_bindings
		WHERE surface = ?`
	args := []any{string(surface)}
	if !includeDisabled {
		query += ` AND enabled = 1`
	}
	query += ` ORDER BY alias ASC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list saved response surface bindings: %w", err)
	}
	defer rows.Close()

	result := make([]SurfaceBinding, 0)
	for rows.Next() {
		binding, err := scanSurfaceBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan saved response surface binding: %w", err)
		}
		result = append(result, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate saved response surface bindings: %w", err)
	}
	return result, nil
}

func (r *SQLiteSurfaceBindingRepository) UpdateBinding(
	ctx context.Context,
	surface Surface,
	alias string,
	reference Reference,
	enabled bool,
	expectedRevision uint64,
) (SurfaceBinding, error) {
	if r == nil || r.db == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	if expectedRevision == 0 {
		return SurfaceBinding{}, ErrBindingConflict
	}
	normalized, err := (SurfaceBinding{
		Surface:   surface,
		Alias:     alias,
		Reference: reference,
		Enabled:   enabled,
	}).Normalize()
	if err != nil {
		return SurfaceBinding{}, err
	}
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		UPDATE saved_response_surface_bindings
		SET provider = ?, provider_scope_id = ?, provider_key = ?,
		    enabled = ?, revision = revision + 1, updated_at = ?
		WHERE surface = ? AND alias = ? AND revision = ?
	`,
		normalized.Reference.Provider,
		normalized.Reference.ScopeID,
		normalized.Reference.Key,
		normalized.Enabled,
		now,
		string(normalized.Surface),
		normalized.Alias,
		expectedRevision,
	)
	if err != nil {
		return SurfaceBinding{}, fmt.Errorf("update saved response surface binding: %w", err)
	}
	if err := requireBindingMutation(ctx, r.db, result, normalized.Surface, normalized.Alias); err != nil {
		return SurfaceBinding{}, err
	}
	updated, err := r.GetBinding(ctx, normalized.Surface, normalized.Alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	if updated == nil {
		return SurfaceBinding{}, ErrBindingNotFound
	}
	return *updated, nil
}

func (r *SQLiteSurfaceBindingRepository) SetBindingEnabled(ctx context.Context, surface Surface, alias string, enabled bool, expectedRevision uint64) (SurfaceBinding, error) {
	if r == nil || r.db == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	if expectedRevision == 0 {
		return SurfaceBinding{}, ErrBindingConflict
	}
	surface, alias, err := normalizeSurfaceAlias(surface, alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		UPDATE saved_response_surface_bindings
		SET enabled = ?, revision = revision + 1, updated_at = ?
		WHERE surface = ? AND alias = ? AND revision = ?
	`, enabled, now, string(surface), alias, expectedRevision)
	if err != nil {
		return SurfaceBinding{}, fmt.Errorf("set saved response surface binding enabled: %w", err)
	}
	if err := requireBindingMutation(ctx, r.db, result, surface, alias); err != nil {
		return SurfaceBinding{}, err
	}
	updated, err := r.GetBinding(ctx, surface, alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	if updated == nil {
		return SurfaceBinding{}, ErrBindingNotFound
	}
	return *updated, nil
}

func (r *SQLiteSurfaceBindingRepository) DeleteBinding(ctx context.Context, surface Surface, alias string, expectedRevision uint64) error {
	if r == nil || r.db == nil {
		return ErrResolverUnavailable
	}
	if expectedRevision == 0 {
		return ErrBindingConflict
	}
	surface, alias, err := normalizeSurfaceAlias(surface, alias)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM saved_response_surface_bindings
		WHERE surface = ? AND alias = ? AND revision = ?
	`, string(surface), alias, expectedRevision)
	if err != nil {
		return fmt.Errorf("delete saved response surface binding: %w", err)
	}
	return requireBindingMutation(ctx, r.db, result, surface, alias)
}

type surfaceBindingScanner interface {
	Scan(...any) error
}

func scanSurfaceBinding(scanner surfaceBindingScanner) (SurfaceBinding, error) {
	var binding SurfaceBinding
	var surface string
	err := scanner.Scan(
		&surface,
		&binding.Alias,
		&binding.Reference.Provider,
		&binding.Reference.ScopeID,
		&binding.Reference.Key,
		&binding.Enabled,
		&binding.Revision,
		&binding.Incarnation,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err != nil {
		return SurfaceBinding{}, err
	}
	binding.Surface = Surface(surface)
	return binding.Normalize()
}

func normalizeSurfaceAlias(surface Surface, alias string) (Surface, string, error) {
	surface = Surface(strings.ToLower(strings.TrimSpace(string(surface))))
	alias = strings.ToLower(strings.TrimSpace(alias))
	if !validSurface(surface) || !validBindingAlias(alias) {
		return "", "", ErrInvalidBinding
	}
	return surface, alias, nil
}

func bindingExists(ctx context.Context, db *database.DB, surface Surface, alias string) bool {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM saved_response_surface_bindings
		WHERE surface = ? AND alias = ?
	`, string(surface), alias).Scan(&count); err != nil {
		return false
	}
	return count != 0
}

func requireBindingMutation(ctx context.Context, db *database.DB, result sql.Result, surface Surface, alias string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	if bindingExists(ctx, db, surface, alias) {
		return ErrBindingConflict
	}
	return ErrBindingNotFound
}

var _ SurfaceBindingRepository = (*SQLiteSurfaceBindingRepository)(nil)
