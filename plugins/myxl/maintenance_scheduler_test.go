package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/secret"
	platformPlugin "github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type failingScheduleClient struct {
	saveErr    error
	disableErr error
}

func (f failingScheduleClient) SaveSchedule(context.Context, jobs.JobSchedule) error {
	return f.saveErr
}

func (f failingScheduleClient) DisableSchedule(context.Context, string) error {
	return f.disableErr
}

func initMyXLPluginWithJobs(t *testing.T, dbPath string, ciServerURL string) (*database.DB, *SQLiteRepository, *jobsqlite.Store, *jobs.Manager, *taskengine.Engine, *Plugin) {
	t.Helper()
	ctx := context.Background()

	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db %s: %v", dbPath, err)
	}
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		_ = db.Close()
		t.Fatalf("run myxl migrations: %v", err)
	}
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		_ = db.Close()
		t.Fatalf("init job schema: %v", err)
	}

	taskEngine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	if err := taskEngine.Start(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("start taskengine: %v", err)
	}

	pump := jobs.NewPersistencePump(1, 8)
	if err := pump.Start(ctx); err != nil {
		_ = taskEngine.Stop(ctx)
		_ = db.Close()
		t.Fatalf("start persistence pump: %v", err)
	}

	jobStore := jobsqlite.NewStore(db.DB)
	jobsMgr := jobs.NewManagerWithPorts(taskEngine, jobsqlite.Ports(jobStore), pump)
	if err := jobsMgr.Start(ctx); err != nil {
		_ = pump.Stop(ctx)
		_ = taskEngine.Stop(ctx)
		_ = db.Close()
		t.Fatalf("start jobs manager: %v", err)
	}

	repo := NewSQLiteRepository(db)
	cfg := DefaultClientConfig()
	if ciServerURL != "" {
		cfg.BaseCIAMURL = ciServerURL
	}
	netSvc := network.NewService(nil, nil)
	client := NewClient(cfg, repo, netSvc.ForOwner("myxl"))
	p := New(repo, client)

	gate := platformPlugin.NewCapabilityGate()
	gate.SetFailClosed(true)
	manifest := Module.Manifest()
	manifest.Name = "MyXL"
	if err := gate.RegisterManifest(manifest); err != nil {
		t.Fatalf("register manifest: %v", err)
	}
	gate.AllowPrivileged("myxl", platformPlugin.CapSecretRead)

	fs, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatalf("filesystem manager: %v", err)
	}
	scope := platformPlugin.NewScope(ctx, "myxl")

	pctx := platformPlugin.NewPluginContext(ctx, platformPlugin.ContextConfig{
		Scope:      scope,
		Owner:      "myxl",
		Gate:       gate,
		Network:    netSvc,
		Secrets:    secret.NewManager(nil),
		Files:      fs,
		Jobs:       jobsMgr,
		TaskClient: taskEngine,
	})
	if err := p.InitPlugin(pctx); err != nil {
		t.Fatalf("InitPlugin: %v", err)
	}

	t.Cleanup(func() {
		_ = scope.Close(context.Background())
		_ = jobsMgr.Stop(context.Background())
		_ = pump.Stop(context.Background())
		_ = taskEngine.Stop(context.Background())
		_ = db.Close()
	})

	return db, repo, jobStore, jobsMgr, taskEngine, p
}

func TestM4_ManifestAndJobRegistration(t *testing.T) {
	manifest := Module.Manifest()
	if !manifest.HasCapability(platformPlugin.CapJobs) || !manifest.HasCapability(platformPlugin.CapScheduler) {
		t.Fatalf("myxl manifest must declare CapJobs and CapScheduler: %+v", manifest.Capabilities)
	}

	dbPath := filepath.Join(t.TempDir(), "myxl.db")
	_, _, jobStore, jobsMgr, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	def, err := jobStore.GetDefinition(context.Background(), "plugin:myxl:token_maintenance")
	if err != nil {
		t.Fatalf("get registered job definition: %v", err)
	}
	if def.Pool != "scheduler" || def.Class != string(tasks.PriorityMaintenance) || def.Timeout != maintenanceJobTimeout || def.RetryPolicy.MaxAttempts != 1 {
		t.Fatalf("unexpected job definition: %+v", def)
	}

	// Idempotent repeated registration must not fail
	if err := p.registerMaintenanceJob(p.jobsClient); err != nil {
		t.Fatalf("repeated registerMaintenanceJob failed: %v", err)
	}
	_ = jobsMgr
}

