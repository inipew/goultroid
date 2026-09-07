package menu

import "github.com/inipew/goultroid/internal/ui"

// ScreenID identifies a specific screen in the assistant navigation tree.
type ScreenID string

const (
	ScreenIDStart    ScreenID = "start"
	ScreenIDSettings ScreenID = "settings"
	ScreenIDHelp     ScreenID = "help"
	ScreenIDStatus   ScreenID = "status"
)

// Screen aliases the repository-wide Telegram-agnostic UI screen model.
type Screen = ui.Screen

// NewScreen creates a canonical UI screen while preserving the assistant menu API.
func NewScreen(id ScreenID, title, body string) *Screen {
	return ui.NewScreen(string(id), title, body)
}
