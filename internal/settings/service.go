package settings

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/runtime"
)

var _ runtime.Component = (*Service)(nil)

type resolveCacheKey struct {
	userID int64
	chatID int64
}

// Service provides a unified management and resolution interface for settings.
type Service struct {
	repo    Repository
	reg     *Registry
	bus     *core.EventBus
	cacheMu sync.RWMutex
	cache   map[string]map[resolveCacheKey]string // namespace:key -> resolveCacheKey -> value

	lifecycleMu  sync.Mutex
	started      bool
	outboxCancel context.CancelFunc
	outboxDone   chan struct{}
	unsubSetting func()
	outboxWake   chan struct{}
	outboxErrMu  sync.RWMutex
	outboxErr    error
}

// NewService instantiates a new settings Service.
func NewService(repo Repository, reg *Registry, bus *core.EventBus) *Service {
	if reg == nil {
		reg = NewRegistry()
	}
	s := &Service{
		repo:       repo,
		reg:        reg,
		bus:        bus,
		cache:      make(map[string]map[resolveCacheKey]string),
		outboxWake: make(chan struct{}, 1),
	}
	return s
}

// runOutboxWorker drains setting_outbox on local commit notifications, with a
// slow fallback poll for recovery. Delivery is intentionally at-least-once:
// consumers that perform side effects must deduplicate by EventMeta.ID.
// Single ordered worker: processes pending outbox rows in created_at order, dispatches synchronously,
// and only marks processed after successful delivery (no drop). Retries on next tick if dispatch fails.
func (s *Service) runOutboxWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	s.drainOutbox(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.outboxWake:
			s.drainOutbox(ctx)
		case <-ticker.C:
			s.drainOutbox(ctx)
		}
	}
}

func (s *Service) drainOutbox(ctx context.Context) {
	entries, err := s.repo.ListPendingOutbox(ctx, 100)
	if err != nil {
		s.setOutboxError(err)
		return
	}
	for _, e := range entries {
		if s.bus == nil {
			return
		}
		evt := &core.SettingChangedEvent{
			MetaData: core.EventMeta{
				ID: fmt.Sprintf("outbox:setting:%d", e.ID),
			},
			At:        e.CreatedAt,
			ScopeType: e.ScopeType,
			ScopeID:   e.ScopeID,
			Namespace: e.Namespace,
			Key:       e.Key,
			OldVal:    e.OldVal,
			NewVal:    e.NewVal,
			ChangedBy: e.ChangedBy,
		}
		if err := s.bus.PublishDurable(ctx, evt); err != nil {
			s.setOutboxError(err)
			return
		}
		if err := s.repo.MarkOutboxProcessed(ctx, e.ID); err != nil {
			s.setOutboxError(err)
			return
		}
	}
	s.setOutboxError(nil)
}

func (s *Service) setOutboxError(err error) {
	s.outboxErrMu.Lock()
	s.outboxErr = err
	s.outboxErrMu.Unlock()
}

func (s *Service) usesDurableOutbox() bool {
	_, ok := s.repo.(*SQLiteRepository)
	return ok
}

func (s *Service) wakeOutbox() {
	select {
	case s.outboxWake <- struct{}{}:
	default:
	}
}

// Start launches the durable outbox worker explicitly (Construct != Start).
// It is idempotent; if already started via NewService, it returns nil.
func (s *Service) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started {
		return nil
	}
	if s.bus == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.started = true
	sub := s.bus.SubscribeOwned("runtime:settings", core.EventTypeSettingChanged, func(event core.Event) {
		if e, ok := event.(*core.SettingChangedEvent); ok {
			s.invalidate(e.Namespace, e.Key)
		}
	})
	if sub != nil {
		s.unsubSetting = sub.Close
	}
	if _, ok := s.repo.(*SQLiteRepository); !ok {
		return nil
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.outboxCancel = cancel
	s.outboxDone = done
	go func() {
		s.runOutboxWorker(cctx)

		s.lifecycleMu.Lock()
		if s.outboxDone == done {
			s.started = false
			s.outboxCancel = nil
			s.outboxDone = nil
			if s.unsubSetting != nil {
				s.unsubSetting()
				s.unsubSetting = nil
			}
		}
		close(done)
		s.lifecycleMu.Unlock()
	}()
	return nil
}

// Name returns the component name for runtime.Component.
func (s *Service) Name() string {
	return "settings"
}

// Dependencies returns component prerequisites for runtime.Component.
func (s *Service) Dependencies() []string {
	return []string{"eventbus"}
}

