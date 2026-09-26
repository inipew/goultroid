package ui

import (
	"fmt"

	"github.com/inipew/goultroid/internal/presentation"
)

// ButtonType identifies the behavior of an inline keyboard button.
type ButtonType int

const (
	// ButtonCallback represents a button that sends a callback query with Data.
	ButtonCallback ButtonType = iota
	// ButtonURL represents a button that opens a web URL.
	ButtonURL
	// ButtonSwitchInline represents a button that prompts inline query in chat.
	ButtonSwitchInline
)

// Button represents a high-level inline keyboard button.
type Button struct {
	Type        ButtonType
	Text        string
	Data        []byte
	URL         string
	InlineQuery string
	SamePeer    bool
}

// ButtonRow represents a horizontal row of inline buttons.
type ButtonRow []Button

// Markup represents a matrix of inline keyboard button rows.
type Markup struct {
	Rows []ButtonRow
}

// NewCallbackButton creates a button that triggers a callback query.
func NewCallbackButton(text string, data []byte) Button {
	return Button{
		Type: ButtonCallback,
		Text: text,
		Data: data,
	}
}

// NewRoleCallbackButton adapts the canonical presentation role vocabulary to
// the legacy callback payload model without changing callback ownership.
func NewRoleCallbackButton(role presentation.ButtonRole, data []byte) Button {
	return NewCallbackButton(presentation.ButtonLabel(role), data)
}

// NewURLButton creates a button that opens an external HTTP/HTTPS URL.
func NewURLButton(text string, url string) Button {
	return Button{
		Type: ButtonURL,
		Text: text,
		URL:  url,
	}
}

// NewRoleURLButton adapts a canonical role label to a legacy URL button.
func NewRoleURLButton(role presentation.ButtonRole, url string) Button {
	return NewURLButton(presentation.ButtonLabel(role), url)
}

// NewSwitchInlineButton creates a button that switches to inline query mode.
func NewSwitchInlineButton(text string, query string, samePeer bool) Button {
	return Button{
		Type:        ButtonSwitchInline,
		Text:        text,
		InlineQuery: query,
		SamePeer:    samePeer,
	}
}

// NewRoleSwitchInlineButton adapts a canonical role label to legacy switch-inline metadata.
func NewRoleSwitchInlineButton(role presentation.ButtonRole, query string, samePeer bool) Button {
	return NewSwitchInlineButton(presentation.ButtonLabel(role), query, samePeer)
}

// NewMarkup initializes a Markup with the given rows.
func NewMarkup(rows ...ButtonRow) Markup {
	return Markup{Rows: rows}
}

// NoopData is the standard non-interactive callback payload (page indicators, placeholders, etc.).
var NoopData = []byte("noop")

// NewPaginationRow creates a standard pagination navigation row.
// e.g. [ < ] [ 2 / 5 ] [ > ]
func NewPaginationRow(prevData, nextData []byte, currentPage, totalPages int) ButtonRow {
	var row ButtonRow

	if len(prevData) > 0 && currentPage > 1 {
		row = append(row, NewRoleCallbackButton(presentation.ButtonRolePrevious, prevData))
	}

	label := fmt.Sprintf("%d / %d", currentPage, totalPages)
	row = append(row, NewCallbackButton(label, NoopData))

	if len(nextData) > 0 && currentPage < totalPages {
		row = append(row, NewRoleCallbackButton(presentation.ButtonRoleNext, nextData))
	}

	return row
}

// NewConfirmCancelRow creates a standard confirmation and cancellation button row.
func NewConfirmCancelRow(confirmData, cancelData []byte) ButtonRow {
	return ButtonRow{
		NewRoleCallbackButton(presentation.ButtonRoleConfirm, confirmData),
		NewRoleCallbackButton(presentation.ButtonRoleCancel, cancelData),
	}
}

// --- Phase 3 UX helpers ---

// NewCloseRow creates a single Close button row. Handler should call DisableButtons on click.
func NewCloseRow(closeData []byte) ButtonRow {
	if len(closeData) == 0 {
		closeData = []byte("noop")
	}
	return ButtonRow{NewRoleCallbackButton(presentation.ButtonRoleClose, closeData)}
}

// NewBackRow creates a Back navigation button.
func NewBackRow(backData []byte) ButtonRow {
	return ButtonRow{NewRoleCallbackButton(presentation.ButtonRoleBack, backData)}
}

// NewPaginationMarkup wraps NewPaginationRow into a Markup for convenience.
func NewPaginationMarkup(prevData, nextData []byte, currentPage, totalPages int) Markup {
	return NewMarkup(NewPaginationRow(prevData, nextData, currentPage, totalPages))
}

// NewConfirmCancelMarkup wraps confirm/cancel row into Markup.
func NewConfirmCancelMarkup(confirmData, cancelData []byte) Markup {
	return NewMarkup(NewConfirmCancelRow(confirmData, cancelData))
}

// NewCloseMarkup creates a markup with a single Close button.
func NewCloseMarkup(closeData []byte) Markup {
	return NewMarkup(NewCloseRow(closeData))
}

// NewBackMarkup creates a markup with a Back button.
func NewBackMarkup(backData []byte) Markup {
	return NewMarkup(NewBackRow(backData))
}

// NewHelpSwitchRow creates a SwitchInline row for inline help search.
func NewHelpSwitchRow(query string) ButtonRow {
	if query == "" {
		query = "help"
	}
	return ButtonRow{NewRoleSwitchInlineButton(presentation.ButtonRoleHelp, query, false)}
}

// NewCommonResultRow creates a row with common inline result actions: URL + SwitchInline Help.
func NewCommonResultRow(helpQuery string, url string, urlText string) ButtonRow {
	var row ButtonRow
	if url != "" {
		if urlText == "" {
			urlText = presentation.ButtonLabel(presentation.ButtonRoleOpen)
		}
		row = append(row, NewURLButton(urlText, url))
	}
	if helpQuery != "" {
		row = append(row, NewRoleSwitchInlineButton(presentation.ButtonRoleHelp, helpQuery, false))
	}
	return row
}

// NewStandardActionRow builds a row with Callback + URL + SwitchInline as needed.
// Empty data/url/query are skipped.
func NewStandardActionRow(callbackData []byte, callbackText string, url string, urlText string, inlineQuery string, inlineText string) ButtonRow {
	var row ButtonRow
	if len(callbackData) > 0 {
		if callbackText == "" {
			callbackText = presentation.ButtonLabel(presentation.ButtonRoleAction)
		}
		row = append(row, NewCallbackButton(callbackText, callbackData))
	}
	if url != "" {
		if urlText == "" {
			urlText = presentation.ButtonLabel(presentation.ButtonRoleOpen)
		}
		row = append(row, NewURLButton(urlText, url))
	}
	if inlineQuery != "" {
		if inlineText == "" {
			inlineText = presentation.ButtonLabel(presentation.ButtonRoleSearch)
		}
		row = append(row, NewSwitchInlineButton(inlineText, inlineQuery, false))
	}
	return row
}
