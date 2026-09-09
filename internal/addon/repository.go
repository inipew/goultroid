package addon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// AddonRecord represents an installed external addon in the persistent registry.
type AddonRecord struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	Description  string    `json:"description"`
	Author       string    `json:"author"`
	SourceURL    string    `json:"source_url"`
	Status       string    `json:"status"` // "active" or "disabled"
	Capabilities string    `json:"capabilities"`
	MinVersion   string    `json:"min_version"`
	InstalledAt  time.Time `json:"installed_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Repository defines persistence operations for addon records.
type Repository interface {
	InitSchema(ctx context.Context) error
	GetAddon(ctx context.Context, name string) (*AddonRecord, error)
	ListAddons(ctx context.Context) ([]*AddonRecord, error)
	SaveAddon(ctx context.Context, rec *AddonRecord) error
	DeleteAddon(ctx context.Context, name string) error
	SetAddonStatus(ctx context.Context, name, status string) error
}

// SQLiteRepository implements Repository backed by SQLExecutor.
type SQLiteRepository struct {
	db database.SQLExecutor
}

// NewSQLiteRepository creates a new SQLiteRepository.
func NewSQLiteRepository(db database.SQLExecutor) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

var _ Repository = (*SQLiteRepository)(nil)

// InitSchema ensures the addon_registry table and indexes exist.
func (r *SQLiteRepository) InitSchema(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS addon_registry (
		name TEXT PRIMARY KEY,
		version TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		author TEXT NOT NULL DEFAULT '',
		source_url TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active',
		capabilities TEXT NOT NULL DEFAULT '',
		min_version TEXT NOT NULL DEFAULT '',
		installed_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_addon_registry_status ON addon_registry(status);
	`
	_, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to init addon_registry schema: %w", err)
	}
	return nil
}

// GetAddon retrieves an addon record by name.
func (r *SQLiteRepository) GetAddon(ctx context.Context, name string) (*AddonRecord, error) {
	query := `
	SELECT name, version, description, author, source_url, status, capabilities, min_version, installed_at, updated_at
	FROM addon_registry
	WHERE name = ?
	`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	row := r.db.QueryRowContext(ctx, query, cleanName)

	var rec AddonRecord
	err := row.Scan(
		&rec.Name, &rec.Version, &rec.Description, &rec.Author, &rec.SourceURL,
		&rec.Status, &rec.Capabilities, &rec.MinVersion, &rec.InstalledAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get addon %q: %w", name, err)
	}
	return &rec, nil
}

// ListAddons returns all registered addons ordered by name ascending.
func (r *SQLiteRepository) ListAddons(ctx context.Context) ([]*AddonRecord, error) {
	query := `
	SELECT name, version, description, author, source_url, status, capabilities, min_version, installed_at, updated_at
	FROM addon_registry
	ORDER BY name ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query addons: %w", err)
	}
	defer rows.Close()

	var records []*AddonRecord
	for rows.Next() {
		var rec AddonRecord
		if err := rows.Scan(
			&rec.Name, &rec.Version, &rec.Description, &rec.Author, &rec.SourceURL,
			&rec.Status, &rec.Capabilities, &rec.MinVersion, &rec.InstalledAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan addon row: %w", err)
		}
		records = append(records, &rec)
	}
	return records, rows.Err()
}

// SaveAddon creates or updates an addon registration.
func (r *SQLiteRepository) SaveAddon(ctx context.Context, rec *AddonRecord) error {
	query := `
	INSERT INTO addon_registry (name, version, description, author, source_url, status, capabilities, min_version, installed_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		version = excluded.version,
		description = excluded.description,
		author = excluded.author,
		source_url = excluded.source_url,
		status = excluded.status,
		capabilities = excluded.capabilities,
		min_version = excluded.min_version,
		updated_at = excluded.updated_at
	`
	cleanName := strings.ToLower(strings.TrimSpace(rec.Name))
	now := time.Now().UTC()
	installed := now
	if !rec.InstalledAt.IsZero() {
		installed = rec.InstalledAt.UTC()
	}

	_, err := r.db.ExecContext(
		ctx, query,
		cleanName, rec.Version, rec.Description, rec.Author, rec.SourceURL,
		rec.Status, rec.Capabilities, rec.MinVersion, installed, now,
	)
	if err != nil {
		return fmt.Errorf("failed to save addon %q: %w", cleanName, err)
	}
	return nil
}

// DeleteAddon removes an addon from the registry.
func (r *SQLiteRepository) DeleteAddon(ctx context.Context, name string) error {
	query := `DELETE FROM addon_registry WHERE name = ?`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if _, err := r.db.ExecContext(ctx, query, cleanName); err != nil {
		return fmt.Errorf("failed to delete addon %q: %w", cleanName, err)
	}
	return nil
}

// SetAddonStatus updates the active/disabled status of an addon.
func (r *SQLiteRepository) SetAddonStatus(ctx context.Context, name, status string) error {
	query := `UPDATE addon_registry SET status = ?, updated_at = ? WHERE name = ?`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, query, status, now, cleanName)
	if err != nil {
		return fmt.Errorf("failed to update addon status %q: %w", cleanName, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("addon not found: %s", cleanName)
	}
	return nil
}
