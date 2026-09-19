package settings

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/runtime"
)

type liveBinding struct {
	namespace string
	key       string
	apply     func(SettingValue) error
}

// LiveBinder applies persisted global settings to long-lived runtime consumers.
// It listens to both the synchronous local commit path and durable EventBus
// replay path. Every application re-resolves the current value from Service,
// so duplicate/out-of-order durable events are idempotent.
type LiveBinder struct {
	service *Service
	bus     *core.EventBus

	mu       sync.Mutex
	applyMu  sync.Mutex
	bindings map[string]liveBinding
	started  bool
	unsubBus func()
	unsubDB  func()

	errMu   sync.RWMutex
	lastErr error
}

var _ runtime.Component = (*LiveBinder)(nil)

func NewLiveBinder(service *Service, bus *core.EventBus) *LiveBinder {
	return &LiveBinder{
		service:  service,
		bus:      bus,
		bindings: make(map[string]liveBinding),
	}
}

func liveBindingKey(namespace, key string) string {
	return strings.ToLower(strings.TrimSpace(namespace)) + ":" + strings.ToLower(strings.TrimSpace(key))
}

// Bind registers one process-global live setting consumer. Bind must be called
// before Start so startup application remains deterministic.
func (b *LiveBinder) Bind(namespace, key string, apply func(SettingValue) error) error {
	if b == nil {
		return fmt.Errorf("settings live binder is nil")
	}
	ns := strings.ToLower(strings.TrimSpace(namespace))
	k := strings.ToLower(strings.TrimSpace(key))
	if ns == "" || k == "" {
		return fmt.Errorf("settings live binding requires namespace and key")
	}
	if apply == nil {
		return fmt.Errorf("settings live binding %s:%s has nil apply function", ns, k)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return fmt.Errorf("cannot register settings live binding after start")
	}
	bindingKey := liveBindingKey(ns, k)
	if _, exists := b.bindings[bindingKey]; exists {
		return fmt.Errorf("settings live binding already registered: %s", bindingKey)
	}
	b.bindings[bindingKey] = liveBinding{namespace: ns, key: k, apply: apply}
	return nil
}

func (b *LiveBinder) Name() string { return "settings-live" }

func (b *LiveBinder) Dependencies() []string {
	return []string{"eventbus", "settings"}
}

func (b *LiveBinder) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.service == nil {
		return fmt.Errorf("settings live binder requires settings service")
	}
	if b.bus == nil {
		return fmt.Errorf("settings live binder requires event bus")
	}

	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	b.started = true
	b.mu.Unlock()

	localUnsub := b.service.SubscribeCommitted(func(eventCtx context.Context, event *core.SettingChangedEvent) {
		if err := b.applyEvent(eventCtx, event); err != nil {
			b.setError(err)
		}
	})
	sub := b.bus.SubscribeContextHandler("runtime:settings-live", core.EventTypeSettingChanged, func(eventCtx context.Context, event core.Event) error {
		changed, ok := event.(*core.SettingChangedEvent)
		if !ok || changed == nil {
			return nil
		}
		err := b.applyEvent(eventCtx, changed)
		b.setError(err)
		return err
	})
	if sub == nil {
		localUnsub()
		b.mu.Lock()
		b.started = false
		b.mu.Unlock()
		return fmt.Errorf("settings live binder failed to subscribe to event bus")
	}

	b.mu.Lock()
	b.unsubDB = localUnsub
	b.unsubBus = sub.Close
	bindings := make([]liveBinding, 0, len(b.bindings))
	for _, binding := range b.bindings {
		bindings = append(bindings, binding)
	}
	b.mu.Unlock()

	sort.Slice(bindings, func(i, j int) bool {
		return liveBindingKey(bindings[i].namespace, bindings[i].key) < liveBindingKey(bindings[j].namespace, bindings[j].key)
	})
	for _, binding := range bindings {
		if err := b.applyBinding(ctx, binding); err != nil {
			_ = b.Stop(context.Background())
			b.setError(err)
			return err
		}
	}
	b.setError(nil)
	return nil
}

func (b *LiveBinder) applyEvent(ctx context.Context, event *core.SettingChangedEvent) error {
	if event == nil || event.ScopeType != string(ScopeGlobal) || event.ScopeID != 0 {
		return nil
	}
	key := liveBindingKey(event.Namespace, event.Key)
	b.mu.Lock()
	binding, ok := b.bindings[key]
	b.mu.Unlock()
	if !ok {
		return nil
	}

	// Durable replay can race with the settings service's own event subscriber.
	// Invalidate explicitly before resolution so current persisted state wins.
	b.service.invalidate(event.Namespace, event.Key)
	return b.applyBinding(ctx, binding)
}

func (b *LiveBinder) applyBinding(ctx context.Context, binding liveBinding) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.applyMu.Lock()
	defer b.applyMu.Unlock()

	b.mu.Lock()
	started := b.started
	b.mu.Unlock()
	if !started {
		return nil
	}

	value := b.service.ResolveValue(ctx, 0, 0, binding.namespace, binding.key)
	if err := value.Err(); err != nil {
		return fmt.Errorf("resolve live setting %s:%s: %w", binding.namespace, binding.key, err)
	}
	if err := binding.apply(value); err != nil {
		return fmt.Errorf("apply live setting %s:%s=%q: %w", binding.namespace, binding.key, value.Raw(), err)
	}
	return nil
}

func (b *LiveBinder) Stop(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return nil
	}
	b.started = false
	unsubBus := b.unsubBus
	unsubDB := b.unsubDB
	b.unsubBus = nil
	b.unsubDB = nil
	b.mu.Unlock()

	if unsubDB != nil {
		unsubDB()
	}
	if unsubBus != nil {
		unsubBus()
	}
	// Wait for any application already admitted before unsubscription. There
	// are no background binder workers, so this is sufficient to fence Stop.
	b.applyMu.Lock()
	b.applyMu.Unlock()
	return nil
}

func (b *LiveBinder) Health(context.Context) runtime.ComponentHealth {
	b.errMu.RLock()
	err := b.lastErr
	b.errMu.RUnlock()
	if err != nil {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "live settings apply failed", Error: err}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

func (b *LiveBinder) setError(err error) {
	b.errMu.Lock()
	b.lastErr = err
	b.errMu.Unlock()
}
