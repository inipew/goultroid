package plugin

import (
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/tasks"
)

// FeatureSpecProvider is an optional plugin contract for non-command surfaces.
// Command entries remain canonical in Plugin.Commands and are projected by the manager.
type FeatureSpecProvider interface {
	FeatureSpec() feature.Spec
}

type featureRegistry struct {
	*feature.Registry
	interactions *interaction.Runtime
}

func newFeatureRegistry() *featureRegistry {
	registry := feature.NewRegistry()
	interactions, err := interaction.NewRuntime(registry, interaction.Config{})
	if err != nil {
		panic(fmt.Sprintf("construct interaction runtime: %v", err))
	}
	return &featureRegistry{Registry: registry, interactions: interactions}
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
	return func() {
		registration.Close()
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
