package ui

import "github.com/inipew/goultroid/internal/presentation"

// Success formats a positive status message through the canonical presentation vocabulary.
func Success(msg string) string {
	return presentation.Success(msg).Render()
}

// Warning formats an alert or cautionary status message through the canonical presentation vocabulary.
func Warning(msg string) string {
	return presentation.Warning(msg).Render()
}

// Error formats a failure or error message through the canonical presentation vocabulary.
func Error(msg string) string {
	return presentation.Error(msg).Render()
}

// Processing formats an in-progress status through the canonical presentation vocabulary.
func Processing(msg string) string {
	return presentation.Progress(msg).Render()
}

// Information formats an informational note through the canonical presentation vocabulary.
func Information(msg string) string {
	return presentation.Information(msg).Render()
}
