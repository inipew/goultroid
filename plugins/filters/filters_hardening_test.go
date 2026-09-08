package filters

import "testing"

func TestCommandsAreGroupOnly(t *testing.T) {
	p := New(nil, nil)
	for _, cmd := range p.Commands() {
		if !cmd.GroupOnly {
			t.Fatalf("command %q must be group-only", cmd.Name)
		}
	}
}

func TestMatchFilterUsesWholeTokenBoundaries(t *testing.T) {
	cases := []struct {
		text, keyword string
		want          bool
	}{
		{"hello foo world", "foo", true},
		{"foobar", "foo", false},
		{"FOO!", "foo", true},
		{"café foo_bar", "foo", false},
	}
	for _, tc := range cases {
		if got := matchFilter(tc.text, tc.keyword); got != tc.want {
			t.Errorf("matchFilter(%q, %q) = %v, want %v", tc.text, tc.keyword, got, tc.want)
		}
	}
}
