package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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

// GetAddon retrieves an addon record by name.
func (d *DB) GetAddon(ctx context.Context, name string) (*AddonRecord, error) {
	query := `
	SELECT name, version, description, author, source_url, status, capabilities, min_version, installed_at, updated_at
	FROM addon_registry
	WHERE name = ?
	`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	row := d.QueryRowContext(ctx, query, cleanName)

	var r AddonRecord
	err := row.Scan(
		&r.Name, &r.Version, &r.Description, &r.Author, &r.SourceURL,
		&r.Status, &r.Capabilities, &r.MinVersion, &r.InstalledAt, &r.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get addon %q: %w", name, err)
	}
	return &r, nil
}

// ListAddons returns all registered addons ordered by name ascending.
func (d *DB) ListAddons(ctx context.Context) ([]*AddonRecord, error) {
	query := `
	SELECT name, version, description, author, source_url, status, capabilities, min_version, installed_at, updated_at
	FROM addon_registry
	ORDER BY name ASC
	`
	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query addons: %w", err)
	}
	defer rows.Close()

	var records []*AddonRecord
	for rows.Next() {
		var r AddonRecord
		if err := rows.Scan(
			&r.Name, &r.Version, &r.Description, &r.Author, &r.SourceURL,
			&r.Status, &r.Capabilities, &r.MinVersion, &r.InstalledAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan addon row: %w", err)
		}
		records = append(records, &r)
	}
	return records, rows.Err()
}

// SaveAddon creates or updates an addon registration.
func (d *DB) SaveAddon(ctx context.Context, rec *AddonRecord) error {
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

	_, err := d.ExecContext(
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
func (d *DB) DeleteAddon(ctx context.Context, name string) error {
	query := `DELETE FROM addon_registry WHERE name = ?`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if _, err := d.ExecContext(ctx, query, cleanName); err != nil {
		return fmt.Errorf("failed to delete addon %q: %w", cleanName, err)
	}
	return nil
}

// SetAddonStatus updates the active/disabled status of an addon.
func (d *DB) SetAddonStatus(ctx context.Context, name, status string) error {
	query := `UPDATE addon_registry SET status = ?, updated_at = ? WHERE name = ?`
	cleanName := strings.ToLower(strings.TrimSpace(name))
	now := time.Now().UTC()
	res, err := d.ExecContext(ctx, query, status, now, cleanName)
	if err != nil {
		return fmt.Errorf("failed to update addon status %q: %w", cleanName, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("addon not found: %s", cleanName)
	}
	return nil
}
