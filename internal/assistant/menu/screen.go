package menu

// ScreenID identifies a specific screen in the assistant navigation tree.
type ScreenID string

const (
	ScreenIDStart    ScreenID = "start"
	ScreenIDSettings ScreenID = "settings"
	ScreenIDHelp     ScreenID = "help"
	ScreenIDStatus   ScreenID = "status"
)

// Screen models an interactive screen decoupled from MTProto rendering.
type Screen struct {
	ID    ScreenID
	Title string
	Body  string
	Rows  [][]Button
}

// NewScreen creates an initialized Screen.
func NewScreen(id ScreenID, title, body string) *Screen {
	return &Screen{
		ID:    id,
		Title: title,
		Body:  body,
		Rows:  make([][]Button, 0),
	}
}

// AddRow adds a row of buttons to the screen.
func (s *Screen) AddRow(buttons ...Button) *Screen {
	if len(buttons) > 0 {
		s.Rows = append(s.Rows, buttons)
	}
	return s
}
