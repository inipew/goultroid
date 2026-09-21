package shell

import (
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
)

func TestFeatureSpecAndHomeView(t *testing.T) {
	if err := ValidateSpec(); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
	spec := NewFeature().FeatureSpec()
	if len(spec.Interactions) != 5 {
		t.Fatalf("interactions = %d, want 5", len(spec.Interactions))
	}
	var home feature.Interaction
	for _, interaction := range spec.Interactions {
		if interaction.Kind == feature.InteractionScreen && interaction.ID == InteractionHome {
			home = interaction
			break
		}
	}
	if home.ID == "" || !home.Surfaces.Supports(execution.SourceAssistant) || !home.Policy.PrivateOnly {
		t.Fatalf("home interaction = %+v", home)
	}

	state := InitialState()
	if got := RefreshCount(state); got != 0 {
		t.Fatalf("initial refresh count = %d", got)
	}
	state = NextRefreshState(state)
	if got := RefreshCount(state); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
	view := HomeView(HomeModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if err := view.Validate(); err != nil {
		t.Fatalf("HomeView() invalid: %v", err)
	}
	if len(view.Rows) != 2 || view.Rows[0][0].ActionID != ActionRefresh || view.Rows[1][0].ActionID != ActionLegacy {
		t.Fatalf("unexpected home actions: %+v", view.Rows)
	}
}
