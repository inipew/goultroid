package ui

import (
	"strings"
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

// Text builds the full message text.
func (s *Screen) Text() string {
	var sb strings.Builder
	if strings.TrimSpace(s.Title) != "" {
		sb.WriteString("<b>")
		sb.WriteString(EscapeHTML(strings.TrimSpace(s.Title)))
		sb.WriteString("</b>\n\n")
	}
	sb.WriteString(s.Body)
	return sb.String()
}

// Render builds the text and Markup (Telegram-agnostic).
func (s *Screen) Render() (string, Markup) {
	return s.Text(), s.Markup()
}

// RenderedScreen represents the rendered presentation output ready for transmission (Telegram-agnostic).
type RenderedScreen struct {
	Text   string
	Markup Markup
}

// RenderScreen renders the screen into a structured RenderedScreen object.
func (s *Screen) RenderScreen() RenderedScreen {
	text, markup := s.Render()
	return RenderedScreen{
		Text:   text,
		Markup: markup,
	}
}

