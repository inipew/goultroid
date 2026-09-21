package interaction

import (
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type fakeCatalog struct {
	mu      sync.RWMutex
	scopes  map[string]tasks.ScopeIdentity
	actions map[string]map[string]bool
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		scopes:  make(map[string]tasks.ScopeIdentity),
		actions: make(map[string]map[string]bool),
	}
}

func (c *fakeCatalog) FeatureScope(featureID string) (tasks.ScopeIdentity, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	scope, ok := c.scopes[featureID]
	return scope, ok
}

func (c *fakeCatalog) HasAction(featureID, actionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.actions[featureID][actionID]
}

func (c *fakeCatalog) setScope(featureID string, scope tasks.ScopeIdentity) {
	c.mu.Lock()
	c.scopes[featureID] = scope
	c.mu.Unlock()
}

func (c *fakeCatalog) removeScope(featureID string) {
	c.mu.Lock()
	delete(c.scopes, featureID)
	c.mu.Unlock()
}

func (c *fakeCatalog) addAction(featureID, actionID string) {
	c.mu.Lock()
	if c.actions[featureID] == nil {
		c.actions[featureID] = make(map[string]bool)
	}
	c.actions[featureID][actionID] = true
	c.mu.Unlock()
}

func testRuntime(t *testing.T, cfg Config) (*Runtime, *fakeCatalog, tasks.ScopeIdentity) {
	t.Helper()
	catalog := newFakeCatalog()
	scope := tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}
	catalog.setScope("demo", scope)
	catalog.addAction("demo", "next")
	runtime, err := NewRuntime(catalog, cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, catalog, scope
}
