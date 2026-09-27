package ui

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

// NewCallbackButton creates a transport-neutral callback button value.
// Callback token ownership belongs to the interaction/runtime layer.
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

// NoopData is the transport-neutral non-interactive callback payload used by
// presentation adapters that need a placeholder button.
var NoopData = []byte("noop")
