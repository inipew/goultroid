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

var ErrV2Admission = errors.New("assistant/client: a2 interaction admission denied")

// SetInteractionDrivers installs feature-owned Assistant a2 drivers. Drivers
// are transport-bound on Start and detached when that Assistant generation
// exits; plugin generation fencing remains owned by the shared feature catalog.
func (c *AssistantClient) SetInteractionDrivers(drivers []assistantinteraction.V2FeatureDriver) {
	if c == nil {
		return
	}
	next := make(map[string]assistantinteraction.V2FeatureDriver, len(drivers))
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
	c.v2Drivers = next
	c.mu.Unlock()
}

func (c *AssistantClient) bindV2Drivers(engine *orchestration.Engine, catalog feature.Catalog, service *v2PresentationServicer) error {
	if c == nil || engine == nil || catalog == nil || service == nil {
		return ErrV2Unavailable
	}
	c.unbindV2Drivers()

	c.mu.RLock()
	drivers := make(map[string]assistantinteraction.V2FeatureDriver, len(c.v2Drivers))
	for id, driver := range c.v2Drivers {
		drivers[id] = driver
	}
	c.mu.RUnlock()

	ids := make([]string, 0, len(drivers))
	for id := range drivers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	cleanups := make([]func(), 0, len(ids))
	rt := assistantinteraction.V2Runtime{
		Engine:  engine,
		Catalog: catalog,
		Service: service,
		Admit:   c.admitV2FeatureInteraction,
	}
	for _, id := range ids {
		driver := drivers[id]
		if _, ok := catalog.Get(id); !ok {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
			return fmt.Errorf("%w: feature %s is not registered", ErrV2Unavailable, id)
		}
		cleanup, err := driver.BindAssistantV2(rt)
		if err != nil {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
			return fmt.Errorf("bind a2 feature %s: %w", id, err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	c.mu.Lock()
	c.v2DriverCleanups = cleanups
	c.mu.Unlock()
	return nil
}

func (c *AssistantClient) unbindV2Drivers() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cleanups := c.v2DriverCleanups
	c.v2DriverCleanups = nil
	c.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		if cleanups[i] != nil {
			cleanups[i]()
		}
	}
}

func (c *AssistantClient) v2Driver(featureID string) assistantinteraction.V2FeatureDriver {
	if c == nil {
		return nil
	}
	featureID = strings.ToLower(strings.TrimSpace(featureID))
	c.mu.RLock()
	driver := c.v2Drivers[featureID]
	c.mu.RUnlock()
	return driver
}

func (c *AssistantClient) admitV2FeatureInteraction(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error {
	c.mu.RLock()
	catalog := c.v2Catalog
	c.mu.RUnlock()
	if catalog == nil {
		return ErrV2Unavailable
	}
	decl, ok := catalog.FindInteraction(featureID, kind, interactionID)
	if !ok {
		return fmt.Errorf("%w: %s:%s", ErrV2Unavailable, kind, interactionID)
	}
	private := false
	if messageTarget, ok := target.(presentationtelegram.MessageTarget); ok {
		private = isPrivatePeer(messageTarget.Peer)
	}
	if err := feature.AdmitInteraction(decl, execution.SourceAssistant, actorID, private, c.shellPermissions()); err != nil {
		return fmt.Errorf("%w: %w", ErrV2Admission, err)
	}
	return nil
}
