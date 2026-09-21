package settings

import (
	"context"
	"errors"
	"testing"
	"time"
)

type registeredMutationRepo struct {
	setStarted chan struct{}
	releaseSet chan struct{}
}

func (r *registeredMutationRepo) GetSetting(context.Context, string, int64, string, string) (*SettingItem, error) {
	return nil, nil
}
func (*registeredMutationRepo) GetEffectiveSetting(context.Context, string, string, int64, int64) (*SettingItem, error) {
	return nil, nil
}
func (r *registeredMutationRepo) SetSetting(context.Context, *SettingItem) error {
	close(r.setStarted)
	<-r.releaseSet
	return nil
}
func (*registeredMutationRepo) SetSettingsBatch(context.Context, []*SettingItem) error { return nil }
func (*registeredMutationRepo) DeleteSetting(context.Context, string, int64, string, string) error {
	return nil
}
func (*registeredMutationRepo) ListSettings(context.Context, string, int64, string) ([]SettingItem, error) {
	return nil, nil
}
func (*registeredMutationRepo) GetSettingHistory(context.Context, string, string, int) ([]SettingChangeRecord, error) {
	return nil, nil
}
func (*registeredMutationRepo) ListPendingOutbox(context.Context, int) ([]SettingOutboxEntry, error) {
	return nil, nil
}
func (*registeredMutationRepo) MarkOutboxProcessed(context.Context, int64) error { return nil }

func TestSetRegisteredHoldsSchemaStableThroughCommit(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(SettingDefinition{
		Namespace: "feature", Key: "enabled", Type: TypeBool, DefaultValue: "false",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	repo := &registeredMutationRepo{
		setStarted: make(chan struct{}),
		releaseSet: make(chan struct{}),
	}
	service := NewService(repo, registry, nil)

	_, version, ok := registry.GetVersioned("feature", "enabled")
	if !ok || version == 0 {
		t.Fatal("registered definition version missing")
	}
	setDone := make(chan error, 1)
	go func() {
		setDone <- service.SetRegistered(context.Background(), ScopeUser, 7, "feature", "enabled", version, "true", 7)
	}()
	select {
	case <-repo.setStarted:
	case <-time.After(time.Second):
		t.Fatal("registered mutation did not reach repository")
	}

	registerStarted := make(chan struct{})
	registerDone := make(chan error, 1)
	go func() {
		close(registerStarted)
		registerDone <- registry.Register(SettingDefinition{
			Namespace: "feature", Key: "enabled", Type: TypeBool, DefaultValue: "true",
		})
	}()
	<-registerStarted
	select {
	case err := <-registerDone:
		t.Fatalf("schema replacement completed before repository commit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(repo.releaseSet)
	if err := <-setDone; err != nil {
		t.Fatalf("SetRegistered() error = %v", err)
	}
	select {
	case err := <-registerDone:
		if err != nil {
			t.Fatalf("replacement Register() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("schema replacement remained blocked after repository commit")
	}
}

func TestSetRegisteredRejectsChangedDefinitionVersion(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(SettingDefinition{
		Namespace: "feature", Key: "enabled", Type: TypeBool, DefaultValue: "false",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	_, version, ok := registry.GetVersioned("feature", "enabled")
	if !ok || version == 0 {
		t.Fatal("definition version missing")
	}
	if err := registry.SetDefault("feature", "enabled", "true"); err != nil {
		t.Fatalf("SetDefault() error = %v", err)
	}
	repo := &registeredMutationRepo{
		setStarted: make(chan struct{}),
		releaseSet: make(chan struct{}),
	}
	service := NewService(repo, registry, nil)
	err := service.SetRegistered(context.Background(), ScopeUser, 7, "feature", "enabled", version, "false", 7)
	if !errors.Is(err, ErrDefinitionChanged) {
		t.Fatalf("SetRegistered() error = %v, want %v", err, ErrDefinitionChanged)
	}
	select {
	case <-repo.setStarted:
		t.Fatal("stale schema version reached repository")
	default:
	}
}
