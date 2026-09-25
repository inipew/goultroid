package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/presentation"
)

func TestMarkupCompilesTypedPresentationButtons(t *testing.T) {
	got := markup(presentation.CompiledView{Rows: []presentation.CompiledRow{{
		{Type: presentation.ButtonAction, Text: "Next", Data: []byte("a2:next")},
		{Type: presentation.ButtonURL, Text: "Docs", URL: "https://example.com/docs"},
		{Type: presentation.ButtonSwitchInline, Text: "Search", InlineQuery: "help ", SamePeer: true},
	}}})
	inline, ok := got.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 1 || len(inline.Rows[0].Buttons) != 3 {
		t.Fatalf("markup=%T %+v", got, got)
	}
	if button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback); !ok || string(button.Data) != "a2:next" {
		t.Fatalf("callback button=%T %+v", inline.Rows[0].Buttons[0], inline.Rows[0].Buttons[0])
	}
	if button, ok := inline.Rows[0].Buttons[1].(*tg.KeyboardButtonURL); !ok || button.URL != "https://example.com/docs" {
		t.Fatalf("url button=%T %+v", inline.Rows[0].Buttons[1], inline.Rows[0].Buttons[1])
	}
	if button, ok := inline.Rows[0].Buttons[2].(*tg.KeyboardButtonSwitchInline); !ok || button.Query != "help " || !button.SamePeer {
		t.Fatalf("switch-inline button=%T %+v", inline.Rows[0].Buttons[2], inline.Rows[0].Buttons[2])
	}
}
