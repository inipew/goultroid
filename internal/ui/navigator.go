package ui

import (
	"github.com/inipew/goultroid/internal/services/interaction/navigation"
)

// Navigator and ScreenState are re-exported from the interaction service layer.
// UI package only provides rendering helpers like BackButton/HomeButton;
// stateful navigation belongs to services/interaction/navigation.
type ScreenState = navigation.ScreenState
type Navigator = navigation.Navigator

var (
	NewNavigator         = navigation.NewNavigator
	DeserializeNavigator = navigation.DeserializeNavigator
)

// BackButton returns a standard back navigation button row.
// Kept in UI for rendering convenience (stateless).
func BackButton(data []byte) ButtonRow {
	if len(data) == 0 {
		return nil
	}
	return NewBackRow(data)
}

// HomeButton returns a standard home navigation button row.
func HomeButton(data []byte) ButtonRow {
	if len(data) == 0 {
		return nil
	}
	return ButtonRow{NewCallbackButton("🏠 Home", data)}
}
