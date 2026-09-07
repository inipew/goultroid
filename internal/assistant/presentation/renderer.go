package presentation

import (
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/ui/render"
)

// RenderScreen delegates Telegram rendering to the repository-wide UI renderer.
// The assistant menu remains a transport adapter, not a second UI implementation.
func RenderScreen(screen *menu.Screen) (string, tg.ReplyMarkupClass) {
	return render.ToTelegram(screen)
}
