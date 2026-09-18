package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a sql.DB connection with custom repository methods and query metrics.
type DB struct {
	*sql.DB
	metricsMu sync.RWMutex
	metrics   DBMetrics
}

// SetMetrics attaches a DBMetrics observer to this database handle.
func (db *DB) SetMetrics(m DBMetrics) {
	if db == nil {
		return
	}
	if m == nil {
		m = NoopDBMetrics{}
	}
	db.metricsMu.Lock()
	defer db.metricsMu.Unlock()
	db.metrics = m
}

// Metrics returns the active DBMetrics observer, or NoopDBMetrics if unset.
func (db *DB) Metrics() DBMetrics {
	if db == nil {
		return NoopDBMetrics{}
	}
	db.metricsMu.RLock()
	defer db.metricsMu.RUnlock()
	if db.metrics != nil {
		return db.metrics
	}
	return NoopDBMetrics{}
}

// Observe records execution duration and error for a database operation label.
func (db *DB) Observe(operation string, elapsed time.Duration, err error) {
	if db == nil {
		return
	}
	db.metricsMu.RLock()
	m := db.metrics
	db.metricsMu.RUnlock()
	if m != nil {
		m.Observe(operation, elapsed, err)
	}
}

const sqliteConnectionPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"

func sqlitePoolLimits(cpuCount int) (maxOpen, maxIdle int) {
	if cpuCount < 1 {
		cpuCount = 1
	}
	// SQLite/WAL benefits from a handful of concurrent readers, but writers are
	// still serialized. Keep pool growth bounded instead of retaining O(CPU)
	// idle connections on large hosts.
	maxOpen = min(max(4, cpuCount), 8)
	maxIdle = min(max(2, maxOpen/2), 4)
	return maxOpen, maxIdle
}

func sqliteOpenDSN(dsn string, inMemory bool) string {
	// Keep the special :memory: sentinel untouched. It is pinned to one physical
	// connection below, so startup PRAGMAs remain connection-complete there.
	if dsn == ":memory:" {
		return dsn
	}

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	configured := dsn + separator + sqliteConnectionPragmas
	if !inMemory {
		// WAL is persisted at database level but is still applied on connection
		// creation so every pooled handle observes the intended journal policy.
		configured += "&_pragma=journal_mode(WAL)"
	}
	return configured
}

// Open initializes SQLite database at the specified path and runs initial migrations.
// If dsn is ":memory:", an in-memory database will be used (useful for tests).
func Open(dsn string) (*DB, error) {
	if dsn == "" {
		dsn = "data/goultroid.db"
	}

	inMemory := dsn == ":memory:" || strings.Contains(dsn, "mode=memory")
	if !inMemory {
		dir := filepath.Dir(dsn)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create db directory: %w", err)
		}
	}

	openDSN := sqliteOpenDSN(dsn, inMemory)
	db, err := sql.Open("sqlite", openDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure pool for SQLite.
	if inMemory {
		// Keep one physical connection so private in-memory databases retain their
		// schema and connection-local PRAGMAs for the lifetime of the DB handle.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
	} else {
		// WAL supports concurrent readers while SQLite still serializes writers.
		// Bound physical/idle handles so large CPU counts do not inflate steady RSS.
		maxOpen, maxIdle := sqlitePoolLimits(runtime.NumCPU())
		db.SetMaxOpenConns(maxOpen)
		db.SetMaxIdleConns(maxIdle)
		db.SetConnMaxLifetime(time.Hour)
	}

	if dsn == ":memory:" {
		// The special sentinel cannot safely carry URI query parameters, but it is
		// pinned to one physical connection, so applying the PRAGMAs once is enough.
		pragmas := []string{
			"PRAGMA busy_timeout = 5000;",
			"PRAGMA foreign_keys = ON;",
			"PRAGMA synchronous = NORMAL;",
		}
		for _, pragma := range pragmas {
			if _, err := db.Exec(pragma); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
			}
		}
	}

	instance := &DB{DB: db}
	if err := instance.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return instance, nil
}
