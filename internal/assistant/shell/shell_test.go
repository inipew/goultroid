package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
)

func TestFeatureSpecAndViews(t *testing.T) {
	if err := ValidateSpec(); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
	spec := NewFeature().FeatureSpec()
	if len(spec.Interactions) != 11 {
		t.Fatalf("interactions = %d, want 11", len(spec.Interactions))
	}
	for _, screenID := range []string{InteractionHome, InteractionStatus, InteractionHelp} {
		interaction, ok := findInteraction(spec, feature.InteractionScreen, screenID)
		if !ok || !interaction.Surfaces.Supports(execution.SourceAssistant) || !interaction.Policy.PrivateOnly {
			t.Fatalf("screen %q = %+v, ok=%v", screenID, interaction, ok)
		}
	}

	state := InitialState()
	if got := RefreshCount(state); got != 0 {
		t.Fatalf("initial refresh count = %d", got)
	}
	state = NextRefreshState(state)
	if got := RefreshCount(state); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}

	home := HomeView(HomeModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if len(home.Rows) != 3 || home.Rows[0][0].ActionID != ActionHelp || home.Rows[0][1].ActionID != ActionStatus {
		t.Fatalf("unexpected home actions: %+v", home.Rows)
	}
	status := StatusView(StatusModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if len(status.Rows) != 1 || status.Rows[0][0].ActionID != ActionStatusRefresh || status.Rows[0][1].ActionID != ActionHome {
		t.Fatalf("unexpected status actions: %+v", status.Rows)
	}
}

func TestHelpViewUsesDeterministicCanonicalSummary(t *testing.T) {
	view := HelpView(HelpModel{Commands: []core.Command{
		{Name: "zeta", Category: "Zulu"},
		{Name: "alpha", Category: "Alpha"},
		{Name: "again", Category: "Alpha"},
	}})
	if err := view.Validate(); err != nil {
		t.Fatalf("HelpView() invalid: %v", err)
	}
	alpha := strings.Index(view.Text, "Alpha")
	zulu := strings.Index(view.Text, "Zulu")
	if alpha < 0 || zulu < 0 || alpha >= zulu {
		t.Fatalf("help categories are not deterministic: %q", view.Text)
	}
	if !strings.Contains(view.Text, "<b>Commands:</b> 3") || !strings.Contains(view.Text, "<b>Modules:</b> 2") {
		t.Fatalf("help summary missing canonical counts: %q", view.Text)
	}
	if len(view.Rows) != 1 || view.Rows[0][0].ActionID != ActionHome || view.Rows[0][1].ActionID != ActionLegacy {
		t.Fatalf("unexpected help actions: %+v", view.Rows)
	}
}

func findInteraction(spec feature.Spec, kind feature.InteractionKind, id string) (feature.Interaction, bool) {
	for _, interaction := range spec.Interactions {
		if interaction.Kind == kind && interaction.ID == id {
			return interaction, true
		}
	}
	return feature.Interaction{}, false
}
