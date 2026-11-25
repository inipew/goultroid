package myxl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type blockingUserlogRepo struct {
	onGet func(ctx context.Context)
}

func (r *blockingUserlogRepo) InitSchema(context.Context) error { return nil }
func (r *blockingUserlogRepo) GetUserLogSetting(ctx context.Context, _ string) (string, error) {
	if r.onGet != nil {
		r.onGet(ctx)
	}
	<-ctx.Done()
	return "", ctx.Err()
}
func (r *blockingUserlogRepo) SetUserLogSetting(context.Context, string, string) error { return nil }

func TestN8_A1_CancellationAndDeadlineDuringPeerResolution(t *testing.T) {
	dests := []NotificationDestination{
		{ID: "global_log", Type: DestGlobalLog, Label: "Global Log", Enabled: true},
		{ID: "owner", Type: DestOwner, Label: "Owner PM", Enabled: true},
	}

	// 1. Cancellation during UserlogService.GetDestination(ctx) -> skipped_cancelled, not invalid_destination.
	cancelCtx, cancel := context.WithCancel(context.Background())
	ulogBlocking := userlog.NewService(&blockingUserlogRepo{
		onGet: func(context.Context) { cancel() },
	}, nil, nil)
	msgSvc := &routingTestMessageServicer{}
	resCancel := deliverMaintenanceNotifications(cancelCtx, msgSvc, 1001, nil, ulogBlocking, EventFailure, dests, "hi", 5*time.Second, 2*time.Second)
	if resCancel.InvalidDest != 0 || resCancel.Failed != 0 || resCancel.Skipped != 2 {
		t.Fatalf("cancel during resolve = %+v, want InvalidDest=0 Failed=0 Skipped=2", resCancel)
	}
	for i, r := range resCancel.PerRecipient {
		if r.Outcome != DeliverySkippedCancelled {
			t.Fatalf("recipient[%d] = %+v, want skipped_cancelled", i, r)
		}
	}

	// 2. Deadline during UserlogService.GetDestination(ctx) -> skipped_deadline, not invalid_destination.
	ulogTimeout := userlog.NewService(&blockingUserlogRepo{}, nil, nil)
	resDL := deliverMaintenanceNotifications(context.Background(), msgSvc, 1001, nil, ulogTimeout, EventFailure, dests, "hi", 30*time.Millisecond, 30*time.Millisecond)
	if resDL.InvalidDest != 0 || resDL.Failed != 0 || resDL.Skipped != 2 {
		t.Fatalf("deadline during resolve = %+v, want InvalidDest=0 Failed=0 Skipped=2", resDL)
	}
	for i, r := range resDL.PerRecipient {
		if r.Outcome != DeliverySkippedDeadline {
			t.Fatalf("recipient[%d] = %+v, want skipped_deadline", i, r)
		}
	}

	// 3. Actual unconfigured global_log and malformed destination -> invalid_destination, subsequent owner succeeds.
	ulogEmpty := userlog.NewService(&stubUserlogRepo{}, nil, nil)
	mixed := []NotificationDestination{
		{ID: "global_log", Type: DestGlobalLog, Label: "Global Log", Enabled: true},
		{ID: "bad_chan", Type: DestChannel, PeerID: 0, AccessHash: 0, Label: "Bad", Enabled: true},
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
	}
	resInv := deliverMaintenanceNotifications(context.Background(), msgSvc, 1001, nil, ulogEmpty, EventFailure, mixed, "hi", 5*time.Second, 2*time.Second)
	if resInv.InvalidDest != 2 || resInv.Sent != 1 || resInv.Failed != 0 || resInv.Skipped != 0 {
		t.Fatalf("actual invalid resolve = %+v, want InvalidDest=2 Sent=1", resInv)
	}
}

