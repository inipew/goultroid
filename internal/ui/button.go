package ui

import (
	"fmt"

	"github.com/gotd/td/tg"
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

// NewURLButton creates a button that opens an external HTTP/HTTPS URL.
func NewURLButton(text string, url string) Button {
	return Button{
		Type: ButtonURL,
		Text: text,
		URL:  url,
	}
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

// NewMarkup initializes a Markup with the given rows.
func NewMarkup(rows ...ButtonRow) Markup {
	return Markup{Rows: rows}
}

// NewPaginationRow creates a standard pagination navigation row.
// e.g. [ < ] [ 2 / 5 ] [ > ]
func NewPaginationRow(prevData, nextData []byte, currentPage, totalPages int) ButtonRow {
	var row ButtonRow

	if len(prevData) > 0 && currentPage > 1 {
		row = append(row, NewCallbackButton("◀ Prev", prevData))
	}

	label := fmt.Sprintf("%d / %d", currentPage, totalPages)
	row = append(row, NewCallbackButton(label, []byte("noop")))

	if len(nextData) > 0 && currentPage < totalPages {
		row = append(row, NewCallbackButton("Next ▶", nextData))
	}

	return row
}

// NewConfirmCancelRow creates a standard confirmation and cancellation button row.
func NewConfirmCancelRow(confirmData, cancelData []byte) ButtonRow {
	return ButtonRow{
		NewCallbackButton("✅ Confirm", confirmData),
		NewCallbackButton("❌ Cancel", cancelData),
	}
}

// --- Phase 3 UX helpers ---

// NewCloseRow creates a single Close button row. Handler should call DisableButtons on click.
func NewCloseRow(closeData []byte) ButtonRow {
	if len(closeData) == 0 {
		closeData = []byte("noop")
	}
	return ButtonRow{NewCallbackButton("✖ Close", closeData)}
}

// NewBackRow creates a Back navigation button.
func NewBackRow(backData []byte) ButtonRow {
	return ButtonRow{NewCallbackButton("◀ Back", backData)}
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
	return ButtonRow{NewSwitchInlineButton("🔍 Help", query, false)}
}

// NewCommonResultRow creates a row with common inline result actions: URL + SwitchInline Help.
func NewCommonResultRow(helpQuery string, url string, urlText string) ButtonRow {
	var row ButtonRow
	if url != "" {
		if urlText == "" {
			urlText = "🔗 Open"
		}
		row = append(row, NewURLButton(urlText, url))
	}
	if helpQuery != "" {
		row = append(row, NewSwitchInlineButton("🔍 Help", helpQuery, false))
	}
	return row
}

// NewStandardActionRow builds a row with Callback + URL + SwitchInline as needed.
// Empty data/url/query are skipped.
func NewStandardActionRow(callbackData []byte, callbackText string, url string, urlText string, inlineQuery string, inlineText string) ButtonRow {
	var row ButtonRow
	if len(callbackData) > 0 {
		if callbackText == "" {
			callbackText = "Action"
		}
		row = append(row, NewCallbackButton(callbackText, callbackData))
	}
	if url != "" {
		if urlText == "" {
			urlText = "Open"
		}
		row = append(row, NewURLButton(urlText, url))
	}
	if inlineQuery != "" {
		if inlineText == "" {
			inlineText = "Search"
		}
		row = append(row, NewSwitchInlineButton(inlineText, inlineQuery, false))
	}
	return row
}

// ToTelegramMarkup converts the high-level Markup into a Telegram MTProto tg.ReplyMarkupClass.
func (m Markup) ToTelegramMarkup() tg.ReplyMarkupClass {
	if len(m.Rows) == 0 {
		return nil
	}

	tgRows := make([]tg.KeyboardButtonRow, 0, len(m.Rows))
	for _, row := range m.Rows {
		if len(row) == 0 {
			continue
		}
		tgButtons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, btn := range row {
			switch btn.Type {
			case ButtonCallback:
				tgButtons = append(tgButtons, &tg.KeyboardButtonCallback{
					Text: btn.Text,
					Data: btn.Data,
				})
			case ButtonURL:
				tgButtons = append(tgButtons, &tg.KeyboardButtonURL{
					Text: btn.Text,
					URL:  btn.URL,
				})
			case ButtonSwitchInline:
				tgButtons = append(tgButtons, &tg.KeyboardButtonSwitchInline{
					Text:     btn.Text,
					Query:    btn.InlineQuery,
					SamePeer: btn.SamePeer,
				})
			}
		}
		if len(tgButtons) > 0 {
			tgRows = append(tgRows, tg.KeyboardButtonRow{
				Buttons: tgButtons,
			})
		}
	}

	if len(tgRows) == 0 {
		return nil
	}

	return &tg.ReplyInlineMarkup{
		Rows: tgRows,
	}
}
