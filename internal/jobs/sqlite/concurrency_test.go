package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
)

func TestStoreConcurrentPrepareWithProductionSQLite(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := InitSchema(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db.DB)
	if err := s.SaveDefinition(ctx, &jobs.JobDefinition{ID: "job", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key"}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.PrepareAttemptLease(ctx, "occ", fmt.Sprintf("task-%d", i), time.Minute)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrOccurrenceNotReady) {
			t.Errorf("unexpected contention result: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("granted %d attempts", successes)
	}
}
