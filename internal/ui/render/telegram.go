package render

import (
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/ui"
)

// ToTelegramMarkup converts a high-level ui.Markup into Telegram MTProto markup.
// Legacy callback payloads are already transport-ready bytes, so this adapter
// maps them directly into compiled presentation rows without allocating a2
// callback tokens.
func ToTelegramMarkup(m ui.Markup) tg.ReplyMarkupClass {
	if len(m.Rows) == 0 {
		return nil
	}

	rows := make([]presentation.CompiledRow, 0, len(m.Rows))
	for _, row := range m.Rows {
		compiled := make(presentation.CompiledRow, 0, len(row))
		for _, button := range row {
			var converted presentation.CompiledButton
			switch button.Type {
			case ui.ButtonCallback:
				converted = presentation.CompiledButton{
					Type: presentation.ButtonAction,
					Text: button.Text,
					Data: button.Data,
				}
			case ui.ButtonURL:
				converted = presentation.CompiledButton{
					Type: presentation.ButtonURL,
					Text: button.Text,
					URL:  button.URL,
				}
			case ui.ButtonSwitchInline:
				converted = presentation.CompiledButton{
					Type:        presentation.ButtonSwitchInline,
					Text:        button.Text,
					InlineQuery: button.InlineQuery,
					SamePeer:    button.SamePeer,
				}
			default:
				continue
			}
			compiled = append(compiled, converted)
		}
		rows = append(rows, compiled)
	}
	return presentationtelegram.EncodeMarkup(rows)
}

// ToTelegram renders a ui.Screen to Telegram text and markup.
// This is the Telegram adapter for ui.Screen; ui package itself remains Telegram-agnostic.
func ToTelegram(s *ui.Screen) (string, tg.ReplyMarkupClass) {
	if s == nil {
		return "", nil
	}
	text := s.Text()
	markup := ToTelegramMarkup(s.Markup())
	return text, markup
}

// RenderScreen is deprecated: use ToTelegram instead.
func RenderScreen(s *ui.Screen) (string, tg.ReplyMarkupClass) {
	return ToTelegram(s)
}
