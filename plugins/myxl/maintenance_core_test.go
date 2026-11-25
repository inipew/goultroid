package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
)

func openMyXLTestDB(t *testing.T) (*database.DB, *SQLiteRepository) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run myxl feature migrations: %v", err)
	}
	return db, NewSQLiteRepository(db)
}

func TestTokenMaintenanceConfig_ValidationAndPersistence(t *testing.T) {
	ctx := context.Background()
	_, repo := openMyXLTestDB(t)

	// Default config when no row exists
	got, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("get default config: %v", err)
	}
	if got.Enabled || got.Interval != 6*time.Hour || got.AnchorTime != "03:00" || got.Timezone != "Asia/Jakarta" || got.RefreshWindow != 30*time.Minute || got.AccountScope != AccountScopeAll {
		t.Fatalf("unexpected default config: %+v", got)
	}

	// Reject invalid configurations explicitly without silent fallback
	invalidCases := []struct {
		name   string
		mutate func(*TokenMaintenanceConfig)
	}{
		{"zero_interval", func(c *TokenMaintenanceConfig) { c.Interval = 0 }},
		{"negative_interval", func(c *TokenMaintenanceConfig) { c.Interval = -time.Hour }},
		{"too_frequent_interval", func(c *TokenMaintenanceConfig) { c.Interval = 30 * time.Minute }},
		{"too_long_interval", func(c *TokenMaintenanceConfig) { c.Interval = 25 * time.Hour }},
		{"non_minute_interval", func(c *TokenMaintenanceConfig) { c.Interval = 6*time.Hour + 15*time.Second }},
		{"invalid_anchor", func(c *TokenMaintenanceConfig) { c.AnchorTime = "25:00" }},
		{"invalid_anchor_format", func(c *TokenMaintenanceConfig) { c.AnchorTime = "3:00" }},
		{"empty_timezone", func(c *TokenMaintenanceConfig) { c.Timezone = "" }},
		{"invalid_timezone", func(c *TokenMaintenanceConfig) { c.Timezone = "Mars/Olympus" }},
		{"zero_window", func(c *TokenMaintenanceConfig) { c.RefreshWindow = 0 }},
		{"small_window", func(c *TokenMaintenanceConfig) { c.RefreshWindow = 2 * time.Minute }},
		{"large_window", func(c *TokenMaintenanceConfig) { c.RefreshWindow = 121 * time.Minute }},
		{"window_ge_interval", func(c *TokenMaintenanceConfig) { c.Interval = time.Hour; c.RefreshWindow = time.Hour }},
		{"invalid_scope", func(c *TokenMaintenanceConfig) { c.AccountScope = "unknown" }},
		{"selected_empty", func(c *TokenMaintenanceConfig) { c.AccountScope = AccountScopeSelected; c.SelectedMSISDNs = nil }},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultTokenMaintenanceConfig()
			tc.mutate(&cfg)
			if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}

	// Save valid updated config
	custom := TokenMaintenanceConfig{
		Enabled:            true,
		Interval:           12 * time.Hour,
		AnchorTime:         "04:30",
		Timezone:           "Asia/Makassar",
		RefreshWindow:      45 * time.Minute,
		AccountScope:       AccountScopeSelected,
		SelectedMSISDNs:    []string{"6281900000001"},
		NotifyOnCompletion: true,
		NotifyOnFailure:    false,
	}
	if err := repo.SaveTokenMaintenanceConfig(ctx, &custom); err != nil {
		t.Fatalf("save custom config: %v", err)
	}

	loaded, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !loaded.Enabled || loaded.Interval != 12*time.Hour || loaded.AnchorTime != "04:30" || loaded.Timezone != "Asia/Makassar" ||
		loaded.RefreshWindow != 45*time.Minute || loaded.AccountScope != AccountScopeSelected || len(loaded.SelectedMSISDNs) != 1 ||
		loaded.SelectedMSISDNs[0] != "6281900000001" || !loaded.NotifyOnCompletion || loaded.NotifyOnFailure {
		t.Fatalf("reloaded config mismatch: %+v", loaded)
	}
}

