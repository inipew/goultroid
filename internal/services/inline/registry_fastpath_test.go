package inline

import (
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type p0dTestHandler struct {
	pattern string
}

func (h *p0dTestHandler) Pattern() string     { return h.pattern }
func (h *p0dTestHandler) Description() string { return "p0d test handler" }
func (h *p0dTestHandler) HandleInline(*InlineContext) ([]InlineResult, error) {
	return nil, nil
}

type p0dCountingMatcher struct {
	calls int
	match bool
	args  []string
}

func (m *p0dCountingMatcher) Match(string) ([]string, bool) {
	m.calls++
	if !m.match {
		return nil, false
	}
	return append([]string(nil), m.args...), true
}

type p0dV2Handler struct {
	p0dTestHandler
	matcher InlineMatcher
}

func (h *p0dV2Handler) Matcher() InlineMatcher           { return h.matcher }
func (h *p0dV2Handler) AccessPolicy() InlineAccessPolicy { return InlineAccessPolicy{} }
func (h *p0dV2Handler) CachePolicy() CachePolicy         { return CacheNone }
func (h *p0dV2Handler) HandleInlineV2(*InlineContext) (*InlineResponse, error) {
	return &InlineResponse{}, nil
}

func TestP0DExactFastPathPreservesHigherPriorityCustomPrecedence(t *testing.T) {
	reg := NewRegistry()
	exact := &p0dTestHandler{pattern: "help"}
	if err := reg.RegisterWithPriority(exact, 10); err != nil {
		t.Fatal(err)
	}

	highMatcher := &p0dCountingMatcher{match: false}
	high := &p0dTestHandler{pattern: "high-custom"}
	if err := reg.RegisterMatcher(high.Pattern(), highMatcher, high, 20); err != nil {
		t.Fatal(err)
	}

	lowMatcher := &p0dCountingMatcher{match: true, args: []string{"shadow"}}
	low := &p0dTestHandler{pattern: "low-custom"}
	if err := reg.RegisterMatcher(low.Pattern(), lowMatcher, low, 5); err != nil {
		t.Fatal(err)
	}

	resolved, ok := reg.ResolveOwnedExplicit("help topic")
	if !ok || resolved.Handler != exact {
		t.Fatalf("resolved=%+v ok=%v, want exact help", resolved, ok)
	}
	if highMatcher.calls != 1 {
		t.Fatalf("higher-priority matcher calls=%d, want 1", highMatcher.calls)
	}
	if lowMatcher.calls != 0 {
		t.Fatalf("lower-priority matcher calls=%d, want 0 after exact fast-path boundary", lowMatcher.calls)
	}
	if len(resolved.Args) != 1 || resolved.Args[0] != "topic" {
		t.Fatalf("exact args=%v, want [topic]", resolved.Args)
	}
}

func TestP0DExactFastPathStillAllowsLongerSamePriorityCustomShadow(t *testing.T) {
	reg := NewRegistry()
	exact := &p0dTestHandler{pattern: "help"}
	if err := reg.RegisterWithPriority(exact, 10); err != nil {
		t.Fatal(err)
	}

	matcher := &p0dCountingMatcher{match: true, args: []string{"custom"}}
	custom := &p0dTestHandler{pattern: "longer-custom"}
	if err := reg.RegisterMatcher(custom.Pattern(), matcher, custom, 10); err != nil {
		t.Fatal(err)
	}

	resolved, ok := reg.ResolveOwnedExplicit("help topic")
	if !ok || resolved.Handler != custom {
		t.Fatalf("resolved=%+v ok=%v, want longer same-priority custom handler", resolved, ok)
	}
	if matcher.calls != 1 {
		t.Fatalf("custom matcher calls=%d, want 1", matcher.calls)
	}
}

func TestP0DExactFastPathPreservesRegistrationOrderForEqualRank(t *testing.T) {
	t.Run("custom registered first shadows exact", func(t *testing.T) {
		reg := NewRegistry()
		matcher := &p0dCountingMatcher{match: true}
		custom := &p0dTestHandler{pattern: "cust"}
		if err := reg.RegisterMatcher(custom.Pattern(), matcher, custom, 10); err != nil {
			t.Fatal(err)
		}
		exact := &p0dTestHandler{pattern: "help"}
		if err := reg.RegisterWithPriority(exact, 10); err != nil {
			t.Fatal(err)
		}

		resolved, ok := reg.ResolveOwnedExplicit("help")
		if !ok || resolved.Handler != custom {
			t.Fatalf("resolved=%+v ok=%v, want earlier equal-rank custom", resolved, ok)
		}
	})

	t.Run("exact registered first stops before equal-rank custom", func(t *testing.T) {
		reg := NewRegistry()
		exact := &p0dTestHandler{pattern: "help"}
		if err := reg.RegisterWithPriority(exact, 10); err != nil {
			t.Fatal(err)
		}
		matcher := &p0dCountingMatcher{match: true}
		custom := &p0dTestHandler{pattern: "cust"}
		if err := reg.RegisterMatcher(custom.Pattern(), matcher, custom, 10); err != nil {
			t.Fatal(err)
		}

		resolved, ok := reg.ResolveOwnedExplicit("help")
		if !ok || resolved.Handler != exact {
			t.Fatalf("resolved=%+v ok=%v, want earlier equal-rank exact", resolved, ok)
		}
		if matcher.calls != 0 {
			t.Fatalf("later equal-rank custom matcher calls=%d, want 0", matcher.calls)
		}
	})
}

func TestP0DCustomDirectPatternFallbackRemainsCompatible(t *testing.T) {
	reg := NewRegistry()
	matcher := &p0dCountingMatcher{match: false}
	custom := &p0dTestHandler{pattern: "math"}
	if err := reg.RegisterMatcher(custom.Pattern(), matcher, custom, 10); err != nil {
		t.Fatal(err)
	}

	resolved, ok := reg.ResolveOwnedExplicit("math not-a-formula")
	if !ok || resolved.Handler != custom {
		t.Fatalf("resolved=%+v ok=%v, want direct custom-pattern compatibility fallback", resolved, ok)
	}
	if matcher.calls != 1 {
		t.Fatalf("matcher calls=%d, want 1 before direct fallback", matcher.calls)
	}
	if len(resolved.Args) != 1 || resolved.Args[0] != "not-a-formula" {
		t.Fatalf("fallback args=%v", resolved.Args)
	}
}

func TestP0DCustomOwnedRegistrationCloseRemovesMatcherIndex(t *testing.T) {
	reg := NewRegistry()
	matcher := &p0dCountingMatcher{match: true}
	handler := &p0dV2Handler{
		p0dTestHandler: p0dTestHandler{pattern: "owned-custom"},
		matcher:        matcher,
	}
	registration, err := reg.RegisterOwned(
		"p0d",
		"custom",
		tasks.ScopeIdentity{Owner: "plugin:p0d", Generation: 1},
		handler,
		10,
	)
	if err != nil {
		t.Fatal(err)
	}

	resolved, ok := reg.ResolveOwnedExplicit("anything")
	if !ok || resolved.Handler != handler {
		t.Fatalf("custom owned handler did not resolve before Close: %+v ok=%v", resolved, ok)
	}
	registration.Close()

	if resolved, ok := reg.ResolveOwnedExplicit("anything"); ok {
		t.Fatalf("closed custom matcher still resolved: %+v", resolved)
	}
}

func TestP0DRegistryRankComparatorMatchesLegacyOrder(t *testing.T) {
	entries := []registryEntry{
		{pattern: "short", priority: 20, token: 4},
		{pattern: "much-longer", priority: 10, token: 3},
		{pattern: "same", priority: 10, token: 1},
		{pattern: "same", priority: 10, token: 2},
	}
	for i := 1; i < len(entries); i++ {
		if !registryEntryPrecedes(entries[i-1], entries[i]) {
			t.Fatalf("entry %d (%+v) should precede %d (%+v)", i-1, entries[i-1], i, entries[i])
		}
	}
}