func TestM4_ScheduleLifecycleAndRestartPreservesNextDueAt(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "myxl_restart.db")

	_, _, jobStore, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	// Enable schedule
	cfg := DefaultTokenMaintenanceConfig()
	cfg.Enabled = true
	cfg.Interval = 6 * time.Hour
	cfg.AnchorTime = "03:00"
	cfg.Timezone = "Asia/Jakarta"
	if err := p.ApplyMaintenanceConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyMaintenanceConfig enable: %v", err)
	}

	sched, err := jobStore.GetSchedule(ctx, "plugin:myxl:token_maintenance")
	if err != nil {
		t.Fatalf("get persisted schedule: %v", err)
	}
	if !sched.Enabled || sched.Recurrence != "interval" || sched.Interval != 6*time.Hour ||
		sched.Timezone != "Asia/Jakarta" || sched.MisfirePolicy != jobs.MisfireRunOnce ||
		sched.OverlapPolicy != jobs.OverlapForbid || sched.JobID != "plugin:myxl:token_maintenance" {
		t.Fatalf("unexpected persisted schedule: %+v", sched)
	}

	// Set a specific past-due next_due_at to simulate downtime misfire, then restart plugin
	pastDue := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	sched.NextDueAt = pastDue
	if err := jobStore.SaveSchedule(ctx, sched); err != nil {
		t.Fatalf("update past due schedule: %v", err)
	}

	// Simulate restart by opening a fresh Plugin + JobsManager against the same SQLite DB
	_, _, jobStore2, _, _, p2 := initMyXLPluginWithJobs(t, dbPath, "")
	afterRestart, err := jobStore2.GetSchedule(ctx, "plugin:myxl:token_maintenance")
	if err != nil {
		t.Fatalf("get schedule after restart: %v", err)
	}
	if !afterRestart.NextDueAt.Equal(pastDue) {
		t.Fatalf("restart overwrote next_due_at: got %v, want %v", afterRestart.NextDueAt, pastDue)
	}

	// Update interval & anchor
	cfg.Interval = 12 * time.Hour
	cfg.AnchorTime = "04:00"
	if err := p2.ApplyMaintenanceConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyMaintenanceConfig update: %v", err)
	}
	updated, err := jobStore2.GetSchedule(ctx, "plugin:myxl:token_maintenance")
	if err != nil || updated.Interval != 12*time.Hour || !updated.Enabled {
		t.Fatalf("unexpected updated schedule: %+v (err=%v)", updated, err)
	}

	// Disable preserves config and disables schedule
	cfg.Enabled = false
	if err := p2.ApplyMaintenanceConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyMaintenanceConfig disable: %v", err)
	}
	disabled, err := jobStore2.GetSchedule(ctx, "plugin:myxl:token_maintenance")
	if err != nil || disabled.Enabled {
		t.Fatalf("expected disabled schedule, got %+v (err=%v)", disabled, err)
	}
	reloadedCfg, err := p2.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil || reloadedCfg.Enabled || reloadedCfg.Interval != 12*time.Hour || reloadedCfg.AnchorTime != "04:00" {
		t.Fatalf("config preferences lost on disable: %+v (err=%v)", reloadedCfg, err)
	}

	// Schedule save failure rolls back config best-effort
	p2.maintMu.Lock()
	origSched := p2.scheduleClient
	p2.scheduleClient = failingScheduleClient{saveErr: errors.New("schedule store unavailable")}
	p2.maintMu.Unlock()

	cfg.Enabled = true
	if err := p2.ApplyMaintenanceConfig(ctx, cfg); err == nil {
		t.Fatal("expected ApplyMaintenanceConfig to fail when SaveSchedule fails")
	}
	rolledBack, err := p2.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil || rolledBack.Enabled {
		t.Fatalf("expected config rollback to disabled, got %+v (err=%v)", rolledBack, err)
	}
	p2.maintMu.Lock()
	p2.scheduleClient = origSched
	p2.maintMu.Unlock()
}

