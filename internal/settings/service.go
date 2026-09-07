package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// Service provides a unified management and resolution interface for settings.
type Service struct {
	repo database.Repository
	reg  *Registry
	bus  *core.EventBus
}

// NewService instantiates a new settings Service.
func NewService(repo database.Repository, reg *Registry, bus *core.EventBus) *Service {
	if reg == nil {
		reg = NewRegistry()
	}
	return &Service{
		repo: repo,
		reg:  reg,
		bus:  bus,
	}
}

// Registry returns the underlying schema registry.
func (s *Service) Registry() *Registry {
	return s.reg
}

// Resolve applies the hierarchical fallback:
// 1. Chat-level override (if chatID != 0)
// 2. User-level override (if userID != 0)
// 3. Global bot-level setting (scope_id = 0)
// 4. Schema default value (if registered)
func (s *Service) Resolve(ctx context.Context, userID, chatID int64, namespace, key string) (string, error) {
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))

	// 1. Chat Scope
	if chatID != 0 {
		item, err := s.repo.GetSetting(ctx, string(ScopeChat), chatID, ns, k)
		if err != nil {
			return "", fmt.Errorf("failed to query chat setting (%d:%s:%s): %w", chatID, ns, k, err)
		}
		if item != nil {
			return item.Value, nil
		}
	}

	// 2. User Scope
	if userID != 0 {
		item, err := s.repo.GetSetting(ctx, string(ScopeUser), userID, ns, k)
		if err != nil {
			return "", fmt.Errorf("failed to query user setting (%d:%s:%s): %w", userID, ns, k, err)
		}
		if item != nil {
			return item.Value, nil
		}
	}

	// 3. Global Scope
	item, err := s.repo.GetSetting(ctx, string(ScopeGlobal), 0, ns, k)
	if err != nil {
		return "", fmt.Errorf("failed to query global setting (0:%s:%s): %w", ns, k, err)
	}
	if item != nil {
		return item.Value, nil
	}

	// 4. Schema Default
	if def, ok := s.reg.Get(ns, k); ok {
		return def.DefaultValue, nil
	}

	return "", nil
}

// ResolveBool returns the resolved boolean value.
func (s *Service) ResolveBool(ctx context.Context, userID, chatID int64, namespace, key string) (bool, error) {
	val, err := s.Resolve(ctx, userID, chatID, namespace, key)
	if err != nil {
		return false, err
	}
	lower := strings.ToLower(strings.TrimSpace(val))
	return lower == "true" || lower == "1" || lower == "yes" || lower == "on", nil
}

// ResolveInt returns the resolved integer value.
func (s *Service) ResolveInt(ctx context.Context, userID, chatID int64, namespace, key string) (int64, error) {
	val, err := s.Resolve(ctx, userID, chatID, namespace, key)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(val) == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("setting %s:%s has non-integer value %q: %w", namespace, key, val, err)
	}
	return parsed, nil
}

// ResolveDuration returns the resolved time.Duration.
func (s *Service) ResolveDuration(ctx context.Context, userID, chatID int64, namespace, key string) (time.Duration, error) {
	val, err := s.Resolve(ctx, userID, chatID, namespace, key)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(val) == "" {
		return 0, nil
	}
	dur, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("setting %s:%s has non-duration value %q: %w", namespace, key, val, err)
	}
	return dur, nil
}

// ResolveString returns the resolved string value.
func (s *Service) ResolveString(ctx context.Context, userID, chatID int64, namespace, key string) (string, error) {
	return s.Resolve(ctx, userID, chatID, namespace, key)
}

// Get retrieves an explicit setting from the repository for a given scope without inheritance.
func (s *Service) Get(ctx context.Context, scope SettingScope, scopeID int64, namespace, key string) (*database.SettingItem, error) {
	return s.repo.GetSetting(ctx, string(scope), scopeID, strings.ToLower(namespace), strings.ToLower(key))
}

