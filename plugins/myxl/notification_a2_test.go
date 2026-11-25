package myxl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/tasks"
)

type a2TestCatalog struct {
	scope tasks.ScopeIdentity
	spec  feature.Spec
}

func (c a2TestCatalog) FeatureScope(featureID string) (tasks.ScopeIdentity, bool) {
	return c.scope, featureID == c.spec.ID
}

func (c a2TestCatalog) HasAction(featureID, actionID string) bool {
	if featureID != c.spec.ID {
		return false
	}
	for _, inter := range c.spec.Interactions {
		if inter.ID == actionID {
			return true
		}
	}
	return false
}

type a2TestTarget struct {
	chatID    int64
	messageID int
}

func (a2TestTarget) PresentationTargetKind() string { return "test" }

func (t a2TestTarget) SessionBinding(actorID int64) interaction.Binding {
	return interaction.Binding{ActorID: actorID, ChatID: t.chatID, MessageID: t.messageID}
}

func (t a2TestTarget) TargetBinding() (interaction.TargetBinding, bool) {
	if t.chatID == 0 || t.messageID <= 0 {
		return interaction.TargetBinding{}, false
	}
	return interaction.TargetBinding{ChatID: t.chatID, MessageID: t.messageID}, true
}

type a2TestPort struct {
	nextMsgID int
	lastView  presentation.CompiledView
	lastAns   presentation.Answer
	target    a2TestTarget
}

func (p *a2TestPort) Send(_ context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	p.nextMsgID++
	t := target.(a2TestTarget)
	t.messageID = p.nextMsgID
	p.target = t
	p.lastView = view
	return t, nil
}

func (p *a2TestPort) Edit(_ context.Context, target presentation.Target, view presentation.CompiledView) error {
	p.target = target.(a2TestTarget)
	p.lastView = view
	return nil
}

func (p *a2TestPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.lastAns = answer
	return nil
}

func setupN6AssistantHarness(t *testing.T) (*Plugin, Repository, *orchestration.Engine, *a2TestPort, *routingTestMessageServicer, *core.Permissions) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "n6_a2.db")
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
	p.assistantRuntime = assistantinteraction.DriverRuntime{
		Admit: func(_ string, _ feature.InteractionKind, _ string, actorID int64, _ presentation.Target) error {
			if actorID != 1001 {
				return core.ErrPermissionDenied
			}
			return nil
		},
	}

	spec := p.FeatureSpec()
	scope := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	sessions, err := interaction.NewRuntime(a2TestCatalog{scope: scope, spec: spec}, interaction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	port := &a2TestPort{nextMsgID: 100}
	engine, err := orchestration.New(sessions, interaction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}
	for i := 0; i < assistantActionSlotCount; i++ {
		slot := i
		if _, err := engine.RegisterAction(scope, spec.ID, assistantSlotID(slot), func(oCtx *orchestration.Context) error {
			return p.handleAssistantSlot(oCtx, slot)
		}); err != nil {
			t.Fatalf("RegisterAction(%s) error = %v", assistantSlotID(slot), err)
		}
	}
	return p, repo, engine, port, sendSvc, core.NewPermissions(1001, nil)
}

func findButtonData(view presentation.CompiledView, labelSubstr string) ([]byte, bool) {
	for _, row := range view.Rows {
		for _, btn := range row {
			if strings.Contains(btn.Text, labelSubstr) {
				return btn.Data, true
			}
		}
	}
	return nil, false
}

func clickA2Button(t *testing.T, engine *orchestration.Engine, port *a2TestPort, actorID int64, labelSubstr string) {
	t.Helper()
	data, ok := findButtonData(port.lastView, labelSubstr)
	if !ok {
		t.Fatalf("button containing %q not found in view %q", labelSubstr, port.lastView.Text)
	}
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data:    data,
		ActorID: actorID,
		QueryID: 1,
		Target:  port.target,
	}); err != nil {
		t.Fatalf("Dispatch(%q) error = %v", labelSubstr, err)
	}
}

