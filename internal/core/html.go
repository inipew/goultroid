package core

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	htmlTagRegex = regexp.MustCompile(`^<(/)?([a-zA-Z0-9_\-]+)([^>]*)>`)
)

type openTag struct {
	name    string
	fullTag string
}

// SplitTelegramHTML splits an HTML-formatted message into chunks of at most maxRunes
// characters (default 4096), preserving UTF-8 rune boundaries and balancing HTML tags
// across chunk boundaries (closing open tags at chunk end and reopening them at the next chunk start).
func SplitTelegramHTML(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 4096
	}

	if utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}

	var chunks []string
	remaining := text
	var tagStack []openTag

	for len(remaining) > 0 {
		// Calculate overhead of reopening tags from stack
		var reopenPrefix strings.Builder
		for _, tag := range tagStack {
			reopenPrefix.WriteString(tag.fullTag)
		}
		prefix := reopenPrefix.String()
		prefixRunes := utf8.RuneCountInString(prefix)

		// Available runes in this chunk
		available := maxRunes - prefixRunes
		if available <= 100 {
			// If tag overhead is enormous, reset available to avoid infinite loops
			available = maxRunes / 2
		}

		// Find slice of remaining that fits within available runes, accounting for closing tags
		chunkBody, rest, newStack := splitOneChunk(remaining, available, tagStack)
		if chunkBody == "" && len(rest) == len(remaining) {
			// Fallback: forced break to avoid infinite loop
			r, size := utf8.DecodeRuneInString(remaining)
			chunkBody = string(r)
			rest = remaining[size:]
		}

		// Close any still-open tags at chunk end
		var closeSuffix strings.Builder
		for i := len(newStack) - 1; i >= 0; i-- {
			closeSuffix.WriteString(fmt.Sprintf("</%s>", newStack[i].name))
		}

		fullChunk := prefix + chunkBody + closeSuffix.String()
		chunks = append(chunks, fullChunk)

		remaining = rest
		tagStack = newStack
	}

	return chunks
}

func splitOneChunk(text string, maxRunes int, initialStack []openTag) (string, string, []openTag) {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		// All remaining fits!
		// Update stack to reflect remaining tags
		stack := copyStack(initialStack)
		updateStackWithText(text, &stack)
		return text, "", stack
	}

	// Try to find a good break point (prefer newline) within maxRunes
	// We scan rune by rune, tracking HTML tags and potential cut points
	bestCutIndex := -1
	inTag := false
	var tagBuf strings.Builder
	currentStack := copyStack(initialStack)
	bestCutStack := copyStack(initialStack)

	for i := 0; i < len(runes) && i < maxRunes; i++ {
		ch := runes[i]
		if ch == '<' {
			inTag = true
			tagBuf.Reset()
			tagBuf.WriteRune(ch)
			continue
		}
		if inTag {
			tagBuf.WriteRune(ch)
			if ch == '>' {
				inTag = false
				tagStr := tagBuf.String()
				processTagString(tagStr, &currentStack)
			}
			continue
		}

		// Outside a tag: valid place to cut
		if ch == '\n' {
			bestCutIndex = i + 1 // include newline
			bestCutStack = copyStack(currentStack)
		}
	}

	// If a newline cut was found and it's reasonably far along (> maxRunes / 3), use it
	if bestCutIndex > maxRunes/3 && !inTag {
		cutRunes := runes[:bestCutIndex]
		restRunes := runes[bestCutIndex:]
		return string(cutRunes), string(restRunes), bestCutStack
	}

	// Otherwise, cut as close to maxRunes as possible without breaking an HTML tag
	cutAt := maxRunes
	if inTag {
		// Find start of current unclosed tag
		for cutAt > 0 && runes[cutAt-1] != '<' {
			cutAt--
		}
		if cutAt > 0 {
			cutAt-- // exclude '<'
		}
	}

	if cutAt <= 0 {
		cutAt = maxRunes
	}

	// Recompute stack up to cutAt
	resultStack := copyStack(initialStack)
	updateStackWithText(string(runes[:cutAt]), &resultStack)

	return string(runes[:cutAt]), string(runes[cutAt:]), resultStack
}

func processTagString(tagStr string, stack *[]openTag) {
	m := htmlTagRegex.FindStringSubmatch(tagStr)
	if len(m) < 3 {
		return
	}
	isClosing := m[1] == "/"
	tagName := strings.ToLower(m[2])

	if isClosing {
		// Pop matching tag from stack (search from top)
		s := *stack
		for i := len(s) - 1; i >= 0; i-- {
			if strings.EqualFold(s[i].name, tagName) {
				*stack = append(s[:i], s[i+1:]...)
				break
			}
		}
	} else {
		// Self-closing tags don't get pushed
		if strings.HasSuffix(tagStr, "/>") || tagName == "br" || tagName == "hr" {
			return
		}
		*stack = append(*stack, openTag{name: tagName, fullTag: tagStr})
	}
}

func updateStackWithText(text string, stack *[]openTag) {
	inTag := false
	var tagBuf strings.Builder
	for _, ch := range text {
		if ch == '<' {
			inTag = true
			tagBuf.Reset()
			tagBuf.WriteRune(ch)
			continue
		}
		if inTag {
			tagBuf.WriteRune(ch)
			if ch == '>' {
				inTag = false
				processTagString(tagBuf.String(), stack)
			}
		}
	}
}

func copyStack(s []openTag) []openTag {
	res := make([]openTag, len(s))
	copy(res, s)
	return res
}
