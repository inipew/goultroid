package database

import (
	"context"
	"testing"
)

type testFeatureMigration struct {
	id       string
	checksum string
	up       func(context.Context, SQLExecutor) error
	legacy   []int
}

func (m testFeatureMigration) ID() string                                   { return m.id }
func (m testFeatureMigration) Description() string                          { return "test migration" }
func (m testFeatureMigration) Checksum() string                             { return m.checksum }
func (m testFeatureMigration) LegacyVersions() []int                        { return m.legacy }
func (m testFeatureMigration) Up(ctx context.Context, tx SQLExecutor) error { return m.up(ctx, tx) }

type testFeatureProvider struct{ migrations []Migration }

func (p testFeatureProvider) Migrations() []Migration { return p.migrations }

func TestRunFeatureMigrationsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migration := testFeatureMigration{
		id:       "test.001",
		checksum: "checksum-v1",
		up: func(ctx context.Context, tx SQLExecutor) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE test_feature (id INTEGER PRIMARY KEY)`)
			return err
		},
	}
	provider := testFeatureProvider{migrations: []Migration{migration}}

	if err := RunFeatureMigrations(ctx, db, provider); err != nil {
		t.Fatal(err)
	}
	if err := RunFeatureMigrations(ctx, db, provider); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'test.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one migration record, got %d", count)
	}
}

func TestRunFeatureMigrationsRejectsChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := testFeatureMigration{
		id:       "test.001",
		checksum: "checksum-v1",
		up: func(ctx context.Context, tx SQLExecutor) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE test_feature (id INTEGER PRIMARY KEY)`)
			return err
		},
	}
	provider := testFeatureProvider{migrations: []Migration{first}}
	if err := RunFeatureMigrations(ctx, db, provider); err != nil {
		t.Fatal(err)
	}

	changed := first
	changed.checksum = "checksum-v2"
	if err := RunFeatureMigrations(ctx, db, testFeatureProvider{migrations: []Migration{changed}}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestRunFeatureMigrationsRollsBackFailedMigration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migration := testFeatureMigration{
		id:       "test.001",
		checksum: "checksum-v1",
		up: func(ctx context.Context, tx SQLExecutor) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE should_rollback (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return context.Canceled
		},
	}
	if err := RunFeatureMigrations(ctx, db, testFeatureProvider{migrations: []Migration{migration}}); err == nil {
		t.Fatal("expected migration failure")
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed feature migration left schema changes behind")
	}
}
