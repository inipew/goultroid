package filters

import (
	"regexp"
	"strings"
	"testing"
)

func TestKeywordMatcherPreservesFilterOrder(t *testing.T) {
	matcher := newKeywordMatcher([]compiledFilter{
		{keyword: "rules"},
		{keyword: "hello"},
	})
	if got := matcher.firstMatch("hello first, rules later"); got != 0 {
		t.Fatalf("match=%d, want first configured filter index 0", got)
	}
}

func TestKeywordMatcherPreservesUnicodeWordBoundaries(t *testing.T) {
	matcher := newKeywordMatcher([]compiledFilter{{keyword: "café"}})
	for _, text := range []string{"xcafé", "café42", "café_name"} {
		if got := matcher.firstMatch(text); got != -1 {
			t.Fatalf("%q matched at %d", text, got)
		}
	}
	if got := matcher.firstMatch("VISIT CAFÉ!"); got != 0 {
		t.Fatalf("valid unicode boundary match=%d", got)
	}
}

func TestKeywordMatcherChecksFailureOutputsIndependently(t *testing.T) {
	matcher := newKeywordMatcher([]compiledFilter{
		{keyword: "he"},
		{keyword: "she"},
	})
	if got := matcher.firstMatch("she!"); got != 1 {
		t.Fatalf("match=%d, want 1 because suffix 'he' has no left boundary", got)
	}
}

func TestKeywordMatcherIgnoresEmptyLegacyKeyword(t *testing.T) {
	matcher := newKeywordMatcher([]compiledFilter{{keyword: ""}, {keyword: "hello"}})
	if got := matcher.firstMatch("hello!"); got != 1 {
		t.Fatalf("match=%d, want 1", got)
	}
}

func TestKeywordMatcherMatchesLegacyRegexSemantics(t *testing.T) {
	keywords := []string{"hello", "good morning", "café", "he", "she", "rule_1", "42"}
	filters := make([]compiledFilter, len(keywords))
	regexes := make([]*regexp.Regexp, len(keywords))
	for i, keyword := range keywords {
		keyword = strings.ToLower(keyword)
		filters[i].keyword = keyword
		pattern := `(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(keyword) + `(?:$|[^\p{L}\p{N}_])`
		regexes[i] = regexp.MustCompile(pattern)
	}
	matcher := newKeywordMatcher(filters)

	messages := []string{
		"hello world",
		"othello",
		"say GOOD MORNING!",
		"xcafé",
		"visit CAFÉ!",
		"she!",
		"rule_1 is exact",
		"rule_12 is not",
		"answer 42.",
		"answer 420.",
		"nothing matches here",
	}
	for _, message := range messages {
		lower := strings.ToLower(message)
		want := -1
		for i, re := range regexes {
			if re.MatchString(lower) {
				want = i
				break
			}
		}
		if got := matcher.firstMatch(message); got != want {
			t.Fatalf("firstMatch(%q)=%d, legacy=%d", message, got, want)
		}
	}
}
