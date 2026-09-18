package idempotency

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIdempotencyManager_CheckAndSet(t *testing.T) {
	mgr := NewManager(10 * time.Millisecond)
	defer mgr.Close()

	ctx := context.Background()
	key := "update:12345"

	// 1. First execution should succeed (not duplicate)
	isNew, err := mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("expected first call to return isNew=true")
	}

	if !mgr.IsProcessed(key) {
		t.Errorf("expected key to be marked processed")
	}

	// 2. Immediate second execution should be identified as duplicate
	isNew, err = mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Errorf("expected second call to return isNew=false (duplicate)")
	}

	// 3. After TTL expires, key can be reprocessed
	time.Sleep(60 * time.Millisecond)
	if mgr.IsProcessed(key) {
		t.Errorf("expected key to be expired after TTL")
	}

	isNew, err = mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("expected call after expiration to return isNew=true")
	}
}

type lifecycleRepo struct {
	deleteCalls atomic.Int32
}

func (r *lifecycleRepo) InitSchema(context.Context) error { return nil }
func (r *lifecycleRepo) Claim(context.Context, string, time.Time, time.Time) (bool, error) { return true, nil }
func (r *lifecycleRepo) IsProcessed(context.Context, string, time.Time) (bool, error) { return false, nil }
func (r *lifecycleRepo) DeleteExpired(ctx context.Context, _ time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.deleteCalls.Add(1)
	return 0, nil
}
func (r *lifecycleRepo) Size(context.Context, time.Time) (int, error) { return 0, nil }

func TestIdempotencyManager_ConstructorIsPassiveAndStopJoins(t *testing.T) {
	repo := &lifecycleRepo{}
	mgr := NewManager(5*time.Millisecond, repo)

	time.Sleep(15 * time.Millisecond)
	if got := repo.deleteCalls.Load(); got != 0 {
		t.Fatalf("constructor started cleanup work: delete calls=%d", got)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := mgr.Start(runCtx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for repo.deleteCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if repo.deleteCalls.Load() == 0 {
		t.Fatal("expected lifecycle-owned cleanup after Start")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("stop manager: %v", err)
	}
	stoppedAt := repo.deleteCalls.Load()
	time.Sleep(15 * time.Millisecond)
	if got := repo.deleteCalls.Load(); got != stoppedAt {
		t.Fatalf("cleanup continued after Stop joined worker: before=%d after=%d", stoppedAt, got)
	}
}

func TestIdempotencyManager_StartRejectsNilContext(t *testing.T) {
	mgr := NewManager(time.Hour)
	if err := mgr.Start(nil); err == nil {
		t.Fatal("expected nil lifecycle context to be rejected")
	}
}

func TestIdempotencyManager_CloseConcurrent(t *testing.T) {
	mgr := NewManager(time.Hour)

	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			mgr.Close()
		}()
	}
	wg.Wait()
}

func TestSQLiteIdempotencyPersistsAcrossManagers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	first := NewManager(time.Hour, repo)
	isNew, err := first.CheckAndSet(context.Background(), "persistent-key", time.Hour)
	first.Close()
	if err != nil || !isNew {
		t.Fatalf("first claim: isNew=%v err=%v", isNew, err)
	}

	second := NewManager(time.Hour, repo)
	defer second.Close()
	isNew, err = second.CheckAndSet(context.Background(), "persistent-key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Fatal("expected key persisted by first manager to be duplicate")
	}
}

func TestSQLiteIdempotencyReclaimsExpiredKey(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if claimed, err := repo.Claim(context.Background(), "expired", now.Add(-time.Hour), now.Add(-time.Minute)); err != nil || !claimed {
		t.Fatalf("seed expired claim: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.Claim(context.Background(), "expired", now, now.Add(time.Hour)); err != nil || !claimed {
		t.Fatalf("reclaim expired key: claimed=%v err=%v", claimed, err)
	}
}

func TestSQLiteIdempotencyClaimIsAtomic(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "idempotency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	const contenders = 12
	var winners atomic.Int32
	var wg sync.WaitGroup
	wg.Add(contenders)
	start := make(chan struct{})
	for range contenders {
		go func() {
			defer wg.Done()
			<-start
			now := time.Now().UTC()
			claimed, claimErr := repo.Claim(context.Background(), "contended", now, now.Add(time.Hour))
			if claimErr != nil {
				t.Errorf("claim failed: %v", claimErr)
				return
			}
			if claimed {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("expected exactly one atomic claim winner, got %d", got)
	}
}
