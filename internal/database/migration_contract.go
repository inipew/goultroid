package database

import (
	"context"
	"database/sql"
)

// SQLExecutor is the minimal transactional SQL surface exposed to migrations.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Migration is the feature-owned schema contract consumed by the migration runner.
type Migration interface {
	ID() string
	Description() string
	Up(context.Context, SQLExecutor) error
}

// MigrationProvider is implemented by persistent feature modules. The returned
// list must be deterministic and immutable for a released version.
type MigrationProvider interface {
	Migrations() []Migration
}
