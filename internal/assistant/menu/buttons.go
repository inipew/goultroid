package menu

// Button represents an interactive inline button.
type Button struct {
	Label string
	Data  string
	URL   string
}

// NewButton creates a standard callback query button.
func NewButton(label, data string) Button {
	return Button{
		Label: label,
		Data:  data,
	}
}

// NewURLButton creates a link button opening an external URL.
func NewURLButton(label, url string) Button {
	return Button{
		Label: label,
		URL:   url,
	}
}
