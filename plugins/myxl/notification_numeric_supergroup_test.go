package myxl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
)

type errorMapPeerResolver struct {
	peers map[string]tg.InputPeerClass
	errs  map[string]error
}

func (r *errorMapPeerResolver) Resolve(_ context.Context, ref string) (tg.InputPeerClass, error) {
	if err, ok := r.errs[ref]; ok {
		return nil, err
	}
	if p, ok := r.peers[ref]; ok {
		return p, nil
	}
	return nil, core.ErrNotFound
}

func (r *errorMapPeerResolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	p, err := r.Resolve(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	if u, ok := p.(*tg.InputPeerUser); ok {
		return u, u.UserID, nil
	}
	return nil, 0, core.ErrNotFound
}

func (r *errorMapPeerResolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	return r.Resolve(ctx, ref)
}

func TestNumericSupergroupResolution_CommandAndA2(t *testing.T) {
	ctx := context.Background()
	const sgRawID = "-1002091172531"
	const sgChanID int64 = 2091172531
	const sgHash int64 = 665544332211

	p, repo, engine, port, sendSvc, perms := setupN6AssistantHarness(t)
	resolver := &errorMapPeerResolver{
		peers: map[string]tg.InputPeerClass{
			sgRawID:  &tg.InputPeerChannel{ChannelID: sgChanID, AccessHash: sgHash},
			"-12345": &tg.InputPeerChat{ChatID: 12345},
		},
		errs: map[string]error{
			"-100999888777": fmt.Errorf("%w: channel 999888777 has no cached access hash", core.ErrAccessHashMissing),
		},
	}
	p.SetNotificationServices(resolver, nil)

	// 1. Command rejects numeric supergroup when AccessHash is missing and gives actionable guidance without leaking secrets.
	cMissing, tgMissing := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "supergroup", "-100999888777")
	if err := p.handleMyXL(cMissing); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgMissing.sent, "access hash") || !strings.Contains(tgMissing.sent, "langsung di chat tujuan") {
		t.Fatalf("expected actionable AccessHash missing message, got %q", tgMissing.sent)
	}

	// 2. Command rejects basic group entered as supergroup.
	cBasicAsSG, tgBasicAsSG := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "supergroup", "-12345")
	if err := p.handleMyXL(cBasicAsSG); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgBasicAsSG.sent, "bukan supergroup") {
		t.Fatalf("expected supergroup type mismatch error, got %q", tgBasicAsSG.sent)
	}

	// 3. Command adds supergroup from numeric ID -1002091172531 when AccessHash is available.
	cAddNum, tgAddNum := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "supergroup", sgRawID)
	if err := p.handleMyXL(cAddNum); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgAddNum.sent, "ditambahkan") {
		t.Fatalf("expected destination added confirmation, got %q", tgAddNum.sent)
	}
	if strings.Contains(tgAddNum.sent, "665544332211") {
		t.Fatalf("command output leaked AccessHash: %q", tgAddNum.sent)
	}
	cfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Destinations) != 1 || cfg.Destinations[0].Type != DestSupergroup || cfg.Destinations[0].PeerID != sgChanID || cfg.Destinations[0].AccessHash != sgHash {
		t.Fatalf("unexpected saved supergroup destination: %+v", cfg.Destinations)
	}

	// 4. Command without target uses cmdCtx.PeerID (in-chat registration).
	cInChat, tgInChat := newTestCoreContextWithPeer(1001, perms, &tg.InputPeerChannel{ChannelID: 3091172531, AccessHash: 112233445566}, &core.Chat{Title: "Ops SG"}, "schedule", "notify", "destination", "add", "supergroup")
	if err := p.handleMyXL(cInChat); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgInChat.sent, "ditambahkan") || strings.Contains(tgInChat.sent, "112233445566") {
		t.Fatalf("unexpected in-chat registration output: %q", tgInChat.sent)
	}

	// Remove first destination so a2 wizard can add -1002091172531 again.
	if _, err := p.maintenanceService().RemoveNotificationDestination(ctx, cfg.Destinations[0].ID); err != nil {
		t.Fatal(err)
	}

	// 5. a2 wizard: missing AccessHash shows specific diagnostic; valid numeric ID succeeds.
	notifyScreen, err := p.menuMgr.BuildMaintenanceNotifyScreen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, view, err := p.assistantScreen(assistantState{Sustain: true}, notifyScreen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Begin(ctx, orchestration.BeginRequest{
		FeatureID: "myxl",
		ActorID:   1001,
		State:     raw,
		Target:    a2TestTarget{chatID: 1001},
		View:      view,
	}); err != nil {
		t.Fatal(err)
	}

	clickA2Button(t, engine, port, 1001, "Tambah Destination")
	clickA2Button(t, engine, port, 1001, "Supergroup")

	oCtx, ok, err := engine.TakeInput(ctx, 1001, 1001)
	if err != nil || !ok {
		t.Fatalf("TakeInput() ok=%v err=%v", ok, err)
	}
	if err := oCtx.SetTarget(port.target); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleAssistantInput(oCtx, "-100999888777"); err != nil {
		t.Fatalf("HandleAssistantInput(missing hash) error = %v", err)
	}
	if !strings.Contains(port.lastView.Text, "Access hash peer belum tersedia") {
		t.Fatalf("expected AccessHash missing diagnostic in a2 prompt, got %s", port.lastView.Text)
	}

	oCtx2, ok, err := engine.TakeInput(ctx, 1001, 1001)
	if err != nil || !ok {
		t.Fatalf("TakeInput(2) ok=%v err=%v", ok, err)
	}
	if err := oCtx2.SetTarget(port.target); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleAssistantInput(oCtx2, sgRawID); err != nil {
		t.Fatalf("HandleAssistantInput(%s) error = %v", sgRawID, err)
	}
	if !strings.Contains(port.lastView.Text, "Notifikasi MyXL Token Maintenance") || !strings.Contains(port.lastView.Text, "2/5") {
		t.Fatalf("expected a2 wizard to transition back to notify screen with 2/5 destinations, got %s", port.lastView.Text)
	}
	if strings.Contains(port.lastView.Text, "665544332211") {
		t.Fatalf("a2 UI leaked AccessHash: %s", port.lastView.Text)
	}
	cfgAfterA2, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundA2SG bool
	for _, d := range cfgAfterA2.Destinations {
		if d.Type == DestSupergroup && d.PeerID == sgChanID && d.AccessHash == sgHash {
			foundA2SG = true
			break
		}
	}
	if !foundA2SG {
		t.Fatalf("a2 wizard did not persist supergroup %d with expected AccessHash: %+v", sgChanID, cfgAfterA2.Destinations)
	}

	// 6. Pre-save test delivery failure must NOT persist destination.
	dbPath2 := filepath.Join(t.TempDir(), "presave_fail.db")
	_, repo2, _, _, _, p2 := initMyXLPluginWithJobs(t, dbPath2, "")
	failSend := &routingTestMessageServicer{
		errByPeerFn: func(tg.InputPeerClass) error { return errors.New("CHAT_WRITE_FORBIDDEN") },
	}
	p2.SetOwnerNotifier(1001, func() core.MessageServicer { return failSend })
	p2.SetNotificationServices(resolver, nil)
	cFail, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "supergroup", sgRawID)
	if err := p2.handleMyXL(cFail); err != nil {
		t.Fatal(err)
	}
	cfg2, err := repo2.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Destinations) != 0 {
		t.Fatalf("pre-save failure still persisted destination: %+v", cfg2.Destinations)
	}
	_ = sendSvc
}
