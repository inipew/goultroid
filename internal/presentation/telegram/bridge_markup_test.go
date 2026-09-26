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
	if button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback); !ok || button.Text != "Next" || string(button.Data) != "a2:next" {
		t.Fatalf("callback button=%T %+v", inline.Rows[0].Buttons[0], inline.Rows[0].Buttons[0])
	}
	if button, ok := inline.Rows[0].Buttons[1].(*tg.KeyboardButtonURL); !ok || button.Text != "Docs" || button.URL != "https://example.com/docs" {
		t.Fatalf("url button=%T %+v", inline.Rows[0].Buttons[1], inline.Rows[0].Buttons[1])
	}
	if button, ok := inline.Rows[0].Buttons[2].(*tg.KeyboardButtonSwitchInline); !ok || button.Text != "Search" || button.Query != "help " || button.SamePeer {
		t.Fatalf("switch-inline button=%T %+v", inline.Rows[0].Buttons[2], inline.Rows[0].Buttons[2])
	}
	if button, ok := inline.Rows[1].Buttons[0].(*tg.KeyboardButtonSwitchInline); !ok || button.Text != "Search here" || button.Query != "help current" || !button.SamePeer {
		t.Fatalf("same-peer switch-inline button=%T %+v", inline.Rows[1].Buttons[0], inline.Rows[1].Buttons[0])
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
	button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("button=%T %+v", inline.Rows[0].Buttons[0], inline.Rows[0].Buttons[0])
	}

	source[0] = 'X'
	if got := string(button.Data); got != "callback" {
		t.Fatalf("encoded callback data changed with source: %q", got)
	}
	button.Data[1] = 'Y'
	if source[1] != 'a' {
		t.Fatalf("source callback data changed with encoded buffer: %q", source)
	}
}
