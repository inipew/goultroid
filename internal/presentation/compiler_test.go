package presentation

import "testing"

func TestViewValidateRejectsRawOrInvalidActionIdentity(t *testing.T) {
	cases := []View{
		{},
		{Text: "x", Rows: []Row{{{Text: "", ActionID: "next"}}}},
		{Text: "x", Rows: []Row{{{Text: "Next", ActionID: "A:raw"}}}},
	}
	for _, view := range cases {
		if err := view.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded", view)
		}
	}
}

func TestViewValidateAcceptsTypedActionButtons(t *testing.T) {
	view := View{Text: "hello", Rows: []Row{{{Text: "Next", ActionID: "next"}}}}
	if err := view.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
