package presentation

import (
	"errors"
	"net/url"
	"strings"
)

var (
	ErrInvalidView   = errors.New("presentation: invalid view")
	ErrInvalidButton = errors.New("presentation: invalid button")
)

// ButtonType identifies the transport-neutral behavior of a presentation button.
// ButtonAction remains the zero value so existing {Text, ActionID} literals keep
// their historical a2 callback semantics.
type ButtonType uint8

const (
	ButtonAction ButtonType = iota
	ButtonURL
	ButtonSwitchInline
)

type Button struct {
	Type        ButtonType
	Text        string
	ActionID    string
	URL         string
	InlineQuery string
	SamePeer    bool
}

func (b Button) Validate() error {
	if strings.TrimSpace(b.Text) == "" {
		return ErrInvalidButton
	}
	switch b.Type {
	case ButtonAction:
		if !validID(b.ActionID) || b.URL != "" || b.InlineQuery != "" || b.SamePeer {
			return ErrInvalidButton
		}
	case ButtonURL:
		if b.ActionID != "" || b.InlineQuery != "" || b.SamePeer || !validButtonURL(b.URL) {
			return ErrInvalidButton
		}
	case ButtonSwitchInline:
		if b.ActionID != "" || b.URL != "" {
			return ErrInvalidButton
		}
	default:
		return ErrInvalidButton
	}
	return nil
}

type Row []Button

type View struct {
	Text string
	Rows []Row
}

func (v View) Validate() error {
	if strings.TrimSpace(v.Text) == "" {
		return ErrInvalidView
	}
	for _, row := range v.Rows {
		if len(row) == 0 {
			return ErrInvalidView
		}
		for _, button := range row {
			if err := button.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func validButtonURL(value string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
