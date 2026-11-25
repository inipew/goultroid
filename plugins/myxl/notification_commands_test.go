package myxl

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
)

type mapPeerResolver struct {
	peers map[string]tg.InputPeerClass
}

func (r *mapPeerResolver) Resolve(_ context.Context, ref string) (tg.InputPeerClass, error) {
	if p, ok := r.peers[ref]; ok {
		return p, nil
	}
	return nil, core.ErrNotFound
}

func (r *mapPeerResolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	p, err := r.Resolve(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	if u, ok := p.(*tg.InputPeerUser); ok {
		return u, u.UserID, nil
	}
	return nil, 0, core.ErrNotFound
}

func (r *mapPeerResolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	return r.Resolve(ctx, ref)
}

func newTestCoreContextWithPeer(senderID int64, perms *core.Permissions, peer tg.InputPeerClass, chat *core.Chat, args ...string) (*core.Context, *mockTgService) {
	tgSvc := &mockTgService{}
	return &core.Context{
		Ctx:      context.Background(),
		Telegram: core.TelegramCapabilities{Messages: tgSvc},
		Perms:    perms,
		PeerID:   peer,
		Message:  &core.Message{ID: 1},
		Chat:     chat,
		Sender:   &core.User{ID: senderID},
		Args:     args,
	}, tgSvc
}

func TestN5_NotifyCommands_ParsingPermissionsAndLifecycle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n5_cmd.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	sendSvc := &routingTestMessageServicer{}
	ulog := userlog.NewService(&stubUserlogRepo{}, nil, nil)
	if err := ulog.SetDestination(ctx, userlog.LogDestination{
		Type: userlog.LogDestinationChannel, ID: 999888, AccessHash: 777666, Title: "Logs",
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &mapPeerResolver{
		peers: map[string]tg.InputPeerClass{
			"@alertbot": &tg.InputPeerUser{UserID: 555001, AccessHash: 99112233},
			"@mychan":   &tg.InputPeerChannel{ChannelID: 444002, AccessHash: 88112233},
		},
	}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })
	p.SetNotificationServices(resolver, ulog)

	perms := core.NewPermissions(1001, nil)

	// 1. Non-owner rejected on all notify operations without leaking destination info
	for _, args := range [][]string{
		{"schedule", "notify", "status"},
		{"schedule", "notify", "completion", "on"},
		{"schedule", "notify", "destination", "list"},
		{"schedule", "notify", "destination", "add", "owner"},
		{"schedule", "notify", "destination", "remove", "owner"},
		{"schedule", "notify", "destination", "test", "owner"},
	} {
		c, tgOut := newTestCoreContext(9999, perms, args...)
		if err := p.handleMyXL(c); err != nil {
			t.Fatalf("non-owner %v: %v", args, err)
		}
		if !strings.Contains(tgOut.sent, "Hanya owner") {
			t.Fatalf("expected owner rejection for %v, got %q", args, tgOut.sent)
		}
	}

	// 2. Status shows defaults and implicit Owner PM fallback
	cStatus, tgStatus := newTestCoreContext(1001, perms, "schedule", "notify", "status")
	if err := p.handleMyXL(cStatus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgStatus.sent, "Fallback implicit aktif") || !strings.Contains(tgStatus.sent, "0/5") {
		t.Fatalf("unexpected initial notify status: %q", tgStatus.sent)
	}

	// 3. Event preferences management
	for _, args := range [][]string{
		{"schedule", "notify", "completion", "on"},
		{"schedule", "notify", "partial_failure", "off"},
		{"schedule", "notify", "failure", "off"},
		{"schedule", "notify", "cancelled", "on"},
	} {
		c, _ := newTestCoreContext(1001, perms, args...)
		if err := p.handleMyXL(c); err != nil {
			t.Fatalf("event toggle %v: %v", args, err)
		}
	}
	cfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NotifyOnCompletion || cfg.NotifyOnPartialFailure || cfg.NotifyOnFailure || !cfg.NotifyOnCancelled {
		t.Fatalf("event preferences mismatch: %+v", cfg)
	}

	// Verify restart persistence (reopen same SQLite DB)
	_, repoReloaded, _, _, _, pReloaded := initMyXLPluginWithJobs(t, dbPath, "")
	reloadedCfg, err := repoReloaded.GetTokenMaintenanceConfig(ctx)
	if err != nil || !reloadedCfg.NotifyOnCancelled || !reloadedCfg.NotifyOnCompletion {
		t.Fatalf("expected cancelled/completion persisted across restart: %+v (err=%v)", reloadedCfg, err)
	}
	_ = pReloaded

	// Invalid event / value / no_op rejected
	for _, args := range [][]string{
		{"schedule", "notify", "no_op", "on"},
		{"schedule", "notify", "completion", "maybe"},
		{"schedule", "notify", "unknown", "on"},
		{"schedule", "notify", "destination", "add", "webhook"},
		{"schedule", "notify", "destination", "remove", "missing-id"},
	} {
		c, tgOut := newTestCoreContext(1001, perms, args...)
		_ = p.handleMyXL(c)
		if !strings.Contains(tgOut.sent, "Format") && !strings.Contains(tgOut.sent, "Gagal") && !strings.Contains(tgOut.sent, "Nilai") {
			t.Fatalf("expected error/usage for %v, got %q", args, tgOut.sent)
		}
	}

	// 4. Add 5 destinations across types (owner, global_log, group from chat, bot_pm, channel)
	cAddOwner, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "owner")
	_ = p.handleMyXL(cAddOwner)

	cAddGLog, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "global_log")
	_ = p.handleMyXL(cAddGLog)

	cAddGrp, _ := newTestCoreContextWithPeer(1001, perms, &tg.InputPeerChat{ChatID: 123456}, &core.Chat{ID: 123456, Type: "group", Title: "Ops Group"}, "schedule", "notify", "destination", "add", "group")
	_ = p.handleMyXL(cAddGrp)

	cAddBot, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "bot_pm", "@alertbot")
	_ = p.handleMyXL(cAddBot)

	cAddChan, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "channel", "@mychan")
	_ = p.handleMyXL(cAddChan)

	cfg, _ = repo.GetTokenMaintenanceConfig(ctx)
	if len(cfg.Destinations) != 5 {
		t.Fatalf("expected 5 registered destinations, got %d (%+v)", len(cfg.Destinations), cfg.Destinations)
	}

	// 6th destination rejected by max 5 limit
	cAdd6, tgAdd6 := newTestCoreContextWithPeer(1001, perms, &tg.InputPeerChannel{ChannelID: 777777, AccessHash: 112233}, &core.Chat{ID: -100777777, Type: "supergroup", Title: "Extra SG"}, "schedule", "notify", "destination", "add", "supergroup")
	_ = p.handleMyXL(cAdd6)
	if !strings.Contains(tgAdd6.sent, "maximum is 5") {
		t.Fatalf("expected max 5 error, got %q", tgAdd6.sent)
	}

	// Status never leaks AccessHash
	cList, tgList := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "list")
	_ = p.handleMyXL(cList)
	for _, secretHash := range []string{"99112233", "88112233", "777666"} {
		if strings.Contains(tgList.sent, secretHash) {
			t.Fatalf("destination list leaked AccessHash %q in %q", secretHash, tgList.sent)
		}
	}

	// Test-send works without creating maintenance runs
	beforeSends := len(sendSvc.sends)
	cTest, tgTest := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "test", "ch-444002")
	_ = p.handleMyXL(cTest)
	if !strings.Contains(tgTest.sent, "Test notifikasi berhasil") || len(sendSvc.sends) != beforeSends+1 {
		t.Fatalf("expected test-send success, got %q (sends=%d)", tgTest.sent, len(sendSvc.sends))
	}
	runs, _ := repo.ListTokenMaintenanceRuns(ctx, 10)
	if len(runs) != 0 {
		t.Fatalf("test-send must not create maintenance runs, got %d", len(runs))
	}

	// Disable, enable, remove all -> back to empty [] with fallback
	cDisable, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "disable", "grp-123456")
	_ = p.handleMyXL(cDisable)
	cfg, _ = repo.GetTokenMaintenanceConfig(ctx)
	if cfg.Destinations[2].Enabled {
		t.Fatal("expected grp-123456 disabled")
	}
	for _, id := range []string{"owner", "global_log", "grp-123456", "bot-555001", "ch-444002"} {
		cRm, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "remove", id)
		_ = p.handleMyXL(cRm)
	}
	cfg, _ = repo.GetTokenMaintenanceConfig(ctx)
	if len(cfg.Destinations) != 0 {
		t.Fatalf("expected empty destinations after removing all, got %+v", cfg.Destinations)
	}
}

