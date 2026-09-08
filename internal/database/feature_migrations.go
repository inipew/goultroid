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

// SchemaInvariantMigration is an optional interface for feature migrations that provides
// physical verification of required database schema objects during legacy adoption.
type SchemaInvariantMigration interface {
	FeatureMigration
	VerifySchema(ctx context.Context, db SQLExecutor) error
}

// RunFeatureMigrations applies feature-owned migrations deterministically.
// Existing integer migrations are immutable history; matching legacy versions
// are adopted only after verifying checksums and schema invariants instead of executing equivalent SQL twice.
func RunFeatureMigrations(ctx context.Context, db *DB, providers ...MigrationProvider) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if ctx == nil {
		return fmt.Errorf("context is nil")
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
			if raw == nil {
				return fmt.Errorf("migration provider returned nil migration")
			}
			migration, ok := raw.(FeatureMigration)
			if !ok || migration == nil {
				return fmt.Errorf("migration %T does not implement FeatureMigration", raw)
			}
			migrations = append(migrations, migration)
		}
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].ID() < migrations[j].ID() })

	seen := make(map[string]struct{}, len(migrations))
	legacyOwners := make(map[int]string)
	for _, migration := range migrations {
		id := strings.TrimSpace(migration.ID())
		if id == "" {
			return fmt.Errorf("feature migration has empty ID")
		}
		if id != migration.ID() {
			return fmt.Errorf("feature migration ID %q contains leading/trailing whitespace", migration.ID())
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate feature migration ID %q", id)
		}
		seen[id] = struct{}{}

		if strings.TrimSpace(migration.Description()) == "" {
			return fmt.Errorf("feature migration %q has empty description", id)
		}
		expected := migration.Checksum()
		if expected == "" {
			return fmt.Errorf("feature migration %q has empty checksum", id)
		}

		seenLegacy := make(map[int]struct{})
		for _, version := range migration.LegacyVersions() {
			if version <= 0 {
				return fmt.Errorf("feature migration %q declares invalid legacy version %d", id, version)
			}
			if _, exists := seenLegacy[version]; exists {
				return fmt.Errorf("feature migration %q declares duplicate legacy version %d", id, version)
			}
			seenLegacy[version] = struct{}{}
			if owner, exists := legacyOwners[version]; exists && owner != id {
				return fmt.Errorf("legacy migration version %d is claimed by both %q and %q", version, owner, id)
			}
			legacyOwners[version] = id
		}

		if saved, exists := applied[id]; exists {
			if saved != expected {
				return fmt.Errorf("feature migration checksum mismatch for %s: recorded %s, calculated %s", id, saved, expected)
			}
			continue
		}
		legacyApplied, legacyVer, err := verifyLegacyAdoption(ctx, db, migration.LegacyVersions())
		if err != nil {
			return fmt.Errorf("check legacy adoption for %s: %w", id, err)
		}
		if legacyApplied {
			if err := adoptFeatureMigration(ctx, db, migration, expected, legacyVer); err != nil {
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
		if strings.TrimSpace(id) == "" || checksum == "" {
			return nil, fmt.Errorf("feature_schema_migrations contains invalid record id=%q", id)
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("feature_schema_migrations contains duplicate id %q", id)
		}
		result[id] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate feature migrations: %w", err)
	}
	return result, nil
}

func verifyLegacyAdoption(ctx context.Context, db *DB, versions []int) (bool, int, error) {
	for _, version := range versions {
		var recordedChecksum string
		err := db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = ?`, version).Scan(&recordedChecksum)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return false, 0, fmt.Errorf("read legacy migration version %d: %w", version, err)
		}

		legacyInfo, found := LookupLegacyMigration(version)
		if !found {
			return false, 0, fmt.Errorf("unknown legacy migration version %d claimed by feature migration", version)
		}

		matched := recordedChecksum == legacyInfo.CanonicalChecksum
		if !matched {
			for _, alt := range legacyInfo.LegacyChecksums {
				if recordedChecksum == alt {
					matched = true
					break
				}
			}
		}

		if !matched {
			return false, 0, fmt.Errorf("legacy migration checksum mismatch for version %d (%s): recorded %q, expected %q",
				version, legacyInfo.Description, recordedChecksum, legacyInfo.CanonicalChecksum)
		}

		return true, version, nil
	}
	return false, 0, nil
}

func adoptFeatureMigration(ctx context.Context, db *DB, migration FeatureMigration, checksum string, legacyVer int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if checker, ok := migration.(SchemaInvariantMigration); ok {
		if err := checker.VerifySchema(ctx, tx); err != nil {
			return fmt.Errorf("schema invariant check failed: %w", err)
		}
	}

	note := fmt.Sprintf("legacy adoption: v%d", legacyVer)
	desc := fmt.Sprintf("%s (%s)", migration.Description(), note)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO feature_schema_migrations (id, description, checksum, applied_at)
		VALUES (?, ?, ?, ?)`, migration.ID(), desc, checksum, time.Now().UTC()); err != nil {
		return fmt.Errorf("record adopted migration: %w", err)
	}

	return tx.Commit()
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
