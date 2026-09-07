package assistant

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/ui"
)

// RenderStartMenu builds the main interactive assistant dashboard.
func RenderStartMenu(botUsername string, startTime time.Time) *ui.Screen {
	uptime := time.Since(startTime).Truncate(time.Second)
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	body := fmt.Sprintf(
		"👋 <b>Welcome to GoUltroid Assistant!</b>\n\n"+
			"• <b>Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Status:</b> 🟢 Online & Active\n\n"+
			"<i>Select an option below to manage and interact with your userbot:</i>",
		botUsername, uptime,
	)

	screen := ui.NewScreen("assistant:start", "🤖 GoUltroid Assistant", body)

	screen.AddRow(
		ui.NewCallbackButton("⚙️ Settings", callback.EncodeCallbackData("settings", callback.ActionNav, callback.ActionNoop)),
		ui.NewCallbackButton("📚 Help / Modules", callback.EncodeCallbackData("help", "home", callback.ActionNoop)),
	)
	screen.AddRow(
		ui.NewCallbackButton("📊 System Status", callback.EncodeCallbackData("assistant", "status", callback.ActionNoop)),
		ui.NewCallbackButton("🏓 Ping", callback.EncodeCallbackData("assistant", "ping", callback.ActionNoop)),
	)
	screen.AddRow(
		ui.NewCallbackButton("🔒 Close Menu", callback.EncodeCallbackData("assistant", "close", callback.ActionNoop)),
	)

	return screen
}

// RenderStatusScreen builds the detailed status and diagnostics screen.
func RenderStatusScreen(botUsername string, startTime time.Time) *ui.Screen {
	uptime := time.Since(startTime).Truncate(time.Second)
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	body := fmt.Sprintf(
		"📊 <b>GoUltroid System Diagnostics</b>\n\n"+
			"• <b>Assistant Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Engine:</b> GoUltroid (MTProto)\n"+
			"• <b>Callback Engine:</b> Active (Pipeline v1)\n"+
			"• <b>Status:</b> All systems operational.\n",
		botUsername, uptime,
	)

	screen := ui.NewScreen("assistant:status", "📊 System Status", body)
	screen.AddRow(
		ui.NewCallbackButton("🔄 Refresh", callback.EncodeCallbackData("assistant", "status", callback.ActionNoop)),
		ui.NewCallbackButton("« Back to Menu", callback.EncodeCallbackData("assistant", "start", callback.ActionNoop)),
	)
	return screen
}