// Health probes the health status of the settings service.
func (s *Service) Health(ctx context.Context) runtime.ComponentHealth {
	s.outboxErrMu.RLock()
	err := s.outboxErr
	s.outboxErrMu.RUnlock()
	if err != nil {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "settings outbox delivery failed", Error: err}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// Stop gracefully shuts down the outbox worker.
func (s *Service) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	if !s.started {
		s.lifecycleMu.Unlock()
		return nil
	}
	cancel := s.outboxCancel
	done := s.outboxDone
	if cancel == nil || done == nil {
		unsub := s.unsubSetting
		s.unsubSetting = nil
		s.started = false
		s.lifecycleMu.Unlock()
		if unsub != nil {
			unsub()
		}
		return nil
	}
	s.lifecycleMu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Registry returns the underlying schema registry.
func (s *Service) Registry() *Registry {
	return s.reg
}

func (s *Service) invalidate(namespace, key string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if namespace == "" && key == "" {
		s.cache = make(map[string]map[resolveCacheKey]string)
		return
	}
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))
	delete(s.cache, ns+":"+k)
}

func (s *Service) putCache(cacheKey string, rKey resolveCacheKey, val string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]map[resolveCacheKey]string)
	}
	byKey, ok := s.cache[cacheKey]
	if !ok {
		byKey = make(map[resolveCacheKey]string)
		s.cache[cacheKey] = byKey
	}
	byKey[rKey] = val
}

// Resolve applies the hierarchical fallback with single-query batch optimization:
// 1. Chat-level override (if chatID != 0)
// 2. User-level override (if userID != 0)
// 3. Global bot-level setting (scope_id = 0)
// 4. Schema default value (if registered)
// Uses GetEffectiveSetting batch query to reduce 3 round-trips to 1.
func (s *Service) Resolve(ctx context.Context, userID, chatID int64, namespace, key string) (string, error) {
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))
	cacheKey := ns + ":" + k
	rKey := resolveCacheKey{userID: userID, chatID: chatID}

	s.cacheMu.RLock()
	if byKey, ok := s.cache[cacheKey]; ok {
		if val, found := byKey[rKey]; found {
			s.cacheMu.RUnlock()
			return val, nil
		}
	}
	s.cacheMu.RUnlock()

	// Batch query: single round-trip for chat > user > global
	item, err := s.repo.GetEffectiveSetting(ctx, ns, k, chatID, userID)
	if err != nil {
		return "", fmt.Errorf("failed to query effective setting (%s:%s chat=%d user=%d): %w", ns, k, chatID, userID, err)
	}
	if item != nil {
		s.putCache(cacheKey, rKey, item.Value)
		return item.Value, nil
	}

	// 4. Schema Default
	if def, ok := s.reg.Get(ns, k); ok {
		s.putCache(cacheKey, rKey, def.DefaultValue)
		return def.DefaultValue, nil
	}

	s.putCache(cacheKey, rKey, "")
	return "", nil
}

// ResolveBool returns the resolved boolean value via typed SettingValue.
func (s *Service) ResolveBool(ctx context.Context, userID, chatID int64, namespace, key string) (bool, error) {
	return s.ResolveValue(ctx, userID, chatID, namespace, key).BoolE()
}

// ResolveInt returns the resolved integer value via typed SettingValue.
func (s *Service) ResolveInt(ctx context.Context, userID, chatID int64, namespace, key string) (int64, error) {
	return s.ResolveValue(ctx, userID, chatID, namespace, key).IntE()
}

// ResolveDuration returns the resolved time.Duration via typed SettingValue.
func (s *Service) ResolveDuration(ctx context.Context, userID, chatID int64, namespace, key string) (time.Duration, error) {
	return s.ResolveValue(ctx, userID, chatID, namespace, key).DurationE()
}

// ResolveString returns the resolved string value.
func (s *Service) ResolveString(ctx context.Context, userID, chatID int64, namespace, key string) (string, error) {
	return s.Resolve(ctx, userID, chatID, namespace, key)
}

// ResolveValue returns a SettingValue wrapping the resolved string, any lookup error, and definition for typed parsing.
func (s *Service) ResolveValue(ctx context.Context, userID, chatID int64, namespace, key string) SettingValue {
	val, err := s.Resolve(ctx, userID, chatID, namespace, key)
	var def *SettingDefinition
	if d, ok := s.reg.Get(strings.ToLower(strings.TrimSpace(namespace)), strings.ToLower(strings.TrimSpace(key))); ok {
		def = d
	}
	return NewSettingValueWithDef(val, err, def)
}

// Get retrieves an explicit setting from the repository for a given scope without inheritance.
func (s *Service) Get(ctx context.Context, scope SettingScope, scopeID int64, namespace, key string) (*SettingItem, error) {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return nil, err
	}
	return s.repo.GetSetting(ctx, string(scope), scopeID, strings.ToLower(namespace), strings.ToLower(key))
}

