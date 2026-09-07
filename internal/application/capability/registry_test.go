package capability_test

import (
	"testing"

	"github.com/inipew/goultroid/internal/application/capability"
	"github.com/inipew/goultroid/internal/execution"
)

func TestCapabilityRegistry(t *testing.T) {
	reg := capability.NewRegistry()

	capPing := capability.Capability{
		ID:          "ping",
		Name:        "Ping",
		Description: "Check response latency",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
	}

	capInlineOnly := capability.Capability{
		ID:          "inline_search",
		Name:        "Inline Search",
		Description: "Search inline",
		Surfaces:    execution.SurfaceInline,
	}

	if err := reg.RegisterBatch([]capability.Capability{capPing, capInlineOnly}); err != nil {
		t.Fatalf("unexpected register batch error: %v", err)
	}

	// Find
	got, ok := reg.Find("ping")
	if !ok || got.ID != "ping" {
		t.Fatalf("expected to find ping capability")
	}

	// Duplicate error
	if err := reg.Register(capPing); err == nil {
		t.Fatalf("expected duplicate registration error")
	}

	// Filter by surface
	userbotCaps := reg.FindBySurface(execution.SourceUserbot)
	if len(userbotCaps) != 1 || userbotCaps[0].ID != "ping" {
		t.Fatalf("expected only ping capability for userbot, got %+v", userbotCaps)
	}

	assistantCaps := reg.FindBySurface(execution.SourceAssistant)
	if len(assistantCaps) != 1 || assistantCaps[0].ID != "ping" {
		t.Fatalf("expected only ping capability for assistant, got %+v", assistantCaps)
	}

	inlineCaps := reg.FindBySurface(execution.SourceInline)
	if len(inlineCaps) != 1 || inlineCaps[0].ID != "inline_search" {
		t.Fatalf("expected only inline_search for inline surface, got %+v", inlineCaps)
	}
}
