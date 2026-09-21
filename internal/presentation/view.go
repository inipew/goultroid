package presentation

import (
	"errors"
	"strings"
)

var (
	ErrInvalidView   = errors.New("presentation: invalid view")
	ErrInvalidButton = errors.New("presentation: invalid button")
)

type Button struct {
	Text     string
	ActionID string
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
			if strings.TrimSpace(button.Text) == "" || !validID(button.ActionID) {
				return ErrInvalidButton
			}
		}
	}
	return nil
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
