package sudo

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestSudoFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run sudo feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.AddSudoUser(ctx, 12345, 1); err != nil {
		t.Fatalf("add sudo user: %v", err)
	}

	isSudo, err := repo.IsSudoUser(ctx, 12345)
	if err != nil {
		t.Fatalf("is sudo user: %v", err)
	}
	if !isSudo {
		t.Fatal("expected user to be sudo")
	}

	users, err := repo.GetSudoUsers(ctx)
	if err != nil {
		t.Fatalf("get sudo users: %v", err)
	}
	if len(users) != 1 || users[0].UserID != 12345 {
		t.Fatalf("unexpected users: %#v", users)
	}

	if err := repo.RemoveSudoUser(ctx, 12345); err != nil {
		t.Fatalf("remove sudo user: %v", err)
	}
}

func TestSudoFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates sudo_users table already
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt sudo legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'sudo.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
