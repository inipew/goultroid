package deeplink_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/deeplink"
	"github.com/inipew/goultroid/internal/services/deeplink/sqlite"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "deeplink_test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := sqlite.InitSchema(context.Background(), db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return db
}

func TestDeepLink_IssueAndConsume(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	ctx := context.Background()
	screenKey := presentation.ScreenKey{Namespace: "settings", Name: "voice"}

	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:      deeplink.PurposeOpenScreen,
		Owner:        "plugin:settings",
		Generation:   1,
		UserID:       12345,
		SourceChatID: -100123,
		Screen:       screenKey,
		SingleUse:    true,
		TTL:          5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue token error: %v", err)
	}
	if len(token) < 20 {
		t.Errorf("token seems too short: %s", token)
	}

	// 1. Peek by same user
	claim, err := svc.Peek(ctx, token, execution.NewActor(12345, 12345, false, false))
	if err != nil {
		t.Fatalf("peek error: %v", err)
	}
	if claim.Screen != screenKey {
		t.Errorf("expected screen %s, got %s", screenKey, claim.Screen)
	}
	if claim.UserID != 12345 {
		t.Errorf("expected user ID 12345, got %d", claim.UserID)
	}

	// 2. Peek by different user -> Scope mismatch
	_, err = svc.Peek(ctx, token, execution.NewActor(99999, 99999, false, false))
	if !errors.Is(err, deeplink.ErrTokenScopeMismatch) {
		t.Errorf("expected ErrTokenScopeMismatch, got %v", err)
	}

	// 3. Consume by authorized user
	claim, err = svc.Consume(ctx, token, execution.NewActor(12345, 12345, false, false))
	if err != nil {
		t.Fatalf("consume error: %v", err)
	}
	if claim.ConsumedAt == nil {
		t.Errorf("expected ConsumedAt to be non-nil")
	}

	// 4. Consume again -> ErrTokenConsumed
	_, err = svc.Consume(ctx, token, execution.NewActor(12345, 12345, false, false))
	if !errors.Is(err, deeplink.ErrTokenConsumed) {
		t.Errorf("expected ErrTokenConsumed, got %v", err)
	}
}

func TestDeepLink_ConcurrentAtomicConsume(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:   deeplink.PurposeOpenScreen,
		Screen:    presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		SingleUse: true,
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	const concurrency = 10
	var wg sync.WaitGroup
	var successes atomic.Int32
	var consumedErrors atomic.Int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := svc.Consume(ctx, token, execution.NewActor(int64(id), 0, false, false))
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, deeplink.ErrTokenConsumed) {
				consumedErrors.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if successes.Load() != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successes.Load())
	}
	if consumedErrors.Load() != concurrency-1 {
		t.Fatalf("expected %d ErrTokenConsumed, got %d", concurrency-1, consumedErrors.Load())
	}
}

func TestDeepLink_GenerationRevocation(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	currentGen := uint64(1)
	svc.SetActiveGenerationResolver(func(owner string) (uint64, bool) {
		if owner == "plugin:test" {
			return currentGen, true
		}
		return 0, false
	})

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:    deeplink.PurposeOpenScreen,
		Screen:     presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		Owner:      "plugin:test",
		Generation: 1,
		TTL:        5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	// Active generation: can peek
	_, err = svc.Peek(ctx, token, execution.NewActor(0, 0, false, false))
	if err != nil {
		t.Fatalf("peek with current gen: %v", err)
	}

	// Plugin reloads to generation 2: old token becomes stale
	currentGen = 2
	_, err = svc.Peek(ctx, token, execution.NewActor(0, 0, false, false))
	if !errors.Is(err, deeplink.ErrTokenGenerationStale) {
		t.Errorf("expected ErrTokenGenerationStale, got %v", err)
	}
}

func TestDeepLink_IssueStartLink(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "MyCoolBot", nil)

	link, exp, err := svc.IssueStartLink(context.Background(), presentation.DeepLinkRequest{
		Screen: presentation.ScreenKey{Namespace: "help", Name: "commands"},
		UserID: 12345,
		TTL:    15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue link error: %v", err)
	}
	if exp.Before(time.Now().Add(14 * time.Minute)) {
		t.Errorf("expected expiry >= 14m in future, got %v", exp)
	}
	prefix := "https://t.me/MyCoolBot?start="
	if len(link) <= len(prefix) || link[:len(prefix)] != prefix {
		t.Errorf("unexpected link format: %s", link)
	}
}

func TestDeepLink_AttackerCannotBurnOwnerToken(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:   deeplink.PurposeOpenScreen,
		Screen:    presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		UserID:    12345,
		SingleUse: true,
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	// 1. Attacker (wrong user) attempts to consume
	attacker := execution.NewActor(99999, 99999, false, false)
	_, err = svc.Consume(ctx, token, attacker)
	if !errors.Is(err, deeplink.ErrTokenScopeMismatch) {
		t.Fatalf("expected ErrTokenScopeMismatch for attacker, got %v", err)
	}

	// 2. Verify token is NOT burned: legitimate user consumes successfully
	owner := execution.NewActor(12345, 12345, false, false)
	claim, err := svc.Consume(ctx, token, owner)
	if err != nil {
		t.Fatalf("expected owner to successfully consume after attacker failed, got %v", err)
	}
	if claim.UserID != 12345 {
		t.Errorf("expected claim.UserID == 12345, got %d", claim.UserID)
	}
}

