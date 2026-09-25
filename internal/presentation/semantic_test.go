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
