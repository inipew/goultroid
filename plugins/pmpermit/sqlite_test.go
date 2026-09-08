package pmpermit

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

func TestPMPermitOperations(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database.Open failed: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	repo := NewSQLiteRepository(db)

	// 1. Initially non-existent
	rec, err := repo.GetPMRecord(ctx, 11111)
	if err != nil {
		t.Fatalf("GetPMRecord failed: %v", err)
	}
	if rec != nil {
		t.Fatalf("expected nil record initially, got %+v", rec)
	}

	// 2. Set status to approved
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	if err := repo.SetPMStatus(ctx, 11111, "approved", "trusted contact", &exp); err != nil {
		t.Fatalf("SetPMStatus failed: %v", err)
	}

	rec, err = repo.GetPMRecord(ctx, 11111)
	if err != nil || rec == nil {
		t.Fatalf("expected record, got err=%v, rec=%+v", err, rec)
	}
	if rec.Status != "approved" || rec.Reason != "trusted contact" {
		t.Errorf("unexpected record data: %+v", rec)
	}

	// 3. Increment warnings
	w1, err := repo.IncrementPMWarn(ctx, 22222)
	if err != nil || w1 != 1 {
		t.Errorf("expected warn count 1, got %d (err=%v)", w1, err)
	}
	w2, err := repo.IncrementPMWarn(ctx, 22222)
	if err != nil || w2 != 2 {
		t.Errorf("expected warn count 2, got %d (err=%v)", w2, err)
	}

	rec2, err := repo.GetPMRecord(ctx, 22222)
	if err != nil || rec2 == nil || rec2.WarnCount != 2 {
		t.Errorf("unexpected record 2: %+v (err=%v)", rec2, err)
	}

	// 4. Reset warnings
	if err := repo.ResetPMWarn(ctx, 22222); err != nil {
		t.Fatalf("ResetPMWarn failed: %v", err)
	}
	rec2, _ = repo.GetPMRecord(ctx, 22222)
	if rec2.WarnCount != 0 {
		t.Errorf("expected warn count 0 after reset, got %d", rec2.WarnCount)
	}

	// 5. Warn message IDs
	if err := repo.AddWarnMsgID(ctx, 22222, 101); err != nil {
		t.Fatalf("AddWarnMsgID failed: %v", err)
	}
	if err := repo.AddWarnMsgID(ctx, 22222, 102); err != nil {
		t.Fatalf("AddWarnMsgID failed: %v", err)
	}
	ids, err := repo.GetWarnMsgIDs(ctx, 22222)
	if err != nil || len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Fatalf("unexpected warn msg ids: %v (err=%v)", ids, err)
	}
	if err := repo.ClearWarnMsgIDs(ctx, 22222); err != nil {
		t.Fatalf("ClearWarnMsgIDs failed: %v", err)
	}
	ids, err = repo.GetWarnMsgIDs(ctx, 22222)
	if err != nil || len(ids) != 0 {
		t.Fatalf("expected empty warn msg ids after clear, got: %v", ids)
	}

	// 6. List and Stats
	pending, approved, blocked, err := repo.CountPMRecords(ctx)
	if err != nil {
		t.Fatalf("CountPMRecords failed: %v", err)
	}
	if approved != 1 || pending != 1 || blocked != 0 {
		t.Errorf("unexpected stats: pending=%d, approved=%d, blocked=%d", pending, approved, blocked)
	}
}
