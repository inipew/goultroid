package database

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FeatureMigration is a durable, namespaced schema migration owned by a feature.
// LegacyVersions identifies old integer migrations equivalent to this migration.
type FeatureMigration interface {
	Migration
	Checksum() string
	LegacyVersions() []int
}

// RunFeatureMigrations applies feature-owned migrations deterministically.
// Existing integer migrations are immutable history; matching legacy versions
// are adopted instead of executing equivalent SQL twice.
func RunFeatureMigrations(ctx context.Context, db *DB, providers ...MigrationProvider) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS feature_schema_migrations (
			id TEXT PRIMARY KEY,
			description TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at DATETIME NOT NULL
		);`); err != nil {
		return fmt.Errorf("failed to create feature_schema_migrations table: %w", err)
	}

	applied, err := loadFeatureMigrations(ctx, db)
	if err != nil {
		return err
	}

	migrations := make([]FeatureMigration, 0)
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		for _, raw := range provider.Migrations() {
			migration, ok := raw.(FeatureMigration)
			if !ok || migration == nil {
				return fmt.Errorf("migration %T does not implement FeatureMigration", raw)
			}
			migrations = append(migrations, migration)
		}
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].ID() < migrations[j].ID() })

	seen := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		id := strings.TrimSpace(migration.ID())
		if id == "" {
			return fmt.Errorf("feature migration has empty ID")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate feature migration ID %q", id)
		}
		seen[id] = struct{}{}

		expected := migration.Checksum()
		if expected == "" {
			return fmt.Errorf("feature migration %q has empty checksum", id)
		}
		if saved, exists := applied[id]; exists {
			if saved != expected {
				return fmt.Errorf("feature migration checksum mismatch for %s: recorded %s, calculated %s", id, saved, expected)
			}
			continue
		}
		legacyApplied, err := anyLegacyVersionApplied(ctx, db, migration.LegacyVersions())
		if err != nil {
			return fmt.Errorf("check legacy adoption for %s: %w", id, err)
		}
		if legacyApplied {
			if err := recordFeatureMigration(ctx, db, migration, expected, "legacy adoption"); err != nil {
				return fmt.Errorf("adopt feature migration %s: %w", id, err)
			}
			applied[id] = expected
			continue
		}
		if err := applyFeatureMigration(ctx, db, migration, expected); err != nil {
			return fmt.Errorf("failed to apply feature migration %s (%s): %w", id, migration.Description(), err)
		}
		applied[id] = expected
	}
	return nil
}

func loadFeatureMigrations(ctx context.Context, db *DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, checksum FROM feature_schema_migrations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query feature_schema_migrations: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var id, checksum string
		if err := rows.Scan(&id, &checksum); err != nil {
			return nil, fmt.Errorf("failed to scan feature migration: %w", err)
		}
		result[id] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate feature migrations: %w", err)
	}
	return result, nil
}

func anyLegacyVersionApplied(ctx context.Context, db *DB, versions []int) (bool, error) {
	for _, version := range versions {
		if version <= 0 {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func applyFeatureMigration(ctx context.Context, db *DB, migration FeatureMigration, checksum string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := migration.Up(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO feature_schema_migrations (id, description, checksum, applied_at)
		VALUES (?, ?, ?, ?)`, migration.ID(), migration.Description(), checksum, time.Now().UTC()); err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}
	return tx.Commit()
}

func recordFeatureMigration(ctx context.Context, db *DB, migration FeatureMigration, checksum, reason string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO feature_schema_migrations (id, description, checksum, applied_at)
		VALUES (?, ?, ?, ?)`, migration.ID(), migration.Description()+" ("+reason+")", checksum, time.Now().UTC())
	return err
}
