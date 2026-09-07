package render

import (
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/ui"
)

// ToTelegramMarkup converts a high-level ui.Markup into Telegram MTProto markup.
func ToTelegramMarkup(m ui.Markup) tg.ReplyMarkupClass {
	if len(m.Rows) == 0 {
		return nil
	}
	tgRows := make([]tg.KeyboardButtonRow, 0, len(m.Rows))
	for _, row := range m.Rows {
		if len(row) == 0 {
			continue
		}
		tgButtons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, btn := range row {
			switch btn.Type {
			case ui.ButtonCallback:
				tgButtons = append(tgButtons, &tg.KeyboardButtonCallback{
					Text: btn.Text,
					Data: btn.Data,
				})
			case ui.ButtonURL:
				tgButtons = append(tgButtons, &tg.KeyboardButtonURL{
					Text: btn.Text,
					URL:  btn.URL,
				})
			case ui.ButtonSwitchInline:
				tgButtons = append(tgButtons, &tg.KeyboardButtonSwitchInline{
					Text:     btn.Text,
					Query:    btn.InlineQuery,
					SamePeer: btn.SamePeer,
				})
			}
		}
		if len(tgButtons) > 0 {
			tgRows = append(tgRows, tg.KeyboardButtonRow{
				Buttons: tgButtons,
			})
		}
	}
	if len(tgRows) == 0 {
		return nil
	}
	return &tg.ReplyInlineMarkup{
		Rows: tgRows,
	}
}

// RenderScreen builds text and Telegram markup from a ui.Screen.
func RenderScreen(s *ui.Screen) (string, tg.ReplyMarkupClass) {
	if s == nil {
		return "", nil
	}
	text := s.Text()
	markup := ToTelegramMarkup(s.Markup())
	return text, markup
}