func TestN5_PreSaveTestFailureRejectsDestinationAndConcurrencySafety(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n5_presave.db")
	_, repo, _, _, _, p := initMyXLPluginWithJobs(t, dbPath, "")

	failSvc := &routingTestMessageServicer{
		errByPeerFn: func(tg.InputPeerClass) error { return errors.New("CHAT_WRITE_FORBIDDEN") },
	}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return failSvc })

	perms := core.NewPermissions(1001, nil)
	cAddFail, tgOut := newTestCoreContextWithPeer(1001, perms, &tg.InputPeerChannel{ChannelID: 654321, AccessHash: 999}, &core.Chat{ID: -100654321, Type: "channel", Title: "ReadOnly"}, "schedule", "notify", "destination", "add", "channel")
	_ = p.handleMyXL(cAddFail)
	if !strings.Contains(tgOut.sent, "Gagal menambahkan destination") {
		t.Fatalf("expected pre-save verification rejection, got %q", tgOut.sent)
	}
	cfg, _ := repo.GetTokenMaintenanceConfig(ctx)
	if len(cfg.Destinations) != 0 {
		t.Fatalf("failed pre-save verification must not persist destination: %+v", cfg.Destinations)
	}

	// Concurrent SetNotifyEvent calls do not lose updates
	svc := p.maintenanceService()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = svc.SetNotifyEvent(ctx, EventCompletion, true)
	}()
	go func() {
		defer wg.Done()
		_, _ = svc.SetNotifyEvent(ctx, EventCancelled, true)
	}()
	wg.Wait()

	cfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NotifyOnCompletion || !cfg.NotifyOnCancelled {
		t.Fatalf("concurrent SetNotifyEvent lost an update: %+v", cfg)
	}
}