func TestDeepLink_ActorZeroRejectedForUserScopedToken(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:   deeplink.PurposeOpenScreen,
		Screen:    presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		UserID:    12345,
		SingleUse: true,
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	// Actor with UserID == 0
	zeroActor := execution.NewActor(0, 0, false, false)
	_, err = svc.Consume(ctx, token, zeroActor)
	if !errors.Is(err, deeplink.ErrTokenScopeMismatch) {
		t.Fatalf("expected ErrTokenScopeMismatch for actor with ID 0, got %v", err)
	}

	// Legitimate owner can still consume
	owner := execution.NewActor(12345, 12345, false, false)
	_, err = svc.Consume(ctx, token, owner)
	if err != nil {
		t.Fatalf("expected owner to successfully consume, got %v", err)
	}
}

func TestDeepLink_StaleGenerationCannotBurnToken(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	currentGen := uint64(1)
	svc.SetActiveGenerationResolver(func(owner string) (uint64, bool) {
		if owner == "plugin:test" {
			return currentGen, true
		}
		return 0, false
	})

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:    deeplink.PurposeOpenScreen,
		Screen:     presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		Owner:      "plugin:test",
		Generation: 1,
		UserID:     12345,
		SingleUse:  true,
		TTL:        5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	// Stale generation
	currentGen = 2
	owner := execution.NewActor(12345, 12345, false, false)
	_, err = svc.Consume(ctx, token, owner)
	if !errors.Is(err, deeplink.ErrTokenGenerationStale) {
		t.Fatalf("expected ErrTokenGenerationStale, got %v", err)
	}

	// If plugin reverts/restores generation 1, token was NOT burned and can still be consumed
	currentGen = 1
	claim, err := svc.Consume(ctx, token, owner)
	if err != nil {
		t.Fatalf("expected consume to succeed once generation is valid, got %v", err)
	}
	if claim.UserID != 12345 {
		t.Errorf("expected claim.UserID 12345, got %d", claim.UserID)
	}
}

func TestDeepLink_ConcurrentAttackerAndOwnerRace(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)

	ctx := context.Background()
	token, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose:   deeplink.PurposeOpenScreen,
		Screen:    presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
		UserID:    12345,
		SingleUse: true,
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue error: %v", err)
	}

	const attackerCount = 10
	var wg sync.WaitGroup
	var ownerSuccess atomic.Bool
	var attackerSuccess atomic.Int32

	// Launch attackers
	for i := 0; i < attackerCount; i++ {
		wg.Add(1)
		go func(attackerID int64) {
			defer wg.Done()
			_, err := svc.Consume(ctx, token, execution.NewActor(attackerID, 0, false, false))
			if err == nil {
				attackerSuccess.Add(1)
			}
		}(int64(90000 + i))
	}

	// Launch owner concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := svc.Consume(ctx, token, execution.NewActor(12345, 0, false, false))
		if err == nil {
			ownerSuccess.Store(true)
		}
	}()

	wg.Wait()

	if attackerSuccess.Load() != 0 {
		t.Fatalf("attackers succeeded %d times; should be 0", attackerSuccess.Load())
	}
	if !ownerSuccess.Load() {
		t.Fatalf("owner failed to consume token during race against attackers")
	}
}

func TestDeepLink_RestartPersistence(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)

	ctx := context.Background()
	screenKey := presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1}

	// Instance 1 issues token
	svc1 := deeplink.NewService(repo, "Bot1", nil)
	token, err := svc1.Issue(ctx, deeplink.IssueRequest{
		Purpose:   deeplink.PurposeOpenScreen,
		Screen:    screenKey,
		UserID:    12345,
		SingleUse: true,
		TTL:       10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("svc1 issue: %v", err)
	}

	// Instance 2 consumes token after restart
	svc2 := deeplink.NewService(repo, "Bot2", nil)
	claim, err := svc2.Consume(ctx, token, execution.NewActor(12345, 12345, false, false))
	if err != nil {
		t.Fatalf("svc2 consume after restart: %v", err)
	}
	if claim.Screen != screenKey {
		t.Errorf("expected screen %s, got %s", screenKey, claim.Screen)
	}

	// Consuming again on Instance 2 fails
	_, err = svc2.Consume(ctx, token, execution.NewActor(12345, 12345, false, false))
	if !errors.Is(err, deeplink.ErrTokenConsumed) {
		t.Errorf("expected ErrTokenConsumed on repeat consume, got %v", err)
	}
}

func TestDeepLink_Validation_InvalidPurposeAndScreen(t *testing.T) {
	db := setupTestDB(t)
	repo := sqlite.NewRepository(db)
	svc := deeplink.NewService(repo, "TestBot", nil)
	ctx := context.Background()

	// Invalid purpose
	_, err := svc.Issue(ctx, deeplink.IssueRequest{
		Purpose: deeplink.Purpose("invalid_purpose"),
		Screen:  presentation.ScreenKey{Namespace: "core", Name: "help"},
		UserID:  123,
	})
	if !errors.Is(err, deeplink.ErrInvalidPurpose) {
		t.Errorf("expected ErrInvalidPurpose, got %v", err)
	}

	// Empty screen key when PurposeOpenScreen
	_, err = svc.Issue(ctx, deeplink.IssueRequest{
		Purpose: deeplink.PurposeOpenScreen,
		Screen:  presentation.ScreenKey{},
		UserID:  123,
	})
	if !errors.Is(err, deeplink.ErrInvalidScreenKey) {
		t.Errorf("expected ErrInvalidScreenKey, got %v", err)
	}
}