func TestM4_JobTriggerExecutesCoreAndRecordsHistory(t *testing.T) {
	ctx := context.Background()
	var ciamHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ciamHits.Add(1)
		_ = r.ParseForm()
		if r.FormValue("refresh_token") == "rt-ok" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "at-new",
				IDToken:      "id-new",
				RefreshToken: "rt-new",
				ExpiresIn:    3600,
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "myxl_exec.db")
	_, repo, _, jobsMgr, _, p := initMyXLPluginWithJobs(t, dbPath, srv.URL)

	now := time.Now().UTC()
	if err := repo.Save(ctx, &Account{
		MSISDN:         "628111111111",
		IsActive:       true,
		RefreshToken:   "rt-ok",
		TokenExpiresAt: now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &Account{
		MSISDN:         "628222222222",
		RefreshToken:   "rt-bad",
		TokenExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &Account{
		MSISDN:         "628333333333",
		RefreshToken:   "rt-fresh",
		TokenExpiresAt: now.Add(4 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.TriggerMaintenanceNow(ctx); err != nil {
		t.Fatalf("TriggerMaintenanceNow: %v", err)
	}

	// Wait deterministically for the triggered run to be recorded in SQLite
	deadline := time.Now().Add(5 * time.Second)
	var runs []*MaintenanceRunSummary
	for time.Now().Before(deadline) {
		var err error
		runs, err = repo.ListTokenMaintenanceRuns(ctx, 10)
		if err == nil && len(runs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 recorded maintenance run, got %d (jobs diag=%+v)", len(runs), jobsMgr.Diagnostics())
	}
	if runs[0].Status != RunStatusPartialFailure || runs[0].RefreshedCount != 1 || runs[0].FailedCount != 1 || runs[0].AlreadyFreshCount != 1 {
		t.Fatalf("unexpected run summary: %+v", runs[0])
	}
	if ciamHits.Load() != 2 {
		t.Fatalf("expected 2 CIAM refresh calls (fresh account skipped), got %d", ciamHits.Load())
	}
}

func TestM4_MaxAccountsPerCycleSequentialBound(t *testing.T) {
	ctx := context.Background()
	var ciamHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ciamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "at-new",
			IDToken:      "id-new",
			RefreshToken: "rt-new",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	_, repo := openMyXLTestDB(t)
	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = srv.URL
	client := NewClient(cfg, repo, network.NewService(srv.Client(), nil).ForOwner("myxl-test"))

	total := MaxAccountsPerCycle + 5
	for i := 0; i < total; i++ {
		if err := repo.Save(ctx, &Account{
			MSISDN:         fmt.Sprintf("628500000%03d", i),
			RefreshToken:   "rt",
			TokenExpiresAt: time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	summary, _, err := runTokenMaintenanceCycle(ctx, repo, client, DefaultTokenMaintenanceConfig(), "scheduled")
	if err != nil {
		t.Fatalf("runTokenMaintenanceCycle: %v", err)
	}
	if summary.RefreshedCount != MaxAccountsPerCycle || summary.SkippedCount != 5 {
		t.Fatalf("expected %d refreshed and 5 skipped, got %+v", MaxAccountsPerCycle, summary)
	}
	if ciamHits.Load() != int64(MaxAccountsPerCycle) {
		t.Fatalf("expected %d CIAM calls, got %d", MaxAccountsPerCycle, ciamHits.Load())
	}
}