// Set validates and saves a setting in the given scope, publishing a change event on success.
func (s *Service) Set(ctx context.Context, scope SettingScope, scopeID int64, namespace, key, value string, updaterID int64) error {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return err
	}

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

	// Read previous value to report accurate event and for idempotency
	oldItem, err := s.repo.GetSetting(ctx, string(scope), scopeID, ns, k)
	if err != nil {
		return fmt.Errorf("failed to check existing setting: %w", err)
	}
	var oldVal string
	if oldItem != nil {
		oldVal = oldItem.Value
	}
	// Idempotency: if value already equals canonical, no DB write, no outbox, no event.
	if oldItem != nil && oldVal == canonicalVal {
		return nil
	}

	item := &SettingItem{
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

	s.invalidate(ns, k)
	if s.usesDurableOutbox() {
		s.wakeOutbox()
		return nil
	}

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
func (s *Service) Reset(ctx context.Context, scope SettingScope, scopeID int64, namespace, key string, updaterID int64) error {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return err
	}

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

	s.invalidate(ns, k)
	if s.usesDurableOutbox() {
		s.wakeOutbox()
		return nil
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
			ChangedBy: updaterID,
		})
	}

	return nil
}

// ListByScope lists all configured settings for a specific scope.
func (s *Service) ListByScope(ctx context.Context, scope SettingScope, scopeID int64, namespace string) ([]SettingItem, error) {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return nil, err
	}
	return s.repo.ListSettings(ctx, string(scope), scopeID, strings.ToLower(namespace))
}

// Export dumps all explicit settings for a scope into a namespace -> key -> value map.
func (s *Service) Export(ctx context.Context, scope SettingScope, scopeID int64) (map[string]map[string]string, error) {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return nil, err
	}
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

// Import atomically validates and bulk-updates settings for a scope from a map.
// Phase 1 pre-validates all values against registered schemas; Phase 2 persists in a single batch transaction.
func (s *Service) Import(ctx context.Context, scope SettingScope, scopeID int64, data map[string]map[string]string, updaterID int64) (int, error) {
	if err := (ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}

	// Phase 1: Pre-validation & canonicalization
	type preparedItem struct {
		ns        string
		k         string
		valType   string
		canonical string
	}
	var prepared []preparedItem
	for ns, kv := range data {
		nsNorm := strings.ToLower(strings.TrimSpace(ns))
		for k, v := range kv {
			kNorm := strings.ToLower(strings.TrimSpace(k))
			valType := string(TypeString)
			canonicalVal := strings.TrimSpace(v)

			if def, ok := s.reg.Get(nsNorm, kNorm); ok {
				valType = string(def.Type)
				cVal, err := def.Canonicalize(v)
				if err != nil {
					return 0, fmt.Errorf("invalid value for %s:%s: %w", nsNorm, kNorm, err)
				}
				canonicalVal = cVal
			}
			prepared = append(prepared, preparedItem{
				ns:        nsNorm,
				k:         kNorm,
				valType:   valType,
				canonical: canonicalVal,
			})
		}
	}

	// Phase 2: Batch Transaction
	now := time.Now().UTC()
	dbItems := make([]*SettingItem, 0, len(prepared))
	for _, p := range prepared {
		dbItems = append(dbItems, &SettingItem{
			ScopeType: string(scope),
			ScopeID:   scopeID,
			Namespace: p.ns,
			Key:       p.k,
			ValueType: p.valType,
			Value:     p.canonical,
			UpdatedBy: updaterID,
			UpdatedAt: now,
		})
	}

	if err := s.repo.SetSettingsBatch(ctx, dbItems); err != nil {
		return 0, fmt.Errorf("failed executing batch settings import: %w", err)
	}

	for _, p := range prepared {
		s.invalidate(p.ns, p.k)
	}
	if s.usesDurableOutbox() {
		s.wakeOutbox()
		return len(prepared), nil
	}

	// Phase 3: Publish events
	if s.bus != nil {
		for _, p := range prepared {
			s.bus.Publish(&core.SettingChangedEvent{
				At:        now,
				ScopeType: string(scope),
				ScopeID:   scopeID,
				Namespace: p.ns,
				Key:       p.k,
				NewVal:    p.canonical,
				ChangedBy: updaterID,
			})
		}
	}

	return len(prepared), nil
}

// GetHistory retrieves the audit log for a setting.
func (s *Service) GetHistory(ctx context.Context, namespace, key string, limit int) ([]SettingChangeRecord, error) {
	return s.repo.GetSettingHistory(ctx, strings.ToLower(namespace), strings.ToLower(key), limit)
}
