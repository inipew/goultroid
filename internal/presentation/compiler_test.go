package presentation

import "testing"

func TestViewValidateRejectsRawOrInvalidActionIdentity(t *testing.T) {
	cases := []View{
		{},
		{Text: "x", Rows: []Row{{{Text: "", ActionID: "next"}}}},
		{Text: "x", Rows: []Row{{{Text: "Next", ActionID: "A:raw"}}}},
		{Text: "x", Rows: []Row{{{Type: ButtonURL, Text: "Open", URL: "javascript:alert(1)"}}}},
		{Text: "x", Rows: []Row{{{Type: ButtonURL, Text: "Open", ActionID: "next", URL: "https://example.com"}}}},
		{Text: "x", Rows: []Row{{{Type: ButtonSwitchInline, Text: "Search", ActionID: "next", InlineQuery: "help"}}}},
	}
	for _, view := range cases {
		if err := view.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded", view)
		}
	}
}

func TestViewValidateAcceptsTypedButtons(t *testing.T) {
	view := View{Text: "hello", Rows: []Row{{
		{Text: "Next", ActionID: "next"},
		{Type: ButtonURL, Text: "Docs", URL: "https://example.com/docs"},
		{Type: ButtonSwitchInline, Text: "Search", InlineQuery: "help ", SamePeer: true},
	}}}
	if err := view.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
