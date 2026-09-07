package ui

import (
	"fmt"

	"github.com/gotd/td/tg"
)

// WizardStep represents a single step in a multi-step interactive flow.
type WizardStep struct {
	Index       int               `json:"i"`
	Total       int               `json:"t"`
	Title       string            `json:"title"`
	Description string            `json:"desc"`
	Data        map[string]string `json:"data,omitempty"`
}

// Wizard provides helpers for generating structured multi-step flow screens.
type Wizard struct {
	TotalSteps int
	Title      string
}

// NewWizard initializes a Wizard with a name and number of steps.
func NewWizard(title string, totalSteps int) *Wizard {
	if totalSteps <= 0 {
		totalSteps = 1
	}
	return &Wizard{
		Title:      title,
		TotalSteps: totalSteps,
	}
}

// RenderStep generates a formatted card and button matrix for a wizard stage:
// Title, Step X / Y indicator, input buttons, and navigation (Back, Cancel, Next).
func (w *Wizard) RenderStep(
	stepIndex int,
	stepTitle string,
	prompt string,
	inputs []ButtonRow,
	backData []byte,
	cancelData []byte,
	nextData []byte,
) (string, tg.ReplyMarkupClass) {
	card := NewCard(w.Title).
		WithIcon("🧙").
		WithHeader(fmt.Sprintf("<b>Step %d / %d: %s</b>", stepIndex, w.TotalSteps, stepTitle))

	if prompt != "" {
		card.WithRaw(prompt)
	}

	var rows []ButtonRow
	rows = append(rows, inputs...)

	var navRow ButtonRow
	if len(backData) > 0 {
		navRow = append(navRow, NewCallbackButton("◀ Back", backData))
	}
	if len(cancelData) > 0 {
		navRow = append(navRow, NewCallbackButton("❌ Cancel", cancelData))
	}
	if len(nextData) > 0 {
		navRow = append(navRow, NewCallbackButton("Next ▶", nextData))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}

	markup := NewMarkup(rows...).ToTelegramMarkup()
	return card.Render(), markup
}
