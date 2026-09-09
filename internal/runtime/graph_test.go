package runtime

import (
	"context"
	"reflect"
	"testing"
)

type mockComponent struct {
	name         string
	dependencies []string
	startCalled  bool
	stopCalled   bool
	startErr     error
	stopErr      error
	health       ComponentHealth
}

func TestDependencyGraph_StartupOrderDeterministic(t *testing.T) {
	g := NewDependencyGraph()
	components := []Component{
		&mockComponent{name: "worker-b", dependencies: []string{"database"}},
		&mockComponent{name: "cache"},
		&mockComponent{name: "worker-a", dependencies: []string{"database"}},
		&mockComponent{name: "database"},
		&mockComponent{name: "api", dependencies: []string{"cache", "worker-a", "worker-b"}},
	}
	for _, component := range components {
		if err := g.Add(component); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"cache", "database", "worker-a", "worker-b", "api"}
	for i := 0; i < 100; i++ {
		order, err := g.StartupOrder()
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(order))
		for j, component := range order {
			got[j] = component.Name()
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: startup order = %v, want %v", i, got, want)
		}
	}
}

func (m *mockComponent) Name() string                               { return m.name }
func (m *mockComponent) Dependencies() []string                     { return m.dependencies }
func (m *mockComponent) Start(ctx context.Context) error            { m.startCalled = true; return m.startErr }
func (m *mockComponent) Stop(ctx context.Context) error             { m.stopCalled = true; return m.stopErr }
func (m *mockComponent) Health(ctx context.Context) ComponentHealth { return m.health }

func TestDependencyGraph_StartupAndShutdownOrder(t *testing.T) {
	g := NewDependencyGraph()

	// Storage (no deps)
	storage := &mockComponent{name: "storage"}
	// Telegram depends on storage
	tg := &mockComponent{name: "telegram", dependencies: []string{"storage"}}
	// EventBus (no deps)
	bus := &mockComponent{name: "eventbus"}
	// Plugins depends on telegram and eventbus
	plugins := &mockComponent{name: "plugins", dependencies: []string{"telegram", "eventbus"}}

	for _, c := range []Component{plugins, tg, bus, storage} {
		if err := g.Add(c); err != nil {
			t.Fatalf("failed to add %s: %v", c.Name(), err)
		}
	}

	startOrder, err := g.StartupOrder()
	if err != nil {
		t.Fatalf("unexpected StartupOrder error: %v", err)
	}

	indices := make(map[string]int)
	for i, c := range startOrder {
		indices[c.Name()] = i
	}

	// Dependencies must appear before dependents
	if indices["storage"] >= indices["telegram"] {
		t.Errorf("storage must start before telegram")
	}
	if indices["telegram"] >= indices["plugins"] {
		t.Errorf("telegram must start before plugins")
	}
	if indices["eventbus"] >= indices["plugins"] {
		t.Errorf("eventbus must start before plugins")
	}

	stopOrder, err := g.ShutdownOrder()
	if err != nil {
		t.Fatalf("unexpected ShutdownOrder error: %v", err)
	}

	stopIndices := make(map[string]int)
	for i, c := range stopOrder {
		stopIndices[c.Name()] = i
	}

	// Dependents must stop before dependencies
	if stopIndices["plugins"] >= stopIndices["telegram"] {
		t.Errorf("plugins must stop before telegram")
	}
	if stopIndices["plugins"] >= stopIndices["eventbus"] {
		t.Errorf("plugins must stop before eventbus")
	}
	if stopIndices["telegram"] >= stopIndices["storage"] {
		t.Errorf("telegram must stop before storage")
	}
}

func TestDependencyGraph_CycleDetection(t *testing.T) {
	g := NewDependencyGraph()

	cA := &mockComponent{name: "A", dependencies: []string{"C"}}
	cB := &mockComponent{name: "B", dependencies: []string{"A"}}
	cC := &mockComponent{name: "C", dependencies: []string{"B"}}

	_ = g.Add(cA)
	_ = g.Add(cB)
	_ = g.Add(cC)

	err := g.Validate()
	if err == nil {
		t.Fatalf("expected cycle detection error, got nil")
	}
}

func TestDependencyGraph_MissingDependency(t *testing.T) {
	g := NewDependencyGraph()

	cA := &mockComponent{name: "A", dependencies: []string{"missing_dep"}}
	_ = g.Add(cA)

	err := g.Validate()
	if err == nil {
		t.Fatalf("expected missing dependency error, got nil")
	}
}

func TestDependencyGraph_SelfDependency(t *testing.T) {
	g := NewDependencyGraph()

	cA := &mockComponent{name: "A", dependencies: []string{"A"}}
	_ = g.Add(cA)

	err := g.Validate()
	if err == nil {
		t.Fatalf("expected self-dependency error, got nil")
	}
}
