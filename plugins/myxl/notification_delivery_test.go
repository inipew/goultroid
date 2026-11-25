package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/services/userlog"
)

type stubUserlogRepo struct {
	settings map[string]string
}

func (r *stubUserlogRepo) InitSchema(context.Context) error { return nil }

func (r *stubUserlogRepo) GetUserLogSetting(_ context.Context, key string) (string, error) {
	return r.settings[key], nil
}

func (r *stubUserlogRepo) SetUserLogSetting(_ context.Context, key, val string) error {
	if r.settings == nil {
		r.settings = make(map[string]string)
	}
	r.settings[key] = val
	return nil
}

type recordedSend struct {
	Peer tg.InputPeerClass
	Text string
}

type routingTestMessageServicer struct {
	core.MockTelegramServicer
	sends       []recordedSend
	errByPeerFn func(tg.InputPeerClass) error
	blockFn     func(context.Context, tg.InputPeerClass) error
}

func (m *routingTestMessageServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if m.blockFn != nil {
		if err := m.blockFn(ctx, peer); err != nil {
			return nil, err
		}
	}
	if m.errByPeerFn != nil {
		if err := m.errByPeerFn(peer); err != nil {
			return nil, err
		}
	}
	m.sends = append(m.sends, recordedSend{Peer: peer, Text: text})
	return &tg.Message{ID: len(m.sends), Message: text}, nil
}

func TestN4_ResolveNotificationPeer_AllSixTypes(t *testing.T) {
	ctx := context.Background()
	ulog := userlog.NewService(&stubUserlogRepo{}, nil, nil)
	if err := ulog.SetDestination(ctx, userlog.LogDestination{
		Type:       userlog.LogDestinationChannel,
		ID:         888999,
		AccessHash: 555666,
		Title:      "Global Logs",
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		dest     NotificationDestination
		wantPeer tg.InputPeerClass
	}{
		{
			name:     "owner",
			dest:     NotificationDestination{ID: "owner", Type: DestOwner},
			wantPeer: &tg.InputPeerUser{UserID: 1001},
		},
		{
			name:     "group",
			dest:     NotificationDestination{ID: "grp", Type: DestGroup, PeerID: 12345},
			wantPeer: &tg.InputPeerChat{ChatID: 12345},
		},
		{
			name:     "supergroup",
			dest:     NotificationDestination{ID: "sg", Type: DestSupergroup, PeerID: 987654, AccessHash: 111},
			wantPeer: &tg.InputPeerChannel{ChannelID: 987654, AccessHash: 111},
		},
		{
			name:     "channel",
			dest:     NotificationDestination{ID: "ch", Type: DestChannel, PeerID: 445566, AccessHash: 222},
			wantPeer: &tg.InputPeerChannel{ChannelID: 445566, AccessHash: 222},
		},
		{
			name:     "bot_pm",
			dest:     NotificationDestination{ID: "bot", Type: DestBotPM, PeerID: 334455, AccessHash: 333},
			wantPeer: &tg.InputPeerUser{UserID: 334455, AccessHash: 333},
		},
		{
			name:     "global_log",
			dest:     NotificationDestination{ID: "glog", Type: DestGlobalLog},
			wantPeer: &tg.InputPeerChannel{ChannelID: 888999, AccessHash: 555666},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer, err := resolveNotificationPeer(ctx, tc.dest, 1001, nil, ulog)
			if err != nil {
				t.Fatalf("resolve %s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(peer, tc.wantPeer) {
				t.Fatalf("resolve %s = %#v, want %#v", tc.name, peer, tc.wantPeer)
			}
		})
	}

	// Unconfigured global_log and invalid inputs fail safely without panic
	emptyUlog := userlog.NewService(&stubUserlogRepo{}, nil, nil)
	if _, err := resolveNotificationPeer(ctx, NotificationDestination{ID: "glog", Type: DestGlobalLog}, 1001, nil, emptyUlog); !errors.Is(err, errGlobalLogMissing) {
		t.Fatalf("expected errGlobalLogMissing, got %v", err)
	}
	if _, err := resolveNotificationPeer(ctx, NotificationDestination{ID: "glog", Type: DestGlobalLog}, 1001, nil, nil); !errors.Is(err, errGlobalLogMissing) {
		t.Fatalf("expected errGlobalLogMissing for nil service, got %v", err)
	}
	if _, err := resolveNotificationPeer(ctx, NotificationDestination{ID: "sg", Type: DestSupergroup, PeerID: 123}, 1001, nil, nil); !errors.Is(err, errDestinationInvalid) {
		t.Fatalf("expected errDestinationInvalid for missing AccessHash, got %v", err)
	}
}

func TestN4_ClassifyMaintenanceNotificationEvent(t *testing.T) {
	cfg := DefaultTokenMaintenanceConfig() // Completion=false, Partial=true, Failure=true, Cancelled=false

	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusCompleted, RefreshedCount: 2}); ok || ev != EventCompletion {
		t.Fatalf("completion with default off: got (%s, %v)", ev, ok)
	}
	cfg.NotifyOnCompletion = true
	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusCompleted, RefreshedCount: 0}); ok || ev != EventNone {
		t.Fatalf("no_op must never notify even when completion=true: got (%s, %v)", ev, ok)
	}
	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusCompleted, RefreshedCount: 1}); !ok || ev != EventCompletion {
		t.Fatalf("completion with refreshed>0: got (%s, %v)", ev, ok)
	}

	// Partial failure is independent of NotifyOnFailure
	cfg.NotifyOnFailure = false
	cfg.NotifyOnPartialFailure = true
	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusPartialFailure}); !ok || ev != EventPartialFailure {
		t.Fatalf("partial_failure with NotifyOnFailure=false: got (%s, %v)", ev, ok)
	}
	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusFailed}); ok || ev != EventFailure {
		t.Fatalf("failure with NotifyOnFailure=false: got (%s, %v)", ev, ok)
	}

	// Cancelled default off
	if ev, ok := classifyMaintenanceNotificationEvent(cfg, &MaintenanceRunSummary{Status: RunStatusCancelled}); ok || ev != EventCancelled {
		t.Fatalf("cancelled default off: got (%s, %v)", ev, ok)
	}
}

