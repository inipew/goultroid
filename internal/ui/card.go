package ui

import (
	"strings"
)

// Field represents a key-value row in a Card.
type Field struct {
	Key   string
	Value string
}

// Card is a builder for structured, aesthetic Telegram messages.
type Card struct {
	icon               string
	title              string
	header             string
	fields             []Field
	collapsibleContent string
	rawContent         string
	footer             string
}

// NewCard initializes a new Card builder with a title.
func NewCard(title string) *Card {
	return &Card{
		title: title,
	}
}

// WithIcon sets the leading emoji icon for the card header.
func (c *Card) WithIcon(icon string) *Card {
	c.icon = strings.TrimSpace(icon)
	return c
}

// WithHeader sets an introductory description below the title.
func (c *Card) WithHeader(header string) *Card {
	c.header = strings.TrimSpace(header)
	return c
}

// AddField adds a key-value bullet point.
func (c *Card) AddField(key, value string) *Card {
	c.fields = append(c.fields, Field{
		Key:   strings.TrimSpace(key),
		Value: strings.TrimSpace(value),
	})
	return c
}

// WithCollapsible wraps the given text inside an expandable blockquote.
func (c *Card) WithCollapsible(content string) *Card {
	c.collapsibleContent = strings.TrimSpace(content)
	return c
}

// WithRaw appends free-form text or custom formatted sections.
func (c *Card) WithRaw(content string) *Card {
	c.rawContent = strings.TrimSpace(content)
	return c
}

// WithFooter sets a closing note or hint at the bottom of the card.
func (c *Card) WithFooter(footer string) *Card {
	c.footer = strings.TrimSpace(footer)
	return c
}

// Render builds the formatted HTML string.
func (c *Card) Render() string {
	var b strings.Builder

	// Title
	if c.icon != "" && c.title != "" {
		b.WriteString(c.icon + " <b>" + EscapeHTML(c.title) + "</b>\n")
	} else if c.title != "" {
		b.WriteString("<b>" + EscapeHTML(c.title) + "</b>\n")
	}

	// Header
	if c.header != "" {
		b.WriteString(c.header + "\n")
	}

	// Spacing before fields or content
	hasBody := len(c.fields) > 0 || c.rawContent != "" || c.collapsibleContent != ""
	hasHead := c.title != "" || c.header != ""
	if hasHead && hasBody {
		b.WriteString("\n")
	}

	// Fields
	for i, f := range c.fields {
		b.WriteString(KeyValue(f.Key, f.Value))
		if i < len(c.fields)-1 || c.rawContent != "" || c.collapsibleContent != "" {
			b.WriteString("\n")
		}
	}

	// Raw Content
	if c.rawContent != "" {
		if len(c.fields) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(c.rawContent + "\n")
	}

	// Collapsible Section
	if c.collapsibleContent != "" {
		if len(c.fields) > 0 || c.rawContent != "" {
			b.WriteString("\n")
		}
		b.WriteString("<blockquote expandable>\n" + c.collapsibleContent + "\n</blockquote>\n")
	}

	// Footer
	if c.footer != "" {
		b.WriteString("\n" + c.footer)
	}

	return strings.TrimSpace(b.String())
}
