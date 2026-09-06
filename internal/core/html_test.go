package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramHTML_ShortText(t *testing.T) {
	text := "Hello, World! <b>Bold</b> and <i>italic</i>."
	chunks := SplitTelegramHTML(text, 4096)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != text {
		t.Errorf("chunk mismatch: got %q, want %q", chunks[0], text)
	}
}

func TestSplitTelegramHTML_PlainTextSplitting(t *testing.T) {
	line1 := strings.Repeat("A", 50) + "\n"
	line2 := strings.Repeat("B", 50) + "\n"
	line3 := strings.Repeat("C", 50)
	text := line1 + line2 + line3

	chunks := SplitTelegramHTML(text, 60)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > 60 {
			t.Errorf("chunk %d exceeds max runes: len=%d, text=%q", i, utf8.RuneCountInString(c), c)
		}
	}
}

func TestSplitTelegramHTML_TagBalancing(t *testing.T) {
	// Long text wrapped inside <b> and <i>
	inner := strings.Repeat("X", 80)
	text := "<b><i>" + inner + "</i></b>"

	// Split with maxRunes = 50
	chunks := SplitTelegramHTML(text, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Chunk 0 must start with <b><i> and end with </i></b>
	if !strings.HasPrefix(chunks[0], "<b><i>") {
		t.Errorf("chunk 0 should start with <b><i>, got: %s", chunks[0])
	}
	if !strings.HasSuffix(chunks[0], "</i></b>") {
		t.Errorf("chunk 0 should close tags with </i></b>, got: %s", chunks[0])
	}

	// Chunk 1 must reopen tags with <b><i> and close with </i></b>
	if !strings.HasPrefix(chunks[1], "<b><i>") {
		t.Errorf("chunk 1 should reopen with <b><i>, got: %s", chunks[1])
	}
	if !strings.HasSuffix(chunks[1], "</i></b>") {
		t.Errorf("chunk 1 should close with </i></b>, got: %s", chunks[1])
	}
}

func TestSplitTelegramHTML_UnicodeRunes(t *testing.T) {
	// 4-byte UTF-8 emojis
	emojiText := strings.Repeat("🚀🌟🔥✨", 20)
	chunks := SplitTelegramHTML(emojiText, 30)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d contains invalid UTF-8 bytes!", i)
		}
	}
}