func TestN4_MultiDestinationDelivery_IsolationAndFallback(t *testing.T) {
	ctx := context.Background()
	emptyUlog := userlog.NewService(&stubUserlogRepo{}, nil, nil)

	svc := &routingTestMessageServicer{
		errByPeerFn: func(p tg.InputPeerClass) error {
			if ch, ok := p.(*tg.InputPeerChannel); ok && ch.ChannelID == 111 {
				return errors.New("CHAT_WRITE_FORBIDDEN")
			}
			return nil
		},
	}

	dests := []NotificationDestination{
		{ID: "ch-fail", Type: DestChannel, Label: "Forbidden Ch", Enabled: true, PeerID: 111, AccessHash: 1},
		{ID: "glog-missing", Type: DestGlobalLog, Label: "Unset Log", Enabled: true},
		{ID: "grp-ok", Type: DestGroup, Label: "Ops Group", Enabled: true, PeerID: 222},
		{ID: "owner-ok", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "bot-disabled", Type: DestBotPM, Label: "Disabled Bot", Enabled: false, PeerID: 333, AccessHash: 3},
	}

	cfg := DefaultTokenMaintenanceConfig()
	cfg.Destinations = dests
	active := effectiveNotificationDestinations(cfg)
	if len(active) != 4 {
		t.Fatalf("expected 4 enabled destinations, got %d", len(active))
	}

	res := deliverMaintenanceNotifications(ctx, svc, 1001, nil, emptyUlog, EventFailure, active, "hello", 15*time.Second, 5*time.Second)
	if res.Sent != 2 || res.Failed != 1 || res.InvalidDest != 1 || res.Skipped != 0 {
		t.Fatalf("unexpected batch result: %+v", res)
	}
	if len(svc.sends) != 2 {
		t.Fatalf("expected 2 successful sends despite earlier failures, got %d", len(svc.sends))
	}

	// Nil MessageServicer does not panic
	nilRes := deliverMaintenanceNotifications(ctx, nil, 1001, nil, nil, EventFailure, effectiveNotificationDestinations(DefaultTokenMaintenanceConfig()), "hello", 15*time.Second, 5*time.Second)
	if nilRes.Failed != 1 || nilRes.Sent != 0 {
		t.Fatalf("expected nil MessageServicer to record 1 failed, got %+v", nilRes)
	}
}

