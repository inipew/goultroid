package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/network"
)

type failingMaintenanceRepo struct {
	Repository
	failNextSave bool
}

func (r *failingMaintenanceRepo) SaveTokenMaintenanceConfig(ctx context.Context, cfg *TokenMaintenanceConfig) error {
	if r.failNextSave {
		r.failNextSave = false
		return errors.New("simulated db write failure")
	}
	return r.Repository.SaveTokenMaintenanceConfig(ctx, cfg)
}

func TestM5_MaintenanceService_UpdatePreservesFieldsAndHandlesRollback(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "m5_svc.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	svc := p.maintenanceService()
	view, err := svc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if view.Config.Enabled || view.Config.Interval != 6*time.Hour {
		t.Fatalf("unexpected initial status: %+v", view)
	}

	// Single-field updates preserve other fields
	if _, err := svc.SetInterval(ctx, 12*time.Hour); err != nil {
		t.Fatalf("SetInterval: %v", err)
	}
	if _, err := svc.SetAnchorTime(ctx, "04:30"); err != nil {
		t.Fatalf("SetAnchorTime: %v", err)
	}
	if _, err := svc.SetTimezone(ctx, "Asia/Makassar"); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}
	if _, err := svc.SetRefreshWindow(ctx, 45*time.Minute); err != nil {
		t.Fatalf("SetRefreshWindow: %v", err)
	}
	cfg, err := svc.SetEnabled(ctx, true)
	if err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if !cfg.Enabled || cfg.Interval != 12*time.Hour || cfg.AnchorTime != "04:30" || cfg.Timezone != "Asia/Makassar" || cfg.RefreshWindow != 45*time.Minute {
		t.Fatalf("single-field updates did not preserve fields: %+v", cfg)
	}

	// Invalid update rejected without altering persisted config
	if _, err := svc.SetInterval(ctx, 10*time.Minute); err == nil {
		t.Fatal("expected invalid interval error")
	}

	// Schedule sync failure + rollback failure surfaces ErrScheduleSyncRollbackFailed
	p.maintMu.Lock()
	origSched := p.scheduleClient
	p.scheduleClient = failingScheduleClient{saveErr: errors.New("sched down")}
	p.maintMu.Unlock()

	wrappedRepo := &failingMaintenanceRepo{Repository: repo}
	origRepo := p.repo
	p.repo = wrappedRepo
	// First SaveTokenMaintenanceConfig succeeds; rollback SaveTokenMaintenanceConfig fails
	if err := repo.SaveTokenMaintenanceConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// Make the 2nd save (rollback) fail by wrapping:
	p.repo = &failSecondSaveRepo{Repository: repo}
	if _, err := svc.SetInterval(ctx, 3*time.Hour); !errors.Is(err, ErrScheduleSyncRollbackFailed) {
		t.Fatalf("expected ErrScheduleSyncRollbackFailed, got %v", err)
	}

	p.repo = origRepo
	p.maintMu.Lock()
	p.scheduleClient = origSched
	p.maintMu.Unlock()
}

type failSecondSaveRepo struct {
	Repository
	calls int
}

func (r *failSecondSaveRepo) SaveTokenMaintenanceConfig(ctx context.Context, cfg *TokenMaintenanceConfig) error {
	r.calls++
	if r.calls == 2 {
		return errors.New("rollback db error")
	}
	return r.Repository.SaveTokenMaintenanceConfig(ctx, cfg)
}

func newTestCoreContext(senderID int64, perms *core.Permissions, args ...string) (*core.Context, *mockTgService) {
	tgSvc := &mockTgService{}
	return &core.Context{
		Ctx:      context.Background(),
		Telegram: core.TelegramCapabilities{Messages: tgSvc},
		Perms:    perms,
		PeerID:   &tg.InputPeerUser{UserID: senderID},
		Message:  &core.Message{ID: 1},
		Chat:     &core.Chat{ID: senderID, Type: "private"},
		Sender:   &core.User{ID: senderID},
		Args:     args,
	}, tgSvc
}

