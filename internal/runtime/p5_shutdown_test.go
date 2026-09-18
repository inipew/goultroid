package runtime

import (
	"context"
	"sync"
	"testing"
)

type lifecycleProbe struct {
	name   string
	deps   []string
	mu     *sync.Mutex
	events *[]string
}

func (p *lifecycleProbe) Name() string                { return p.name }
func (p *lifecycleProbe) Dependencies() []string      { return p.deps }
func (p *lifecycleProbe) Start(context.Context) error { return nil }
func (p *lifecycleProbe) Health(context.Context) ComponentHealth {
	return ComponentHealth{Status: HealthHealthy}
}
func (p *lifecycleProbe) record(s string) {
	p.mu.Lock()
	*p.events = append(*p.events, s)
	p.mu.Unlock()
}
func (p *lifecycleProbe) Quiesce(context.Context) error { p.record("q:" + p.name); return nil }
func (p *lifecycleProbe) Drain(context.Context) error   { p.record("d:" + p.name); return nil }
func (p *lifecycleProbe) Stop(context.Context) error    { p.record("s:" + p.name); return nil }

func TestShutdownDrainsDependentBeforeQuiescingDependency(t *testing.T) {
	r := New()
	var mu sync.Mutex
	var events []string
	a := &lifecycleProbe{name: "a", mu: &mu, events: &events}
	b := &lifecycleProbe{name: "b", deps: []string{"a"}, mu: &mu, events: &events}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"q:b", "d:b", "q:a", "d:a", "s:b", "s:a"}
	if len(events) != len(want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
}
