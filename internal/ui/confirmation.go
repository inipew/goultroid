package ui

import (
	"sort"
)

// BuildConfirmationCard formats an alert card and confirmation buttons for destructive actions.
func BuildConfirmationCard(
	title string,
	warningText string,
	details map[string]string,
	confirmText string,
	confirmData []byte,
	cancelText string,
	cancelData []byte,
) (string, Markup) {
	if title == "" {
		title = "Confirmation Required"
	}
	if warningText == "" {
		warningText = "Are you sure you want to perform this action? This operation cannot be undone."
	}
	if confirmText == "" {
		confirmText = "🗑 Confirm"
	}
	if cancelText == "" {
		cancelText = "❌ Cancel"
	}

	card := NewCard(title).
		WithIcon("⚠️").
		WithHeader(warningText)

	if len(details) > 0 {
		var keys []string
		for k := range details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			card.AddField(k, Code(details[k]))
		}
	}

	card.WithFooter("<i>Please confirm or cancel the operation below.</i>")

	confirmBtn := NewCallbackButton(confirmText, confirmData)
	cancelBtn := NewCallbackButton(cancelText, cancelData)
	markup := NewMarkup(ButtonRow{confirmBtn, cancelBtn})

	return card.Render(), markup
}

// BuildPreviewActionCard creates a detailed operation preview before execution,
// with options to Confirm, Edit parameters, or Cancel.
func BuildPreviewActionCard(
	entityTitle string,
	actionName string,
	details map[string]string,
	confirmText string,
	confirmData []byte,
	editText string,
	editData []byte,
	cancelData []byte,
) (string, Markup) {
	if entityTitle == "" {
		entityTitle = "Operation Preview"
	}
	if confirmText == "" {
		confirmText = "✓ Execute"
	}
	if editText == "" {
		editText = "✏️ Edit"
	}

	card := NewCard(entityTitle).
		WithIcon("📋").
		WithHeader("Please review the operation details before proceeding:")

	card.AddField("Action", Code(actionName))

	if len(details) > 0 {
		var keys []string
		for k := range details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			card.AddField(k, Code(details[k]))
		}
	}

	card.WithFooter("<i>Verify all parameters above before executing.</i>")

	var row ButtonRow
	row = append(row, NewCallbackButton(confirmText, confirmData))
	if len(editData) > 0 {
		row = append(row, NewCallbackButton(editText, editData))
	}
	if len(cancelData) > 0 {
		row = append(row, NewCallbackButton("❌ Cancel", cancelData))
	}

	markup := NewMarkup(row)
	return card.Render(), markup
}