func TestN4_DeadlineAndPerDestinationTimeout(t *testing.T) {
	ctx := context.Background()
	baseGoroutines := runtime.NumGoroutine()

	svc := &routingTestMessageServicer{
		blockFn: func(destCtx context.Context, p tg.InputPeerClass) error {
			// Slow destination #1 & #2 block until destCtx deadline; #3 succeeds immediately if reached
			if ch, ok := p.(*tg.InputPeerChat); ok && ch.ChatID <= 2 {
				<-destCtx.Done()
				return destCtx.Err()
			}
			return nil
		},
	}

	dests := []NotificationDestination{
		{ID: "d1", Type: DestGroup, Enabled: true, PeerID: 1},
		{ID: "d2", Type: DestGroup, Enabled: true, PeerID: 2},
		{ID: "d3", Type: DestGroup, Enabled: true, PeerID: 3},
	}

	// Per-dest 35ms, total budget 60ms:
	// d1 times out at ~35ms; d2 gets remaining ~25ms and hits total deadline; d3 is skipped!
	res := deliverMaintenanceNotifications(ctx, svc, 1001, nil, nil, EventFailure, dests, "hello", 60*time.Millisecond, 35*time.Millisecond)
	if res.Failed != 2 || res.Skipped != 1 || res.Sent != 0 {
		t.Fatalf("expected 2 failed (timeout) + 1 skipped (total deadline), got %+v", res)
	}
	if res.PerRecipient[2].Outcome != DeliverySkippedDeadline {
		t.Fatalf("expected d3 skipped_deadline, got %+v", res.PerRecipient[2])
	}
	if res.Duration > 250*time.Millisecond {
		t.Fatalf("total budget exceeded significantly: %v", res.Duration)
	}
	if after := runtime.NumGoroutine(); after > baseGoroutines+1 {
		t.Fatalf("goroutine leak detected: before=%d after=%d", baseGoroutines, after)
	}
}

func TestN4_MaintenanceIsolation_DeliveryFailureDoesNotAlterRunOutcome(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("refresh_token") == "rt-ok" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "at-new-secret",
				IDToken:      "id-new-secret",
				RefreshToken: "rt-new-secret",
				ExpiresIn:    3600,
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "n4_isolation.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, srv.URL)

	notifySvc := &routingTestMessageServicer{
		errByPeerFn: func(p tg.InputPeerClass) error {
			return errors.New("telegram rpc failed")
		},
	}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return notifySvc })

	cfg := DefaultTokenMaintenanceConfig()
	cfg.NotifyOnPartialFailure = true
	cfg.Destinations = []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "grp", Type: DestGroup, Label: "Ops", Enabled: true, PeerID: 777},
	}
	if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := repo.Save(ctx, &Account{MSISDN: "628111111111", IsActive: true, RefreshToken: "rt-ok", TokenExpiresAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &Account{MSISDN: "628222222222", RefreshToken: "rt-bad", TokenExpiresAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}

	if err := p.handleTokenMaintenanceJob(ctx, jobs.JobDefinition{ID: maintenanceJobID}); err != nil {
		t.Fatalf("delivery failure must not fail job handler: %v", err)
	}

	runs, err := repo.ListTokenMaintenanceRuns(ctx, 5)
	if err != nil || len(runs) != 1 || runs[0].Status != RunStatusPartialFailure {
		t.Fatalf("expected persisted partial_failure run, got %+v (err=%v)", runs, err)
	}
	diag := p.MaintenanceDiagnostics()
	if diag.NotificationsAttempted != 2 || diag.NotificationsFailed != 2 || diag.RunsPartialFailure != 1 {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}

	// Verify formatted message never leaks tokens or raw MSISDN
	msg := formatMaintenanceNotificationHTML(runs[0], []AccountMaintenanceOutcome{{MSISDN: "628222222222", Status: StatusAuthenticationRejected}})
	for _, forbidden := range []string{"at-new-secret", "id-new-secret", "rt-new-secret", "628222222222"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("notification leaked %q in %q", forbidden, msg)
		}
	}
}