func TestM5_ScheduleCommand_AllSubcommandsAndPermissions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m5_cmd.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	perms := core.NewPermissions(1001, nil)

	// Non-owner rejected
	nonOwnerCtx, nonOwnerTg := newTestCoreContext(9999, perms, "schedule", "enable")
	if err := p.handleMyXL(nonOwnerCtx); err != nil {
		t.Fatalf("non-owner handleMyXL: %v", err)
	}
	if !strings.Contains(nonOwnerTg.sent, "Hanya owner") {
		t.Fatalf("expected owner rejection, got %q", nonOwnerTg.sent)
	}
	cfg, _ := repo.GetTokenMaintenanceConfig(context.Background())
	if cfg.Enabled {
		t.Fatal("non-owner must not enable schedule")
	}

	// Valid owner subcommands
	cmds := [][]string{
		{"schedule", "interval", "12h"},
		{"schedule", "time", "04:00"},
		{"schedule", "timezone", "Asia/Jakarta"},
		{"schedule", "window", "45m"},
		{"schedule", "accounts", "active"},
		{"schedule", "enable"},
		{"schedule", "status"},
		{"schedule", "run"},
		{"schedule", "history"},
		{"schedule", "disable"},
	}
	for _, args := range cmds {
		c, tgMock := newTestCoreContext(1001, perms, args...)
		if err := p.handleMyXL(c); err != nil {
			t.Fatalf("command %v failed: %v", args, err)
		}
		if tgMock.sent == "" {
			t.Fatalf("command %v produced empty output", args)
		}
	}

	// Status must not fabricate next_due_at / next run claims
	statusCtx, statusTg := newTestCoreContext(1001, perms, "schedule", "status")
	_ = p.handleMyXL(statusCtx)
	if strings.Contains(strings.ToLower(statusTg.sent), "next_due_at") || strings.Contains(strings.ToLower(statusTg.sent), "jadwal berikutnya") {
		t.Fatalf("status output must not claim unreadable next_due_at: %q", statusTg.sent)
	}

	// Invalid arguments rejected with guidance
	invalidCtx, invalidTg := newTestCoreContext(1001, perms, "schedule", "interval", "10m")
	_ = p.handleMyXL(invalidCtx)
	if !strings.Contains(invalidTg.sent, "Gagal") {
		t.Fatalf("expected error message for invalid interval, got %q", invalidTg.sent)
	}
}

func TestM5_MenuManager_MaintenanceScreensAndDashboardEntry(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "m5_menu.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	if err := repo.Save(ctx, &Account{
		MSISDN:         "6281900000001",
		IsActive:       true,
		RefreshToken:   "rt",
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	dash, err := p.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildDashboardScreen: %v", err)
	}
	foundMaintBtn := false
	for _, row := range dash.Rows {
		for _, btn := range row {
			if string(btn.Data) == "myxl:maintenance" {
				foundMaintBtn = true
			}
		}
	}
	if !foundMaintBtn {
		t.Fatal("dashboard screen missing myxl:maintenance button")
	}

	maintScreen, err := p.menuMgr.BuildMaintenanceScreen(ctx)
	if err != nil {
		t.Fatalf("BuildMaintenanceScreen: %v", err)
	}
	if !strings.Contains(maintScreen.Body, "MyXL Token Maintenance") {
		t.Fatalf("unexpected maintenance screen text: %q", maintScreen.Body)
	}

	if err := repo.RecordTokenMaintenanceRun(ctx, &MaintenanceRunSummary{
		TriggerSource:  "manual",
		Status:         RunStatusCompleted,
		StartedAt:      time.Now().UTC(),
		CompletedAt:    time.Now().UTC(),
		Duration:       time.Second,
		TotalAccounts:  2,
		RefreshedCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	histScreen, err := p.menuMgr.BuildMaintenanceHistoryScreen(ctx)
	if err != nil {
		t.Fatalf("BuildMaintenanceHistoryScreen: %v", err)
	}
	if !strings.Contains(histScreen.Body, "completed") {
		t.Fatalf("expected recorded run in history screen, got %q", histScreen.Body)
	}
}

func TestM5_Security_RefreshRedactsAllTokensAndMasksMSISDN(t *testing.T) {
	ctx := context.Background()
	const (
		secretAccess  = "eyJ-SECRET-ACCESS-TOKEN-VALUE-1234567890"
		secretID      = "eyJ-SECRET-ID-TOKEN-VALUE-0987654321"
		secretRefresh = "eyJ-SECRET-REFRESH-TOKEN-VALUE-ABCDEFGH"
		rawMSISDN     = "6281912345678"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  secretAccess,
			IDToken:      secretID,
			RefreshToken: secretRefresh,
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	_, repo := openMyXLTestDB(t)
	if err := repo.Save(ctx, &Account{
		MSISDN:         rawMSISDN,
		IsActive:       true,
		AccessToken:    "old-at",
		IDToken:        "old-id",
		RefreshToken:   "old-rt",
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-sec"))
	p := New(repo, client)

	perms := core.NewPermissions(1001, nil)
	cmdCtx, tgMock := newTestCoreContext(1001, perms, "refresh")
	if err := p.handleMyXL(cmdCtx); err != nil {
		t.Fatalf("handleMyXL refresh: %v", err)
	}

	for _, forbidden := range []string{
		secretAccess,
		secretAccess[:12],
		secretID,
		secretID[:12],
		secretRefresh,
		secretRefresh[:12],
		rawMSISDN,
	} {
		if strings.Contains(tgMock.sent, forbidden) {
			t.Fatalf("refresh output leaked sensitive data %q in %q", forbidden, tgMock.sent)
		}
	}
	if !strings.Contains(tgMock.sent, MaskMSISDN(rawMSISDN)) {
		t.Fatalf("expected masked MSISDN %q in %q", MaskMSISDN(rawMSISDN), tgMock.sent)
	}
}
