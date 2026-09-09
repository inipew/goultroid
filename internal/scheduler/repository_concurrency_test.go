package scheduler_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/scheduler"
	_ "modernc.org/sqlite"
)

func TestClaimDueScheduledJobs_ConcurrentWorkersClaimEachJobOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	repo1 := scheduler.NewSQLiteRepository(db1)
	if err := repo1.InitSchema(context.Background()); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	repo2 := scheduler.NewSQLiteRepository(db2)

	ctx := context.Background()
	now := time.Now().UTC()
	const jobCount = 100
	for i := 0; i < jobCount; i++ {
		_, err := repo1.CreateScheduledJob(ctx, &scheduler.ScheduledJob{
			ChatID: int64(i + 1), PeerType: "chat", ActionType: "message",
			Payload: "concurrency-test", NextRunAt: now.Add(-time.Second),
			CreatedAt: now, Status: scheduler.JobStatusPending, MaxAttempts: 3,
		})
		if err != nil {
			t.Fatalf("create job %d: %v", i, err)
		}
	}

	type claimResult struct {
		jobs []scheduler.ScheduledJob
		err  error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, repo := range []*scheduler.SQLiteRepository{repo1, repo2} {
		repo := repo
		wg.Add(1)
		go func() {
			defer wg.Done()
			var claimed []scheduler.ScheduledJob
			for {
				jobs, err := repo.ClaimDueScheduledJobs(ctx, time.Now().UTC(), 10, 90*time.Second)
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

	claimedSet := make(map[int64]string)
	totalClaimed := 0
	for res := range results {
		if res.err != nil {
			t.Fatalf("concurrent claim worker failed: %v", res.err)
		}
		for _, j := range res.jobs {
			totalClaimed++
			if prevToken, exists := claimedSet[j.ID]; exists {
				t.Fatalf("duplicate claim detected on job %d! prev token %s, current token %s", j.ID, prevToken, j.ClaimToken)
			}
			if j.ClaimToken == "" {
				t.Fatalf("claimed job %d has empty claim_token", j.ID)
			}
			claimedSet[j.ID] = j.ClaimToken
		}
	}

	if totalClaimed != jobCount {
		t.Fatalf("expected all %d jobs to be claimed, got %d", jobCount, totalClaimed)
	}
}
