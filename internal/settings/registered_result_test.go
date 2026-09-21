package settings

import (
	"context"
	"strconv"
	"testing"
)

type registeredResultRepo struct {
	items    map[string]*SettingItem
	setCalls int
	delCalls int
}

func newRegisteredResultRepo() *registeredResultRepo {
	return &registeredResultRepo{items: make(map[string]*SettingItem)}
}

func (r *registeredResultRepo) ref(scope string, scopeID int64, namespace, key string) string {
	return scope + ":" + namespace + ":" + key + ":" + strconv.FormatInt(scopeID, 10)
}

func (r *registeredResultRepo) GetSetting(_ context.Context, scope string, scopeID int64, namespace, key string) (*SettingItem, error) {
	item := r.items[r.ref(scope, scopeID, namespace, key)]
	if item == nil {
		return nil, nil
	}
	copyItem := *item
	return &copyItem, nil
}

func (r *registeredResultRepo) GetEffectiveSetting(_ context.Context, namespace, key string, chatID, userID int64) (*SettingItem, error) {
	for _, candidate := range []struct {
		scope string
		id    int64
	}{
		{scope: string(ScopeChat), id: chatID},
		{scope: string(ScopeUser), id: userID},
		{scope: string(ScopeGlobal), id: 0},
	} {
		if candidate.id == 0 && candidate.scope != string(ScopeGlobal) {
			continue
		}
		if item := r.items[r.ref(candidate.scope, candidate.id, namespace, key)]; item != nil {
			copyItem := *item
			return &copyItem, nil
		}
	}
	return nil, nil
}

func (r *registeredResultRepo) SetSetting(_ context.Context, item *SettingItem) error {
	r.setCalls++
	copyItem := *item
	r.items[r.ref(item.ScopeType, item.ScopeID, item.Namespace, item.Key)] = &copyItem
	return nil
}

func (r *registeredResultRepo) SetSettingsBatch(ctx context.Context, items []*SettingItem) error {
	for _, item := range items {
		if err := r.SetSetting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *registeredResultRepo) DeleteSetting(_ context.Context, scope string, scopeID int64, namespace, key string) error {
	r.delCalls++
	delete(r.items, r.ref(scope, scopeID, namespace, key))
	return nil
}

func (*registeredResultRepo) ListSettings(context.Context, string, int64, string) ([]SettingItem, error) {
	return nil, nil
}
func (*registeredResultRepo) GetSettingHistory(context.Context, string, string, int) ([]SettingChangeRecord, error) {
	return nil, nil
}
func (*registeredResultRepo) ListPendingOutbox(context.Context, int) ([]SettingOutboxEntry, error) {
	return nil, nil
}
func (*registeredResultRepo) MarkOutboxProcessed(context.Context, int64) error { return nil }

func TestRegisteredMutationResultReflectsActualPersistence(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(SettingDefinition{
		Namespace: "feature", Key: "enabled", Type: TypeBool, DefaultValue: "false",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	_, version, ok := registry.GetVersioned("feature", "enabled")
	if !ok {
		t.Fatal("definition version missing")
	}
	repo := newRegisteredResultRepo()
	repo.items[repo.ref(string(ScopeUser), 7, "feature", "enabled")] = &SettingItem{
		ScopeType: string(ScopeUser), ScopeID: 7, Namespace: "feature", Key: "enabled", Value: "true",
	}
	service := NewService(repo, registry, nil)

	result, err := service.SetRegisteredResult(context.Background(), ScopeUser, 7, "feature", "enabled", version, "true", 7)
	if err != nil {
		t.Fatalf("SetRegisteredResult() error = %v", err)
	}
	if result.Changed || result.Previous != "true" || result.Persisted != "true" || repo.setCalls != 0 {
		t.Fatalf("set no-op result = %+v setCalls=%d", result, repo.setCalls)
	}

	result, err = service.SetRegisteredResult(context.Background(), ScopeUser, 7, "feature", "enabled", version, "false", 7)
	if err != nil {
		t.Fatalf("SetRegisteredResult(change) error = %v", err)
	}
	if !result.Changed || result.Previous != "true" || result.Persisted != "false" || repo.setCalls != 1 {
		t.Fatalf("set changed result = %+v setCalls=%d", result, repo.setCalls)
	}

	result, err = service.ResetRegisteredResult(context.Background(), ScopeUser, 7, "feature", "enabled", version, 7)
	if err != nil {
		t.Fatalf("ResetRegisteredResult() error = %v", err)
	}
	if !result.Changed || result.Previous != "false" || repo.delCalls != 1 {
		t.Fatalf("reset changed result = %+v delCalls=%d", result, repo.delCalls)
	}

	result, err = service.ResetRegisteredResult(context.Background(), ScopeUser, 7, "feature", "enabled", version, 7)
	if err != nil {
		t.Fatalf("ResetRegisteredResult(noop) error = %v", err)
	}
	if result.Changed || repo.delCalls != 1 {
		t.Fatalf("reset no-op result = %+v delCalls=%d", result, repo.delCalls)
	}
}