// Set validates and saves a setting in the given scope, publishing a change event on success.
func (s *Service) Set(ctx context.Context, scope SettingScope, scopeID int64, namespace, key, value string, updaterID int64) error {
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))

	var valType string = string(TypeString)
	canonicalVal := strings.TrimSpace(value)

	// Validate against definition if registered
	if def, ok := s.reg.Get(ns, k); ok {
		valType = string(def.Type)
		cVal, err := def.Canonicalize(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s:%s: %w", ns, k, err)
		}
		canonicalVal = cVal
	}

	// Read previous value to report accurate event
	oldItem, err := s.repo.GetSetting(ctx, string(scope), scopeID, ns, k)
	if err != nil {
		return fmt.Errorf("failed to check existing setting: %w", err)
	}
	var oldVal string
	if oldItem != nil {
		oldVal = oldItem.Value
	}

	item := &database.SettingItem{
		ScopeType: string(scope),
		ScopeID:   scopeID,
		Namespace: ns,
		Key:       k,
		ValueType: valType,
		Value:     canonicalVal,
		UpdatedBy: updaterID,
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.repo.SetSetting(ctx, item); err != nil {
		return fmt.Errorf("failed to save setting (%s:%d:%s:%s): %w", scope, scopeID, ns, k, err)
	}

	// Publish SettingChangedEvent
	if s.bus != nil {
		s.bus.Publish(&core.SettingChangedEvent{
			At:        time.Now().UTC(),
			ScopeType: string(scope),
			ScopeID:   scopeID,
			Namespace: ns,
			Key:       k,
			OldVal:    oldVal,
			NewVal:    canonicalVal,
			ChangedBy: updaterID,
		})
	}

	return nil
}

// Reset removes an override from the specified scope, falling back to lower scopes or default.
func (s *Service) Reset(ctx context.Context, scope SettingScope, scopeID int64, namespace, key string) error {
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))

	oldItem, err := s.repo.GetSetting(ctx, string(scope), scopeID, ns, k)
	if err != nil {
		return fmt.Errorf("failed to check setting before reset: %w", err)
	}
	var oldVal string
	if oldItem != nil {
		oldVal = oldItem.Value
	}

	if err := s.repo.DeleteSetting(ctx, string(scope), scopeID, ns, k); err != nil {
		return fmt.Errorf("failed to delete setting: %w", err)
	}

	if s.bus != nil {
		s.bus.Publish(&core.SettingChangedEvent{
			At:        time.Now().UTC(),
			ScopeType: string(scope),
			ScopeID:   scopeID,
			Namespace: ns,
			Key:       k,
			OldVal:    oldVal,
			NewVal:    "",
			ChangedBy: 0,
		})
	}

	return nil
}

// ListByScope lists all configured settings for a specific scope.
func (s *Service) ListByScope(ctx context.Context, scope SettingScope, scopeID int64, namespace string) ([]database.SettingItem, error) {
	return s.repo.ListSettings(ctx, string(scope), scopeID, strings.ToLower(namespace))
}

// Export dumps all explicit settings for a scope into a namespace -> key -> value map.
func (s *Service) Export(ctx context.Context, scope SettingScope, scopeID int64) (map[string]map[string]string, error) {
	items, err := s.repo.ListSettings(ctx, string(scope), scopeID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to export settings: %w", err)
	}

	exportData := make(map[string]map[string]string)
	for _, item := range items {
		if _, ok := exportData[item.Namespace]; !ok {
			exportData[item.Namespace] = make(map[string]string)
		}
		exportData[item.Namespace][item.Key] = item.Value
	}
	return exportData, nil
}

// Import bulk-updates settings for a scope from a map. Returns number of settings imported.
func (s *Service) Import(ctx context.Context, scope SettingScope, scopeID int64, data map[string]map[string]string, updaterID int64) (int, error) {
	count := 0
	for ns, kv := range data {
		for k, v := range kv {
			if err := s.Set(ctx, scope, scopeID, ns, k, v, updaterID); err != nil {
				return count, fmt.Errorf("failed importing setting %s:%s: %w", ns, k, err)
			}
			count++
		}
	}
	return count, nil
}

// GetHistory retrieves the audit log for a setting.
func (s *Service) GetHistory(ctx context.Context, namespace, key string, limit int) ([]database.SettingChangeRecord, error) {
	return s.repo.GetSettingHistory(ctx, strings.ToLower(namespace), strings.ToLower(key), limit)
}
