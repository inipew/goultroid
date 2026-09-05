package ui

import "fmt"

// Success formats a positive status message.
func Success(msg string) string {
	return fmt.Sprintf("✅ <b>Success:</b> %s", msg)
}

// Warning formats an alert or cautionary status message.
func Warning(msg string) string {
	return fmt.Sprintf("⚠️ <b>Warning:</b> %s", msg)
}

// Error formats a failure or error message.
func Error(msg string) string {
	return fmt.Sprintf("❌ <b>Error:</b> %s", msg)
}

// Processing formats an in-progress or loading status message.
func Processing(msg string) string {
	return fmt.Sprintf("⏳ <b>Processing:</b> %s", msg)
}

// Information formats an informative note or status.
func Information(msg string) string {
	return fmt.Sprintf("ℹ️ <b>Info:</b> %s", msg)
}