func TestTokenMaintenanceRunHistory_BoundedRetention(t *testing.T) {
	ctx := context.Background()
	_, repo := openMyXLTestDB(t)

	base := time.Now().UTC().Add(-time.Hour)
	total := MaxMaintenanceRunHistory + 7
	for i := 1; i <= total; i++ {
		run := &MaintenanceRunSummary{
			TriggerSource:     "scheduled",
			Status:            RunStatusCompleted,
			StartedAt:         base.Add(time.Duration(i) * time.Minute),
			CompletedAt:       base.Add(time.Duration(i)*time.Minute + 2*time.Second),
			Duration:          2 * time.Second,
			TotalAccounts:     i,
			RefreshedCount:    1,
			AlreadyFreshCount: i - 1,
		}
		if err := repo.RecordTokenMaintenanceRun(ctx, run); err != nil {
			t.Fatalf("record run %d: %v", i, err)
		}
	}

	runs, err := repo.ListTokenMaintenanceRuns(ctx, 50)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != MaxMaintenanceRunHistory {
		t.Fatalf("expected %d bounded runs, got %d", MaxMaintenanceRunHistory, len(runs))
	}
	if runs[0].TotalAccounts != total {
		t.Fatalf("expected newest run TotalAccounts=%d, got %d", total, runs[0].TotalAccounts)
	}
	if runs[len(runs)-1].TotalAccounts != total-MaxMaintenanceRunHistory+1 {
		t.Fatalf("expected oldest retained run TotalAccounts=%d, got %d", total-MaxMaintenanceRunHistory+1, runs[len(runs)-1].TotalAccounts)
	}
}

func TestEvaluateAccountToken_DecisionsAndPriority(t *testing.T) {
	now := time.Now().UTC()
	window := 30 * time.Minute

	fresh := &Account{MSISDN: "6281", RefreshToken: "rt", TokenExpiresAt: now.Add(2 * time.Hour)}
	within := &Account{MSISDN: "6282", RefreshToken: "rt", TokenExpiresAt: now.Add(15 * time.Minute)}
	expired := &Account{MSISDN: "6283", RefreshToken: "rt", TokenExpiresAt: now.Add(-time.Minute)}
	epochUnknown := &Account{MSISDN: "6284", RefreshToken: "rt", TokenExpiresAt: time.Unix(0, 0).UTC()}
	zeroUnknown := &Account{MSISDN: "6285", RefreshToken: "rt"}
	missingRT := &Account{MSISDN: "6286", TokenExpiresAt: now.Add(-time.Minute)}

	if d := evaluateAccountToken(fresh, now, window); d != evalAlreadyFresh {
		t.Fatalf("fresh = %v, want evalAlreadyFresh", d)
	}
	if d := evaluateAccountToken(within, now, window); d != evalNeedsRefreshWithinWindow {
		t.Fatalf("within = %v, want evalNeedsRefreshWithinWindow", d)
	}
	if d := evaluateAccountToken(expired, now, window); d != evalNeedsRefreshExpired {
		t.Fatalf("expired = %v, want evalNeedsRefreshExpired", d)
	}
	if d := evaluateAccountToken(epochUnknown, now, window); d != evalNeedsRefreshUnknownExpiry {
		t.Fatalf("epochUnknown = %v, want evalNeedsRefreshUnknownExpiry", d)
	}
	if d := evaluateAccountToken(zeroUnknown, now, window); d != evalNeedsRefreshUnknownExpiry {
		t.Fatalf("zeroUnknown = %v, want evalNeedsRefreshUnknownExpiry", d)
	}
	if d := evaluateAccountToken(missingRT, now, window); d != evalMissingRefreshToken {
		t.Fatalf("missingRT = %v, want evalMissingRefreshToken", d)
	}

	cfg := DefaultTokenMaintenanceConfig()
	ordered := selectMaintenanceAccounts([]*Account{fresh, zeroUnknown, within, expired}, cfg, now)
	if len(ordered) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(ordered))
	}
	wantOrder := []string{"6283", "6282", "6285", "6281"}
	for i, want := range wantOrder {
		if ordered[i].account.MSISDN != want {
			t.Fatalf("priority[%d] = %s, want %s", i, ordered[i].account.MSISDN, want)
		}
	}
}

