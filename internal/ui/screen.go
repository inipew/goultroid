package ui

import (
	"strings"

	"github.com/gotd/td/tg"
)

// Screen represents a single navigable UI view containing text and inline button rows.
type Screen struct {
	ID       string
	Title    string
	Body     string
	Rows     []ButtonRow
	Metadata map[string]string
}

// NewScreen creates an initialized Screen.
func NewScreen(id, title, body string) *Screen {
	return &Screen{
		ID:       id,
		Title:    title,
		Body:     body,
		Rows:     make([]ButtonRow, 0),
		Metadata: make(map[string]string),
	}
}

// AddRow appends a row of buttons to the screen.
func (s *Screen) AddRow(buttons ...Button) *Screen {
	if len(buttons) > 0 {
		s.Rows = append(s.Rows, buttons)
	}
	return s
}

// Markup returns the UI Markup constructed from the screen rows.
func (s *Screen) Markup() Markup {
	return Markup{Rows: s.Rows}
}

// Render builds the full message text and tg.ReplyMarkupClass.
func (s *Screen) Render() (string, *tg.ReplyInlineMarkup) {
	var sb strings.Builder
	if strings.TrimSpace(s.Title) != "" {
		sb.WriteString("**")
		sb.WriteString(strings.TrimSpace(s.Title))
		sb.WriteString("**\n\n")
	}
	sb.WriteString(s.Body)
	text := sb.String()

	markupClass := Markup{Rows: s.Rows}.ToTelegramMarkup()
	if markupClass == nil {
		return text, nil
	}

	if inlineMarkup, ok := markupClass.(*tg.ReplyInlineMarkup); ok {
		return text, inlineMarkup
	}

	return text, nil
}
