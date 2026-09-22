package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/moderation"
)

func TestP7IWarningRepositoryBoundsDirectCallers(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteWarningRepository(db)
	ctx := context.Background()

	err = repo.AddWarning(
		ctx,
		100,
		200,
		strings.Repeat("x", moderation.MaxWarningReasonBytes+1),
		999,
	)
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("oversized reason error=%v, want ErrInvalidArgs", err)
	}

	const extra = 8
	start := make(chan struct{})
	errs := make(chan error, moderation.MaxWarningThreshold+extra)
	var wg sync.WaitGroup
	for i := 0; i < moderation.MaxWarningThreshold+extra; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- repo.AddWarning(
				ctx,
				100,
				200,
				fmt.Sprintf("reason-%02d", i),
				999,
			)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	success := 0
	limited := 0
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, core.ErrResourceLimit):
			limited++
		default:
			t.Fatalf("unexpected concurrent AddWarning error: %v", err)
		}
	}
	if success != moderation.MaxWarningThreshold || limited != extra {
		t.Fatalf("success/limited=%d/%d, want %d/%d",
			success, limited, moderation.MaxWarningThreshold, extra)
	}

	count, err := repo.GetWarningCount(ctx, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != moderation.MaxWarningThreshold {
		t.Fatalf("warning count=%d, want %d", count, moderation.MaxWarningThreshold)
	}

	records, err := repo.GetWarnings(ctx, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != moderation.MaxWarningThreshold {
		t.Fatalf("GetWarnings returned %d rows, want bounded %d",
			len(records), moderation.MaxWarningThreshold)
	}
}
