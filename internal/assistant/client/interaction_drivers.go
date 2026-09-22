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

func (c *AssistantClient) bindFeatureDrivers(engine *orchestration.Engine, catalog feature.Catalog, service *interactionPresentationServicer) error {
	if c == nil || engine == nil || catalog == nil || service == nil {
		return ErrInteractionUnavailable
	}
	c.unbindFeatureDrivers()

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

	cleanups := make([]func(), 0, len(ids))
	rt := assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: catalog,
		Service: service,
		Admit:   c.admitFeatureInteraction,
	}
	for _, id := range ids {
		driver := drivers[id]
		if _, ok := catalog.Get(id); !ok {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
			return fmt.Errorf("%w: feature %s is not registered", ErrInteractionUnavailable, id)
		}
		cleanup, err := driver.BindAssistant(rt)
		if err != nil {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
			return fmt.Errorf("bind Assistant feature %s: %w", id, err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	c.mu.Lock()
	c.featureDriverCleanups = cleanups
	c.mu.Unlock()
	return nil
}

func (c *AssistantClient) unbindFeatureDrivers() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cleanups := c.featureDriverCleanups
	c.featureDriverCleanups = nil
	c.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] != nil {
			cleanups[i]()
		}
	}
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
