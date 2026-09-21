package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/settings"
)

type mutationTrackingRepo struct {
	*shellSettingsRepo
	setCalls int
	setErr   error
}

func (r *mutationTrackingRepo) SetSetting(ctx context.Context, item *settings.SettingItem) error {
	r.setCalls++
	if r.setErr != nil {
		return r.setErr
	}
	return r.shellSettingsRepo.SetSetting(ctx, item)
}

func registerBoolSetting(t *testing.T, registry *settings.Registry, namespace, key string) (*settings.SettingDefinition, uint64) {
	t.Helper()
	if err := registry.Register(settings.SettingDefinition{
		Namespace: namespace, Key: key, Type: settings.TypeBool, DefaultValue: "false",
		Title: "Enabled", Category: settings.CategoryGeneral,
	}); err != nil {
		t.Fatalf("Register(%s:%s) error = %v", namespace, key, err)
	}
	def, version, ok := registry.GetVersioned(namespace, key)
	if !ok || def == nil || version == 0 {
		t.Fatalf("versioned definition %s:%s missing", namespace, key)
	}
	return def, version
}

func beginBoundBoolMutation(t *testing.T, engine *orchestration.Engine, port *shellTestPort, peer tg.InputPeerClass, def *settings.SettingDefinition, version uint64, current, source string, explicit bool) []byte {
	t.Helper()
	state := assistantshell.OpenCategoryState(assistantshell.InitialState(), 1)
	state = assistantshell.OpenSettingState(state, 1)
	state = assistantshell.BindSettingState(state, def.Namespace, def.Key, version)
	_, err := engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: assistantshell.FeatureID,
		ActorID:   7,
		State:     state,
		Target:    presentationtelegram.MessageTarget{Peer: peer, ChatID: 7},
		View: assistantshell.SettingDetailView(assistantshell.SettingDetailModel{
			Definition:   *def,
			Current:      current,
			Source:       source,
			ExplicitUser: explicit,
		}),
	})
	if err != nil {
		t.Fatalf("Begin(bound mutation) error = %v", err)
	}
	return callbackForAction(t, port.sent, assistantshell.ActionSettingChange)
}

func TestAssistantShellMutationCommitsAndConsumesRevision(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{shellSettingsRepo: base}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	if err := dispatchShell(t, engine, token, 500, peer); err != nil {
		t.Fatalf("Dispatch(change) error = %v", err)
	}
	item, err := base.GetSetting(context.Background(), string(settings.ScopeUser), 7, "feature", "enabled")
	if err != nil || item == nil || item.Value != "true" {
		t.Fatalf("persisted item = %+v err=%v", item, err)
	}
	if repo.setCalls != 1 {
		t.Fatalf("set calls = %d, want 1", repo.setCalls)
	}
	if port.answered.Text != "Setting saved." {
		t.Fatalf("callback answer = %q", port.answered.Text)
	}
	if err := dispatchShell(t, engine, token, 501, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old mutation token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}

func TestAssistantShellMutationRenderFailureReportsActualCommit(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{shellSettingsRepo: base}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	port.editErr = errors.New("telegram edit failed")
	err := dispatchShell(t, engine, token, 510, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("render failure = %v, want MutationError", err)
	}
	if mutationErr.Stage != assistantshell.MutationStageRender || !mutationErr.Committed || mutationErr.Result.Outcome != assistantshell.MutationChanged {
		t.Fatalf("render mutation error = %+v", mutationErr)
	}
	item, _ := base.GetSetting(context.Background(), string(settings.ScopeUser), 7, "feature", "enabled")
	if item == nil || item.Value != "true" {
		t.Fatalf("committed value = %+v, want true", item)
	}
}

func TestAssistantShellMutationNoopRenderFailureIsNotCommitted(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	if err := base.SetSetting(context.Background(), &settings.SettingItem{
		ScopeType: string(settings.ScopeUser), ScopeID: 7, Namespace: "feature", Key: "enabled", Value: "true",
	}); err != nil {
		t.Fatalf("seed user override error = %v", err)
	}
	if err := base.SetSetting(context.Background(), &settings.SettingItem{
		ScopeType: string(settings.ScopeChat), ScopeID: 7, Namespace: "feature", Key: "enabled", Value: "false",
	}); err != nil {
		t.Fatalf("seed chat override error = %v", err)
	}
	repo := &mutationTrackingRepo{shellSettingsRepo: base}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Chat override", true)
	port.editErr = errors.New("telegram edit failed")
	err := dispatchShell(t, engine, token, 520, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("render failure = %v, want MutationError", err)
	}
	if mutationErr.Stage != assistantshell.MutationStageRender || mutationErr.Committed || mutationErr.Result.Outcome != assistantshell.MutationNoop {
		t.Fatalf("no-op render mutation error = %+v", mutationErr)
	}
	if repo.setCalls != 0 {
		t.Fatalf("no-op reached persistence: setCalls=%d", repo.setCalls)
	}
}

func TestAssistantShellMutationPersistenceFailureRendersSafeRetry(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{
		shellSettingsRepo: base,
		setErr:            errors.New("storage unavailable"),
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	err := dispatchShell(t, engine, token, 530, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("persist failure = %v, want MutationError", err)
	}
	if mutationErr.Stage != assistantshell.MutationStagePersist || mutationErr.Committed {
		t.Fatalf("persist mutation error = %+v", mutationErr)
	}
	if !strings.Contains(port.edited.Text, "Update failed; no change was committed.") {
		t.Fatalf("recovery view missing failure notice: %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, token, 531, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old failed-mutation token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
}

func TestAssistantShellMutationPersistenceAndRecoveryRenderFailureRequiresReopen(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{
		shellSettingsRepo: base,
		setErr:            errors.New("storage unavailable"),
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	port.editErr = errors.New("telegram edit failed")
	err := dispatchShell(t, engine, token, 535, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("persist+recovery failure = %v, want MutationError", err)
	}
	if mutationErr.Stage != assistantshell.MutationStagePersist || mutationErr.Committed {
		t.Fatalf("persist+recovery mutation error = %+v", mutationErr)
	}
	if port.answered.Text != "Setting update failed. Reopen Settings." {
		t.Fatalf("callback answer = %q", port.answered.Text)
	}
}

func TestAssistantShellMutationRejectsSchemaChangedAfterDetailOpened(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{shellSettingsRepo: base}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	if err := registry.SetDefault("feature", "enabled", "true"); err != nil {
		t.Fatalf("SetDefault() error = %v", err)
	}
	err := dispatchShell(t, engine, token, 540, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Stage != assistantshell.MutationStageBinding {
		t.Fatalf("schema-change error = %+v raw=%v", mutationErr, err)
	}
	if repo.setCalls != 0 {
		t.Fatalf("stale schema reached persistence: setCalls=%d", repo.setCalls)
	}
}

func TestAssistantShellMutationRejectsRegistryReorder(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	def, version := registerBoolSetting(t, registry, "feature", "z_enabled")
	base := newShellSettingsRepo()
	repo := &mutationTrackingRepo{shellSettingsRepo: base}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	token := beginBoundBoolMutation(t, engine, port, peer, def, version, "false", "Default", false)
	registerBoolSetting(t, registry, "feature", "a_enabled")
	err := dispatchShell(t, engine, token, 550, peer)
	var mutationErr *assistantshell.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Stage != assistantshell.MutationStageBinding {
		t.Fatalf("reorder error = %+v raw=%v", mutationErr, err)
	}
	if repo.setCalls != 0 {
		t.Fatalf("reordered cursor reached persistence: setCalls=%d", repo.setCalls)
	}
}
