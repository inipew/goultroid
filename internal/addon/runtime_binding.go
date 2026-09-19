package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

const addonEventHandlerTimeout = 5 * time.Second

type runtimeInvoker interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}

type runtimeBinding struct {
	scope tasks.ScopeIdentity

	once          sync.Once
	subscriptions []*core.Subscription
}

func (b *runtimeBinding) close(client tasks.Client) {
	if b == nil {
		return
	}
	b.once.Do(func() {
		for _, subscription := range b.subscriptions {
			if subscription != nil {
				subscription.Close()
			}
		}
		b.subscriptions = nil
		if client != nil && !b.scope.IsZero() {
			client.CancelScope(b.scope, tasks.CauseShutdown)
		}
	})
}

// SetRuntimeBoundary attaches the canonical EventBus and shared TaskEngine used
// by external addon runtimes. Addon work is always scoped under addon:<name>.
func (m *Manager) SetRuntimeBoundary(bus *core.EventBus, client tasks.Client) {
	if m == nil {
		return
	}
	m.runtimeMu.Lock()
	m.eventBus = bus
	m.taskClient = client
	m.runtimeMu.Unlock()
}

// CallRuntimeOperation is the typed host-to-addon IPC boundary. Callers cannot
// pair an arbitrary method string with an unrelated capability.
func (m *Manager) CallRuntimeOperation(ctx context.Context, name string, operation RuntimeOperation, params any) (interface{}, error) {
	required, ok := RequiredCapability(operation)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRuntimeOperation, operation)
	}
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if err := m.broker.Authorize(cleanName, required); err != nil {
		return nil, runtimeBoundaryError(err)
	}
	result, err := m.callRuntime(ctx, cleanName, string(operation), params)
	if err != nil {
		return nil, runtimeBoundaryError(err)
	}
	return result, nil
}

func (m *Manager) bindRuntimeEvents(name string, manifest Manifest, invoker runtimeInvoker) (*runtimeBinding, error) {
	generation := m.runtimeGeneration.Add(1)
	binding := &runtimeBinding{
		scope: tasks.ScopeIdentity{Owner: "addon:" + name, Generation: generation},
	}
	if len(manifest.Events) == 0 {
		return binding, nil
	}

	m.runtimeMu.RLock()
	bus := m.eventBus
	client := m.taskClient
	m.runtimeMu.RUnlock()
	if bus == nil || client == nil {
		return nil, ErrRuntimeBoundaryUnavailable
	}

	seen := make(map[EventType]struct{}, len(manifest.Events))
	for _, eventType := range manifest.Events {
		if _, duplicate := seen[eventType]; duplicate {
			continue
		}
		seen[eventType] = struct{}{}

		required, ok := eventCapability(eventType)
		if !ok {
			binding.close(client)
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedEvent, eventType)
		}
		declaredEvent := eventType
		requiredCapability := required
		if err := m.broker.Authorize(name, requiredCapability); err != nil {
			binding.close(client)
			return nil, runtimeBoundaryError(err)
		}
		subscription := bus.SubscribeWithOptions(
			core.EventType(declaredEvent),
			func(ctx context.Context, event core.Event) error {
				envelope, err := CanonicalizeEvent(event)
				if err != nil {
					return runtimeBoundaryError(err)
				}
				if envelope.Type != declaredEvent {
					return runtimeBoundaryError(fmt.Errorf("%w: received %s for %s subscription", ErrUnsupportedEvent, envelope.Type, declaredEvent))
				}
				if err := m.broker.Authorize(name, requiredCapability); err != nil {
					return runtimeBoundaryError(err)
				}
				if _, err := invoker.Call(ctx, string(OperationEventHandle), envelope); err != nil {
					return runtimeBoundaryError(err)
				}
				return nil
			},
			core.SubscribeOptions{
				Owner:       "addon:" + name,
				Scope:       binding.scope,
				Timeout:     addonEventHandlerTimeout,
				MinPriority: core.PriorityLow,
			},
		)
		if subscription == nil {
			binding.close(client)
			return nil, ErrRuntimeBoundaryUnavailable
		}
		binding.subscriptions = append(binding.subscriptions, subscription)
	}
	return binding, nil
}

func (m *Manager) watchRuntime(name string, runtime *ExternalRuntime) {
	if m == nil || runtime == nil {
		return
	}
	done := runtime.Done()
	if done == nil {
		return
	}
	<-done

	m.runtimeMu.Lock()
	if m.runtimes[name] != runtime {
		m.runtimeMu.Unlock()
		return
	}
	binding := m.runtimeBindings[name]
	client := m.taskClient
	delete(m.runtimes, name)
	delete(m.runtimeBindings, name)
	m.runtimeMu.Unlock()

	if binding != nil {
		binding.close(client)
	}
}
