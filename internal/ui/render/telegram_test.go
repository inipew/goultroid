package render

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/ui"
)

func TestToTelegramMarkup(t *testing.T) {
	empty := ui.Markup{}
	if ToTelegramMarkup(empty) != nil {
		t.Errorf("expected nil for empty markup")
	}

	m := ui.NewMarkup(
		ui.ButtonRow{
			ui.NewCallbackButton("Callback", []byte("cb_1")),
			ui.NewURLButton("Link", "https://example.com"),
		},
		ui.ButtonRow{
			ui.NewSwitchInlineButton("Inline", "query", false),
		},
	)

	tgMarkup := ToTelegramMarkup(m)
	if tgMarkup == nil {
		t.Fatalf("expected non-nil tgMarkup")
	}

	inlineMarkup, ok := tgMarkup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("expected *tg.ReplyInlineMarkup, got %T", tgMarkup)
	}

	if len(inlineMarkup.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(inlineMarkup.Rows))
	}

	if len(inlineMarkup.Rows[0].Buttons) != 2 {
		t.Fatalf("expected 2 buttons in row 0, got %d", len(inlineMarkup.Rows[0].Buttons))
	}
	btn0, ok := inlineMarkup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok || btn0.Text != "Callback" || string(btn0.Data) != "cb_1" {
		t.Errorf("unexpected button 0: %+v", inlineMarkup.Rows[0].Buttons[0])
	}
	btn1, ok := inlineMarkup.Rows[0].Buttons[1].(*tg.KeyboardButtonURL)
	if !ok || btn1.Text != "Link" || btn1.URL != "https://example.com" {
		t.Errorf("unexpected button 1: %+v", inlineMarkup.Rows[0].Buttons[1])
	}

	if len(inlineMarkup.Rows[1].Buttons) != 1 {
		t.Fatalf("expected 1 button in row 1, got %d", len(inlineMarkup.Rows[1].Buttons))
	}
	btn2, ok := inlineMarkup.Rows[1].Buttons[0].(*tg.KeyboardButtonSwitchInline)
	if !ok || btn2.Text != "Inline" || btn2.Query != "query" || btn2.SamePeer {
		t.Errorf("unexpected button 2: %+v", inlineMarkup.Rows[1].Buttons[0])
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
