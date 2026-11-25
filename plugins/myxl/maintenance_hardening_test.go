package myxl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/network"
)

func TestM7_SQLiteContention_ConfigRunsAndTokens(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "m7_contention.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewSQLiteRepository(db)

	if err := repo.Save(ctx, &Account{
		MSISDN:         "6281900000001",
		IsActive:       true,
		RefreshToken:   "rt-init",
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 6
	const iters = 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*iters*2)

	for w := 0; w < workers; w++ {
		workerID := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if workerID%2 == 0 {
					cfg := DefaultTokenMaintenanceConfig()
					cfg.Enabled = i%2 == 0
					cfg.Interval = time.Duration(1+((workerID+i)%12)) * time.Hour
					if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
						errs <- fmt.Errorf("save config: %w", err)
						return
					}
					if _, err := repo.GetTokenMaintenanceConfig(ctx); err != nil {
						errs <- fmt.Errorf("get config: %w", err)
						return
					}
				} else {
					run := &MaintenanceRunSummary{
						TriggerSource:  "scheduled",
						Status:         RunStatusCompleted,
						StartedAt:      time.Now().UTC(),
						CompletedAt:    time.Now().UTC(),
						Duration:       10 * time.Millisecond,
						TotalAccounts:  1,
						RefreshedCount: 1,
					}
					if err := repo.RecordTokenMaintenanceRun(ctx, run); err != nil {
						errs <- fmt.Errorf("record run: %w", err)
						return
					}
					if _, err := repo.ListTokenMaintenanceRuns(ctx, MaxMaintenanceRunHistory); err != nil {
						errs <- fmt.Errorf("list runs: %w", err)
						return
					}
				}
				if err := repo.Save(ctx, &Account{
					MSISDN:         "6281900000001",
					IsActive:       true,
					AccessToken:    fmt.Sprintf("at-%d-%d", workerID, i),
					RefreshToken:   fmt.Sprintf("rt-%d-%d", workerID, i),
					TokenExpiresAt: time.Now().UTC().Add(time.Hour),
				}); err != nil {
					errs <- fmt.Errorf("save account: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	runs, err := repo.ListTokenMaintenanceRuns(ctx, 50)
	if err != nil {
		t.Fatalf("final list runs: %v", err)
	}
	if len(runs) > MaxMaintenanceRunHistory {
		t.Fatalf("history exceeded bound %d: got %d", MaxMaintenanceRunHistory, len(runs))
	}

	// Cancelled context on DB read fails cleanly
	cCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.GetTokenMaintenanceConfig(cCtx); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestM7_TokenRotationAndSharedSingleflightAcrossNormalAndMaintenance(t *testing.T) {
	ctx := context.Background()
	var rotation atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rotation.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  fmt.Sprintf("at-rot-%d", n),
			IDToken:      fmt.Sprintf("id-rot-%d", n),
			RefreshToken: fmt.Sprintf("rt-rot-%d", n),
			ExpiresIn:    1800,
		})
	}))
	defer srv.Close()

	_, repo := openMyXLTestDB(t)
	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-rot"))

	acc := &Account{
		MSISDN:         "6281900000009",
		IsActive:       true,
		AccessToken:    "at-0",
		IDToken:        "id-0",
		RefreshToken:   "rt-0",
		TokenExpiresAt: time.Now().UTC().Add(10 * time.Minute), // within 30m window, outside 2m skew
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}

	// Cycle 1: rotates tokens
	summary, _, err := runTokenMaintenanceCycle(ctx, repo, client, DefaultTokenMaintenanceConfig(), "scheduled")
	if err != nil || summary.RefreshedCount != 1 {
		t.Fatalf("cycle 1: summary=%+v err=%v", summary, err)
	}
	rot1, err := repo.GetByMSISDN(ctx, acc.MSISDN)
	if err != nil || rot1.AccessToken != "at-rot-1" || rot1.IDToken != "id-rot-1" || rot1.RefreshToken != "rt-rot-1" {
		t.Fatalf("unexpected rotated tokens after cycle 1: %+v (err=%v)", rot1, err)
	}

	// Cycle 2: expiry is now 30m (1800s), which is within 30m window boundary or if we set 20m -> rotates again to rot-2
	rot1.TokenExpiresAt = time.Now().UTC().Add(20 * time.Minute)
	if err := repo.Save(ctx, rot1); err != nil {
		t.Fatal(err)
	}
	summary2, _, err := runTokenMaintenanceCycle(ctx, repo, client, DefaultTokenMaintenanceConfig(), "scheduled")
	if err != nil || summary2.RefreshedCount != 1 {
		t.Fatalf("cycle 2: summary=%+v err=%v", summary2, err)
	}
	rot2, err := repo.GetByMSISDN(ctx, acc.MSISDN)
	if err != nil || rot2.AccessToken != "at-rot-2" || rot2.RefreshToken != "rt-rot-2" {
		t.Fatalf("unexpected rotated tokens after cycle 2: %+v (err=%v)", rot2, err)
	}

	// Cycle 3: concurrent EnsureFreshToken + EnsureFreshTokenWithin share singleflight
	rot2.TokenExpiresAt = time.Now().UTC().Add(time.Minute) // within both 2m skew and 30m window
	if err := repo.Save(ctx, rot2); err != nil {
		t.Fatal(err)
	}
	beforeHits := rotation.Load()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			clone := *rot2
			if idx%2 == 0 {
				_ = client.EnsureFreshToken(ctx, &clone)
			} else {
				_ = client.EnsureFreshTokenWithin(ctx, &clone, 30*time.Minute)
			}
		}(i)
	}
	wg.Wait()
	if got := rotation.Load() - beforeHits; got != 1 {
		t.Fatalf("expected 1 singleflight CIAM call across mixed callers, got %d", got)
	}
}

