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
	"github.com/inipew/goultroid/internal/jobs"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type failingNotifyTgService struct {
	core.MockTelegramServicer
	err  error
	sent []string
}

func (m *failingNotifyTgService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.sent = append(m.sent, text)
	return &tg.Message{ID: 77, Message: text}, nil
}

func TestM6_StructuredLoggingDiagnosticsAndSecurity(t *testing.T) {
	ctx := context.Background()
	const (
		sentinelAccess  = "SENTINEL-ACCESS-SECRET-9988"
		sentinelID      = "SENTINEL-ID-SECRET-7766"
		sentinelRefresh = "SENTINEL-REFRESH-SECRET-5544"
		rawMSISDN       = "6281912345678"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("refresh_token") == sentinelRefresh {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  sentinelAccess,
				IDToken:      sentinelID,
				RefreshToken: sentinelRefresh,
				ExpiresIn:    3600,
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "m6_obs.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, srv.URL)

	coreObs, recorded := observer.New(zap.InfoLevel)
	p.SetLogger(zap.New(coreObs))

	notifyTg := &failingNotifyTgService{}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return notifyTg })

	// 1. No-op run (no accounts)
	if err := p.handleTokenMaintenanceJob(ctx, jobs.JobDefinition{ID: maintenanceJobID}); err != nil {
		t.Fatalf("no-op run: %v", err)
	}
	diag := p.MaintenanceDiagnostics()
	if diag.RunsTotal != 1 || diag.RunsNoOp != 1 || diag.RunsSucceeded != 0 || diag.NotificationsAttempted != 0 {
		t.Fatalf("unexpected diagnostics after no-op: %+v", diag)
	}

	// 2. Successful refresh with NotifyOnCompletion=true
	on := true
	if _, err := p.maintenanceService().SetNotifyPreferences(ctx, &on, nil); err != nil {
		t.Fatalf("SetNotifyPreferences: %v", err)
	}
	if err := repo.Save(ctx, &Account{
		MSISDN:         rawMSISDN,
		IsActive:       true,
		AccessToken:    sentinelAccess,
		IDToken:        sentinelID,
		RefreshToken:   sentinelRefresh,
		TokenExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.handleTokenMaintenanceJob(ctx, jobs.JobDefinition{ID: maintenanceJobID}); err != nil {
		t.Fatalf("success run: %v", err)
	}
	diag = p.MaintenanceDiagnostics()
	if diag.RunsTotal != 2 || diag.RunsSucceeded != 1 || diag.AccountsRefreshed != 1 ||
		diag.NotificationsAttempted != 1 || diag.NotificationsSucceeded != 1 || diag.NotificationsFailed != 0 {
		t.Fatalf("unexpected diagnostics after success run: %+v", diag)
	}
	if len(notifyTg.sent) != 1 {
		t.Fatalf("expected 1 owner notification, got %d", len(notifyTg.sent))
	}

	// Verify logs & notification never leak sentinel credentials or raw MSISDN
	for _, entry := range recorded.All() {
		line := entry.Message
		for _, f := range entry.Context {
			line += " " + f.Key + "=" + f.String
		}
		for _, secretVal := range []string{sentinelAccess, sentinelID, sentinelRefresh, sentinelAccess[:10], rawMSISDN} {
			if strings.Contains(line, secretVal) {
				t.Fatalf("structured log leaked %q: %s", secretVal, line)
			}
		}
	}
	for _, secretVal := range []string{sentinelAccess, sentinelID, sentinelRefresh, sentinelAccess[:10], rawMSISDN} {
		if strings.Contains(notifyTg.sent[0], secretVal) {
			t.Fatalf("notification leaked %q: %s", secretVal, notifyTg.sent[0])
		}
	}
}

func TestM6_NotificationFailureDoesNotAlterRunOutcome(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "m6_notify_fail.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, srv.URL)

	coreObs, recorded := observer.New(zap.InfoLevel)
	p.SetLogger(zap.New(coreObs))

	notifyTg := &failingNotifyTgService{err: errors.New("telegram flood wait")}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return notifyTg })

	if err := repo.Save(ctx, &Account{
		MSISDN:         "628199999999",
		IsActive:       true,
		RefreshToken:   "rt-bad",
		TokenExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// Handler must return nil (cycle executed & persisted) even when notification fails
	if err := p.handleTokenMaintenanceJob(ctx, jobs.JobDefinition{ID: maintenanceJobID}); err != nil {
		t.Fatalf("handleTokenMaintenanceJob returned error on notify failure: %v", err)
	}

	diag := p.MaintenanceDiagnostics()
	if diag.RunsFailed != 1 || diag.NotificationsAttempted != 1 || diag.NotificationsFailed != 1 {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}

	runs, err := repo.ListTokenMaintenanceRuns(ctx, 5)
	if err != nil || len(runs) != 1 || runs[0].Status != RunStatusFailed {
		t.Fatalf("expected persisted failed run, got %+v (err=%v)", runs, err)
	}

	foundNotifyWarn := false
	for _, entry := range recorded.All() {
		if entry.Message == "myxl token maintenance notification failed" {
			foundNotifyWarn = true
		}
	}
	if !foundNotifyWarn {
		t.Fatal("expected structured warning log for failed notification")
	}

	// Command .myxl schedule notify works
	perms := core.NewPermissions(1001, nil)
	c, tgMock := newTestCoreContext(1001, perms, "schedule", "notify", "completion", "on")
	if err := p.handleMyXL(c); err != nil {
		t.Fatalf("notify command: %v", err)
	}
	if !strings.Contains(tgMock.sent, "completion") {
		t.Fatalf("unexpected notify command output: %q", tgMock.sent)
	}
}
