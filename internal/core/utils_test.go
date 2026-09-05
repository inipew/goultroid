package core

import "testing"

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello world"},
		{"foo <bar> & baz", "foo &lt;bar&gt; &amp; baz"},
		{"<script>alert('x&y')</script>", "&lt;script&gt;alert('x&amp;y')&lt;/script&gt;"},
		{"a > b < c & d", "a &gt; b &lt; c &amp; d"},
		{"", ""},
	}

	for _, tt := range tests {
		got := EscapeHTML(tt.input)
		if got != tt.expected {
			t.Errorf("EscapeHTML(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