func TestN8_A2_MissingMessageServicerAndOwnerIDDiagnostics(t *testing.T) {
	ctx := context.Background()
	summary := &MaintenanceRunSummary{ID: 10, Status: RunStatusFailed, FailedCount: 1, TotalAccounts: 1}

	newPlug := func(t *testing.T) (*Plugin, *observer.ObservedLogs) {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "n8_a2.db")
		_, _, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")
		coreObs, logs := observer.New(zap.InfoLevel)
		p.SetLogger(zap.New(coreObs))
		return p, logs
	}

	// 1. Implicit Owner PM with ownerID <= 0 and nil MessageServicer factory -> failed (not silent).
	p1, logs1 := newPlug(t)
	cfgDefault := DefaultTokenMaintenanceConfig()
	p1.maybeNotifyDestinations(ctx, cfgDefault, summary, nil)
	if d := p1.MaintenanceDiagnostics(); d.NotificationsAttempted != 1 || d.NotificationsFailed != 1 || d.NotificationsInvalid != 0 {
		t.Fatalf("nil factory diagnostics = %+v, want Attempted=1 Failed=1", d)
	}
	if logs1.Len() == 0 {
		t.Fatal("expected structured failure log when MessageServicer factory is nil")
	}

	// 2. Implicit Owner PM with factory returning nil -> failed.
	p2, _ := newPlug(t)
	p2.SetOwnerNotifier(1001, func() core.MessageServicer { return nil })
	p2.maybeNotifyDestinations(ctx, cfgDefault, summary, nil)
	if d := p2.MaintenanceDiagnostics(); d.NotificationsAttempted != 1 || d.NotificationsFailed != 1 {
		t.Fatalf("factory returning nil diagnostics = %+v, want Attempted=1 Failed=1", d)
	}

	// 3. Implicit Owner PM with ownerID <= 0 and valid MessageServicer -> invalid_destination.
	p3, _ := newPlug(t)
	sendSvc3 := &routingTestMessageServicer{}
	p3.SetOwnerNotifier(0, func() core.MessageServicer { return sendSvc3 })
	p3.maybeNotifyDestinations(ctx, cfgDefault, summary, nil)
	if d := p3.MaintenanceDiagnostics(); d.NotificationsAttempted != 1 || d.NotificationsInvalid != 1 {
		t.Fatalf("missing ownerID diagnostics = %+v, want Attempted=1 Invalid=1", d)
	}

	// 4. Explicit non-owner destination succeeds even when ownerID <= 0.
	p4, _ := newPlug(t)
	sendSvc4 := &routingTestMessageServicer{}
	p4.SetOwnerNotifier(0, func() core.MessageServicer { return sendSvc4 })
	cfgExplicit := cfgDefault
	cfgExplicit.Destinations = []NotificationDestination{
		{ID: "channel:444002", Type: DestChannel, PeerID: 444002, AccessHash: 88112233, Label: "@mychan", Enabled: true},
	}
	p4.maybeNotifyDestinations(ctx, cfgExplicit, summary, nil)
	if d := p4.MaintenanceDiagnostics(); d.NotificationsSucceeded != 1 || len(sendSvc4.sends) != 1 {
		t.Fatalf("explicit dest with ownerID=0 = %+v sends=%d, want Succeeded=1", d, len(sendSvc4.sends))
	}
}

func TestN8_C_TelegramEdgeCasesAndIsolation(t *testing.T) {
	dests := []NotificationDestination{
		{ID: "deleted", Type: DestChannel, PeerID: 101, AccessHash: 111, Label: "Deleted", Enabled: true},
		{ID: "blocked", Type: DestBotPM, PeerID: 102, AccessHash: 222, Label: "Blocked", Enabled: true},
		{ID: "forbidden", Type: DestGroup, PeerID: 103, Label: "Group", Enabled: true},
		{ID: "ok", Type: DestOwner, Label: "Owner", Enabled: true},
	}
	svc := &routingTestMessageServicer{
		errByPeerFn: func(p tg.InputPeerClass) error {
			switch v := p.(type) {
			case *tg.InputPeerChannel:
				return errors.New("PEER_ID_INVALID")
			case *tg.InputPeerUser:
				if v.UserID == 102 {
					return errors.New("USER_IS_BLOCKED")
				}
				return nil
			case *tg.InputPeerChat:
				return errors.New("CHAT_WRITE_FORBIDDEN")
			default:
				return errors.New("unknown")
			}
		},
	}
	res := deliverMaintenanceNotifications(context.Background(), svc, 1001, nil, nil, EventPartialFailure, dests, "report", 5*time.Second, 2*time.Second)
	if res.Total != 4 || res.Failed != 3 || res.Sent != 1 || res.InvalidDest != 0 || res.Skipped != 0 {
		t.Fatalf("edge cases batch = %+v, want Total=4 Failed=3 Sent=1", res)
	}
	if res.PerRecipient[3].Outcome != DeliverySent {
		t.Fatalf("final owner recipient = %+v, want sent", res.PerRecipient[3])
	}
}

