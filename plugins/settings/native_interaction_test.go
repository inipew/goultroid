package settings

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	settingssvc "github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
)

type nativeSettingsTaskClient struct {
	mu        sync.Mutex
	submitted int
}

func (c *nativeSettingsTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.submitted++
	c.mu.Unlock()
	err := spec.Handler(ctx)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err != nil {
		result.Outcome = tasks.OutcomeFailed
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	return nil, nil
}

func (*nativeSettingsTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*nativeSettingsTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*nativeSettingsTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type nativeSettingsHarness struct {
	runtime      *rootinteraction.Runtime
	adapter      *nativeinteraction.Adapter
	scope        tasks.ScopeIdentity
	registration *feature.Registration
	cleanup      func()
	tasks        *nativeSettingsTaskClient
}

func bindNativeSettings(t *testing.T, p *Plugin, svc core.TelegramServicer, ownerID int64) *nativeSettingsHarness {
	t.Helper()
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:settings", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatalf("register settings feature: %v", err)
	}
	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		registration.Close()
		t.Fatalf("new interaction runtime: %v", err)
	}
	taskClient := &nativeSettingsTaskClient{}
	adapter, err := nativeinteraction.New(
		registry,
		sessions,
		rootinteraction.NewDispatcher(sessions),
		taskClient,
		func() core.TelegramServicer { return svc },
		core.NewPermissions(ownerID, nil),
	)
	if err != nil {
		sessions.Close()
		registration.Close()
		t.Fatalf("new native adapter: %v", err)
	}
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{
		Interactions: adapter,
		Catalog:      registry,
		Scope:        scope,
	})
	if err != nil {
		sessions.Close()
		registration.Close()
		t.Fatalf("bind native settings: %v", err)
	}
	h := &nativeSettingsHarness{
		runtime:      sessions,
		adapter:      adapter,
		scope:        scope,
		registration: registration,
		cleanup:      cleanup,
		tasks:        taskClient,
	}
	t.Cleanup(func() {
		if h.cleanup != nil {
			h.cleanup()
		}
		_ = h.runtime.Close()
		h.registration.Close()
	})
	return h
}

func nativeSettingsCommandContext(svc core.TelegramServicer, ownerID int64, args ...string) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionInteractive,
		Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		Svc:     svc,
		PeerID:  &tg.InputPeerUser{UserID: ownerID, AccessHash: 99},
		Args:    args,
	}
}

func TestP1DNativeSettingsUsesA2WithoutLegacyCallbackState(t *testing.T) {
	p, _, store, tgSvc := setupTestPlugin(t)
	const ownerID int64 = 12345
	h := bindNativeSettings(t, p, tgSvc, ownerID)

	if err := p.handleSettingsCommand(nativeSettingsCommandContext(tgSvc, ownerID)); err != nil {
		t.Fatalf("open native settings: %v", err)
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("native settings retained %d legacy callback states, want 0", got)
	}

	markup := snapshotNativeMarkup(t, tgSvc)
	if len(markup.Rows) == 0 {
		t.Fatal("native settings produced no buttons")
	}
	for _, row := range markup.Rows {
		for _, button := range row.Buttons {
			callbackButton, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if !rootinteraction.OwnsCallbackData(callbackButton.Data) {
				t.Fatalf("native settings emitted non-a2 callback data %q", callbackButton.Data)
			}
			if _, err := rootinteraction.ParseCallbackToken(callbackButton.Data); err != nil {
				t.Fatalf("native settings emitted invalid a2 callback %q: %v", callbackButton.Data, err)
			}
		}
	}
	if got := h.runtime.CancelScope(h.scope); got != 1 {
		t.Fatalf("native settings session count = %d, want 1", got)
	}
}

func TestP1DTextOnlySettingsAllocatesNoA2Session(t *testing.T) {
	p, svc, store, tgSvc := setupTestPlugin(t)
	const ownerID int64 = 12345
	h := bindNativeSettings(t, p, tgSvc, ownerID)
	if err := svc.Set(context.Background(), settingssvc.ScopeGlobal, 0, "ui", "inline_buttons", "false", ownerID); err != nil {
		t.Fatalf("disable inline buttons: %v", err)
	}

	if err := p.handleSettingsCommand(nativeSettingsCommandContext(tgSvc, ownerID)); err != nil {
		t.Fatalf("open text-only settings: %v", err)
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("text-only settings retained %d legacy callback states, want 0", got)
	}
	if got := h.runtime.CancelScope(h.scope); got != 0 {
		t.Fatalf("text-only settings allocated %d a2 sessions, want 0", got)
	}
	tgSvc.mu.Lock()
	markup := tgSvc.lastMarkup
	text := tgSvc.lastText
	tgSvc.mu.Unlock()
	if markup != nil {
		t.Fatal("text-only settings unexpectedly sent inline markup")
	}
	if !strings.Contains(text, "Available settings categories") {
		t.Fatalf("unexpected text-only settings output: %s", text)
	}
}

func TestP1DNativeSettingsMutationRevisesA2AndRejectsStaleButton(t *testing.T) {
	p, svc, store, tgSvc := setupTestPlugin(t)
	const ownerID int64 = 12345
	h := bindNativeSettings(t, p, tgSvc, ownerID)

	if err := p.handleSettingsCommand(nativeSettingsCommandContext(tgSvc, ownerID, "security")); err != nil {
		t.Fatalf("open security settings: %v", err)
	}
	data := findNativeCallbackData(t, snapshotNativeMarkup(t, tgSvc), "PM Guard Protection")
	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}
	event := &core.CallbackQueryEvent{
		QueryID: 777,
		UserID:  ownerID,
		ChatID:  ownerID,
		MsgID:   100,
		Data:    data,
		Origin:  core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: 100,
		},
	}
	handled, err := h.adapter.HandleCallback(context.Background(), event)
	if err != nil || !handled {
		t.Fatalf("native toggle callback handled=%v err=%v", handled, err)
	}
	value, err := svc.ResolveBool(context.Background(), ownerID, 0, "pmpermit", "enabled")
	if err != nil {
		t.Fatalf("resolve pmpermit enabled: %v", err)
	}
	if value {
		t.Fatal("native toggle did not disable pmpermit:enabled")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("native mutation retained %d legacy callback states, want 0", got)
	}

	stale := *event
	stale.QueryID = 778
	handled, err = h.adapter.HandleCallback(context.Background(), &stale)
	if !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale native callback handled=%v err=%v, want ErrStaleToken", handled, err)
	}
	value, err = svc.ResolveBool(context.Background(), ownerID, 0, "pmpermit", "enabled")
	if err != nil {
		t.Fatalf("resolve pmpermit enabled after stale callback: %v", err)
	}
	if value {
		t.Fatal("stale native callback mutated setting")
	}
}

func snapshotNativeMarkup(t *testing.T, svc *mockTelegramService) *tg.ReplyInlineMarkup {
	t.Helper()
	svc.mu.Lock()
	markup := svc.lastMarkup
	svc.mu.Unlock()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || inline == nil {
		t.Fatalf("settings markup = %T, want *tg.ReplyInlineMarkup", markup)
	}
	return inline
}

func findNativeCallbackData(t *testing.T, markup *tg.ReplyInlineMarkup, label string) []byte {
	t.Helper()
	for _, row := range markup.Rows {
		for _, button := range row.Buttons {
			callbackButton, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if strings.Contains(callbackButton.Text, label) {
				return append([]byte(nil), callbackButton.Data...)
			}
		}
	}
	t.Fatalf("callback button containing %q not found", label)
	return nil
}
