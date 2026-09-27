package inline

import (
	"testing"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

func TestFallbackResultsUseCanonicalSwitchInlineHelpButton(t *testing.T) {
	for _, results := range [][]InlineResult{
		fallbackErrorResults(nil),
		fallbackEmptyResults("missing"),
	} {
		if len(results) != 1 || results[0].Markup == nil || len(results[0].Markup.Rows) != 1 || len(results[0].Markup.Rows[0]) != 1 {
			t.Fatalf("unexpected fallback markup: %#v", results)
		}
		button := results[0].Markup.Rows[0][0]
		if button.Type != ui.ButtonSwitchInline {
			t.Fatalf("button type = %v, want switch-inline", button.Type)
		}
		if button.Text != presentation.ButtonLabel(presentation.ButtonRoleHelp) {
			t.Fatalf("button text = %q", button.Text)
		}
		if button.InlineQuery != "help" || button.SamePeer {
			t.Fatalf("switch-inline = query %q same_peer=%v", button.InlineQuery, button.SamePeer)
		}
	}
}