func TestEnsureFreshTokenWithin_SharedSingleflightAndPartialFailure(t *testing.T) {
	ctx := context.Background()
	_, repo := openMyXLTestDB(t)

	var ciamHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ciamHits.Add(1)
		_ = r.ParseForm()
		rt := r.FormValue("refresh_token")
		switch rt {
		case "rt-good":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "new-at",
				IDToken:      "new-id",
				RefreshToken: "new-rt",
				ExpiresIn:    3600,
			})
		case "rt-rejected":
			w.WriteHeader(http.StatusUnauthorized)
		case "rt-ratelimit":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-test"))

	now := time.Now().UTC()
	accGood := &Account{
		MSISDN:         "628111111111",
		IsActive:       true,
		AccessToken:    "old-at",
		IDToken:        "old-id",
		RefreshToken:   "rt-good",
		TokenExpiresAt: now.Add(10 * time.Minute),
	}
	accRejected := &Account{
		MSISDN:         "628222222222",
		AccessToken:    "keep-at",
		IDToken:        "keep-id",
		RefreshToken:   "rt-rejected",
		TokenExpiresAt: now.Add(-time.Minute),
	}
	accFresh := &Account{
		MSISDN:         "628333333333",
		AccessToken:    "fresh-at",
		IDToken:        "fresh-id",
		RefreshToken:   "rt-fresh",
		TokenExpiresAt: now.Add(3 * time.Hour),
	}
	for _, a := range []*Account{accGood, accRejected, accFresh} {
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("save %s: %v", a.MSISDN, err)
		}
	}

	// Verify shared singleflight across concurrent EnsureFreshTokenWithin calls
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clone := *accGood
			errs <- client.EnsureFreshTokenWithin(ctx, &clone, 30*time.Minute)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent EnsureFreshTokenWithin failed: %v", err)
		}
	}
	if hits := ciamHits.Load(); hits != 1 {
		t.Fatalf("expected 1 singleflight CIAM call, got %d", hits)
	}

	// Now reset accGood to within window and run full maintenance cycle with partial failure
	accGood.TokenExpiresAt = now.Add(10 * time.Minute)
	accGood.RefreshToken = "rt-good"
	if err := repo.Save(ctx, accGood); err != nil {
		t.Fatal(err)
	}
	ciamHits.Store(0)

	maintCfg := DefaultTokenMaintenanceConfig()
	maintCfg.Enabled = true
	summary, outcomes, err := runTokenMaintenanceCycle(ctx, repo, client, maintCfg, "manual")
	if err != nil {
		t.Fatalf("runTokenMaintenanceCycle: %v", err)
	}
	if summary.Status != RunStatusPartialFailure {
		t.Fatalf("status = %s, want partial_failure", summary.Status)
	}
	if summary.RefreshedCount != 1 || summary.FailedCount != 1 || summary.AlreadyFreshCount != 1 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(outcomes))
	}

	// Rejected account must keep its old tokens and not alter active account
	reloadedRejected, err := repo.GetByMSISDN(ctx, accRejected.MSISDN)
	if err != nil || reloadedRejected == nil {
		t.Fatalf("reload rejected account: %v", err)
	}
	if reloadedRejected.AccessToken != "keep-at" || reloadedRejected.RefreshToken != "rt-rejected" {
		t.Fatalf("rejected account tokens were overwritten: %+v", reloadedRejected)
	}
	active, err := repo.GetActive(ctx)
	if err != nil || active == nil || active.MSISDN != accGood.MSISDN {
		t.Fatalf("active account changed unexpectedly: %+v (err=%v)", active, err)
	}
}

func TestRunTokenMaintenanceCycle_CancellationAndErrorClassification(t *testing.T) {
	ctx := context.Background()
	_, repo := openMyXLTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.FormValue("refresh_token") {
		case "rt-429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "rt-500":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-test"))

	for i, rt := range []string{"rt-429", "rt-500"} {
		if err := repo.Save(ctx, &Account{
			MSISDN:         fmt.Sprintf("62890000000%d", i),
			RefreshToken:   rt,
			TokenExpiresAt: time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	summary, outcomes, err := runTokenMaintenanceCycle(ctx, repo, client, DefaultTokenMaintenanceConfig(), "scheduled")
	if err != nil {
		t.Fatalf("runTokenMaintenanceCycle: %v", err)
	}
	if summary.Status != RunStatusFailed || summary.FailedCount != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	seen := map[AccountMaintenanceStatus]bool{}
	for _, o := range outcomes {
		seen[o.Status] = true
	}
	if !seen[StatusRateLimited] || !seen[StatusNetworkError] {
		t.Fatalf("expected rate_limited and network_error outcomes, got %+v", outcomes)
	}

	// Cancelled context stops cooperatively before processing
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	summaryCancelled, _, err := runTokenMaintenanceCycle(cancelCtx, repo, client, DefaultTokenMaintenanceConfig(), "manual")
	if !errors.Is(err, context.Canceled) && (summaryCancelled == nil || summaryCancelled.Status != RunStatusCancelled) {
		t.Fatalf("expected cancelled cycle, got summary=%+v err=%v", summaryCancelled, err)
	}
}
