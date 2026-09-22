package blacklist

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type blacklistEdge struct {
	value byte
	next  int
}

type blacklistNode struct {
	edges []blacklistEdge
	fail  int
	match bool
}

type blacklistMatcher struct {
	nodes []blacklistNode
}

func newBlacklistMatcher(items []compiledBlacklist) *blacklistMatcher {
	matcher := &blacklistMatcher{nodes: []blacklistNode{{}}}
	for _, item := range items {
		word := item.word
		if word == "" {
			continue
		}
		state := 0
		for i := 0; i < len(word); i++ {
			next, ok := matcher.transition(state, word[i])
			if !ok {
				next = len(matcher.nodes)
				matcher.nodes = append(matcher.nodes, blacklistNode{})
				matcher.nodes[state].edges = append(
					matcher.nodes[state].edges,
					blacklistEdge{value: word[i], next: next},
				)
			}
			state = next
		}
		matcher.nodes[state].match = true
	}
	matcher.buildFailures()
	return matcher
}

func (m *blacklistMatcher) transition(state int, value byte) (int, bool) {
	for _, edge := range m.nodes[state].edges {
		if edge.value == value {
			return edge.next, true
		}
	}
	return 0, false
}

func (m *blacklistMatcher) buildFailures() {
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
			if m.nodes[m.nodes[nextState].fail].match {
				m.nodes[nextState].match = true
			}
		}
	}
}

func (m *blacklistMatcher) matches(text string) bool {
	if m == nil || len(m.nodes) == 0 || text == "" {
		return false
	}
	lowerText := strings.ToLower(text)
	state := 0
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
		if !m.nodes[state].match {
			continue
		}

		// A state may inherit a shorter suffix match through its failure link.
		// Walk the failure chain to find a boundary-valid terminal without
		// rescanning every configured rule.
		candidate := state
		for {
			if m.nodes[candidate].match {
				length := blacklistTerminalLength(m, candidate)
				if length > 0 {
					start := pos + 1 - length
					if start >= 0 && blacklistBoundaryOK(lowerText, start, pos+1) {
						return true
					}
				}
			}
			if candidate == 0 {
				break
			}
			candidate = m.nodes[candidate].fail
		}
	}
	return false
}

// blacklistTerminalLength reconstructs the byte length represented by a trie
// state. The rule set is bounded to 512x256 bytes, so this control-path helper
// remains bounded; hot-path matching never loops over every configured rule.
func blacklistTerminalLength(m *blacklistMatcher, target int) int {
	if m == nil || target <= 0 || target >= len(m.nodes) {
		return 0
	}
	// Trie nodes do not currently retain depth; derive it by one bounded walk
	// from root. This is only called for terminal candidates.
	type entry struct {
		state int
		depth int
	}
	queue := []entry{{state: 0, depth: 0}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range m.nodes[current.state].edges {
			depth := current.depth + 1
			if edge.next == target {
				return depth
			}
			queue = append(queue, entry{state: edge.next, depth: depth})
		}
	}
	return 0
}

func blacklistBoundaryOK(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if isBlacklistWordRune(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if isBlacklistWordRune(next) {
			return false
		}
	}
	return true
}

func isBlacklistWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}
