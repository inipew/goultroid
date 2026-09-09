package userlog_test

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/userlog"
)

func TestSQLiteRepository(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	repo := userlog.NewSQLiteRepository(db)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	val, err := repo.GetUserLogSetting(ctx, "log_chat_id")
	if err != nil {
		t.Fatalf("GetUserLogSetting failed: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty value for unset key, got %q", val)
	}

	if err := repo.SetUserLogSetting(ctx, "log_chat_id", "-100123456789"); err != nil {
		t.Fatalf("SetUserLogSetting failed: %v", err)
	}

	val, err = repo.GetUserLogSetting(ctx, "log_chat_id")
	if err != nil {
		t.Fatalf("GetUserLogSetting failed: %v", err)
	}
	if val != "-100123456789" {
		t.Fatalf("expected -100123456789, got %q", val)
	}

	// Update existing key
	if err := repo.SetUserLogSetting(ctx, "log_chat_id", "-100987654321"); err != nil {
		t.Fatalf("SetUserLogSetting update failed: %v", err)
	}
	val, err = repo.GetUserLogSetting(ctx, "log_chat_id")
	if err != nil {
		t.Fatalf("GetUserLogSetting failed: %v", err)
	}
	if val != "-100987654321" {
		t.Fatalf("expected -100987654321, got %q", val)
	}
}
