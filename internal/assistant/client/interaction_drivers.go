package client

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

var ErrInteractionAdmission = errors.New("assistant/client: interaction admission denied")

// SetInteractionDrivers installs feature-owned Assistant interaction drivers. Drivers
// are transport-bound on Start and detached when that Assistant generation
// exits; plugin generation fencing remains owned by the shared feature catalog.
func (c *AssistantClient) SetInteractionDrivers(drivers []assistantinteraction.FeatureDriver) {
	if c == nil {
		return
	}
	next := make(map[string]assistantinteraction.FeatureDriver, len(drivers))
	for _, driver := range drivers {
		if driver == nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(driver.AssistantFeatureID()))
		if id == "" {
			continue
		}
		next[id] = driver
	}
	c.mu.Lock()
	c.featureDrivers = next
	c.mu.Unlock()
}

type featureDriverBinding struct {
	scope   tasks.ScopeIdentity
	cleanup func()
}

func (c *AssistantClient) bindFeatureDrivers(engine *orchestration.Engine, catalog feature.Catalog, service *interactionPresentationServicer) error {
	if c == nil || engine == nil || catalog == nil || service == nil {
		return ErrInteractionUnavailable
	}

	c.mu.RLock()
	drivers := make(map[string]assistantinteraction.FeatureDriver, len(c.featureDrivers))
	for id, driver := range c.featureDrivers {
		drivers[id] = driver
	}
	c.mu.RUnlock()

	ids := make([]string, 0, len(drivers))
	for id := range drivers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	c.featureDriverMu.Lock()
	defer c.featureDriverMu.Unlock()
	if c.featureDriverBindings == nil {
		c.featureDriverBindings = make(map[string]featureDriverBinding, len(drivers))
	}

	for id, binding := range c.featureDriverBindings {
		_, configured := drivers[id]
		scope, active := catalog.FeatureScope(id)
		if configured && active && !scope.IsZero() && scope == binding.scope {
			continue
		}
		if binding.cleanup != nil {
			binding.cleanup()
		}
		delete(c.featureDriverBindings, id)
	}

	rt := assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: catalog,
		Service: service,
		Admit:   c.admitFeatureInteraction,
	}
	added := make([]string, 0, len(ids))
	for _, id := range ids {
		driver := drivers[id]
		scope, active := catalog.FeatureScope(id)
		if !active || scope.IsZero() {
			continue
		}
		if current, ok := c.featureDriverBindings[id]; ok && current.scope == scope {
			continue
		}

		cleanup, err := driver.BindAssistant(rt)
		if err != nil {
			for i := len(added) - 1; i >= 0; i-- {
				binding := c.featureDriverBindings[added[i]]
				if binding.cleanup != nil {
					binding.cleanup()
				}
				delete(c.featureDriverBindings, added[i])
			}
			return fmt.Errorf("bind Assistant feature %s: %w", id, err)
		}
		c.featureDriverBindings[id] = featureDriverBinding{scope: scope, cleanup: cleanup}
		added = append(added, id)
	}

	return nil
}

func (c *AssistantClient) unbindFeatureDrivers() {
	if c == nil {
		return
	}
	c.featureDriverMu.Lock()
	bindings := c.featureDriverBindings
	c.featureDriverBindings = nil
	c.featureDriverMu.Unlock()

	ids := make([]string, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	for _, id := range ids {
		if cleanup := bindings[id].cleanup; cleanup != nil {
			cleanup()
		}
	}
}

// RefreshInteractionBindings reconciles transport-bound shell and feature-driver
// actions against the current feature catalog. It is safe to call from plugin
// generation validation; when the Assistant transport is not running yet it is
// intentionally a no-op.
func (c *AssistantClient) RefreshInteractionBindings() error {
	if c == nil {
		return nil
	}

	c.lifecycleOpMu.Lock()
	defer c.lifecycleOpMu.Unlock()

	c.mu.RLock()
	ingress := c.interactionIngress
	catalog := c.featureCatalog
	c.mu.RUnlock()
	if ingress == nil || ingress.engine == nil || catalog == nil {
		return nil
	}
	service, ok := ingress.ack.(*interactionPresentationServicer)
	if !ok || service == nil {
		return ErrInteractionUnavailable
	}
	if err := c.syncShellActions(ingress.engine, catalog); err != nil {
		return fmt.Errorf("bind Assistant shell actions: %w", err)
	}
	if err := c.bindFeatureDrivers(ingress.engine, catalog, service); err != nil {
		return fmt.Errorf("bind Assistant feature drivers: %w", err)
	}
	return nil
}

func (c *AssistantClient) featureDriver(featureID string) assistantinteraction.FeatureDriver {
	if c == nil {
		return nil
	}
	featureID = strings.ToLower(strings.TrimSpace(featureID))
	c.mu.RLock()
	driver := c.featureDrivers[featureID]
	c.mu.RUnlock()
	return driver
}

func (c *AssistantClient) admitFeatureInteraction(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error {
	c.mu.RLock()
	catalog := c.featureCatalog
	c.mu.RUnlock()
	if catalog == nil {
		return ErrInteractionUnavailable
	}
	decl, ok := catalog.FindInteraction(featureID, kind, interactionID)
	if !ok {
		return fmt.Errorf("%w: %s:%s", ErrInteractionUnavailable, kind, interactionID)
	}
	var err error
	switch typed := target.(type) {
	case presentationtelegram.MessageTarget:
		err = feature.AdmitInteraction(
			decl,
			execution.SourceAssistant,
			actorID,
			isPrivatePeer(typed.Peer),
			c.shellPermissions(),
		)
	case presentationtelegram.InlineTarget:
		// Inline query admission validated chat scope before an a2 token could
		// be minted. Callback updates do not carry that chat type, so revalidate
		// only the still-observable surface/actor/permission dimensions here.
		err = feature.AdmitInteractionIdentity(
			decl,
			execution.SourceInline,
			actorID,
			c.shellPermissions(),
		)
	default:
		err = feature.AdmitInteraction(
			decl,
			execution.SourceAssistant,
			actorID,
			false,
			c.shellPermissions(),
		)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInteractionAdmission, err)
	}
	return nil
}