func TestN6_A2NotificationRouting_EndToEndAndBidirectional(t *testing.T) {
	ctx := context.Background()
	p, repo, engine, port, sendSvc, perms := setupN6AssistantHarness(t)

	// 1. Start at Maintenance Screen and verify "Notifikasi Routing" button exists.
	maintScreen, err := p.menuMgr.BuildMaintenanceScreen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, view, err := p.assistantScreen(assistantState{Sustain: true}, maintScreen)
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

	// 2. Navigate to Notification Routing dashboard.
	clickA2Button(t, engine, port, 1001, "Notifikasi Routing")
	text := port.lastView.Text
	for _, want := range []string{"completion", "partial_failure", "failure", "cancelled", "0/5", "Fallback implicit aktif"} {
		if !strings.Contains(text, want) {
			t.Fatalf("notify screen missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "no_op") {
		t.Fatalf("notify screen must not expose no_op: %s", text)
	}

	// 3. Non-owner callback is rejected without mutating state.
	data, ok := findButtonData(port.lastView, "Completion:")
	if !ok {
		t.Fatal("missing Completion toggle button")
	}
	if err := engine.Dispatch(ctx, orchestration.CallbackRequest{
		Data:    data,
		ActorID: 9999,
		QueryID: 2,
		Target:  port.target,
	}); err == nil {
		t.Fatal("expected non-owner callback to be rejected")
	}

	// 4. Stale / malformed callbacks fail safely.
	if err := engine.Dispatch(ctx, orchestration.CallbackRequest{
		Data:    []byte("v1:myxl:bad:token"),
		ActorID: 1001,
		QueryID: 3,
		Target:  port.target,
	}); err == nil {
		t.Fatal("expected malformed callback to be rejected")
	}

	// 5. Toggle all 4 events via a2 and verify persistence + command view consistency.
	clickA2Button(t, engine, port, 1001, "Completion:")
	clickA2Button(t, engine, port, 1001, "Partial Failure:")
	clickA2Button(t, engine, port, 1001, "✅ Failure:")
	clickA2Button(t, engine, port, 1001, "Cancelled:")

	cfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NotifyOnCompletion || cfg.NotifyOnPartialFailure || cfg.NotifyOnFailure || !cfg.NotifyOnCancelled {
		t.Fatalf("unexpected toggled event config: %+v", cfg)
	}

	cStatus, tgStatus := newTestCoreContext(1001, perms, "schedule", "notify", "status")
	if err := p.handleMyXL(cStatus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgStatus.sent, "<code>completion</code>: <b>true</b>") ||
		!strings.Contains(tgStatus.sent, "<code>cancelled</code>: <b>true</b>") {
		t.Fatalf("command status out of sync with a2 toggles: %q", tgStatus.sent)
	}

	// 6. Add owner and global_log destinations via a2 (pre-save test runs automatically).
	clickA2Button(t, engine, port, 1001, "Tambah Destination")
	clickA2Button(t, engine, port, 1001, "Owner PM")
	clickA2Button(t, engine, port, 1001, "Tambah Destination")
	clickA2Button(t, engine, port, 1001, "Global Log")

	// 7. Add bot_pm destination via a2 wizard input.
	clickA2Button(t, engine, port, 1001, "Tambah Destination")
	clickA2Button(t, engine, port, 1001, "Bot PM")
	oCtx, ok, err := engine.TakeInput(ctx, 1001, 1001)
	if err != nil || !ok {
		t.Fatalf("TakeInput() ok=%v err=%v", ok, err)
	}
	if err := oCtx.SetTarget(port.target); err != nil {
		t.Fatalf("SetTarget() error = %v", err)
	}
	if err := p.HandleAssistantInput(oCtx, "@alertbot"); err != nil {
		t.Fatalf("HandleAssistantInput(@alertbot) error = %v", err)
	}
	if !strings.Contains(port.lastView.Text, "3/5") {
		t.Fatalf("expected 3/5 destinations after adding owner, global_log, bot_pm; got %s", port.lastView.Text)
	}
	// Ensure AccessHash / raw secrets never appear in rendered view.
	for _, secret := range []string{"777666", "99112233", "88112233"} {
		if strings.Contains(port.lastView.Text, secret) {
			t.Fatalf("rendered view leaked secret/AccessHash %q: %s", secret, port.lastView.Text)
		}
	}

	// 8. Destination detail: test-send, disable, and delete with confirmation.
	clickA2Button(t, engine, port, 1001, "@alertbot")
	beforeSent := len(sendSvc.sends)
	clickA2Button(t, engine, port, 1001, "Tes Kirim")
	if len(sendSvc.sends) != beforeSent+1 {
		t.Fatalf("expected test-send to deliver 1 message, got delta %d", len(sendSvc.sends)-beforeSent)
	}
	clickA2Button(t, engine, port, 1001, "Nonaktifkan")
	if !strings.Contains(port.lastView.Text, "Status: <b>OFF</b>") {
		t.Fatalf("expected destination disabled in detail view: %s", port.lastView.Text)
	}
	clickA2Button(t, engine, port, 1001, "Hapus Destination")
	clickA2Button(t, engine, port, 1001, "Ya, Hapus")
	if !strings.Contains(port.lastView.Text, "2/5") {
		t.Fatalf("expected 2/5 destinations after delete, got %s", port.lastView.Text)
	}

	// 9. Bidirectional: command adds channel, a2 refresh displays it; Back returns to maintenance.
	cAddChan, _ := newTestCoreContext(1001, perms, "schedule", "notify", "destination", "add", "channel", "@mychan")
	if err := p.handleMyXL(cAddChan); err != nil {
		t.Fatal(err)
	}
	clickA2Button(t, engine, port, 1001, "Muat Ulang")
	if !strings.Contains(port.lastView.Text, "3/5") || !strings.Contains(port.lastView.Text, "channel") {
		t.Fatalf("a2 did not reflect channel added via command: %s", port.lastView.Text)
	}
	clickA2Button(t, engine, port, 1001, "Kembali")
	if !strings.Contains(port.lastView.Text, "Notifikasi Routing") && !strings.Contains(port.lastView.Text, "Jadwal") {
		// Verify we transitioned back to maintenance screen
		if _, hasNotifyBtn := findButtonData(port.lastView, "Notifikasi Routing"); !hasNotifyBtn {
			t.Fatalf("Back button did not return to maintenance screen: %s", port.lastView.Text)
		}
	}
}
