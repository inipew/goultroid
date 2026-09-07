package presentation

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
)

// PingResult records latency metrics for assistant ping presentations (§19 parity).
type PingResult struct {
	Latency time.Duration
}

// RenderPing formats the standard ping response message (§19 parity).
func RenderPing(result PingResult) string {
	return fmt.Sprintf("🏓 <b>Pong!</b>\n\n<b>Latency:</b> %d ms", result.Latency.Milliseconds())
}

// BuildStartScreen constructs the main assistant dashboard screen.
func BuildStartScreen(botUsername string, uptime time.Duration) *menu.Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	uptimeStr := uptime.Truncate(time.Second).String()
	body := fmt.Sprintf(
		"👋 <b>Welcome to GoUltroid Assistant!</b>\n\n"+
			"• <b>Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Status:</b> 🟢 Online & Active\n\n"+
			"<i>Select an option below to manage and interact with your userbot:</i>",
		botUsername, uptimeStr,
	)

	screen := menu.NewScreen(menu.ScreenIDStart, "🤖 GoUltroid Assistant", body)
	screen.AddRow(
		menu.NewButton("⚙️ Settings", "a1:assistant:settings"),
		menu.NewButton("📚 Help / Modules", "a1:assistant:help"),
	)
	screen.AddRow(
		menu.NewButton("📊 System Status", "a1:assistant:status"),
		menu.NewButton("🏓 Ping", "a1:assistant:ping"),
	)
	screen.AddRow(
		menu.NewButton("🔒 Close Menu", "a1:assistant:close"),
	)
	return screen
}

// BuildSettingsScreen constructs the settings navigation screen.
func BuildSettingsScreen(botUsername string) *menu.Screen {
	body := "⚙️ <b>GoUltroid Settings Subsystem</b>\n\n" +
		"Manage your userbot configurations, privacy, security, and plugins.\n\n" +
		"• Use <code>.set</code> commands in chat, or open the interactive dashboard below."

	screen := menu.NewScreen(menu.ScreenIDSettings, "⚙️ Assistant Settings", body)
	screen.AddRow(
		menu.NewButton("📂 Settings Dashboard", "v1:settings:nav:noop"),
	)
	screen.AddRow(
		menu.NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// BuildHelpScreen constructs the help and commands index screen.
func BuildHelpScreen(botUsername string) *menu.Screen {
	body := "<b>GoUltroid Assistant Commands</b>\n\n" +
		"/start — open the interactive dashboard\n" +
		"/help — show this help overview\n" +
		"/ping — check responsiveness\n" +
		"/status — view system status\n" +
		"/alive — check assistant status\n\n" +
		"<i>Browse all installed userbot modules below:</i>"

	screen := menu.NewScreen(menu.ScreenIDHelp, "📚 Help / Modules", body)
	screen.AddRow(
		menu.NewButton("📚 Browse All Modules", "v1:help:cat:noop"),
	)
	screen.AddRow(
		menu.NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// BuildStatusScreen constructs the system diagnostics screen.
func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *menu.Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	if engine == "" {
		engine = "GoUltroid (MTProto) v2"
	}
	uptimeStr := uptime.Truncate(time.Second).String()
	body := fmt.Sprintf(
		"• <b>Assistant Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Engine:</b> %s\n"+
			"• <b>Callback Engine:</b> Active (Pipeline v2)\n"+
			"• <b>Status:</b> All systems operational.\n",
		botUsername, uptimeStr, engine,
	)

	screen := menu.NewScreen(menu.ScreenIDStatus, "📊 System Status", body)
	screen.AddRow(
		menu.NewButton("🔄 Refresh", "a1:assistant:status"),
		menu.NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}
