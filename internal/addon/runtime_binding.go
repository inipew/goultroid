package addon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	addonEventHandlerTimeout   = 5 * time.Second
	addonCommandHandlerTimeout = 30 * time.Second
)

type runtimeInvoker interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}

type runtimeBinding struct {
	scope tasks.ScopeIdentity

	once          sync.Once
	router        *core.Router
	commands      []core.Command
	subscriptions []*core.Subscription
}

func (b *runtimeBinding) close(client tasks.Client) {
	if b == nil {
		return
	}
	b.once.Do(func() {
		if b.router != nil && len(b.commands) > 0 {
			b.router.UnregisterBatch(b.commands)
		}
		for _, subscription := range b.subscriptions {
			if subscription != nil {
				subscription.Close()
			}
		}
		b.subscriptions = nil
		if client != nil && !b.scope.IsZero() {
			client.CancelScope(b.scope, tasks.CauseScopeClosed)
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

func (m *Manager) SetCommandRouter(router *core.Router) {
	if m == nil {
		return
	}
	m.runtimeMu.Lock()
	m.commandRouter = router
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
	if required == "" {
		return nil, runtimeBoundaryError(fmt.Errorf("%w: operation %q is host-contract only", ErrUnauthorizedCapability, operation))
	}
	if err := m.broker.Authorize(cleanName, required); err != nil {
		return nil, runtimeBoundaryError(err)
	}
	result, err := m.callRuntime(ctx, cleanName, string(operation), params)
	if err != nil {
		return nil, runtimeBoundaryError(err)
	}
	return result, nil
}

func (m *Manager) bindRuntimeContract(name string, manifest Manifest, invoker runtimeInvoker) (*runtimeBinding, error) {
	generation := m.runtimeGeneration.Add(1)
	binding := &runtimeBinding{
		scope: tasks.ScopeIdentity{Owner: "addon:" + name, Generation: generation},
	}
	if len(manifest.Commands) == 0 && len(manifest.Events) == 0 {
		return binding, nil
	}

	m.runtimeMu.RLock()
	bus := m.eventBus
	client := m.taskClient
	router := m.commandRouter
	m.runtimeMu.RUnlock()
	if client == nil {
		return nil, ErrRuntimeBoundaryUnavailable
	}
	if len(manifest.Commands) > 0 && router == nil {
		return nil, ErrRuntimeBoundaryUnavailable
	}
	if len(manifest.Events) > 0 && bus == nil {
		return nil, ErrRuntimeBoundaryUnavailable
	}

	if len(manifest.Commands) > 0 {
		commands := make([]core.Command, 0, len(manifest.Commands))
		for _, commandName := range manifest.Commands {
			commandName := commandName
			commands = append(commands, core.Command{
				Name:        commandName,
				Description: "External addon command provided by " + name,
				Category:    "Addon",
				Permission:  core.PermissionOwner,
				Invocation: core.InvocationPolicy{
					Userbot:   core.InvocationSelfOnly,
					Assistant: core.InvocationSelfOnly,
				},
				Surfaces: execution.SurfaceUserbot | execution.SurfaceAssistant,
				Timeout:  addonCommandHandlerTimeout,
				Scope:    binding.scope,
				Handler: func(ctx *core.Context) error {
					return m.invokeRuntimeCommand(name, commandName, invoker, ctx)
				},
			})
		}
		if err := router.RegisterBatch(commands); err != nil {
			return nil, fmt.Errorf("register addon commands: %w", err)
		}
		binding.router = router
		binding.commands = commands
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

func (m *Manager) invokeRuntimeCommand(name, commandName string, invoker runtimeInvoker, ctx *core.Context) error {
	if ctx == nil {
		return runtimeBoundaryError(errors.New("addon command context is nil"))
	}
	callCtx := ctx.Ctx
	if callCtx == nil {
		callCtx = context.Background()
	}
	invocation := CommandInvocation{
		Version:       AddonProtocolVersion,
		Command:       commandName,
		Args:          append([]string(nil), ctx.Args...),
		RawArgs:       ctx.RawArgs,
		Source:        ctx.Source.String(),
		CorrelationID: ctx.CorrelationID,
	}
	raw, err := invoker.Call(callCtx, string(OperationCommandHandle), invocation)
	if err != nil {
		return runtimeBoundaryError(err)
	}

	var result CommandResult
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &result); err != nil {
			return runtimeBoundaryError(fmt.Errorf("decode addon command result: %w", err))
		}
	}
	resultErr := result.executionError()
	if result.Reply != "" {
		if err := m.broker.Authorize(name, CapTelegramSend); err != nil {
			return runtimeBoundaryError(err)
		}
		if err := ctx.EditOrReply(result.Reply); err != nil {
			return err
		}
	}
	return resultErr
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