func TestM7_ConcurrentTriggerGuardAndConfigChangeDuringRun(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "at-done",
			IDToken:      "id-done",
			RefreshToken: "rt-done",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "m7_concurrent.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, srv.URL)

	if err := repo.Save(ctx, &Account{
		MSISDN:         "6281911111111",
		IsActive:       true,
		RefreshToken:   "rt-init",
		TokenExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	baseGoroutines := runtime.NumGoroutine()

	jobDone := make(chan error, 1)
	go func() {
		jobDone <- p.handleTokenMaintenanceJob(ctx, jobs.JobDefinition{ID: maintenanceJobID})
	}()

	<-started

	// While job is running, TriggerMaintenanceNow must be rejected with ErrMaintenanceAlreadyRunning
	if err := p.TriggerMaintenanceNow(ctx); err != ErrMaintenanceAlreadyRunning {
		t.Fatalf("expected ErrMaintenanceAlreadyRunning, got %v", err)
	}

	// Updating config mid-flight succeeds without disrupting active run
	if _, err := p.maintenanceService().SetInterval(ctx, 12*time.Hour); err != nil {
		t.Fatalf("mid-flight SetInterval: %v", err)
	}

	close(release)
	if err := <-jobDone; err != nil {
		t.Fatalf("job failed: %v", err)
	}
	srv.CloseClientConnections()
	srv.Close()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}

	// Allow HTTP transport idle goroutines to exit and verify no permanent goroutine growth
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseGoroutines+2 {
			break
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > baseGoroutines+2 {
		t.Fatalf("unexpected goroutine growth: before=%d after=%d", baseGoroutines, after)
	}
}

func benchmarkMaintenanceCycle(b *testing.B, totalAccounts, dueAccounts int) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "at-bench",
			IDToken:      "id-bench",
			RefreshToken: "rt-bench",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	db, err := database.Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		b.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-bench"))
	maintCfg := DefaultTokenMaintenanceConfig()

	now := time.Now().UTC()
	for i := 0; i < totalAccounts; i++ {
		exp := now.Add(4 * time.Hour)
		if i < dueAccounts {
			exp = now.Add(-time.Minute)
		}
		if err := repo.Save(ctx, &Account{
			MSISDN:         fmt.Sprintf("628700000%03d", i),
			RefreshToken:   "rt",
			TokenExpiresAt: exp,
		}); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < dueAccounts; j++ {
			_, _ = db.ExecContext(ctx, `UPDATE myxl_accounts SET token_expires_at = ? WHERE msisdn = ?`,
				time.Now().UTC().Add(-time.Minute), fmt.Sprintf("628700000%03d", j))
		}
		if _, _, err := runTokenMaintenanceCycle(ctx, repo, client, maintCfg, "scheduled"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMaintenanceCycle_1Account(b *testing.B) {
	benchmarkMaintenanceCycle(b, 1, 1)
}

func BenchmarkMaintenanceCycle_10Accounts(b *testing.B) {
	benchmarkMaintenanceCycle(b, 10, 5)
}

func BenchmarkMaintenanceCycle_50Accounts_MixedFreshAndDue(b *testing.B) {
	benchmarkMaintenanceCycle(b, 50, 10)
}
