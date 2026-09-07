package menu

import "github.com/inipew/goultroid/internal/ui"

// Button is the canonical UI button used by the assistant menu.
type Button = ui.Button

// NewButton creates a standard callback button through the shared UI layer.
func NewButton(label, data string) Button {
	return ui.NewCallbackButton(label, []byte(data))
}

// NewURLButton creates a link button through the shared UI layer.
func NewURLButton(label, url string) Button {
	return ui.NewURLButton(label, url)
}
