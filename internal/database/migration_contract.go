package database

import "context"

// Migration is the feature-owned schema contract consumed by the migration
// runner. New IDs must be namespaced by feature, for example clone.001.
type Migration interface {
	ID() string
	Description() string
	Up(context.Context, *DB) error
}

// MigrationProvider is implemented by persistent feature modules. The returned
// list must be deterministic and immutable for a released version.
type MigrationProvider interface {
	Migrations() []Migration
}
