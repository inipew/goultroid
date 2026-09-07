package presentation

import (
	"time"

	appPing "github.com/inipew/goultroid/internal/application/ping"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/core"
)

// PingResult records latency metrics for assistant ping presentations (§19 parity).
type PingResult struct {
	Latency time.Duration
}

// RenderPing formats the standard ping response message (§19 parity).
func RenderPing(result PingResult) string {
	return appPing.FormatResult(result.Latency)
}

// BuildStartScreen constructs the main assistant dashboard screen delegating to menu.
func BuildStartScreen(botUsername string, uptime time.Duration) *menu.Screen {
	return menu.BuildStartScreen(botUsername, uptime)
}

// BuildSettingsScreen constructs the settings navigation screen delegating to menu.
func BuildSettingsScreen(botUsername string) *menu.Screen {
	return menu.BuildSettingsScreen(botUsername)
}

// BuildHelpScreen constructs the help and commands index screen with defaults.
func BuildHelpScreen(botUsername string) *menu.Screen {
	return menu.BuildHelpScreen(botUsername)
}

// BuildHelpScreenWithCommands constructs the help screen with dynamic commands.
func BuildHelpScreenWithCommands(botUsername string, cmds []core.Command) *menu.Screen {
	return menu.BuildHelpScreenWithCommands(botUsername, cmds)
}

// BuildStatusScreen constructs the system diagnostics screen delegating to menu.
func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *menu.Screen {
	return menu.BuildStatusScreen(botUsername, uptime, engine)
}