func TestN8_D_SQLiteContentionAndCrossSurfaceConcurrency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n8_contention.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	sendSvc := &routingTestMessageServicer{}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })
	svc := p.maintenanceService()
	perms := core.NewPermissions(1001, nil)

	const workers = 8
	const iters = 20
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters)

	for w := 0; w < workers; w++ {
		id := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch (id + i) % 5 {
				case 0:
					if _, err := svc.SetNotifyEvent(ctx, EventCompletion, i%2 == 0); err != nil {
						errCh <- err
					}
				case 1:
					_, _ = svc.AddNotificationDestination(ctx, NotificationDestination{
						ID:         fmt.Sprintf("bot_pm:%d", 800000+((id+i)%7)),
						Type:       DestBotPM,
						PeerID:     int64(800000 + ((id + i) % 7)),
						AccessHash: int64(900000 + ((id + i) % 7)),
						Label:      "Bot",
						Enabled:    true,
					})
				case 2:
					_, _ = svc.RemoveNotificationDestination(ctx, fmt.Sprintf("bot_pm:%d", 800000+(i%7)))
				case 3:
					if _, err := svc.GetStatus(ctx); err != nil {
						errCh <- err
					}
				case 4:
					c, _ := newTestCoreContext(1001, perms, "schedule", "notify", "status")
					if err := p.handleMyXL(c); err != nil {
						errCh <- err
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("unexpected contention error: %v", err)
	}

	finalCfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("GetTokenMaintenanceConfig() error = %v", err)
	}
	if err := ValidateTokenMaintenanceConfig(*finalCfg); err != nil {
		t.Fatalf("final config invalid after contention: %v", err)
	}
	if len(finalCfg.Destinations) > MaxNotificationDestinations {
		t.Fatalf("destination cap violated: %d > %d", len(finalCfg.Destinations), MaxNotificationDestinations)
	}
}

func TestN8_E_BoundedSoakAndGoroutineStability(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n8_soak.db")
	_, _, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	sendSvc := &routingTestMessageServicer{}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })
	svc := p.maintenanceService()

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	const iterations = 100
	summary := &MaintenanceRunSummary{ID: 1, Status: RunStatusFailed, FailedCount: 1, TotalAccounts: 1}
	for i := 0; i < iterations; i++ {
		if _, err := svc.SetNotifyEvent(ctx, EventFailure, true); err != nil {
			t.Fatalf("iter %d SetNotifyEvent: %v", i, err)
		}
		status, err := svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("iter %d GetStatus: %v", i, err)
		}
		p.maybeNotifyDestinations(ctx, status.Config, summary, nil)
	}

	d := p.MaintenanceDiagnostics()
	if d.NotificationsAttempted != iterations || d.NotificationsSucceeded != iterations {
		t.Fatalf("soak diagnostics = %+v, want Attempted=%d Succeeded=%d", d, iterations, iterations)
	}

	runtime.GC()
	afterGoroutines := runtime.NumGoroutine()
	if afterGoroutines > baseGoroutines+4 {
		t.Fatalf("goroutine growth after soak: before=%d after=%d", baseGoroutines, afterGoroutines)
	}
}
