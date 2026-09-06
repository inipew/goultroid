package database_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

func TestClaimDueScheduledJobs_ConcurrentWorkersClaimEachJobOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	db1, err := database.Open(path)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()
	db2, err := database.Open(path)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	const jobCount = 100
	for i := 0; i < jobCount; i++ {
		_, err := db1.CreateScheduledJob(ctx, &database.ScheduledJob{
			ChatID: int64(i + 1), PeerType: "chat", ActionType: "message",
			Payload: "concurrency-test", NextRunAt: now.Add(-time.Second),
			CreatedAt: now, Status: database.JobStatusPending, MaxAttempts: 3,
		})
		if err != nil {
			t.Fatalf("create job %d: %v", i, err)
		}
	}

	type claimResult struct {
		jobs []database.ScheduledJob
		err  error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, db := range []*database.DB{db1, db2} {
		db := db
		wg.Add(1)
		go func() {
			defer wg.Done()
			var claimed []database.ScheduledJob
			for {
				jobs, err := db.ClaimDueScheduledJobs(ctx, time.Now().UTC(), 10, 90*time.Second)
				if err != nil {
					results <- claimResult{err: err}
					return
				}
				if len(jobs) == 0 {
					break
				}
				claimed = append(claimed, jobs...)
			}
			results <- claimResult{jobs: claimed}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[int64]int)
	total := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent claim failed: %v", result.err)
		}
		for _, job := range result.jobs {
			seen[job.ID]++
			total++
		}
	}
	if total != jobCount {
		t.Fatalf("expected %d total claims, got %d", jobCount, total)
	}
	if len(seen) != jobCount {
		t.Fatalf("expected %d unique claims, got %d", jobCount, len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("job %d was claimed %d times", id, count)
		}
	}
}
