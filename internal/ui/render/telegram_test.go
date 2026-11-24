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
	btn0 := inline.Rows[0].Buttons[0]
	cb0, ok := btn0.Type.(*tg.InlineButtonTypeCallback)
	if !ok || string(cb0.Data) != "cb_1" {
		t.Fatalf("callback button=%T %+v", btn0, btn0)
	}
	btn1 := inline.Rows[0].Buttons[1]
	url1, ok := btn1.Type.(*tg.InlineButtonTypeURL)
	if !ok || url1.URL != "https://example.com" {
		t.Fatalf("url button=%T %+v", btn1, btn1)
	}
	btn2 := inline.Rows[0].Buttons[2]
	sw2, ok := btn2.Type.(*tg.InlineButtonTypeSwitchInline)
	if !ok || sw2.Query != "query" || sw2.SamePeer {
		t.Fatalf("switch-inline button=%T %+v", btn2, btn2)
	}
	btnRow1 := inline.Rows[1].Buttons[0]
	swRow1, ok := btnRow1.Type.(*tg.InlineButtonTypeSwitchInline)
	if !ok || swRow1.Query != "query current" || !swRow1.SamePeer {
		t.Fatalf("same-peer switch-inline button=%T %+v", btnRow1, btnRow1)
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
	btn := inline.Rows[0].Buttons[0]
	cb, ok := btn.Type.(*tg.InlineButtonTypeCallback)
	if !ok {
		t.Fatalf("button=%T %+v", btn, btn)
	}

	data[0] = 'X'
	if got := string(cb.Data); got != "legacy" {
		t.Fatalf("encoded callback data changed with legacy source: %q", got)
	}
	cb.Data[1] = 'Y'
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
