package render

import (
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/ui"
)

func TestToTelegramMarkupDelegatesToCanonicalEncoder(t *testing.T) {
	callbackData := []byte("cb_1")
	legacy := ui.NewMarkup(
		ui.ButtonRow{},
		ui.ButtonRow{
			ui.NewCallbackButton("Callback", callbackData),
			ui.NewURLButton("Link", "https://example.com"),
			ui.NewSwitchInlineButton("Inline", "query", false),
		},
		ui.ButtonRow{
			ui.NewSwitchInlineButton("Inline here", "query current", true),
		},
	)
	canonical := []presentation.CompiledRow{
		{},
		{
			{Type: presentation.ButtonAction, Text: "Callback", Data: callbackData},
			{Type: presentation.ButtonURL, Text: "Link", URL: "https://example.com"},
			{Type: presentation.ButtonSwitchInline, Text: "Inline", InlineQuery: "query", SamePeer: false},
		},
		{
			{Type: presentation.ButtonSwitchInline, Text: "Inline here", InlineQuery: "query current", SamePeer: true},
		},
	}

	got := ToTelegramMarkup(legacy)
	want := presentationtelegram.EncodeMarkup(canonical)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy markup\n got: %#v\nwant: %#v", got, want)
	}

	inline, ok := got.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 2 || len(inline.Rows[0].Buttons) != 3 || len(inline.Rows[1].Buttons) != 1 {
		t.Fatalf("markup=%T %+v", got, got)
	}
	if button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback); !ok || string(button.Data) != "cb_1" {
		t.Fatalf("callback button=%T %+v", inline.Rows[0].Buttons[0], inline.Rows[0].Buttons[0])
	}
	if button, ok := inline.Rows[0].Buttons[1].(*tg.KeyboardButtonURL); !ok || button.URL != "https://example.com" {
		t.Fatalf("url button=%T %+v", inline.Rows[0].Buttons[1], inline.Rows[0].Buttons[1])
	}
	if button, ok := inline.Rows[0].Buttons[2].(*tg.KeyboardButtonSwitchInline); !ok || button.Query != "query" || button.SamePeer {
		t.Fatalf("switch-inline button=%T %+v", inline.Rows[0].Buttons[2], inline.Rows[0].Buttons[2])
	}
	if button, ok := inline.Rows[1].Buttons[0].(*tg.KeyboardButtonSwitchInline); !ok || button.Query != "query current" || !button.SamePeer {
		t.Fatalf("same-peer switch-inline button=%T %+v", inline.Rows[1].Buttons[0], inline.Rows[1].Buttons[0])
	}
}

func TestToTelegramMarkupEmptyReturnsNil(t *testing.T) {
	for name, markup := range map[string]ui.Markup{
		"zero":       {},
		"empty rows": ui.NewMarkup(ui.ButtonRow{}, ui.ButtonRow{}),
	} {
		t.Run(name, func(t *testing.T) {
			if got := ToTelegramMarkup(markup); got != nil {
				t.Fatalf("ToTelegramMarkup()=%T %+v, want nil", got, got)
			}
		})
	}
}

func TestToTelegramMarkupCopiesLegacyCallbackData(t *testing.T) {
	data := []byte("legacy")
	got := ToTelegramMarkup(ui.NewMarkup(ui.ButtonRow{
		ui.NewCallbackButton("Run", data),
	}))
	inline, ok := got.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 1 || len(inline.Rows[0].Buttons) != 1 {
		t.Fatalf("markup=%T %+v", got, got)
	}
	button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("button=%T %+v", inline.Rows[0].Buttons[0], inline.Rows[0].Buttons[0])
	}

	data[0] = 'X'
	if got := string(button.Data); got != "legacy" {
		t.Fatalf("encoded callback data changed with legacy source: %q", got)
	}
	button.Data[1] = 'Y'
	if data[1] != 'e' {
		t.Fatalf("legacy source changed with encoded callback buffer: %q", data)
	}
}

func TestRenderScreen(t *testing.T) {
	s := ui.NewScreen("test", "Title", "Body")
	s.AddRow(ui.NewCallbackButton("Btn", []byte("data")))
	text, markup := RenderScreen(s)
	if text == "" {
		t.Fatalf("expected text")
	}
	if markup == nil {
		t.Fatalf("expected markup")
	}
}
