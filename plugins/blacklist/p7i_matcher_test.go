package blacklist

import (
	"fmt"
	"testing"
)

func TestP7IBlacklistMatcherPreservesWordBoundarySemantics(t *testing.T) {
	items := compileBlacklist([]string{"scam", "free crypto", "hello"})
	matcher := newBlacklistMatcher(items)

	tests := []struct {
		text string
		want bool
	}{
		{text: "SCAM", want: true},
		{text: "this is a scam!", want: true},
		{text: "hello there", want: true},
		{text: "join free crypto now", want: true},
		{text: "scammer", want: false},
		{text: "free cryptocurrencies", want: false},
		{text: "foo_scam_bar", want: false},
		{text: "🔥scam🔥", want: true},
	}
	for _, tc := range tests {
		if got := matcher.matches(tc.text); got != tc.want {
			t.Fatalf("matches(%q)=%v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestP7IBlacklistMatcherHandlesMaxPerChatWithoutRuleScan(t *testing.T) {
	raw := make([]string, 0, MaxRulesPerChat)
	for i := 0; i < MaxRulesPerChat-1; i++ {
		raw = append(raw, fmt.Sprintf("rule-%03d", i))
	}
	raw = append(raw, "needle phrase")

	matcher := newBlacklistMatcher(compileBlacklist(raw))
	if !matcher.matches("prefix: needle phrase!") {
		t.Fatal("max-cardinality matcher missed terminal rule")
	}
	if matcher.matches("needle phrases") {
		t.Fatal("max-cardinality matcher ignored word boundary")
	}
}

func TestP7IBlacklistRevisionIsChatScoped(t *testing.T) {
	p := New(nil, nil)

	p.invalidateChat(10, true)
	rev10 := p.AssistantRuleRevision(10)
	if rev10 == 0 {
		t.Fatal("chat 10 revision did not advance")
	}

	p.invalidateChat(20, true)
	rev20 := p.AssistantRuleRevision(20)
	if rev20 == 0 {
		t.Fatal("chat 20 revision did not advance")
	}
	if got := p.AssistantRuleRevision(10); got != rev10 {
		t.Fatalf("chat 20 mutation changed chat 10 revision: got=%d want=%d", got, rev10)
	}

	p.invalidateChat(10, true)
	if got := p.AssistantRuleRevision(10); got == rev10 || got == 0 {
		t.Fatalf("chat 10 revision did not advance independently: old=%d new=%d", rev10, got)
	}
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("chat 10 mutation changed chat 20 revision: got=%d want=%d", got, rev20)
	}

	p.invalidateChat(10, false)
	if got := p.AssistantRuleRevision(10); got != 0 {
		t.Fatalf("inactive chat retained revision=%d", got)
	}
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("deactivating chat 10 changed chat 20 revision: got=%d want=%d", got, rev20)
	}
}
