package sudo

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestSQLiteRepository_Operations(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	repo := NewSQLiteRepository(db)

	// 1. Initial list should be empty
	users, err := repo.GetSudoUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 sudo users, got %d", len(users))
	}

	// 2. Add sudo user
	if err := repo.AddSudoUser(ctx, 12345, 99999); err != nil {
		t.Fatalf("failed to add sudo user: %v", err)
	}
	if err := repo.AddSudoUser(ctx, 67890, 99999); err != nil {
		t.Fatalf("failed to add second sudo user: %v", err)
	}

	// 3. Verify list
	users, err = repo.GetSudoUsers(ctx)
	if err != nil {
		t.Fatalf("failed to get sudo users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 sudo users, got %d", len(users))
	}
	if users[0].UserID != 12345 || users[0].AddedBy != 99999 {
		t.Errorf("unexpected user 0: %+v", users[0])
	}

	// 4. Check existence
	isSudo, err := repo.IsSudoUser(ctx, 12345)
	if err != nil || !isSudo {
		t.Errorf("expected 12345 to be sudo, got %v (err=%v)", isSudo, err)
	}
	isSudo, err = repo.IsSudoUser(ctx, 11111)
	if err != nil || isSudo {
		t.Errorf("expected 11111 to NOT be sudo, got %v (err=%v)", isSudo, err)
	}

	// 5. Update existing sudo user
	if err := repo.AddSudoUser(ctx, 12345, 88888); err != nil {
		t.Fatalf("failed to update sudo user: %v", err)
	}
	users, _ = repo.GetSudoUsers(ctx)
	if len(users) != 2 {
		t.Fatalf("expected 2 sudo users, got %d", len(users))
	}
	found := false
	for _, u := range users {
		if u.UserID == 12345 {
			found = true
			if u.AddedBy != 88888 {
				t.Errorf("expected updated added_by 88888, got %d", u.AddedBy)
			}
		}
	}
	if !found {
		t.Errorf("expected user 12345 in sudo users list")
	}

	// 6. Remove sudo user
	if err := repo.RemoveSudoUser(ctx, 12345); err != nil {
		t.Fatalf("failed to remove sudo user: %v", err)
	}
	isSudo, _ = repo.IsSudoUser(ctx, 12345)
	if isSudo {
		t.Errorf("expected 12345 to be removed")
	}

	// 7. Remove non-existent returns error
	if err := repo.RemoveSudoUser(ctx, 99999); err == nil {
		t.Errorf("expected error when removing non-existent user")
	}
}
