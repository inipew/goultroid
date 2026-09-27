//go:build linux

package app

import "testing"

func TestRSSStatmParser(t *testing.T) {
	got, ok := parseRSSStatm([]byte("100 42 0 0 0 0 0"), 4096)
	if !ok || got != 42*4096 {
		t.Fatalf("RSS = %d, available = %v", got, ok)
	}
	for _, input := range []string{"", "100", "100 invalid", "100 0"} {
		if _, ok := parseRSSStatm([]byte(input), 4096); ok {
			t.Fatalf("invalid statm %q reported available", input)
		}
	}
}
