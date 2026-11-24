package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/presentation"
)

func TestEncodeMarkupCompilesTypedPresentationButtons(t *testing.T) {
	got := EncodeMarkup([]presentation.CompiledRow{
		{},
		{
			{Type: presentation.ButtonAction, Text: "Next", Data: []byte("a2:next")},
			{Type: presentation.ButtonURL, Text: "Docs", URL: "https://example.com/docs"},
			{Type: presentation.ButtonSwitchInline, Text: "Search", InlineQuery: "help ", SamePeer: false},
		},
		{
			{Type: presentation.ButtonSwitchInline, Text: "Search here", InlineQuery: "help current", SamePeer: true},
		},
	})
	inline, ok := got.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 2 {
		t.Fatalf("markup=%T %+v", got, got)
	}
	if len(inline.Rows[0].Buttons) != 3 || len(inline.Rows[1].Buttons) != 1 {
		t.Fatalf("rows=%+v", inline.Rows)
	}
	btn0 := inline.Rows[0].Buttons[0]
	cb0, ok := btn0.Type.(*tg.InlineButtonTypeCallback)
	if !ok || btn0.Text != "Next" || string(cb0.Data) != "a2:next" {
		t.Fatalf("callback button=%T %+v", btn0, btn0)
	}
	btn1 := inline.Rows[0].Buttons[1]
	url1, ok := btn1.Type.(*tg.InlineButtonTypeURL)
	if !ok || btn1.Text != "Docs" || url1.URL != "https://example.com/docs" {
		t.Fatalf("url button=%T %+v", btn1, btn1)
	}
	btn2 := inline.Rows[0].Buttons[2]
	sw2, ok := btn2.Type.(*tg.InlineButtonTypeSwitchInline)
	if !ok || btn2.Text != "Search" || sw2.Query != "help " || sw2.SamePeer {
		t.Fatalf("switch-inline button=%T %+v", btn2, btn2)
	}
	btnRow1 := inline.Rows[1].Buttons[0]
	swRow1, ok := btnRow1.Type.(*tg.InlineButtonTypeSwitchInline)
	if !ok || btnRow1.Text != "Search here" || swRow1.Query != "help current" || !swRow1.SamePeer {
		t.Fatalf("same-peer switch-inline button=%T %+v", btnRow1, btnRow1)
	}
}

func TestEncodeMarkupEmptyRowsReturnNil(t *testing.T) {
	for name, rows := range map[string][]presentation.CompiledRow{
		"nil":        nil,
		"empty":      {},
		"empty rows": {{}, {}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := EncodeMarkup(rows); got != nil {
				t.Fatalf("EncodeMarkup()=%T %+v, want nil", got, got)
			}
		})
	}
}

func TestEncodeMarkupCopiesCallbackData(t *testing.T) {
	source := []byte("callback")
	got := EncodeMarkup([]presentation.CompiledRow{{{
		Type: presentation.ButtonAction,
		Text: "Run",
		Data: source,
	}}})
	inline, ok := got.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 1 || len(inline.Rows[0].Buttons) != 1 {
		t.Fatalf("markup=%T %+v", got, got)
	}
	btn := inline.Rows[0].Buttons[0]
	cb, ok := btn.Type.(*tg.InlineButtonTypeCallback)
	if !ok {
		t.Fatalf("button=%T %+v", btn, btn)
	}

	source[0] = 'X'
	if got := string(cb.Data); got != "callback" {
		t.Fatalf("encoded callback data changed with source: %q", got)
	}
	cb.Data[1] = 'Y'
	if source[1] != 'a' {
		t.Fatalf("source callback data changed with encoded buffer: %q", source)
	}
}
