package presentation

import "testing"

func TestSemanticResponseRendering(t *testing.T) {
	tests := []struct {
		name string
		in   Response
		want string
	}{
		{name: "status", in: Status("Ready"), want: "ℹ️ <b>Status:</b> Ready"},
		{name: "success", in: Success("Saved"), want: "✅ <b>Success:</b> Saved"},
		{name: "error", in: Error("Failed"), want: "❌ <b>Error:</b> Failed"},
		{name: "progress", in: Progress("Downloading"), want: "⏳ <b>Processing:</b> Downloading"},
		{name: "result", in: Result("<b>Result</b>"), want: "<b>Result</b>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Render(); got != tc.want {
				t.Fatalf("Render()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestSemanticResponseTrimsOuterWhitespace(t *testing.T) {
	if got := Progress("  working  ").Render(); got != "⏳ <b>Processing:</b> working" {
		t.Fatalf("Render()=%q", got)
	}
	if got := Result("   ").Render(); got != "" {
		t.Fatalf("empty result Render()=%q", got)
	}
}

func TestSemanticResponseViewUsesCanonicalText(t *testing.T) {
	view := Progress("Loading").View(Row{{Text: "Cancel", ActionID: "cancel"}})
	if view.Text != "⏳ <b>Processing:</b> Loading" {
		t.Fatalf("view text=%q", view.Text)
	}
	if len(view.Rows) != 1 || len(view.Rows[0]) != 1 || view.Rows[0][0].ActionID != "cancel" {
		t.Fatalf("view rows=%+v", view.Rows)
	}
}
