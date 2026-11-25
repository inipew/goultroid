package myxl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestN7_DeliveryCancellationAndTimeoutSemantics(t *testing.T) {
	dests := []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "bot_pm:555001", Type: DestBotPM, PeerID: 555001, AccessHash: 99112233, Label: "@alertbot", Enabled: true},
		{ID: "channel:444002", Type: DestChannel, PeerID: 444002, AccessHash: 88112233, Label: "@mychan", Enabled: true},
	}

	// 1. Pre-cancelled parentCtx marks all destinations skipped_cancelled (never failed).
	preCtx, preCancel := context.WithCancel(context.Background())
	preCancel()
	msgSvc := &routingTestMessageServicer{}
	res := deliverMaintenanceNotifications(preCtx, msgSvc, 1001, nil, nil, EventFailure, dests, "hi", 15*time.Second, 5*time.Second)
	if res.Sent != 0 || res.Failed != 0 || res.Skipped != 3 {
		t.Fatalf("pre-cancelled batch = %+v, want Sent=0 Failed=0 Skipped=3", res)
	}
	for i, r := range res.PerRecipient {
		if r.Outcome != DeliverySkippedCancelled || r.Reason != "context_cancelled" {
			t.Fatalf("recipient[%d] = %+v, want skipped_cancelled/context_cancelled", i, r)
		}
	}

	// 2. Cancelled during delivery: 1st succeeds, 2nd & 3rd are skipped_cancelled (not failed).
	midCtx, midCancel := context.WithCancel(context.Background())
	var calls int
	midSvc := &routingTestMessageServicer{
		blockFn: func(ctx context.Context, _ tg.InputPeerClass) error {
			calls++
			if calls == 2 {
				midCancel()
				return ctx.Err()
			}
			return nil
		},
	}
	resMid := deliverMaintenanceNotifications(midCtx, midSvc, 1001, nil, nil, EventFailure, dests, "hi", 15*time.Second, 5*time.Second)
	if resMid.Sent != 1 || resMid.Failed != 0 || resMid.Skipped != 2 {
		t.Fatalf("mid-cancelled batch = %+v, want Sent=1 Failed=0 Skipped=2", resMid)
	}
	if resMid.PerRecipient[1].Outcome != DeliverySkippedCancelled || resMid.PerRecipient[2].Outcome != DeliverySkippedCancelled {
		t.Fatalf("expected skipped_cancelled for remaining recipients, got %+v", resMid.PerRecipient)
	}

	// 3. Per-destination timeout does not stop subsequent destination when batch budget remains.
	timeoutSvc := &routingTestMessageServicer{
		blockFn: func(ctx context.Context, peer tg.InputPeerClass) error {
			if u, ok := peer.(*tg.InputPeerUser); ok && u.UserID == 1001 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	resTO := deliverMaintenanceNotifications(context.Background(), timeoutSvc, 1001, nil, nil, EventFailure, dests[:2], "hi", 500*time.Millisecond, 25*time.Millisecond)
	if resTO.Failed != 1 || resTO.Sent != 1 {
		t.Fatalf("per-dest timeout batch = %+v, want Failed=1 Sent=1", resTO)
	}
	if resTO.PerRecipient[0].Outcome != DeliveryFailed || resTO.PerRecipient[0].Reason != "timeout" {
		t.Fatalf("first recipient = %+v, want failed/timeout", resTO.PerRecipient[0])
	}
	if resTO.PerRecipient[1].Outcome != DeliverySent {
		t.Fatalf("second recipient = %+v, want sent", resTO.PerRecipient[1])
	}
}

func TestN7_ObservabilityCountersLogsAndRedaction(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n7_obs.db")
	_, _, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	coreObs, logs := observer.New(zap.InfoLevel)
	p.SetLogger(zap.New(coreObs))

	sendSvc := &routingTestMessageServicer{
		errByPeerFn: func(peer tg.InputPeerClass) error {
			if c, ok := peer.(*tg.InputPeerChannel); ok && c.ChannelID == 444002 {
				return errors.New("CHAT_WRITE_FORBIDDEN")
			}
			return nil
		},
	}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })

	cfg := DefaultTokenMaintenanceConfig()
	cfg.NotifyOnFailure = true
	cfg.NotifyOnCompletion = true
	cfg.Destinations = []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "bot_pm:555001", Type: DestBotPM, PeerID: 555001, AccessHash: 99112233, Label: "Bot", Enabled: false},
		{ID: "channel:444002", Type: DestChannel, PeerID: 444002, AccessHash: 88112233, Label: "Chan", Enabled: true},
	}

	// no_op never notifies or increments counters.
	p.maybeNotifyDestinations(ctx, cfg, &MaintenanceRunSummary{ID: 1, Status: RunStatusCompleted, RefreshedCount: 0}, nil)
	if d := p.MaintenanceDiagnostics(); d.NotificationsAttempted != 0 || d.NotificationsSkipped != 0 {
		t.Fatalf("no_op altered notification counters: %+v", d)
	}

	// Partial notification outcome: 1 sent, 1 failed, 1 disabled skipped.
	p.maybeNotifyDestinations(ctx, cfg, &MaintenanceRunSummary{ID: 2, Status: RunStatusFailed, FailedCount: 1, TotalAccounts: 1}, nil)
	d := p.MaintenanceDiagnostics()
	if d.NotificationsAttempted != 2 || d.NotificationsSucceeded != 1 || d.NotificationsFailed != 1 || d.NotificationsSkipped != 1 || d.NotificationsInvalid != 0 {
		t.Fatalf("unexpected diagnostics counters: %+v", d)
	}

	// All disabled destinations: skipped=3, attempted unchanged.
	cfgAllDisabled := cfg
	cfgAllDisabled.Destinations = []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: false},
		{ID: "bot_pm:555001", Type: DestBotPM, PeerID: 555001, AccessHash: 99112233, Label: "Bot", Enabled: false},
		{ID: "channel:444002", Type: DestChannel, PeerID: 444002, AccessHash: 88112233, Label: "Chan", Enabled: false},
	}
	p.maybeNotifyDestinations(ctx, cfgAllDisabled, &MaintenanceRunSummary{ID: 3, Status: RunStatusFailed, FailedCount: 1, TotalAccounts: 1}, nil)
	d2 := p.MaintenanceDiagnostics()
	if d2.NotificationsAttempted != 2 || d2.NotificationsSkipped != 4 {
		t.Fatalf("expected Skipped=4 and Attempted=2, got %+v", d2)
	}

	// Verify structured logs never leak AccessHash, raw PeerIDs, tokens, or credentials across ALL field types.
	sentinels := []string{"99112233", "88112233", "555001", "444002", "rt-secret", "at-secret", "ciam-secret", "Authorization"}
	for _, entry := range logs.All() {
		assertNoSentinelsInLogEntry(t, entry, sentinels)
	}
	// Self-check: ensure assertNoSentinelsInLogEntry detects numeric zap.Int64 and ByteString sentinels.
	if !logFieldHasSentinel(zap.Int64("access_hash", 99112233), sentinels) {
		t.Fatal("sentinel checker failed to detect numeric Int64 field")
	}
	if !logFieldHasSentinel(zap.ByteString("raw", []byte("at-secret")), sentinels) {
		t.Fatal("sentinel checker failed to detect ByteString field")
	}
}

