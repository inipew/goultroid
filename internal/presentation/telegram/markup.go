package telegram

import (
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/presentation"
)

// EncodeMarkup converts transport-ready presentation rows into Telegram inline
// keyboard markup. Callback payloads are copied so callers keep ownership of
// their source buffers after encoding.
func EncodeMarkup(rows []presentation.CompiledRow) tg.ReplyMarkupClass {
	if len(rows) == 0 {
		return nil
	}

	tgRows := make([]tg.KeyboardButtonRow, 0, len(rows))
	for _, row := range rows {
		buttons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, button := range row {
			switch button.Type {
			case presentation.ButtonAction:
				buttons = append(buttons, &tg.KeyboardButtonCallback{
					Text: button.Text,
					Data: append([]byte(nil), button.Data...),
				})
			case presentation.ButtonURL:
				buttons = append(buttons, &tg.KeyboardButtonURL{
					Text: button.Text,
					URL:  button.URL,
				})
			case presentation.ButtonSwitchInline:
				buttons = append(buttons, &tg.KeyboardButtonSwitchInline{
					Text:     button.Text,
					Query:    button.InlineQuery,
					SamePeer: button.SamePeer,
				})
			}
		}
		if len(buttons) > 0 {
			tgRows = append(tgRows, tg.KeyboardButtonRow{Buttons: buttons})
		}
	}
	if len(tgRows) == 0 {
		return nil
	}
	return &tg.ReplyInlineMarkup{Rows: tgRows}
}
