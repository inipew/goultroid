package wikipedia

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
)

func TestWikipediaPlugin_InitPlugin(t *testing.T) {
	gate := plugin.NewCapabilityGate()
	netSvc := network.NewService(nil, nil)

	// Denied when capability not in manifest
	gate.Register("wikipedia", []string{})
	pctxDenied := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "wikipedia",
		Gate:    gate,
		Network: netSvc,
	})
	p := New()
	if err := p.InitPlugin(pctxDenied); err == nil {
		t.Fatal("expected capability error when CapHTTP is not registered")
	}

	// Granted when capability registered
	gate.Register("wikipedia", []string{plugin.CapHTTP})
	pctxGranted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "wikipedia",
		Gate:    gate,
		Network: netSvc,
	})
	if err := p.InitPlugin(pctxGranted); err != nil {
		t.Fatalf("unexpected error when CapHTTP is granted: %v", err)
	}
	if p.http == nil {
		t.Fatal("expected http service to be set")
	}
}

func TestWikipediaPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "wikipedia" {
		t.Errorf("got name %q, want wikipedia", p.Name())
	}
	if len(p.Commands()) == 0 {
		t.Fatal("expected commands to be declared")
	}
	cmd := p.Commands()[0]
	if cmd.Name != "wiki" {
		t.Errorf("got command %q, want wiki", cmd.Name)
	}
	if !strings.Contains(cmd.Usage, ".wiki") {
		t.Errorf("unexpected usage %q", cmd.Usage)
	}
}