func logFieldHasSentinel(f zapcore.Field, sentinels []string) bool {
	var vals []string
	switch f.Type {
	case zapcore.StringType, zapcore.ErrorType, zapcore.StringerType:
		vals = append(vals, f.String)
		if f.Interface != nil {
			vals = append(vals, fmt.Sprint(f.Interface))
		}
	case zapcore.ByteStringType:
		if b, ok := f.Interface.([]byte); ok {
			vals = append(vals, string(b))
		}
	case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
		zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type,
		zapcore.DurationType:
		vals = append(vals, fmt.Sprintf("%d", f.Integer))
	default:
		if f.String != "" {
			vals = append(vals, f.String)
		}
		if f.Interface != nil {
			vals = append(vals, fmt.Sprint(f.Interface))
		}
	}
	for _, v := range vals {
		for _, s := range sentinels {
			if strings.Contains(v, s) {
				return true
			}
		}
	}
	return false
}

func assertNoSentinelsInLogEntry(t *testing.T, entry observer.LoggedEntry, sentinels []string) {
	t.Helper()
	for _, s := range sentinels {
		if strings.Contains(entry.Message, s) {
			t.Fatalf("structured log message leaked sensitive sentinel %q: %q", s, entry.Message)
		}
	}
	for _, f := range entry.Context {
		if logFieldHasSentinel(f, sentinels) {
			t.Fatalf("structured log field %q (type=%v) leaked sensitive sentinel", f.Key, f.Type)
		}
	}
}

func TestN7_CancelledScheduledRunTriggersNotificationAndConcurrentCapIsEnforced(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n7_sched.db")
	runCtx, cancel := context.WithCancel(ctx)
	ciSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer ciSrv.Close()

	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, ciSrv.URL)

	sendSvc := &routingTestMessageServicer{}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })

	cfg := DefaultTokenMaintenanceConfig()
	cfg.Enabled = true
	cfg.NotifyOnCancelled = true
	if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		t.Fatal(err)
	}

	// Seed an account with stale token so runTokenMaintenanceCycle hits cancelled ctx mid-run.
	now := time.Now().UTC()
	if err := repo.Save(ctx, &Account{
		MSISDN:         "6281911112222",
		RefreshToken:   "rt-secret",
		AccessToken:    "at-secret",
		TokenExpiresAt: now.Add(5 * time.Minute),
		UpdatedAt:      now,
	}); err != nil {
		t.Fatal(err)
	}

	err := p.handleTokenMaintenanceJob(runCtx, jobs.JobDefinition{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handleTokenMaintenanceJob() err = %v, want context.Canceled", err)
	}
	if len(sendSvc.sends) != 1 || !strings.Contains(sendSvc.sends[0].Text, "Cancelled") {
		t.Fatalf("expected 1 cancelled notification, got %+v", sendSvc.sends)
	}

	// TestNotificationDestination must NOT create a maintenance run.
	svc := p.maintenanceService()
	if err := svc.TestNotificationDestination(ctx, "owner"); err != nil {
		t.Fatalf("TestNotificationDestination(owner) error = %v", err)
	}
	runs, err := repo.ListTokenMaintenanceRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if strings.Contains(strings.ToLower(r.TriggerSource), "test") {
			t.Fatalf("test-send created a maintenance run: %+v", r)
		}
	}

	// Concurrent destination additions must strictly enforce MaxNotificationDestinations (5).
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.AddNotificationDestination(ctx, NotificationDestination{
				ID:         fmt.Sprintf("bot_pm:%d", 700000+idx),
				Type:       DestBotPM,
				PeerID:     int64(700000 + idx),
				AccessHash: int64(800000 + idx),
				Label:      "Bot",
				Enabled:    true,
			})
		}()
	}
	wg.Wait()

	finalCfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalCfg.Destinations) != MaxNotificationDestinations {
		t.Fatalf("expected exact cap %d destinations under concurrency, got %d", MaxNotificationDestinations, len(finalCfg.Destinations))
	}
}
