package filters

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type keywordEdge struct {
	value byte
	next  int
}

type keywordNode struct {
	edges   []keywordEdge
	fail    int
	outputs []int
}

type keywordMatcher struct {
	nodes        []keywordNode
	keywordBytes []int
}

func newKeywordMatcher(filters []compiledFilter) *keywordMatcher {
	matcher := &keywordMatcher{
		nodes:        []keywordNode{{}},
		keywordBytes: make([]int, len(filters)),
	}
	for i := range filters {
		keyword := filters[i].keyword
		matcher.keywordBytes[i] = len(keyword)
		if keyword == "" {
			continue
		}
		state := 0
		for j := 0; j < len(keyword); j++ {
			next, ok := matcher.transition(state, keyword[j])
			if !ok {
				next = len(matcher.nodes)
				matcher.nodes = append(matcher.nodes, keywordNode{})
				matcher.nodes[state].edges = append(matcher.nodes[state].edges, keywordEdge{value: keyword[j], next: next})
			}
			state = next
		}
		matcher.nodes[state].outputs = append(matcher.nodes[state].outputs, i)
	}
	matcher.buildFailures()
	return matcher
}

func (m *keywordMatcher) transition(state int, value byte) (int, bool) {
	for _, edge := range m.nodes[state].edges {
		if edge.value == value {
			return edge.next, true
		}
	}
	return 0, false
}

func (m *keywordMatcher) buildFailures() {
	queue := make([]int, 0, len(m.nodes))
	for _, edge := range m.nodes[0].edges {
		queue = append(queue, edge.next)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for _, edge := range m.nodes[state].edges {
			nextState := edge.next
			queue = append(queue, nextState)

			failure := m.nodes[state].fail
			for failure != 0 {
				if _, ok := m.transition(failure, edge.value); ok {
					break
				}
				failure = m.nodes[failure].fail
			}
			if next, ok := m.transition(failure, edge.value); ok && next != nextState {
				m.nodes[nextState].fail = next
			}
			fallback := m.nodes[nextState].fail
			m.nodes[nextState].outputs = append(m.nodes[nextState].outputs, m.nodes[fallback].outputs...)
		}
	}
}

func (m *keywordMatcher) firstMatch(text string) int {
	if m == nil || len(m.nodes) == 0 || text == "" {
		return -1
	}
	lowerText := strings.ToLower(text)
	state := 0
	best := -1
	for pos := 0; pos < len(lowerText); pos++ {
		value := lowerText[pos]
		for state != 0 {
			if _, ok := m.transition(state, value); ok {
				break
			}
			state = m.nodes[state].fail
		}
		if next, ok := m.transition(state, value); ok {
			state = next
		}
		for _, filterIndex := range m.nodes[state].outputs {
			if best >= 0 && filterIndex >= best {
				continue
			}
			keywordBytes := m.keywordBytes[filterIndex]
			start := pos + 1 - keywordBytes
			if start < 0 || !filterBoundaryOK(lowerText, start, pos+1) {
				continue
			}
			best = filterIndex
			if best == 0 {
				return 0
			}
		}
	}
	return best
}

func filterBoundaryOK(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if isFilterWordRune(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if isFilterWordRune(next) {
			return false
		}
	}
	return true
}

func isFilterWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}
