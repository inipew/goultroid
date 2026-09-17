package inline_test

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/services/inline"
)

type dummyHandler struct {
	pattern string
	desc    string
}

func (d *dummyHandler) Pattern() string     { return d.pattern }
func (d *dummyHandler) Description() string { return d.desc }
func (d *dummyHandler) HandleInline(ctx *inline.InlineContext) ([]inline.InlineResult, error) {
	return []inline.InlineResult{{ID: "1", Title: "dummy"}}, nil
}

func TestRegistry_RegisterDefinitionAndLease(t *testing.T) {
	reg := inline.NewRegistry()

	lease, err := reg.RegisterDefinition(inline.Definition{
		Capability: inline.Capability{
			ID:          "ping",
			Pattern:     "ping",
			Title:       "Ping",
			Description: "Check bot latency",
			Keywords:    []string{"latency", "status"},
		},
		Handler: &dummyHandler{pattern: "ping", desc: "Check latency"},
	})
	if err != nil {
		t.Fatalf("register definition: %v", err)
	}

	caps := reg.ListCapabilities(inline.CatalogQuery{})
	if len(caps) != 1 || caps[0].ID != "ping" {
		t.Fatalf("expected ping capability, got %+v", caps)
	}

	lease.Close()
	caps = reg.ListCapabilities(inline.CatalogQuery{})
	if len(caps) != 0 {
		t.Fatalf("expected 0 capabilities after lease close, got %d", len(caps))
	}
}

func TestRegistry_ListCapabilities_AuthorizationFilter(t *testing.T) {
	reg := inline.NewRegistry()

	// 1. Public capability
	_, _ = reg.RegisterDefinition(inline.Definition{
		Capability: inline.Capability{
			ID:          "public_tool",
			Pattern:     "tool",
			Title:       "Public Tool",
			Description: "Anyone can see",
		},
		Handler: &dummyHandler{pattern: "tool"},
	})

	// 2. Owner only capability
	_, _ = reg.RegisterDefinition(inline.Definition{
		Capability: inline.Capability{
			ID:          "admin_eval",
			Pattern:     "eval",
			Title:       "Admin Eval",
			Description: "Owner only tool",
			Access:      inline.InlineAccessPolicy{OwnerOnly: true},
		},
		Handler: &dummyHandler{pattern: "eval"},
	})

	// 3. Sudo only capability
	_, _ = reg.RegisterDefinition(inline.Definition{
		Capability: inline.Capability{
			ID:          "sudo_exec",
			Pattern:     "exec",
			Title:       "Sudo Exec",
			Description: "Sudo tool",
			Access:      inline.InlineAccessPolicy{SudoOnly: true},
		},
		Handler: &dummyHandler{pattern: "exec"},
	})

	// Caller A: Normal user
	capsUser := reg.ListCapabilities(inline.CatalogQuery{
		Actor: execution.NewActor(12345, 0, false, false),
	})
	if len(capsUser) != 1 || capsUser[0].ID != "public_tool" {
		t.Errorf("normal user should only see public_tool, got %+v", capsUser)
	}

	// Caller B: Sudo user
	capsSudo := reg.ListCapabilities(inline.CatalogQuery{
		Actor: execution.NewActor(12345, 0, false, true),
	})
	if len(capsSudo) != 2 {
		t.Errorf("sudo user should see 2 capabilities, got %d", len(capsSudo))
	}

	// Caller C: Owner
	capsOwner := reg.ListCapabilities(inline.CatalogQuery{
		Actor: execution.NewActor(999, 0, true, true),
	})
	if len(capsOwner) != 3 {
		t.Errorf("owner should see all 3 capabilities, got %d", len(capsOwner))
	}
}

func TestCatalogHandler_Discovery(t *testing.T) {
	reg := inline.NewRegistry()
	_, _ = reg.RegisterDefinition(inline.Definition{
		Capability: inline.Capability{
			ID:          "weather",
			Pattern:     "weather",
			Title:       "Weather Forecast",
			Description: "Get local temperature",
			Usage:       "@bot weather <city>",
		},
		Handler: &dummyHandler{pattern: "weather"},
	})

	catalog := inline.NewCatalogHandler(reg, "TestBot")

	// 1. All search
	res, err := catalog.HandleInline(&inline.InlineContext{
		Ctx:    context.Background(),
		UserID: 123,
	})
	if err != nil {
		t.Fatalf("catalog handle error: %v", err)
	}
	if len(res) != 1 || res[0].Title != "Weather Forecast" {
		t.Fatalf("unexpected catalog result: %+v", res)
	}

	// 2. Keyword search match
	res, err = catalog.HandleInline(&inline.InlineContext{
		Ctx:    context.Background(),
		UserID: 123,
		Args:   []string{"weather"},
	})
	if err != nil || len(res) != 1 {
		t.Fatalf("expected search match, got %v / len=%d", err, len(res))
	}

	// 3. Keyword search not found -> empty fallback article
	res, err = catalog.HandleInline(&inline.InlineContext{
		Ctx:    context.Background(),
		UserID: 123,
		Args:   []string{"nonexistent"},
	})
	if err != nil || len(res) != 1 || res[0].ID != "catalog_empty" {
		t.Fatalf("expected empty fallback article, got %+v", res)
	}
}
