package presentation

import (
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/menu"
)

// RenderScreen converts a platform-agnostic menu.Screen into Telegram HTML text and reply markup.
func RenderScreen(screen *menu.Screen) (string, tg.ReplyMarkupClass) {
	if screen == nil {
		return "", nil
	}

	var text string
	if screen.Title != "" && screen.Body != "" {
		text = fmt.Sprintf("<b>%s</b>\n\n%s", screen.Title, screen.Body)
	} else if screen.Title != "" {
		text = fmt.Sprintf("<b>%s</b>", screen.Title)
	} else {
		text = screen.Body
	}

	if len(screen.Rows) == 0 {
		return text, nil
	}

	rows := make([]tg.KeyboardButtonRow, 0, len(screen.Rows))
	for _, r := range screen.Rows {
		buttons := make([]tg.KeyboardButtonClass, 0, len(r))
		for _, b := range r {
			if b.URL != "" {
				buttons = append(buttons, &tg.KeyboardButtonURL{
					Text: b.Label,
					URL:  b.URL,
				})
			} else {
				buttons = append(buttons, &tg.KeyboardButtonCallback{
					Text: b.Label,
					Data: []byte(b.Data),
				})
			}
		}
		if len(buttons) > 0 {
			rows = append(rows, tg.KeyboardButtonRow{Buttons: buttons})
		}
	}

	return text, &tg.ReplyInlineMarkup{Rows: rows}
}
