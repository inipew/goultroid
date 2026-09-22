package plugin

import (
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction"
	interactionorchestration "github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

// FeatureSpecProvider is an optional plugin contract for non-command surfaces.
// Command entries remain canonical in Plugin.Commands and are projected by the manager.
type FeatureSpecProvider interface {
	FeatureSpec() feature.Spec
}

// InlineFeatureProvider optionally binds implementations to InteractionInline
// declarations. Query patterns remain handler concerns, while identity/policy
// come from the canonical FeatureSpec.
type InlineFeatureProvider interface {
	InlineBindings() []inlineservice.Binding
}

type featureInlineHandler struct {
	interaction feature.Interaction
	delegate    inlineservice.InlineHandler
	scope       tasks.ScopeIdentity
}

func (h *featureInlineHandler) Pattern() string { return h.delegate.Pattern() }

func (h *featureInlineHandler) Description() string {
	if description := strings.TrimSpace(h.interaction.Description); description != "" {
		return description
	}
	return h.delegate.Description()
}

func (h *featureInlineHandler) Version() string {
	if h == nil || h.scope.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s:%d", h.scope.Owner, h.scope.Generation)
}


func (h *featureInlineHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	return h.delegate.HandleInline(ctx)
}

func (h *featureInlineHandler) Matcher() inlineservice.InlineMatcher {
	if extended, ok := h.delegate.(inlineservice.InlineHandlerV2); ok {
		return extended.Matcher()
	}
	return nil
}

func (h *featureInlineHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	policy := h.interaction.Policy
	access := policy.Invocation.Inline
	result := inlineservice.InlineAccessPolicy{}
	if policy.Permission == core.PermissionOwner || access == core.InvocationSelfOnly {
		result.OwnerOnly = true
	} else if policy.Permission == core.PermissionSudo || access == core.InvocationSelfOrSudo {
		result.SudoOnly = true
	}
	if policy.PrivateOnly {
		result.AllowedChatTypes = []inlineservice.InlineChatType{inlineservice.ChatTypePrivate}
	} else if policy.GroupOnly {
		result.AllowedChatTypes = []inlineservice.InlineChatType{
			inlineservice.ChatTypeGroup,
			inlineservice.ChatTypeSupergroup,
		}
	}
	return result
}

func (h *featureInlineHandler) CachePolicy() inlineservice.CachePolicy {
	if extended, ok := h.delegate.(inlineservice.InlineHandlerV2); ok {
		return extended.CachePolicy()
	}
	return inlineservice.CacheGlobal
}

func (h *featureInlineHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	if extended, ok := h.delegate.(inlineservice.InlineHandlerV2); ok {
		return extended.HandleInlineV2(ctx)
	}
	results, err := h.delegate.HandleInline(ctx)
	if err != nil {
		return nil, err
	}
	return &inlineservice.InlineResponse{Results: results, Cache: h.CachePolicy()}, nil
}

type featureRegistry struct {
	*feature.Registry
	interactions *interaction.Runtime
	actions      *interaction.Dispatcher
}

func newFeatureRegistry() *featureRegistry {
	registry := feature.NewRegistry()
	interactions, err := interaction.NewRuntime(registry, interaction.Config{})
	if err != nil {
		panic(fmt.Sprintf("construct interaction runtime: %v", err))
	}
	return &featureRegistry{
		Registry:     registry,
		interactions: interactions,
		actions:      interaction.NewDispatcher(interactions),
	}
}

// FeatureCatalog returns the read-only feature surface catalog. Registration
// remains owned by plugin lifecycle transactions.
func (m *Manager) FeatureCatalog() feature.Catalog {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	registry := m.featureRegistry
	m.mu.RUnlock()
	return registry
}

// InteractionRuntime returns the lifecycle-bound interaction session runtime.
func (m *Manager) InteractionRuntime() *interaction.Runtime {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	registry := m.featureRegistry
	m.mu.RUnlock()
	if registry == nil {
		return nil
	}
	return registry.interactions
}

// ActionDispatcher returns the typed P2 action-dispatch boundary.
// Feature-specific registrations are intentionally deferred until UI migration.
func (m *Manager) ActionDispatcher() *interaction.Dispatcher {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	registry := m.featureRegistry
	m.mu.RUnlock()
	if registry == nil {
		return nil
	}
	return registry.actions
}

// NewInteractionEngine binds the shared P0/P1/P2 interaction foundation to one
// presentation transport. Assistant and userbot-inline transports can therefore
// expose the same feature-facing P3 API without duplicating session semantics.
func (m *Manager) NewInteractionEngine(port presentation.Port) (*interactionorchestration.Engine, error) {
	if m == nil {
		return nil, interactionorchestration.ErrInvalidEngine
	}
	m.mu.RLock()
	registry := m.featureRegistry
	m.mu.RUnlock()
	if registry == nil {
		return nil, interactionorchestration.ErrInvalidEngine
	}
	return interactionorchestration.New(registry.interactions, registry.actions, port)
}

func (m *Manager) registerFeatureContract(name string, p Plugin, scope tasks.ScopeIdentity, commands []core.Command) (func(), error) {
	m.mu.RLock()
	registry := m.featureRegistry
	m.mu.RUnlock()
	if registry == nil {
		return nil, nil
	}
	spec, err := buildFeatureSpec(name, p, commands)
	if err != nil {
		return nil, err
	}
	registration, err := registry.Register(feature.Owner{ID: name, Scope: scope}, spec)
	if err != nil {
		return nil, err
	}

	var inlineRegistrations []*inlineservice.Registration
	if provider, ok := p.(InlineFeatureProvider); ok {
		m.mu.RLock()
		inlineRegistry := m.inlineRegistry
		m.mu.RUnlock()
		bindings := provider.InlineBindings()
		if len(bindings) > 0 && inlineRegistry == nil {
			registration.Close()
			return nil, fmt.Errorf("feature %s declares inline implementations but inline registry is unavailable", name)
		}
		declared := make(map[string]feature.Interaction)
		for _, interaction := range spec.Interactions {
			if interaction.Kind == feature.InteractionInline {
				declared[strings.ToLower(strings.TrimSpace(interaction.ID))] = interaction
			}
		}
		seen := make(map[string]struct{}, len(bindings))
		for _, binding := range bindings {
			interactionID := strings.ToLower(strings.TrimSpace(binding.InteractionID))
			interaction, exists := declared[interactionID]
			if !exists {
				for _, inlineRegistration := range inlineRegistrations {
					inlineRegistration.Close()
				}
				registration.Close()
				return nil, fmt.Errorf("feature %s inline binding %q has no InteractionInline declaration", name, binding.InteractionID)
			}
			if _, duplicate := seen[interactionID]; duplicate {
				for _, inlineRegistration := range inlineRegistrations {
					inlineRegistration.Close()
				}
				registration.Close()
				return nil, fmt.Errorf("feature %s inline binding %q is duplicated", name, interactionID)
			}
			if binding.Handler == nil {
				for _, inlineRegistration := range inlineRegistrations {
					inlineRegistration.Close()
				}
				registration.Close()
				return nil, fmt.Errorf("feature %s inline binding %q has nil handler", name, interactionID)
			}
			seen[interactionID] = struct{}{}
			owned, registerErr := inlineRegistry.RegisterOwned(
				name,
				interactionID,
				scope,
				&featureInlineHandler{interaction: interaction, delegate: binding.Handler, scope: scope},
				binding.Priority,
			)
			if registerErr != nil {
				for _, inlineRegistration := range inlineRegistrations {
					inlineRegistration.Close()
				}
				registration.Close()
				return nil, fmt.Errorf("feature %s inline binding %q: %w", name, interactionID, registerErr)
			}
			inlineRegistrations = append(inlineRegistrations, owned)
		}
	}

	return func() {
		for _, inlineRegistration := range inlineRegistrations {
			inlineRegistration.Close()
		}
		registration.Close()
		if registry.actions != nil {
			registry.actions.UnregisterScope(scope)
		}
		if registry.interactions != nil {
			registry.interactions.CancelScope(scope)
		}
	}, nil
}

func buildFeatureSpec(name string, p Plugin, commands []core.Command) (feature.Spec, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	spec := feature.Spec{ID: name, Name: p.Name()}
	if provider, ok := p.(FeatureSpecProvider); ok {
		spec = provider.FeatureSpec()
		if strings.TrimSpace(spec.ID) == "" {
			spec.ID = name
		}
		if strings.ToLower(strings.TrimSpace(spec.ID)) != name {
			return feature.Spec{}, fmt.Errorf("feature spec id %q must match plugin id %q", spec.ID, name)
		}
		if strings.TrimSpace(spec.Name) == "" {
			spec.Name = p.Name()
		}
	}
	if described, ok := p.(DescribedPlugin); ok {
		metadata := described.Metadata()
		if strings.TrimSpace(spec.Name) == "" {
			spec.Name = metadata.Name
		}
		if strings.TrimSpace(spec.Description) == "" {
			spec.Description = metadata.Description
		}
	}
	return feature.BindCanonicalCommands(spec, commands)
}
